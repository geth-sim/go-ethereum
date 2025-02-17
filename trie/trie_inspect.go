package trie

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/fdlimit"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/rlp"
)

//
// Ethereum important hard forks for execution client (blockNum - stateRoot)
//
// Cancun: 19,426,587 - 0xbd33ab68095087d81beb810b3f5d0b16050b3f798ae3978e440bab048dd78992
// Shanghai: 17,034,870 - 0x7fd42f5027bc18315b3781e65f19e4c8828fd5c5fce33410f0fb4fea0b65541f
// Paris: 15,537,394 - 0x40c07091e16263270f3579385090fea02dd5f061ba6750228fcc082ff762fda7
// London: 12,965,000 - 0x41cf6e8e60fd087d2b00360dc29e5bfb21959bce1f4c242fd1ad7c4da968eb87
// Berlin: 12,244,000 - 0xfdec060ee45e55da9e36060fc95dddd0bdc47e447224666a895d9f0dc9adaa0c
// Istanbul: 9,069,000 - 0xf3917914f693a985a23ee2b623f6d1c1bcffb8e597e63cf1fb0cbabb9947a9c7
// Constantinople: 7,280,000 - 0x1e302241298f913b30f7a0df60272c9983d8d8726932f66582f182bd99ef42bc
// Byzantium: 4,370,000 - 0xe7a73d3c05829730c750ca483b5a65f8321adb25d8abb9da23a4cbb6473464ee

// TODO(jmlee): trie 자체는 잘 복사하는거 같은데 snapshot을 뭔가 제대로 만들어내지 못하는듯? 확인해볼것

var (
	saveTrieNodeInfos = true

	// leveldbPathPrefix           = "/ethereum/evm_simulator_jmlee/stateTries/"
	// trieInspectResultPathPrefix = "/ethereum/evm_simulator_jmlee/stateTries/inspectResults/"
	// trieNodeInfosPath           = "/ethereum/th_plus/"
	leveldbPathPrefix           = "/ethereum/th_plus/stateTries/ethereum/"
	trieInspectResultPathPrefix = "/ethereum/th_plus/stateTries/ethereum/inspectResults/"
	trieNodeInfosPath           = "/ethereum/th_plus/stateTries/ethereum/trieNodeInfos/"
)

var (
	// copied from core/rawdb/schema.go
	snapshotGeneratorKey  = []byte("SnapshotGenerator")
	trieNodeAccountPrefix = []byte("A") // trieNodeAccountPrefix + hexPath -> trie node
	trieNodeStoragePrefix = []byte("O") // trieNodeStoragePrefix + accountHash + hexPath -> trie node
)

// copied from core/rawdb/accessors_trie.go
// accountTrieNodeKey = trieNodeAccountPrefix + nodePath.
func accountTrieNodeKey(path []byte) []byte {
	return append(trieNodeAccountPrefix, path...)
}

// copied from core/rawdb/accessors_trie.go
// storageTrieNodeKey = trieNodeStoragePrefix + accountHash + nodePath.
func storageTrieNodeKey(accountHash common.Hash, path []byte) []byte {
	buf := make([]byte, len(trieNodeStoragePrefix)+common.HashLength+len(path))
	n := copy(buf, trieNodeStoragePrefix)
	n += copy(buf[n:], accountHash.Bytes())
	copy(buf[n:], path)
	return buf
}

