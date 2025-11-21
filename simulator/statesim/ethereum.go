package statesim

import (
	"fmt"
	"math/big"
	"os"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/fdlimit"
	"github.com/ethereum/go-ethereum/common/prque"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/state/snapshot"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/ethereum/go-ethereum/triedb/hashdb"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
	realleveldb "github.com/syndtr/goleveldb/leveldb"
)

var (
	//
	// database
	//
	// choose leveldb vs memorydb
	useLeveldb = true
	// leveldb path ($ sudo chmod -R 777 /ethereum)
	leveldbPathPrefix = "/ethereum/state_simulator_jmlee/port_"
	leveldbPath       = leveldbPathPrefix + ServerPort
	// leveldb cache size (MB) (archive mode: 2048, full mode: 2048, min: 16) (memory leak might occur when calling reset() frequently with too big cache size)
	leveldbCache int
	// leveldb options
	leveldbNamespace = "eth/db/chaindata/"
	leveldbReadonly  = false
	// # of max open files for leveldb (Geth default: 524288)
	leveldbHandles = 524288
	// disk to store trie nodes (either leveldb or memorydb)
	diskdb   ethdb.KeyValueStore
	frdiskdb ethdb.Database

	// trie's database including diskdb and clean cache
	mainTrieDB  *triedb.Database // state trie (Ethereum, Ethanos) or active trie (Ethane)
	indepTrieDB *triedb.Database // independent trie db for reading cached tries (is it needed for fair performance measure?)
	// trie cache size (MB) (archive mode: 1228, full mode: 614, min: 32)
	trieCacheSize int

	// TODO(jmlee): implement non-archive mode
	myTriegc        = prque.New[int64, common.Hash](nil) // Priority queue mapping block numbers to tries to gc
	myGcproc        time.Duration                        // Accumulates canonical block processing for trie dumping
	myLastWrite     uint64                               // Last block when the state was flushed
	myFlushInterval atomic.Int64                         // Time interval (processing time) after which to flush a state
	dirtyCacheSize  int                                  // path-based trie's dirty cache or hash-based trie's gc limit

	// Geth's snapshot
	mySnaps *snapshot.Tree
	// snapshot cache size (MB) (archive mode: 820, full mode: 410, min: 0)
	snapshotCacheSize int

	// sum of all cache sizes (MB) (default: 4096)
	totalCacheSize = 4096

	//
	// for EVM simulation
	//

	myChainConfig = params.MainnetChainConfig

	stateCache state.Database

	// TODO(jmlee): split txs for their purposes
	txList           = make([]*types.Transaction, 0)
	txArgsList       = make([]*core.TransactionArgs, 0)
	attackTxArgsList = make([]*core.TransactionArgs, 0) // txs for DoS attack

	myChainContext = MyChainContext{
		Headers: make(map[uint64]*types.Header, 0),
	}

	// uncleInfos[blockNum] = uncleInfo
	uncleInfos = make(map[uint64][]*UncleInfo, 0)

	currentBlockNum  = uint64(0)
	currentStateRoot = common.Hash{}

	enabledExpensive = true // measure performance metrics or not (same as metrics.EnabledExpensive)
)

func SetDbPath(dbPath string) {
	if dbPath == "" {
		leveldbPath = leveldbPathPrefix + ServerPort
	} else {
		leveldbPath = dbPath
	}
}

func openLevelDB(dbPath string) (ethdb.KeyValueStore, ethdb.Database) {
	dbPath = leveldbPathPrefix + ServerPort + dbPath
	fmt.Println("set leveldb at:", dbPath)

	kvdb, err := leveldb.New(dbPath, leveldbCache, leveldbHandles, leveldbNamespace, leveldbReadonly)
	if err != nil {
		fmt.Println("leveldb.New error!! ->", err)
		os.Exit(1)
	}
	fmt.Println("leveldb cache size:", leveldbCache, "MB")
	frdb, err := rawdb.NewDatabaseWithFreezer(kvdb, dbPath, leveldbNamespace, leveldbReadonly)
	if err != nil {
		fmt.Println("frdb error:", err)
		os.Exit(1)
	}

	return kvdb, frdb
}

