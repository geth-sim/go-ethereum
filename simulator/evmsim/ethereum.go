package evmsim

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
	"github.com/ethereum/go-ethereum/trie"

	realleveldb "github.com/syndtr/goleveldb/leveldb"
)

var (
	//
	// database
	//
	// choose leveldb vs memorydb
	useLeveldb = true
	// leveldb path ($ sudo chmod -R 777 /ethereum)
	leveldbPathPrefix = "/ethereum/evm_simulator_jmlee/port_"
	leveldbPath       = leveldbPathPrefix + ServerPort
	// leveldb cache size (MB) (archive mode: 2048, full mode: 2048, min: 16) (memory leak might occur when calling reset() frequently with too big cache size)
	leveldbCache = 2048
	// leveldb options
	leveldbNamespace = "eth/db/chaindata/"
	leveldbReadonly  = false
	// # of max open files for leveldb (Geth default: 524288)
	leveldbHandles = 524288
	// disk to store trie nodes (either leveldb or memorydb)
	diskdb   ethdb.KeyValueStore
	frdiskdb ethdb.Database

	// trie's database including diskdb and clean cache
	mainTrieDB  *trie.Database // state trie (Ethereum, Ethanos) or active trie (Ethane)
	subTrieDB   *trie.Database // cached trie (Ethanos) or inactive trie (Ethane)
	indepTrieDB *trie.Database // independent trie db for reading cached tries (is it needed for fair performance measure?)
	// trie cache size (MB) (archive mode: 1228, full mode: 614, min: 32)
	trieCacheSize = 1228 // min: 32 (maybe)

	// Geth's snapshot
	mySnaps *snapshot.Tree
	// snapshot cache size (MB) (archive mode: 820, full mode: 410, min: 0)
	snapshotCacheSize = 820

	// sum of all cache sizes (MB) (default: 4096)
	totalCacheSize = 4096

	// TODO(jmlee): implement non-archive mode
	myTriegc        = prque.New[int64, common.Hash](nil) // Priority queue mapping block numbers to tries to gc
	myGcproc        time.Duration                        // Accumulates canonical block processing for trie dumping
	myLastWrite     uint64                               // Last block when the state was flushed
	myFlushInterval atomic.Int64                         // Time interval (processing time) after which to flush a state
	dirtyCacheSize  int

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

		leveldbPath = leveldbPathPrefix + ServerPort
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
	stateCache = state.NewDatabaseWithConfig(frdiskdb, &trie.Config{
		Cache:     trieCacheSize, // default depends on gcmode -> "full": 614, "archive": 1228
		Preimages: false,         // default: false
	})
	mainTrieDB = stateCache.TrieDB()
	subTrieDB = mainTrieDB

	// independent trie db with independent cache for fair performance comparison
	indepTrieDB = trie.NewDatabaseWithConfig(frdiskdb, &trie.Config{
		Cache:     trieCacheSize, // default depends on gcmode -> "full": 614, "archive": 1228
		Preimages: false,         // default: false
	})

	// subNormTrieDB = trie.NewDatabaseWithConfig(diskdb, &trie.Config{Cache: trieCacheSize}) // if want to split clean caches
	fmt.Println("trie clean cache size:", trieCacheSize, "MB")

	// reset leveldb cache stats
	realleveldb.ResetCacheStat(0)
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