// open leveldb
func openLevelDB(blockNum uint64, rootHash common.Hash, dbPathSuffix string) ethdb.Database {
	// set maximum number of open files
	limit, err := fdlimit.Maximum()
	if err != nil {
		// Fatalf("Failed to retrieve file descriptor allowance: %v", err)
		fmt.Println("Failed to retrieve file descriptor allowance:", err)
	}
	raised, err := fdlimit.Raise(uint64(limit))
	if err != nil {
		// Fatalf("Failed to raise file descriptor allowance: %v", err)
		fmt.Println("Failed to raise file descriptor allowance:", err)
	}
	if raised <= 1000 {
		fmt.Println("max open file num is too low")
		os.Exit(1)
	}
	leveldbHandles := int(raised / 2)
	fmt.Println("open file limit:", limit, "/ raised:", raised, "/ leveldbHandles:", leveldbHandles)

	// reset normal trie
	leveldbPath := leveldbPathPrefix + strconv.FormatUint(blockNum, 10) + "_" + rootHash.Hex() + dbPathSuffix
	fmt.Println("set leveldb at:", leveldbPath)

	leveldbCache := 2048
	leveldbNamespace := "eth/db/chaindata/"
	leveldbReadonly := false
	kvdb, err := leveldb.New(leveldbPath, leveldbCache, leveldbHandles, leveldbNamespace, leveldbReadonly)
	if err != nil {
		fmt.Println("leveldb.New error!! ->", err)
		os.Exit(1)
	}
	// realleveldb.ResetCacheStat(0)
	fmt.Println("leveldb cache size:", leveldbCache, "MB")
	frdb, err := rawdb.NewDatabaseWithFreezer(kvdb, leveldbPath, leveldbNamespace, leveldbReadonly)
	if err != nil {
		fmt.Println("frdb error:", err)
		os.Exit(1)
	}

	return frdb
}

// copied from core/state/snapshot/journal.go
type journalGenerator struct {
	// Indicator that whether the database was in progress of being wiped.
	// It's deprecated but keep it here for background compatibility.
	Wiping bool

	Done     bool // Whether the generator finished creating the snapshot
	Marker   []byte
	Accounts uint64
	Slots    uint64
	Storage  uint64
}

// copied from core/state/snapshot/context.go
type generatorStats struct {
	// origin   uint64             // Origin prefix where generation started
	// start    time.Time          // Timestamp when generation started
	Accounts uint64 // Number of accounts indexed(generated or recovered)
	Slots    uint64 // Number of storage slots indexed(generated or recovered)
	// dangling uint64             // Number of dangling storage slots
	Storage common.StorageSize // Total account and storage slot size(generation or recovery)
}

type TrieStat struct {
	// trie's root hash
	RootHash common.Hash
	// address hash of CA for storage trie
	AddrHash common.Hash

	// "full", "short", "leaf"
	NodeNums  map[string]int64
	NodeSizes map[string]int64

	// "min", "max", "sum"
	// root node's depth is 1
	DepthInfos map[string]int64

	// sum of leaf node's value size
	ValueSizeSum int64

	// if this trie is state trie, list of storage tries' sizes
	StorageTrieSizes      []int64
	StorageTrieNodeNums   []int64
	StorageTriesCodeSizes []int64

	// metadata for snapshot
	SnapshotStats generatorStats

	// trie inspect execution time (nanoseconds)
	ElapsedTime int64

	//
	// vars start with lower case are not stored as a json file
	//

	trieNodeInfosFile *os.File
}

// TODO(jmlee): add option to measure trie node infos and write them as a file)
// ex. ex. diskKey, diskValue, depth, path, nodeHash, nodeType (full, short, leaf), trieType (state, storage)
type TrieNodeInfo struct {
	NodeHash common.Hash

	Path []byte

	AddrHash common.Hash

	Depth int64

	NodeType string

	EncodedNode []byte
}

func (tni *TrieNodeInfo) WriteTrieNodeInfo(infosFile *os.File) {
	writeStr := ""
	delimiter := ","

	writeStr += tni.NodeHash.Hex() + delimiter
	writeStr += hex.EncodeToString(tni.Path) + delimiter
	writeStr += tni.AddrHash.Hex() + delimiter
	writeStr += strconv.FormatInt(tni.Depth, 10) + delimiter
	writeStr += tni.NodeType + delimiter
	writeStr += hex.EncodeToString(tni.EncodedNode) + delimiter

	if _, err := infosFile.WriteString(writeStr + "\n"); err != nil {
		fmt.Println("ERROR: at WriteTrieNodeInfo() ->", err)
		os.Exit(1)
	}
}

func NewTrieStat(rootHash common.Hash) *TrieStat {
	ts := new(TrieStat)
	ts.RootHash = rootHash
	ts.NodeNums = make(map[string]int64)
	ts.NodeSizes = make(map[string]int64)
	ts.DepthInfos = make(map[string]int64)
	ts.StorageTrieSizes = make([]int64, 0)
	ts.StorageTrieNodeNums = make([]int64, 0)
	ts.StorageTriesCodeSizes = make([]int64, 0)
	return ts
}