// set disk, stateDB, trieDB
func setDatabase(deleteDisk bool) {

	// set flush interval for non-archive mode (default: 60 mins)
	myFlushInterval.Store(int64(60 * time.Minute))
	// myFlushInterval.Store(int64(1 * time.Millisecond))

	//
	// set cache sizes
	//

	if common.IsArchiveMode && common.IsPathScheme {
		fmt.Println("ERROR: path-based state should be non-archive mode")
		fmt.Println("  common.IsPathScheme:", common.IsPathScheme)
		fmt.Println("  common.IsArchiveMode:", common.IsArchiveMode)
		os.Exit(1)
	}

	if common.IsArchiveMode {
		leveldbCache = totalCacheSize * 50 / 100      // 50%
		dirtyCacheSize = 0                            // 0%
		snapshotCacheSize = totalCacheSize * 20 / 100 // 20%
		trieCacheSize = totalCacheSize * 30 / 100     // 30%
		if !common.EnableSnapshot {
			// snapshot's cache -> trie's clean cache
			snapshotCacheSize = 0                     // 20% -> 0%
			trieCacheSize = totalCacheSize * 50 / 100 // 30% -> 50%
		}
	} else {
		leveldbCache = totalCacheSize * 50 / 100      // 50%
		dirtyCacheSize = totalCacheSize * 25 / 100    // 25%
		snapshotCacheSize = totalCacheSize * 10 / 100 // 10%
		trieCacheSize = totalCacheSize * 15 / 100     // 15%
		if !common.EnableSnapshot {
			// snapshot's cache -> trie's clean cache
			snapshotCacheSize = 0                     // 10% -> 0%
			trieCacheSize = totalCacheSize * 25 / 100 // 15% -> 25%
		}
	}

	//
	// set modifyHash options
	//
	if common.ModifyHashMethod == "" {
		// do nothing

	} else if common.ModifyHashMethod == "JMT" {

		//
		// v1 -> state/storage key: version 8 + path 54 + path len 2
		// infeasible design: key collision occurs between state trie node and storage trie node that have the same path
		//

		//
		// v2 -> state/storage key: version 8 + path 46 + nodeHash 10
		//

		// version
		common.VersionLength = 8
		common.EnableVersionPadding = true

		// path
		common.PathLength = 46
		common.FixedPathLength = true
		common.PathPaddingAtEnd = true
		common.AppendPathFirst = false
		common.AppendPathLen = false
		common.LenOfPathLen = 2

		//
		common.LastPaddingBound = 54

		// section byte
		common.AppendTrieType = false

		// CA's addrHash
		common.AppendContractAddrHash = false
		common.AddrHashPrefixLen = 0

	} else if common.ModifyHashMethod == "JMT_fixed" {

		// TODO(jmlee): is this optimal for JMT?

		// state key: version 8 + section 1 + path 53 + path len 2
		// storage key: version 8 + section 1 + addrHash 24 + path 29 + path len 2

		// version
		common.VersionLength = 8
		common.EnableVersionPadding = true

		// path
		common.PathLength = 53
		common.FixedPathLength = true
		common.PathPaddingAtEnd = true
		common.AppendPathFirst = false
		common.AppendPathLen = true
		common.LenOfPathLen = 2

		//
		common.LastPaddingBound = 62

		// section byte
		common.AppendTrieType = true

		// CA's addrHash
		common.AppendContractAddrHash = true
		common.AddrHashPrefixLen = 24

	} else if common.ModifyHashMethod == "JMT_balanced" {

		//
		// v2 -> state/storage key: version 27 + path 27 + nodeHash 10
		//

		// version
		common.VersionLength = 27
		common.EnableVersionPadding = true

		// path
		common.PathLength = 27
		common.FixedPathLength = true
		common.PathPaddingAtEnd = true
		common.AppendPathFirst = false
		common.AppendPathLen = false
		common.LenOfPathLen = 2

		//
		common.LastPaddingBound = 54

		// section byte
		common.AppendTrieType = false

		// CA's addrHash
		common.AppendContractAddrHash = false
		common.AddrHashPrefixLen = 0

	} else if common.ModifyHashMethod == "PrefixTree" {

		//
		// v1: state/storage key: path 48 + version 8 + nodeHash 8
		// infeasible design: error at block 5,871,711 / tx index 35 -> gas limit reached
		//

		//
		// v2: state/storage key: path 46 + version 8 + nodeHash 10 -> okay until 6M block
		//

		// version
		common.VersionLength = 8
		common.EnableVersionPadding = true

		// path
		common.PathLength = 46
		common.FixedPathLength = true
		common.PathPaddingAtEnd = true
		common.AppendPathFirst = true
		common.AppendPathLen = false
		common.LenOfPathLen = 2

		//
		common.LastPaddingBound = 54

		// section byte
		common.AppendTrieType = false

		// CA's addrHash
		common.AppendContractAddrHash = false
		common.AddrHashPrefixLen = 0

	} else if common.ModifyHashMethod == "PrefixTree_balanced" {

		//
		// v2: state/storage key: path 27 + version 27 + nodeHash 10 -> okay until 6M block
		//

		// version
		common.VersionLength = 27
		common.EnableVersionPadding = true

		// path
		common.PathLength = 27
		common.FixedPathLength = true
		common.PathPaddingAtEnd = true
		common.AppendPathFirst = true
		common.AppendPathLen = false
		common.LenOfPathLen = 2

		//
		common.LastPaddingBound = 54

		// section byte
		common.AppendTrieType = false

		// CA's addrHash
		common.AppendContractAddrHash = false
		common.AddrHashPrefixLen = 0

	} else if common.ModifyHashMethod == "PrefixTree_fixed" {

		// TODO(jmlee): is this optimal for PrefixTree?

		// state key: section 1 + path 53 + version 8 + path len 2
		// storage key: section 1 + addrHash 24 + path 29 + version 8 + path len 2

		// version
		common.VersionLength = 8
		common.EnableVersionPadding = true

		// path
		common.PathLength = 53
		common.FixedPathLength = true
		common.PathPaddingAtEnd = true
		common.AppendPathFirst = true
		common.AppendPathLen = true
		common.LenOfPathLen = 2

		//
		common.LastPaddingBound = 0

		// section byte
		common.AppendTrieType = true

		// CA's addrHash
		common.AppendContractAddrHash = true
		common.AddrHashPrefixLen = 24

	} else if common.ModifyHashMethod == "HalfPath" {

		// state key: section 1 + path 24 + nodeHash 37 + path len 2
		// storage key: section 1 + addrHash 24 + path 24 + nodeHash 13 + path len 2

		// version
		common.VersionLength = 0
		common.EnableVersionPadding = true

		// path
		common.PathLength = 24
		common.FixedPathLength = true
		common.PathPaddingAtEnd = true
		common.AppendPathFirst = true
		common.AppendPathLen = true
		common.LenOfPathLen = 2

		//
		common.LastPaddingBound = 0

		// section byte
		common.AppendTrieType = true

		// CA's addrHash
		common.AppendContractAddrHash = true
		common.AddrHashPrefixLen = 24

	} else if common.ModifyHashMethod == "PBSS" {

		// activate PBSS options
		common.IsArchiveMode = false
		common.IsPathScheme = true

		// original hash-based Ethereum

		// version
		common.VersionLength = 0
		common.EnableVersionPadding = true

		// path
		common.PathLength = 0
		common.FixedPathLength = true
		common.PathPaddingAtEnd = true
		common.AppendPathFirst = true
		common.AppendPathLen = false
		common.LenOfPathLen = 2

		//
		common.LastPaddingBound = 0

		// section byte
		common.AppendTrieType = false

		// CA's addrHash
		common.AppendContractAddrHash = false
		common.AddrHashPrefixLen = 0

		//
		// TODO(jmlee): Geth does not work properly in situations where nodes with the same node hash are created in several blocks
		// just activate common.IsPathScheme option or find other proper ways
		//

		// // state key: section 1 + path 24 + 0-padding 37 + path len 2
		// // storage key: section 1 + addrHash 24 + path 24 + 0-padding 13 + path len 2

		// // version
		// common.VersionLength = 0
		// common.EnableVersionPadding = true

		// // path
		// common.PathLength = 24
		// common.FixedPathLength = true
		// common.PathPaddingAtEnd = true
		// common.AppendPathFirst = true
		// common.AppendPathLen = true
		// common.LenOfPathLen = 2

		// //
		// common.LastPaddingBound = 62

		// // section byte
		// common.AppendTrieType = true

		// // CA's addrHash
		// common.AppendContractAddrHash = true
		// common.AddrHashPrefixLen = 24

	} else if common.ModifyHashMethod == "TH" {

		// state/storage key: version 8 + nodeHash 56

		// version
		common.VersionLength = 8
		common.EnableVersionPadding = true

		// path
		common.PathLength = 0
		common.FixedPathLength = false
		common.PathPaddingAtEnd = false
		common.AppendPathFirst = false
		common.AppendPathLen = false
		common.LenOfPathLen = 2

		//
		common.LastPaddingBound = 0

		// section byte
		common.AppendTrieType = false

		// CA's addrHash
		common.AppendContractAddrHash = false
		common.AddrHashPrefixLen = 0

	} else if common.ModifyHashMethod == "none" {

		// original hash-based Ethereum

		// version
		common.VersionLength = 0
		common.EnableVersionPadding = true

		// path
		common.PathLength = 0
		common.FixedPathLength = true
		common.PathPaddingAtEnd = true
		common.AppendPathFirst = true
		common.AppendPathLen = false
		common.LenOfPathLen = 2

		//
		common.LastPaddingBound = 0

		// section byte
		common.AppendTrieType = false

		// CA's addrHash
		common.AppendContractAddrHash = false
		common.AddrHashPrefixLen = 0

	} else {
		fmt.Println("ERROR: unknown ModifyHashMethod:", common.ModifyHashMethod)
		os.Exit(1)
	}

	//
	// set diskdb (TODO(jmlee): enable pebbleDB)
	//

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
	leveldbHandles = int(raised / 2)
	fmt.Println("open file limit:", limit, "/ raised:", raised, "/ leveldbHandles:", leveldbHandles)

	// reset normal trie
	if diskdb != nil {
		diskdb.Close()
	}
	if frdiskdb != nil {
		frdiskdb.Close()
	}
	if useLeveldb {
		if common.LoggingReadStats != realleveldb.IsLogging {
			fmt.Println("ERROR: set leveldb's branch properly -> benchmark vs noBenchmark")
			fmt.Println("  common.LoggingReadStats:", common.LoggingReadStats)
			fmt.Println("  realleveldb.IsLogging:", realleveldb.IsLogging)
			os.Exit(1)
		}

		// fmt.Println("before leveldb path:", leveldbPath, "/ port:", ServerPort)
		// leveldbPath = leveldbPathPrefix + ServerPort
		fmt.Println("set leveldb at:", leveldbPath)
		// if do not delete directory, this just reopens existing db
		if deleteDisk {
			fmt.Println("delete disk, open new disk")
			err := os.RemoveAll(leveldbPath)
			if err != nil {
				fmt.Println("RemoveAll error ! ->", err)
			}
		} else {
			fmt.Println("do not delete disk, open old db if it exist")
		}

		kvdb, err := leveldb.New(leveldbPath, leveldbCache, leveldbHandles, leveldbNamespace, leveldbReadonly)
		if err != nil {
			fmt.Println("leveldb.New error!! ->", err)
			os.Exit(1)
		}
		fmt.Println("leveldb cache size:", leveldbCache, "MB")
		frdb, err := rawdb.NewDatabaseWithFreezer(kvdb, leveldbPath, leveldbNamespace, leveldbReadonly)
		if err != nil {
			fmt.Println("frdb error:", err)
			os.Exit(1)
		}
		diskdb = kvdb
		frdiskdb = frdb
	} else {
		fmt.Println("set memorydb")
		diskdb = memorydb.New()
	}

	fmt.Println("1")

	//
	// set triedb
	//
	triedbConfig := &triedb.Config{Preimages: false} // TODO(jmlee): dafault is false, push this later
	if common.IsPathScheme {
		fmt.Println("set path based scheme")
		triedbConfig.PathDB = &pathdb.Config{
			StateHistory:   90000, // (default = 90,000 blocks, 0 = entire chain)
			CleanCacheSize: trieCacheSize * 1024 * 1024,
			// TODO(jmlee): check this is right, 얘는 아마 64 ~ 256 MB 까지밖에 설정이 안되는듯함, sanitize() 함수 확인해보기
			//   메인넷에서 얼마로 설정되나 확인해보기
			//   -> 실제로 1024가 들어가도 sanitize() 함수에서 256으로 바뀌어서 설정됨
			DirtyCacheSize: dirtyCacheSize * 1024 * 1024,
		}
	} else {
		fmt.Println("set hash based scheme")
		triedbConfig.HashDB = &hashdb.Config{
			CleanCacheSize: trieCacheSize * 1024 * 1024,
		}
	}
	mainTrieDB = triedb.NewDatabase(frdiskdb, triedbConfig)
	// TODO(jmlee): path-based 에선 이렇게 2개 동시에 못여나봄? -> ㅇㅇ 그게 맞는듯
	// indepTrieDB = triedb.NewDatabase(frdiskdb, triedbConfig)
	fmt.Println("trie clean cache size:", trieCacheSize, "MB")

	//
	// set statedb
	//
	stateCache = state.NewDatabaseWithNodeDB(frdiskdb, mainTrieDB)

	// reset cache stats
	realleveldb.ResetCacheStat(0)

	fmt.Println("setDatabase() finished")
}

