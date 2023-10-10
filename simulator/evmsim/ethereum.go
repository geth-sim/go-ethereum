package evmsim

import (
	"fmt"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/fdlimit"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
)

var (
	//
	// database
	//
	// choose leveldb vs memorydb
	useLeveldb = true
	// leveldb path ($ sudo chmod -R 777 /ethereum)
	leveldbPathPrefix = "/ethereum/evm_simulator_jmlee/port_"
	leveldbPath = leveldbPathPrefix + ServerPort
	// leveldb cache size (MB) (Geth default for mainnet: 4096 * 0.5 = 2048) (memory leak might occur when calling reset() frequently with too big cache size)
	leveldbCache = 2048 // min: 16
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
	// trie cache size (MB) (Geth default for archive mainnet: 4096 * 30% = 1228)
	trieCacheSize = 1228 // min: 32 (maybe)

	//
	// for EVM simulation
	//

	myChainConfig = params.MainnetChainConfig

	stateCache state.Database

	txList     = make([]*types.Transaction, 0)
	txArgsList = make([]*core.TransactionArgs, 0)

	myChainContext = MyChainContext{
		Headers: make(map[uint64]*types.Header, 0),
	}

	// uncleInfos[blockNum] = uncleInfo
	uncleInfos = make(map[uint64][]*UncleInfo, 0)

	currentBlockNum  = uint64(0)
	currentStateRoot = common.Hash{}

	enabledExpensive = true // measure performance metrics or not (same as metrics.EnabledExpensive)
)

// set disk, stateDB, trieDB
func setDatabase(deleteDisk bool) {
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
		Preimages: true,          // default: true
	})
	mainTrieDB = stateCache.TrieDB()
	subTrieDB = mainTrieDB

	// independent trie db with independent cache for fair performance comparison
	indepTrieDB = trie.NewDatabaseWithConfig(frdiskdb, &trie.Config{
		Cache:     trieCacheSize, // default depends on gcmode -> "full": 614, "archive": 1228
		Preimages: true,          // default: true
	})

	// subNormTrieDB = trie.NewDatabaseWithConfig(diskdb, &trie.Config{Cache: trieCacheSize}) // if want to split clean caches
	fmt.Println("trie clean cache size:", trieCacheSize, "MB")
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