func (ts *TrieStat) GetTrieSize() int64 {
	trieSize := int64(0)
	for _, size := range ts.NodeSizes {
		trieSize += size
	}
	return trieSize
}

func (ts *TrieStat) GetTrieNodeNum() int64 {
	nodeNum := int64(0)
	for _, num := range ts.NodeNums {
		nodeNum += num
	}
	return nodeNum
}

// TODO(jmlee): sizeInDb is based on hash-based only, also implement path-based size
func (ts *TrieStat) Print() {
	fmt.Println("\n******************************")
	fmt.Println("print trie stats")
	fmt.Println("  root hash:", ts.RootHash.Hex())

	fmt.Println("\nState trie node infos")
	mapKeys := make([]string, 0)
	for k, _ := range ts.NodeNums {
		mapKeys = append(mapKeys, k)
	}
	sort.Strings(mapKeys)
	for _, nodeType := range mapKeys {
		fmt.Println("  for", nodeType, "-> num:", ts.NodeNums[nodeType], "/ size:", ts.NodeSizes[nodeType], "B")
	}
	stateTrieNodeNum := ts.GetTrieNodeNum()
	stateTrieSize := ts.GetTrieSize()
	stateTrieSizeInDb := stateTrieSize + 32*stateTrieNodeNum
	fmt.Println("  total node num:", stateTrieNodeNum)
	fmt.Println("  total node size:", stateTrieSize, "B + 32 *", stateTrieNodeNum, "B")
	fmt.Println("    =>", float64(stateTrieSizeInDb)/1000000, "MB in database")

	fmt.Println("\nState trie depth infos (roo node's depth is 1)")
	for infoType, value := range ts.DepthInfos {
		fmt.Println("  ", infoType, ": ", value)
	}
	if ts.DepthInfos["sum"] != 0 && ts.NodeNums["leaf"] != 0 {
		fmt.Println("  avg:", float64(ts.DepthInfos["sum"])/float64(ts.NodeNums["leaf"]))
	}

	fmt.Println("\nEOA num:", ts.NodeNums["leaf"]-int64(len(ts.StorageTriesCodeSizes)))
	fmt.Println("CA num:", len(ts.StorageTriesCodeSizes))
	fmt.Println("total accounts size:", float64(ts.ValueSizeSum)/1000000, "MB")

	fmt.Println("\nStorage trie infos")
	var storageTriesSizeInDb int64
	var codesSizeInDb int64
	if len(ts.StorageTrieSizes) != 0 {
		fmt.Println("  # of storage tries:", len(ts.StorageTrieSizes), "(including empty tries)")
		minSize := int64(0) // non-zero size
		maxSize := int64(0)
		storageTriesSize := int64(0)
		storageTriesNodeNum := int64(0)
		codeSizeSum := int64(0)
		for i, size := range ts.StorageTrieSizes {
			storageTriesSize += size
			if size > 0 && (minSize > size || minSize == 0) {
				minSize = size
			}
			if maxSize < size {
				maxSize = size
			}
			storageTriesNodeNum += ts.StorageTrieNodeNums[i]
			codeSizeSum += ts.StorageTriesCodeSizes[i]
		}
		storageTriesSizeInDb = storageTriesSize + 32*storageTriesNodeNum
		codesSizeInDb = codeSizeSum + 33*int64(len(ts.StorageTriesCodeSizes))
		fmt.Println("  min size:", minSize, "B (among non-empty storage tries)")
		fmt.Println("  avg size:", float64(storageTriesSize)/float64(int64(len(ts.StorageTrieSizes))), "B")
		fmt.Println("  max size:", maxSize, "B")
		fmt.Println("  storage tries' total size:", storageTriesSize, "B + 32 *", storageTriesNodeNum)
		fmt.Println("    =>", float64(storageTriesSizeInDb)/1000000, "MB in database")
		fmt.Println("  contract codes num:", len(ts.StorageTriesCodeSizes))
		fmt.Println("  code size sum:", codeSizeSum, "B (this may count same codes multiple times)")
	} else {
		fmt.Println("  this trie may not be state trie or has no storage tries")
	}

	fmt.Println("\nSnapshot infos")
	fmt.Println("  accounts:", ts.SnapshotStats.Accounts)
	fmt.Println("  slots:", ts.SnapshotStats.Slots)
	fmt.Println("  storage:", ts.SnapshotStats.Storage)

	fmt.Println("\nTotal size of state trie + storage tries + contract codes in database")
	fmt.Println("  =>", float64(stateTrieSizeInDb)/1000000, "+", float64(storageTriesSizeInDb)/1000000, "+", float64(codesSizeInDb)/1000000, "=", float64(stateTrieSizeInDb+storageTriesSizeInDb)/1000000, "MB")

	fmt.Println("\nElapsed time:", ts.ElapsedTime, "ns")
	fmt.Println("******************************\n")
}