func isHardforkedBlock(blockNum uint64) bool {
	// Cancun: 19,426,587 - 0xbd33ab68095087d81beb810b3f5d0b16050b3f798ae3978e440bab048dd78992
	// Shanghai: 17,034,870 - 0x7fd42f5027bc18315b3781e65f19e4c8828fd5c5fce33410f0fb4fea0b65541f
	// Paris: 15,537,394 - 0x40c07091e16263270f3579385090fea02dd5f061ba6750228fcc082ff762fda7
	// London: 12,965,000 - 0x41cf6e8e60fd087d2b00360dc29e5bfb21959bce1f4c242fd1ad7c4da968eb87
	// Berlin: 12,244,000 - 0xfdec060ee45e55da9e36060fc95dddd0bdc47e447224666a895d9f0dc9adaa0c
	// Istanbul: 9,069,000 - 0xf3917914f693a985a23ee2b623f6d1c1bcffb8e597e63cf1fb0cbabb9947a9c7
	// Constantinople: 7,280,000 - 0x1e302241298f913b30f7a0df60272c9983d8d8726932f66582f182bd99ef42bc
	// Byzantium: 4,370,000 - 0xe7a73d3c05829730c750ca483b5a65f8321adb25d8abb9da23a4cbb6473464ee

	hardforkBlockNums := []uint64{4370000, 7280000, 9069000, 12244000, 12965000, 15537394, 17034870, 19426587}
	for _, hardforhardforkBlockNum := range hardforkBlockNums {
		if blockNum == hardforhardforkBlockNum {
			return true
		}

		// for test, delete this later
		// if blockNum % 10000 == 0 {
		// 	return true
		// }
	}
	return false
}

// (jmlee) ChainContext is not needed only to execute txs through EVM, so just add meaningless ChainContext-like struct
type MyChainContext struct {
	Headers map[uint64]*types.Header
}

func (mcc *MyChainContext) Engine() consensus.Engine {
	fmt.Println("ERR: MyChainContext.Engine() should not be called")
	os.Exit(1)
	return nil
}

func (mcc *MyChainContext) GetHeader(blockHash common.Hash, blockNum uint64) *types.Header {
	if header, exists := mcc.Headers[blockNum]; exists {
		// check validity
		// if header.Hash() != blockHash {
		// 	fmt.Println("MyChainContext.GetHeader(): such header does not exist")
		// 	fmt.Println("  => requested blockHash:", blockHash.Hex())
		// 	fmt.Println("  => existing blockHash:", header.Hash().Hex())
		// 	return nil
		// }

		return header
	} else {
		fmt.Println("MyChainContext.GetHeader(): header does not exist -> blocknum:", blockNum)
		return nil
	}
}

type UncleInfo struct {
	Coinbase    common.Address
	UncleHeight *big.Int
}