// TODO(jmlee): add trie copy options -> hash-based, hash-based with snapshot, path-based, path-based with snapshot
func InspectAndCopyState(blockNum uint64, rootHash common.Hash, originDB ethdb.Database, copyStateHash, copyStateHashSnapshot, copyStatePath, copyStatePathSnapshot bool) {

	startTime := time.Now()

	fmt.Println("InspectAndCopyState executed! -> root:", rootHash.Hex())
	stateTrieStat := NewTrieStat(rootHash)

	if saveTrieNodeInfos {
		stateTrieStat.trieNodeInfosFile, _ = os.OpenFile(trieNodeInfosPath+"trieNodeInfos_"+strconv.FormatUint(blockNum, 10)+"_"+rootHash.Hex(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	}

	copyDBs := make(map[string]ethdb.Database)
	if copyStateHash {
		copyDBs["H"] = openLevelDB(blockNum, rootHash, "_hash")
		defer func() {
			copyDB := copyDBs["H"]
			err := copyDB.Sync()
			if err != nil {
				fmt.Println("ERROR: copyDBs[H] Sync error:", err)
				os.Exit(1)
			}
			err = copyDB.Close()
			if err != nil {
				fmt.Println("ERROR: copyDBs[H] Close error ->", err)
				os.Exit(1)
			}
		}()
	}
	if copyStateHashSnapshot {
		copyDBs["HS"] = openLevelDB(blockNum, rootHash, "_hashSnapshot")
		defer func() {
			copyDB := copyDBs["HS"]

			// need to write empty journalGenerator
			entry := journalGenerator{
				Done:   true,
				Marker: nil,
			}
			// entry.Accounts = stateTrieStat.SnapshotStats.accounts
			// entry.Slots = stateTrieStat.SnapshotStats.slots
			// entry.Storage = uint64(stateTrieStat.SnapshotStats.storage)
			fmt.Println("print entry infos")
			fmt.Println("  Done:", entry.Done)
			fmt.Println("  Marker:", entry.Marker)
			fmt.Println("  Accounts:", entry.Accounts)
			fmt.Println("  Slots:", entry.Slots)
			fmt.Println("  Storage:", entry.Storage)
			blob, err := rlp.EncodeToBytes(entry)
			if err != nil {
				fmt.Println("ERROR: encoding entry error ->", err)
				os.Exit(1)
			}
			err = copyDB.Put(snapshotGeneratorKey, blob)
			if err != nil {
				fmt.Println("ERROR: copyDBs[HS] write error:", err)
				fmt.Println("  key:", snapshotGeneratorKey)
				fmt.Println("  value:", blob)
				os.Exit(1)
			}

			err = copyDB.Put(rawdb.SnapshotRootKey, rootHash.Bytes())
			if err != nil {
				fmt.Println("ERROR: copyDBs[HS] write error:", err)
				fmt.Println("  key:", rawdb.SnapshotRootKey)
				fmt.Println("  value:", rootHash.Hex())
				os.Exit(1)
			}
			err = copyDB.Sync()
			if err != nil {
				fmt.Println("ERROR: copyDBs[HS] Sync error:", err)
				os.Exit(1)
			}
			err = copyDB.Close()
			if err != nil {
				fmt.Println("ERROR: copyDBs[HS] Close error ->", err)
				os.Exit(1)
			}
		}()
	}
	if copyStatePath {
		copyDBs["P"] = openLevelDB(blockNum, rootHash, "_path")
		defer func() {
			copyDB := copyDBs["P"]
			err := copyDB.Sync()
			if err != nil {
				fmt.Println("ERROR: copyDBs[P] Sync error:", err)
				os.Exit(1)
			}
			err = copyDB.Close()
			if err != nil {
				fmt.Println("ERROR: copyDBs[P] Close error ->", err)
				os.Exit(1)
			}
		}()
	}
	if copyStatePathSnapshot {
		copyDBs["PS"] = openLevelDB(blockNum, rootHash, "_pathSnapshot")
		defer func() {
			copyDB := copyDBs["PS"]

			// need to write empty journalGenerator
			entry := journalGenerator{
				Done:   true,
				Marker: nil,
			}
			// entry.Accounts = stateTrieStat.SnapshotStats.accounts
			// entry.Slots = stateTrieStat.SnapshotStats.slots
			// entry.Storage = uint64(stateTrieStat.SnapshotStats.storage)
			blob, err := rlp.EncodeToBytes(entry)
			if err != nil {
				fmt.Println("ERROR: encoding entry error ->", err)
				os.Exit(1)
			}
			err = copyDB.Put(snapshotGeneratorKey, blob)
			if err != nil {
				fmt.Println("ERROR: copyDBs[PS] write error:", err)
				fmt.Println("  key:", snapshotGeneratorKey)
				fmt.Println("  value:", blob)
				os.Exit(1)
			}

			err = copyDB.Put(rawdb.SnapshotRootKey, rootHash.Bytes())
			if err != nil {
				fmt.Println("ERROR: copyDBs[PS] write error:", err)
				fmt.Println("  key:", rawdb.SnapshotRootKey)
				fmt.Println("  value:", rootHash.Hex())
				os.Exit(1)
			}
			err = copyDB.Sync()
			if err != nil {
				fmt.Println("ERROR: copyDBs[PS] Sync error:", err)
				os.Exit(1)
			}
			err = copyDB.Close()
			if err != nil {
				fmt.Println("ERROR: copyDBs[PS] Close error ->", err)
				os.Exit(1)
			}
		}()
	}

	// root node's depth is 1
	inspectAndCopyState(stateTrieStat, rootHash, 1, startTime, originDB, copyDBs, nil)

	stateTrieStat.ElapsedTime = time.Since(startTime).Nanoseconds()

	// print trie stats
	stateTrieStat.Print()

	//
	// save trie inspect result as json
	//
	inspectResults := make(map[string]*TrieStat)
	inspectResults[rootHash.Hex()] = stateTrieStat
	var jsonData []byte
	var err error
	// save all CacheStats at once
	jsonData, err = json.MarshalIndent(inspectResults, "", "  ")
	if err != nil {
		fmt.Println("JSON marshaling error:", err)
		return
	}
	fileName := "inspectResult_" + strconv.FormatUint(blockNum, 10) + "_" + rootHash.Hex() + ".json"
	err = os.WriteFile(trieInspectResultPathPrefix+fileName, jsonData, 0644)
	if err != nil {
		fmt.Println("File write error:", err)
		return
	}

	fmt.Println("elapsed time:", time.Since(startTime))
}

func inspectAndCopyState(ts *TrieStat, nodeHash common.Hash, depth int64, startTime time.Time, originDB ethdb.Database, copyDBs map[string]ethdb.Database, nodePath []byte) {

	totalNodeNum := ts.GetTrieNodeNum()
	if totalNodeNum%10000 == 0 && len(ts.StorageTrieSizes) > 0 {
		fmt.Print("\r  show intermediate result -> node num: ", totalNodeNum, " / size: ", ts.GetTrieSize()/1000000, " MB / elapsed time: ", time.Since(startTime))
	}

	var tni TrieNodeInfo

	// read trie node from disk
	var blob []byte
	if common.IsPathScheme {
		var nodeKey []byte
		if ts.AddrHash == common.HexToHash("0x0") {
			// this trie is state trie
			nodeKey = accountTrieNodeKey(nodePath)
		} else {
			// this trie is storage trie
			nodeKey = storageTrieNodeKey(ts.AddrHash, nodePath)
		}
		blob, _ = originDB.Get(nodeKey)
	} else {
		blob, _ = originDB.Get(nodeHash.Bytes())
	}

	if len(blob) == 0 {
		fmt.Println("ERROR: this node does not exist -> nodeHash:", nodeHash.Hex())
		os.Exit(1)
	}

	if copyDB, exist := copyDBs["H"]; exist {
		err := copyDB.Put(nodeHash.Bytes(), blob)
		if err != nil {
			fmt.Println("ERROR: copyDBs[H] write error:", err)
			fmt.Println("  key:", nodeHash.Hex())
			fmt.Println("  value:", blob)
			os.Exit(1)
		}
	}
	if copyDB, exist := copyDBs["HS"]; exist {
		err := copyDB.Put(nodeHash.Bytes(), blob)
		if err != nil {
			fmt.Println("ERROR: copyDBs[HS] write error:", err)
			fmt.Println("  key:", nodeHash.Hex())
			fmt.Println("  value:", blob)
			os.Exit(1)
		}
	}
	if copyDB, exist := copyDBs["P"]; exist {
		var nodeKey []byte
		if ts.AddrHash == common.HexToHash("0x0") {
			// this trie is state trie
			nodeKey = accountTrieNodeKey(nodePath)
		} else {
			// this trie is storage trie
			nodeKey = storageTrieNodeKey(ts.AddrHash, nodePath)
		}
		err := copyDB.Put(nodeKey, blob)
		if err != nil {
			fmt.Println("ERROR: copyDBs[P] write error:", err)
			fmt.Println("  key:", nodeKey)
			fmt.Println("  value:", blob)
			os.Exit(1)
		}
	}
	if copyDB, exist := copyDBs["PS"]; exist {
		var nodeKey []byte
		if ts.AddrHash == common.HexToHash("0x0") {
			// this trie is state trie
			nodeKey = accountTrieNodeKey(nodePath)
		} else {
			// this trie is storage trie
			nodeKey = storageTrieNodeKey(ts.AddrHash, nodePath)
		}
		err := copyDB.Put(nodeKey, blob)
		if err != nil {
			fmt.Println("ERROR: copyDBs[PS] write error:", err)
			fmt.Println("  key:", nodeKey)
			fmt.Println("  value:", blob)
			os.Exit(1)
		}
	}

	// decode trie node
	decNode := mustDecodeNode(nodeHash.Bytes(), blob)

	// collect trie stats
	nodeType := ""
	switch node := decNode.(type) {
	case *shortNode:

		nodePath = append(nodePath, node.Key...)

		if hasTerm(node.Key) {
			// this is leaf node
			ts.NodeNums["leaf"]++
			ts.NodeSizes["leaf"] += int64(len(blob))
			nodeType = "leaf"

			ts.DepthInfos["sum"] += depth
			if ts.DepthInfos["min"] > depth || ts.DepthInfos["min"] == 0 {
				ts.DepthInfos["min"] = depth
			}
			if ts.DepthInfos["max"] < depth {
				ts.DepthInfos["max"] = depth
			}

			// measure value size
			value := node.Val.(valueNode)
			ts.ValueSizeSum += int64(len(value))

			// try to decode value as an account
			var acc types.StateAccount
			err := rlp.DecodeBytes(value, &acc)
			if err != nil {
				// this is storage trie's leaf node

				ts.SnapshotStats.Slots++
				ts.SnapshotStats.Storage += common.StorageSize(1 + 2*common.HashLength + len(value))

				if copyDB, exist := copyDBs["HS"]; exist {
					addrHashBytes := ts.AddrHash.Bytes()
					storageHashBytes := hexToKeybytes(nodePath)
					keySuffix := append(addrHashBytes, storageHashBytes...)
					err := copyDB.Put(append(rawdb.SnapshotStoragePrefix[:], keySuffix...), value)
					if err != nil {
						fmt.Println("ERROR: copyDBs[HS] write error:", err)
						fmt.Println("  key:", append(rawdb.SnapshotStoragePrefix[:], keySuffix...))
						fmt.Println("  value:", value)
						os.Exit(1)
					}
				}

			} else {
				// this is state trie's leaf node

				addrHashBytes := hexToKeybytes(nodePath)
				slimAccData := types.SlimAccountRLP(acc)

				ts.SnapshotStats.Accounts++
				ts.SnapshotStats.Storage += common.StorageSize(1 + common.HashLength + len(slimAccData))

				if copyDB, exist := copyDBs["HS"]; exist {
					err := copyDB.Put(append(rawdb.SnapshotAccountPrefix[:], addrHashBytes...), slimAccData)
					if err != nil {
						fmt.Println("ERROR: copyDBs[HS] write error:", err)
						fmt.Println("  key:", append(rawdb.SnapshotAccountPrefix[:], addrHashBytes...))
						fmt.Println("  value:", slimAccData)
						os.Exit(1)
					}
				}

				if common.BytesToHash(acc.CodeHash) != types.EmptyCodeHash {
					// this account is CA

					codeBlob, _ := originDB.Get(append(rawdb.CodePrefix[:], acc.CodeHash...))
					if len(codeBlob) == 0 {
						fmt.Println("ERROR: this contract code does not exist -> codeHash:", common.BytesToHash(acc.CodeHash).Hex())
						os.Exit(1)
					}
					ts.StorageTriesCodeSizes = append(ts.StorageTriesCodeSizes, int64(len(codeBlob)))

					for i, copyDB := range copyDBs {
						err := copyDB.Put(append(rawdb.CodePrefix[:], acc.CodeHash...), codeBlob)
						if err != nil {
							fmt.Println("ERROR: copyDBs[", i, "] write contract code error:", err)
							fmt.Println("  key:", append(rawdb.CodePrefix[:], acc.CodeHash...))
							fmt.Println("  value:", codeBlob)
							os.Exit(1)
						}
					}

					if acc.Root != types.EmptyRootHash {
						// this CA has non-empty storage trie
						storageTrieStat := NewTrieStat(acc.Root)
						storageTrieStat.AddrHash = common.BytesToHash(addrHashBytes)
						storageTrieStat.trieNodeInfosFile = ts.trieNodeInfosFile

						// root node's depth is 1
						inspectAndCopyState(storageTrieStat, acc.Root, 1, startTime, originDB, copyDBs, nil)

						ts.StorageTrieSizes = append(ts.StorageTrieSizes, storageTrieStat.GetTrieSize())
						ts.StorageTrieNodeNums = append(ts.StorageTrieNodeNums, storageTrieStat.GetTrieNodeNum())
						ts.SnapshotStats.Slots += storageTrieStat.SnapshotStats.Slots
						ts.SnapshotStats.Storage += storageTrieStat.SnapshotStats.Storage
					} else {
						// this CA has empty storage trie
						ts.StorageTrieSizes = append(ts.StorageTrieSizes, 0)
						ts.StorageTrieNodeNums = append(ts.StorageTrieNodeNums, 0)
					}
				} else {
					// this account is EoA

				}

			}

		} else {
			// this is intermediate short node
			ts.NodeNums["short"]++
			ts.NodeSizes["short"] += int64(len(blob))
			nodeType = "short"

			childHashNode := (node.Val).(hashNode)
			childHash := common.BytesToHash(childHashNode)

			inspectAndCopyState(ts, childHash, depth+1, startTime, originDB, copyDBs, nodePath)
		}

	case *fullNode:

		ts.NodeNums["full"]++
		ts.NodeSizes["full"] += int64(len(blob))
		nodeType = "full"

		for i := 0; i < 17; i++ {
			if node.Children[i] != nil {
				if childHashNode, ok := (node.Children[i]).(hashNode); ok {
					childHash := common.BytesToHash(childHashNode)
					nodeKeyByte := common.HexToHash("0x" + indices[i])
					childNodePath := append(nodePath, nodeKeyByte[len(nodeKeyByte)-1])
					inspectAndCopyState(ts, childHash, depth+1, startTime, originDB, copyDBs, childNodePath)
				}
			}
		}

	default:
		panic(fmt.Sprintf("unknown node: %+v", decNode))
	}

	// collect trie node infos
	if ts.trieNodeInfosFile != nil {
		tni.NodeHash = nodeHash
		tni.Path = nodePath
		tni.AddrHash = ts.AddrHash
		tni.Depth = depth
		tni.NodeType = nodeType
		tni.EncodedNode = blob

		tni.WriteTrieNodeInfo(ts.trieNodeInfosFile)
	}

}
