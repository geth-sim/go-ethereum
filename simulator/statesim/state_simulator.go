package statesim

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/state/snapshot"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb/hashdb"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
	"github.com/holiman/uint256"
	"github.com/syndtr/goleveldb/leveldb"
	"golang.org/x/crypto/sha3"
)

var (
	//
	// simulator server
	//
	// port for requests
	// 8999: test
	ServerPort = "8999"
	// maximum byte length of response
	maxResponseLen = 4097

	//
	// log files
	//
	// simulation result log file path
	logFilePath      = "./logFiles/evm/"
	simBlocksPath    = logFilePath + "simBlocks/"
	cacheStatsPath   = logFilePath + "cacheStats/"
	opcodeStatsPath  = logFilePath + "opcodeStats/"
	leveldbStatsPath = logFilePath + "leveldbStats/"
	errLogPath       = logFilePath + "errLogs/"

	//
	// etc
	//
	u256_8                 = uint256.NewInt(8)
	u256_32                = uint256.NewInt(32)
	diskSizeMeasureEpoch   = uint64(10000)
	diskSizeMeasureCnt     = 0
	diskSizeMeasureElapsed time.Duration
	saveLevelDBStatsEpoch  = uint64(100000)
)

func connHandler(conn net.Conn) {
	recvBuf := make([]byte, 4096)
	for {
		// wait for message from client
		n, err := conn.Read(recvBuf)
		if err != nil {
			if err == io.EOF {
				log.Println(err)
				return
			}
			log.Println(err)
			return
		}

		// deal with request
		if 0 < n {
			// read message from client
			data := recvBuf[:n]
			request := string(data)
			// fmt.Println("message from client:", request)

			//
			// do something with the request
			//
			response := make([]byte, maxResponseLen)
			params := strings.Split(request, ",")
			// fmt.Println("params:", params)
			switch params[0] {

			case "setDatabase":
				// fmt.Println("execute setDatabase()")
				deleteDisk, _ := strconv.ParseUint(params[1], 10, 64)
				if deleteDisk == 0 {
					setDatabase(false)
				} else {
					setDatabase(true)
				}

				response = []byte("success")

			case "setDbPath":
				// fmt.Println("execute setDbPath()")
				newDbPath := params[1]
				SetDbPath(newDbPath)

				response = []byte("success")

			// TODO(jmlee): add option for path-based state scheme
			case "setSimulationOptions":

				fmt.Println("ERROR: setSimulationOptions() is temply depreceated, do not use it or properly update it")
				os.Exit(1)

				// // fmt.Println("execute setSimulationOptions()")
				// snapshotOption, _ := strconv.ParseInt(params[1], 10, 64)
				// trieNodePrefixLen, _ := strconv.ParseInt(params[2], 10, 64)
				// opcodeLoggingOption, _ := strconv.ParseInt(params[3], 10, 64)

				// // enable snapshot or not
				// common.EnableSnapshot = (snapshotOption != 0)

				// // prefixing trie nodes or not
				// common.PrefixLength = int(trieNodePrefixLen)
				// common.EnableNodePrefixing = (trieNodePrefixLen != 0)

				// // logging or not
				// common.LoggingOpcodeStats = (opcodeLoggingOption != 0)

				// fmt.Println("setSimulationOptions complete")
				// fmt.Println("  common.EnableSnapshot:", common.EnableSnapshot)
				// fmt.Println("  common.EnableNodePrefixing:", common.EnableNodePrefixing)
				// fmt.Println("  common.PrefixLength:", common.PrefixLength)
				// fmt.Println("  common.LoggingOpcodeStats:", common.LoggingOpcodeStats)

				// response = []byte("success")

			case "getSimulationTypeName":
				typeName := common.GetSimulationTypeName()
				response = []byte(typeName)

			case "insertHeader":
				// get params
				// fmt.Println("execute insertHeader()")

				header := types.Header{}
				number, _ := strconv.ParseInt(params[1], 10, 64)
				header.Number = big.NewInt(number)
				timestamp, _ := strconv.ParseUint(params[2], 10, 64)
				header.Time = timestamp
				header.Coinbase = common.HexToAddress(params[3])
				difficulty, _ := strconv.ParseInt(params[4], 10, 64)
				header.Difficulty = big.NewInt(difficulty)
				gasUsed, _ := strconv.ParseUint(params[5], 10, 64)
				header.GasUsed = gasUsed
				header.GasUsed = 0 // TODO(jmlee): this should be 0, do not send this from python client
				gasLimit, _ := strconv.ParseUint(params[6], 10, 64)
				header.GasLimit = gasLimit
				header.Extra = common.Hex2Bytes(params[7])
				header.ParentHash = common.HexToHash(params[8])
				header.UncleHash = common.HexToHash(params[9])
				header.Root = common.HexToHash(params[10])
				nonce, _ := strconv.ParseUint(params[11], 16, 64) // params[11] is hex str
				header.Nonce = types.EncodeNonce(nonce)
				header.ReceiptHash = common.HexToHash(params[12])
				header.TxHash = common.HexToHash(params[13])
				header.MixDigest = common.HexToHash(params[14])
				header.Bloom = types.BytesToBloom(common.Hex2Bytes(params[15]))
				baseFee, _ := strconv.ParseInt(params[16], 10, 64)
				header.BaseFee = big.NewInt(baseFee)

				myChainContext.Headers[header.Number.Uint64()] = &header

				// fmt.Println("success insertHeader -> blockNum:", header.Number)

				response = []byte("success")

			case "insertUncles":
				// get params
				// fmt.Println("execute insertUncles()")

				blockNum, _ := strconv.ParseUint(params[1], 10, 64)
				// fmt.Println("uncles for block num:", blockNum)

				cnt := 2
				unclesNum := (len(params) - 2) / 2
				for i := 0; i < unclesNum; i++ {
					uncleCoinbase := common.HexToAddress(params[cnt])
					cnt++
					uncleHeight, _ := strconv.ParseInt(params[cnt], 10, 64)
					cnt++

					// fmt.Println("  uncleCoinbase:", uncleCoinbase)
					// fmt.Println("  uncleHeight:", uncleHeight)
					uncleInfo := UncleInfo{
						Coinbase:    uncleCoinbase,
						UncleHeight: big.NewInt(uncleHeight),
					}
					uncleInfos[blockNum] = append(uncleInfos[blockNum], &uncleInfo)
				}
				fmt.Println()

				response = []byte("success")

			case "insertTransactionArgs":
				// get params
				// fmt.Println("execute insertTransactionArgs()")

				// receive large msg
				if params[len(params)-1] != "@" {
					finalBuf := make([]byte, 0)
					finalBuf = append(finalBuf, recvBuf[:n]...)
					cnt := 0
					ns := make([]int, 0)
					for {
						cnt++
						n, err := conn.Read(recvBuf)
						if err != nil {
							if err == io.EOF {
								log.Println(err)
								return
							}
							log.Println(err)
							return
						}
						ns = append(ns, n)
						finalBuf = append(finalBuf, recvBuf[:n]...)

						request := string(finalBuf)

						if request[len(request)-1] == '@' {
							params = strings.Split(request, ",")
							break
						}
					}
				}

				txArgs := new(core.TransactionArgs)

				fromAddr := common.HexToAddress(params[1])
				txArgs.From = &fromAddr

				if params[2] != "None" {
					toAddr := common.HexToAddress(params[2])
					txArgs.To = &toAddr
				}

				gasLimit, _ := strconv.ParseUint(params[3], 10, 64)
				gasLimitHexutilUint64 := hexutil.Uint64(gasLimit)
				txArgs.Gas = &gasLimitHexutilUint64

				if params[4] != "None" {
					gasPrice, _ := strconv.ParseInt(params[4], 10, 64)
					gasPriceBig := big.NewInt(gasPrice)
					txArgs.GasPrice = (*hexutil.Big)(gasPriceBig)
				}

				var valueBig big.Int
				valueBig.SetString(params[5], 10)
				txArgs.Value = (*hexutil.Big)(&valueBig)

				nonce, _ := strconv.ParseUint(params[6], 10, 64)
				nonceHexutilUint64 := hexutil.Uint64(nonce)
				txArgs.Nonce = &nonceHexutilUint64

				input := hexutil.Bytes(common.Hex2Bytes(params[7]))
				txArgs.Input = &input

				if params[8] != "None" {
					maxFeePerGas, _ := strconv.ParseInt(params[8], 10, 64)
					maxFeePerGasBig := big.NewInt(maxFeePerGas)
					txArgs.MaxFeePerGas = (*hexutil.Big)(maxFeePerGasBig)

					// gas price should be nil if maxFeePerGas or maxPriorityFeePerGas exist
					txArgs.GasPrice = nil
				}

				if params[9] != "None" {
					maxPriorityFeePerGas, _ := strconv.ParseInt(params[9], 10, 64)
					maxPriorityFeePerGasBig := big.NewInt(maxPriorityFeePerGas)
					txArgs.MaxPriorityFeePerGas = (*hexutil.Big)(maxPriorityFeePerGasBig)

					// gas price should be nil if maxFeePerGas or maxPriorityFeePerGas exist
					txArgs.GasPrice = nil
				}

				txArgsList = append(txArgsList, txArgs)

				response = []byte("success")

			// TODO(jmlee): implement this
			case "insertTransactionAccessList":

				txindex, _ := strconv.ParseUint(params[1], 10, 64)
				addr := common.HexToAddress(params[2])
				var storageKey common.Hash
				hasStorageKey := false
				if len(params) == 4 {
					hasStorageKey = true
					storageKey = common.HexToHash(params[3])
				}

				// fmt.Println("address:", addr.Hex())
				// if hasStorageKey {
				// 	fmt.Println("storagekey:", storageKey.Hex())
				// }

				txArg := txArgsList[txindex]

				// fmt.Println("before")
				// txArg.Print()

				if txArg.AccessList == nil {
					txArg.AccessList = new(types.AccessList)
				}

				didAdd := false
				for indexx, accessInfo := range *txArg.AccessList {
					if addr.Hex() == accessInfo.Address.Hex() && hasStorageKey {
						// fmt.Println("matched!")
						accessInfo.StorageKeys = append(accessInfo.StorageKeys, storageKey)

						// accessInfo is not reference, is value
						temp := *txArg.AccessList
						temp[indexx] = accessInfo

						didAdd = true
						break
					}
				}

				if !didAdd {
					tuple := types.AccessTuple{Address: addr, StorageKeys: []common.Hash{}}
					if hasStorageKey {
						tuple.StorageKeys = append(tuple.StorageKeys, storageKey)
					}
					*txArg.AccessList = append(*txArg.AccessList, tuple)
				}

				// temp code, delete this later
				// tuple := types.AccessTuple{Address: common.HexToAddress("0xb1dd690cc9af7bb1a906a9b5a94f94191cc553ce"), StorageKeys: []common.Hash{}}
				// *txArg.AccessList = append(*txArg.AccessList, tuple)

				fmt.Println("after")
				txArg.Print()

				response = []byte("success")

			// TODO(jmlee): implement this
			case "insertTransactionAccessListV2":

				// receive large msg
				if params[len(params)-1] != "@" {
					finalBuf := make([]byte, 0)
					finalBuf = append(finalBuf, recvBuf[:n]...)
					cnt := 0
					ns := make([]int, 0)
					for {
						cnt++
						n, err := conn.Read(recvBuf)
						if err != nil {
							if err == io.EOF {
								log.Println(err)
								return
							}
							log.Println(err)
							return
						}
						ns = append(ns, n)
						finalBuf = append(finalBuf, recvBuf[:n]...)

						request := string(finalBuf)

						if request[len(request)-1] == '@' {
							params = strings.Split(request, ",")
							break
						}
					}
				}

				txindex, _ := strconv.ParseUint(params[1], 10, 64)
				addr := common.HexToAddress(params[2])

				tuple := types.AccessTuple{Address: addr, StorageKeys: []common.Hash{}}

				for i := 3; i < len(params)-1; i++ {
					storageKey := common.HexToHash(params[i])
					tuple.StorageKeys = append(tuple.StorageKeys, storageKey)
				}

				txArg := txArgsList[txindex]
				if txArg.AccessList == nil {
					txArg.AccessList = new(types.AccessList)
				}

				// fmt.Println("before")
				// txArg.Print()

				*txArg.AccessList = append(*txArg.AccessList, tuple)

				// fmt.Println("after")
				// txArg.Print()

				response = []byte("success")

			case "clearTransactionArgsList":
				txArgsList = make([]*core.TransactionArgs, 0)

				response = []byte("success")

			case "executeTransactionArgsList":
				// get params
				// fmt.Println("execute executeTransactionArgsList()")

				if common.VersionLength+common.PathLength > 0 {
					trie.SetCurrentBlockNum(currentBlockNum)
				}

				if currentBlockNum == 0 {
					fmt.Println("set genesis state")

					_, _, err := core.SetupGenesisBlock(frdiskdb, mainTrieDB, nil)
					if err != nil {
						fmt.Println("SetupGenesisBlock() err:", err)
						os.Exit(1)
					}

					// check validity
					// genesisHeader := myChainContext.GetHeader(common.Hash{}, currentBlockNum)
					// if common.SimulationMode == common.EthereumMode && genesisBlockHash != genesisHeader.Root {
					// 	if !common.EnableNodePrefixing {
					// 		fmt.Println("genesis state is wrong")
					// 		fmt.Println("generated state root:\t", genesisHash.Hex())
					// 		fmt.Println("genesis header.Root:\t", genesisHeader.Root.Hex())
					// 		os.Exit(1)
					// 	}
					// }

					// prepare next block
					currentStateRoot = common.GenesisStateRoot
					fmt.Println("set genesis state complete -> currentStateRoot:", currentStateRoot.Hex())

					blockNumStr := fmt.Sprintf("%08d", currentBlockNum)
					simBlock := new(common.SimBlock) // save simulation result
					simBlock.Number = currentBlockNum
					simBlock.StateRoot = currentStateRoot
					common.SimBlocks[blockNumStr] = simBlock

					currentBlockNum++
					response = []byte("success")
					break
				}

				// code for debugging
				// beforeStateRoot := currentStateRoot

				//
				// get header
				//
				header := myChainContext.GetHeader(common.Hash{}, currentBlockNum)
				fmt.Println("start block execution for block", currentBlockNum)

				blockNumStr := fmt.Sprintf("%08d", currentBlockNum)
				simBlock := new(common.SimBlock) // save simulation result
				simBlock.Number = currentBlockNum

				//
				// set stateDB
				//
				// fmt.Println("set stateDB")
				blockStartTime := time.Now()
				if common.EnableSnapshot && mySnaps == nil {
					mySnapconfig := snapshot.Config{
						CacheSize: snapshotCacheSize,
						// CacheSize:  bc.cacheConfig.SnapshotLimit,
						// Recovery:   recover,
						// NoBuild:    bc.cacheConfig.SnapshotNoBuild,
						AsyncBuild: false,
						// AsyncBuild: !bc.cacheConfig.SnapshotWait,
					}

					fmt.Println("try to get snapshot")
					mySnaps, err = snapshot.New(mySnapconfig, diskdb, mainTrieDB, currentStateRoot)
					if err != nil {
						fmt.Println("err: snapshot is not made")
						os.Exit(1)
					} else {
						fmt.Println("snapshot enabled!")
					}

					if common.LoggingReadStats {
						hashdb.ResetCacheStat()
						pathdb.ResetCacheStat()
						if currentBlockNum == 1 {
							leveldb.ResetCacheStat(0)
						} else {
							leveldb.ResetCacheStat(currentBlockNum)
						}
					}
				}
				stateDB, err := state.New(currentStateRoot, stateCache, mySnaps)
				if err != nil {
					fmt.Println("ERROR: state.New() err:", err)
					os.Exit(1)
				}

				//
				// execute transactionArgsList
				//
				// fmt.Println("execute transactions")
				stateDB.StartPrefetcher("miner") // when snapshot is enabled, read needed trie nodes at background
				gasPool := new(core.GasPool).AddGas(header.GasLimit)

				// add balance & gasLimit for executing random txs
				// sourceAddr := common.HexToAddress("0x0")
				// sourceBalance := new(uint256.Int)
				// sourceBalance.SetFromDecimal("99999999999999")
				// stateDB.AddBalance(sourceAddr, sourceBalance)
				// gasPool.AddGas(uint64(30000000))
				
				deleteEmptyObjects := myChainConfig.IsEIP158(header.Number) // blockNum > 2,675,000
				for txIndex, txArg := range txArgsList {

					start := time.Now()
					// snap := stateDB.Snapshot()
					receipt, err := core.ApplyTransactionArgs(myChainConfig, &myChainContext, &header.Coinbase, gasPool, stateDB,
						header, txArg, &header.GasUsed, vm.Config{})

					if err != nil {
						fmt.Println("ApplyTransaction() err:", err)
						fmt.Println("at block", header.Number, "/ tx index:", txIndex)
						txArg.Print()

						// stateDB.RevertToSnapshot(snap) // do not needed maybe, since we do not have to rollback state
						os.Exit(1)
					}

					if metrics.EnabledExpensive {
						txExecute := time.Since(start)
						if receipt.GasUsed == 21000 {
							// payment tx
							simBlock.PaymentTxLen++
							simBlock.PaymentTxExecutes += txExecute
						} else {
							// contract call tx
							simBlock.CallTxLen++
							simBlock.CallTxExecutes += txExecute
						}
					}

					_ = receipt
					// fmt.Println("execute", txIndex+1, "th transaction success")
					// fmt.Println("receipt:", receipt)

					// for debugging: print intermediate trie
					// newStateRoot, err := stateDB.Commit(currentBlockNum, deleteEmptyObjects)
					// if err != nil {
					// 	fmt.Println("stateDB.Commit() err:", err)
					// 	os.Exit(1)
					// }
					// newTrie, err := trie.New(trie.StateTrieID(newStateRoot), mainTrieDB)
					// fmt.Println("inter state root:", newStateRoot.Hex())
					// newTrie.Print()
				}
				simBlock.GasUsed = header.GasUsed

				// block rewards is deprecated after the Merge
				if header.Difficulty.Cmp(big.NewInt(0)) != 0 {
					//
					// block reward + uncle rewards
					//
					// Select the correct block reward based on chain progression
					blockReward := ethash.FrontierBlockReward
					if myChainConfig.IsByzantium(header.Number) {
						blockReward = ethash.ByzantiumBlockReward
					}
					if myChainConfig.IsConstantinople(header.Number) {
						blockReward = ethash.ConstantinopleBlockReward
					}
					// Accumulate the rewards for the miner and any included uncles
					reward := new(uint256.Int).Set(blockReward)
					r := new(uint256.Int)
					hNum, _ := uint256.FromBig(header.Number)
					for _, uncleInfo := range uncleInfos[currentBlockNum] {

						uNum, _ := uint256.FromBig(uncleInfo.UncleHeight)
						r.AddUint64(uNum, 8)
						r.Sub(r, hNum)
						r.Mul(r, blockReward)
						r.Div(r, u256_8)
						stateDB.AddBalance(uncleInfo.Coinbase, r)

						r.Div(blockReward, u256_32)
						reward.Add(reward, r)
					}
					stateDB.AddBalance(header.Coinbase, reward)
				}

				//
				// deal with DAO hard fork
				//
				if myChainConfig.DAOForkSupport && myChainConfig.DAOForkBlock != nil && myChainConfig.DAOForkBlock.Cmp(header.Number) == 0 {
					misc.ApplyDAOHardFork(stateDB)
				}

				// measure tx processing time
				myGcproc += time.Since(blockStartTime)

				//
				// commit final state
				//
				currentStateRoot, err = stateDB.Commit(currentBlockNum, deleteEmptyObjects)
				if err != nil {
					fmt.Println("stateDB.Commit() err:", err)
					os.Exit(1)
				}
				simBlock.StateRoot = currentStateRoot

				//
				// flush or garbage collect state (codes from writeBlockWithState function in core/blockchain.go)
				//
				fmt.Println("flush or garbage collect state")
				if !common.IsPathScheme {
					start := time.Now()
					triedb := stateDB.Database().TrieDB()
					if common.IsArchiveMode {
						// If we're running an archive node, always flush
						triedb.Commit(currentStateRoot, false)
					} else if isHardforkedBlock(currentBlockNum) || isHardforkedBlock(currentBlockNum+1) || currentBlockNum%1000000 == 0 {
						// commit state trie when this block is hard forked (to copy the tries for simulation later)
						// (note that this is not essential)
						triedb.Commit(currentStateRoot, false)
					} else {
						// Full but not archive node, do proper garbage collection
						triedb.Reference(currentStateRoot, common.Hash{}) // metadata reference to keep trie alive
						myTriegc.Push(currentStateRoot, -int64(currentBlockNum))

						// Flush limits are not considered for the first TriesInMemory blocks.
						if currentBlockNum > core.TriesInMemory {
							// If we exceeded our memory allowance, flush matured singleton nodes to disk
							var (
								_, nodes, imgs = triedb.Size() // all memory is contained within the nodes return for hashdb
								limit          = common.StorageSize(dirtyCacheSize) * 1024 * 1024
							)
							if nodes > limit || imgs > 4*1024*1024 {
								triedb.Cap(limit - ethdb.IdealBatchSize)
							}
							// Find the next state trie we need to commit
							chosen := currentBlockNum - core.TriesInMemory
							flushInterval := time.Duration(myFlushInterval.Load())
							// If we exceeded time allowance, flush an entire trie to disk
							if myGcproc > flushInterval {
								// If the header is missing (canonical chain behind), we're reorging a low
								// diff sidechain. Suspend committing until this operation is completed.
								chosenHeader := myChainContext.GetHeader(common.Hash{}, chosen)
								if chosenHeader == nil {
									// TODO(jmlee): can this happen? reorg should not happen in simulation
									//   -> looks not happen
									fmt.Println("Reorg in progress, trie commit postponed", "number", chosen)
									os.Exit(1)
								} else {
									// If we're exceeding limits but haven't reached a large enough memory gap,
									// warn the user that the system is becoming unstable.
									if chosen < myLastWrite+core.TriesInMemory && myGcproc >= 2*flushInterval {
										fmt.Println("State in memory for too long, committing", "time", myGcproc, "allowance", flushInterval, "optimum", float64(chosen-myLastWrite)/core.TriesInMemory)
									}
									// Flush an entire trie and restart the counters
									triedb.Commit(chosenHeader.Root, true)
									myLastWrite = chosen
									myGcproc = 0
								}
							}
							// Garbage collect anything below our required write retention
							for !myTriegc.Empty() {
								root, number := myTriegc.Pop()
								if uint64(-number) > chosen {
									myTriegc.Push(root, number)
									break
								}
								triedb.Dereference(root)
							}
						}
					}

					if metrics.EnabledExpensive {
						diskCommits := time.Since(start)
						simBlock.DiskCommits += diskCommits
						// fmt.Println("trie.Database.Commit() time:", diskCommits.Nanoseconds(), "ns")
					}

				} else {
					// If node is running in path mode, skip explicit gc operation
					// which is unnecessary in this mode.
				}

				// collect performance metrics
				stateDB.SaveMeters(simBlock)
				// stateDB.PrintMeters()

				// check results
				simBlock.BlockExecuteTime = time.Since(blockStartTime)
				fmt.Println("<<< execution success for block", header.Number, ">>>", "( mode:", common.GetSimulationTypeName(), "/ ModifyHashMethod:", common.ModifyHashMethod, "/ port:", ServerPort, ")",
					"\n  current state root:", currentStateRoot.Hex())
				if common.SimulationMode == common.EthereumMode && currentStateRoot != header.Root {
					// code for debugging
					// if common.IsPathScheme || !common.IsArchiveMode {
					// 	fmt.Println("commit state... for state root:", beforeStateRoot.Hex())
					// 	stateDB, err := state.New(beforeStateRoot, stateCache, mySnaps)
					// 	if err != nil {
					// 		fmt.Println("ERROR: state.New() err:", err)
					// 		os.Exit(1)
					// 	}
					// 	stateDB.Database().TrieDB().Commit(beforeStateRoot, false)
					// 	fmt.Println("  -> commit state completed")
					// }

					if common.VersionLength+common.PathLength == 0 {
						fmt.Println("ERR: executeTransactionArgsList: Ethereum state not match !!!")
						fmt.Println("  current state root:", currentStateRoot.Hex())
						fmt.Println("  sub state root:", simBlock.SubStateRoot.Hex())
						fmt.Println("  mainnet header.Root:", header.Root.Hex())
						fmt.Println("  executed txArgs len:", len(txArgsList))
						os.Exit(1)
					}
				}
				// myNewNormTrie, _ := trie.New(trie.StateTrieID(currentStateRoot), myTrieDB)
				// myNewNormTrie.Print()

				// measure disk usage
				if currentBlockNum%diskSizeMeasureEpoch == 0 {
					fmt.Println("try to get disk size")
					fmt.Println("  total cnt:", diskSizeMeasureCnt)
					fmt.Println("  total elapsed:", diskSizeMeasureElapsed)
					cnt := 0
					start := time.Now()
					for {
						cnt++
						if cnt > 100 {
							fmt.Println("ERROR: cannot measure directory size")
							os.Exit(1)
						}
						size, err := getDirectorySizeV2(leveldbPath)
						if err != nil {
							time.Sleep(3 * time.Second)
							continue
						}
						simBlock.DiskSize = size
						break
					}
					if common.IsPathScheme {
						cnt = 0
						for {
							cnt++
							if cnt > 100 {
								fmt.Println("ERROR: cannot measure directory size")
								os.Exit(1)
							}
							size, err := getDirectorySizeV2(leveldbPath + "/" + rawdb.StateFreezerName)
							if err != nil {
								time.Sleep(3 * time.Second)
								continue
							}
							simBlock.HistorySize = size
							break
						}
					}
					diskSizeMeasureCnt += cnt
					diskSizeMeasureElapsed += time.Since(start)
					fmt.Println("Directory Size (in bytes):", simBlock.DiskSize, "/ history size:", simBlock.HistorySize, "/ after", cnt, "attempts")
				}

				// TODO(jmlee): measure path-based state's read stat
				//  (trie node's key is not 32B length in path-based state, consider this)
				// print trie node read stats
				// fmt.Println()
				// fmt.Println("Geth trie cache size:", trieCacheSize, "MB / dirty cache size:", dirtyCacheSize, "MB")
				// if common.IsPathScheme {
				// 	pathdb.PrintReadStats()
				// 	pathdb.ResetReadStats()
				// } else {
				// 	hashdb.PrintReadStats()
				// }
				// fmt.Println()
				// fmt.Println("LevelDB cache size:", leveldbCache, "MB")
				// leveldb.PrintReadStats()
				if currentBlockNum%1000 == 0 {
					if common.LoggingReadStats {
						var nums, times, sizes map[string]int64
						var depthSum int64
						if common.IsPathScheme {
							nums, times, sizes, depthSum = pathdb.ResetCacheStat()
						} else {
							nums, times, sizes, depthSum = hashdb.ResetCacheStat()
						}
						leveldb.SaveCacheStat(currentBlockNum, nums, times, sizes, depthSum)
						leveldb.ResetCacheStat(currentBlockNum + 1)
					}
					if common.LoggingOpcodeStats {
						// common.CurrentOpcodeStat.Print()
						common.SaveOpcodeStat(currentBlockNum)
						common.ResetOpcodeStat(currentBlockNum + 1)
					}
				}

				fmt.Println("NodeReadFuncCnt:", common.NodeReadFuncCnt, "/ AdditionalNodeReadFuncCnt:", common.AdditionalNodeReadFuncCnt,
					"\n  clean:", common.CleanHitCnt,
					"\n  dirty:", common.DirtyHitCnt,
					"\n  disk:", common.DiskHitCnt,
					"\n  not found:", common.NotFoundHitCnt)

				// print and save leveldb's read stats (cache hit rate, fake reads, bloom filter)
				// use goleveldb's branch: "measureReadStats"
				// leveldb.PrintMyReadStats()
				// if currentBlockNum%saveLevelDBStatsEpoch == 0 {
				// 	readStatFilePath := leveldbStatsPath
				// 	readStatFileName := "read_stats_" + common.GetSimulationTypeName() + "_" + common.ModifyHashMethod + "_" + strconv.FormatUint(currentBlockNum, 10) + ".json"
				// 	leveldb.SaveMyReadStats(readStatFilePath+readStatFileName)
				// }
				
				// measure modifyHash()'s overhead (this is included in AccountHashes & StorageHashes)
				simBlock.ModifyHashes = common.ModifyHashes
				common.ModifyHashes = 0

				// save leveldb stats
				if currentBlockNum%saveLevelDBStatsEpoch == 0 {
					leveldbStat := new(common.LevelDBStat)
					leveldbStat.BlockNum = currentBlockNum

					properties := map[string]string{
						"stats":        "leveldb.stats",
						"iostats":      "leveldb.iostats",
						"writedelay":   "leveldb.writedelay",
						"compcount":    "leveldb.compcount",
						"openedtables": "leveldb.openedtables",
					}

					for key, prop := range properties {
						val, err := diskdb.Stat(prop)
						if err != nil {
							fmt.Printf("Property %s: error -> %v\n", prop, err)
							continue
						}
						fmt.Printf("Property %s:\n%s\n\n", prop, val)

						switch key {
						case "stats":
							leveldbStat.Compaction = common.ParseStats(val)
						case "iostats":
							leveldbStat.IO = common.ParseIOStats(val)
						case "writedelay":
							leveldbStat.WriteDelay = common.ParseWriteDelay(val)
						case "compcount":
							leveldbStat.CompactionCount = common.ParseCompCount(val)
						case "openedtables":
							n, _ := strconv.Atoi(val)
							leveldbStat.OpenedTables = n
						}
					}

					common.LevelDBStats[blockNumStr] = leveldbStat
				}

				//
				// cleanups
				//

				// save this block's simulation result
				common.SimBlocks[blockNumStr] = simBlock

				// delete old block infos to deal with out-of-memory error
				if common.SimulationMode == common.EthaneMode && currentBlockNum > 2000000 {
					oldBlockNum := fmt.Sprintf("%08d", currentBlockNum-2000000)
					delete(common.SimBlocks, oldBlockNum)
				}

				// clear txArgsList
				txArgsList = make([]*core.TransactionArgs, 0)

				// delete current uncle info
				delete(uncleInfos, currentBlockNum)

				// delete old header (to maintain recent 256 blocks)
				delete(myChainContext.Headers, currentBlockNum-300)

				// prepare next block
				currentBlockNum++

				response = []byte("success")

				//
				// TODO(jmlee): we might be able to utilize "BlockGen", "GenerateChain()"
				// for more easy & realistic tx execution simulation
				//

			// commit dirty states when finishing simulation
			case "commitDirtyStates":
				fmt.Println("execute commitDirtyStates()")

				// commit snapshot's diff layers
				if common.EnableSnapshot && mySnaps != nil {
					fmt.Println("save snapshot...")
					err := mySnaps.Cap(currentStateRoot, 0) // commit all diff layers
					if err != nil {
						fmt.Println("ERROR: fail to save snapshot -> err:", err)
						os.Exit(1)
					}
					fmt.Println("  -> save snapshot completed")
				}

				// commit state for path-based state or non-archive state
				if common.IsPathScheme || !common.IsArchiveMode {
					fmt.Println("commit state...")
					stateDB, err := state.New(currentStateRoot, stateCache, mySnaps)
					if err != nil {
						fmt.Println("ERROR: state.New() err:", err)
						os.Exit(1)
					}
					stateDB.Database().TrieDB().Commit(currentStateRoot, false)
					fmt.Println("  -> commit state completed")
				}

				response = []byte("success")

			case "setEnvForEVM":
				// get params
				// fmt.Println("execute setEnvForEVM()")

				// set current block state
				currentBlockNum, _ = strconv.ParseUint(params[1], 10, 64)
				currentStateRoot = common.HexToHash(params[2])
				fmt.Println("next block num:", currentBlockNum, "/ state root:", currentStateRoot.Hex())

				if common.LoggingReadStats {
					hashdb.ResetCacheStat()
					pathdb.ResetCacheStat()
					leveldb.ResetCacheStat(currentBlockNum)
				}
				if common.LoggingOpcodeStats {
					common.ResetOpcodeStat(currentBlockNum)
				}

				response = []byte("success")

			case "saveLevelDBStats":
				// get params
				fmt.Println("execute saveLevelDBStats()")

				// set file name
				mapKeys := make([]string, 0)
				for k, _ := range common.LevelDBStats {
					mapKeys = append(mapKeys, k)
				}
				sort.Strings(mapKeys)
				firstBlockNum := uint64(0)
				lastBlockNum := common.LevelDBStats[mapKeys[len(mapKeys)-1]].BlockNum
				fileName := "leveldb_stats_" + common.GetSimulationTypeName() + "_" + strconv.FormatUint(firstBlockNum, 10) + "_" + strconv.FormatUint(lastBlockNum, 10) + ".json"

				// encoding map to json
				var jsonData []byte
				var err error
				// save all LevelDBStats at once
				jsonData, err = json.MarshalIndent(common.LevelDBStats, "", "  ")
				if err != nil {
					fmt.Println("JSON marshaling error:", err)
					return
				}

				// save as a json file
				err = os.WriteFile(leveldbStatsPath+fileName, jsonData, 0644)
				if err != nil {
					fmt.Println("File write error:", err)
					return
				}
				fmt.Println("  saved file name:", fileName)

				response = []byte("success")

			case "saveSimBlocks":
				// get params
				fmt.Println("execute saveSimBlocks()")

				// save logged stats
				if common.LoggingReadStats {
					fmt.Println("Geth trie clean cache size:", trieCacheSize, "MB / dirty cache size:", dirtyCacheSize)
					if common.IsPathScheme {
						pathdb.PrintReadStats()
					} else {
						hashdb.PrintReadStats()
					}
					fmt.Println("LevelDB cache size:", leveldbCache, "MB")
					leveldb.PrintTotalCacheStat()
					leveldb.SaveCacheLogs(cacheStatsPath, "cache_stats_"+common.GetSimulationTypeName())
				}
				if common.LoggingOpcodeStats {
					common.SaveOpcodeLogs(opcodeStatsPath)
				}

				fileName := params[1]
				// blockNumToSave, _ := strconv.ParseUint(params[2], 10, 64)

				// TODO(jmlee): do not receive filaName from python client
				mapKeys := make([]string, 0)
				for k, _ := range common.SimBlocks {
					mapKeys = append(mapKeys, k)
				}
				sort.Strings(mapKeys)
				firstBlockNum := common.SimBlocks[mapKeys[0]].Number
				lastBlockNum := common.SimBlocks[mapKeys[len(mapKeys)-1]].Number
				fileName = "evm_simulation_result_" + common.GetSimulationTypeName() + "_" + strconv.FormatUint(firstBlockNum, 10) + "_" + strconv.FormatUint(lastBlockNum, 10) + ".json"

				// encoding map to json
				var jsonData []byte
				var err error
				// save all SimBlocks at once
				jsonData, err = json.MarshalIndent(common.SimBlocks, "", "  ")

				if err != nil {
					fmt.Println("JSON marshaling error:", err)
					return
				}

				// save as a json file
				err = os.WriteFile(simBlocksPath+fileName, jsonData, 0644)
				if err != nil {
					fmt.Println("File write error:", err)
					return
				}
				fmt.Println("  saved file name:", fileName)

				response = []byte("success")

			case "loadSimBlocks":
				fmt.Println("execute loadSimBlocks()")

				// starBlockNum, _ := strconv.ParseUint(params[1], 10, 64)
				// endBlockNum, _ := strconv.ParseUint(params[2], 10, 64)
				lastBlockNumToLoad, _ := strconv.ParseUint(params[3], 10, 64)
				lastBlockNumToLoadStr := fmt.Sprintf("%08d", lastBlockNumToLoad)
				fmt.Println("last block num to load:", lastBlockNumToLoad)

				jsonFileName := "evm_simulation_result_"
				if common.SimulationMode == common.EthereumMode {
					jsonFileName += common.GetSimulationTypeName() + "_" + params[1] + "_" + params[2] + ".json"
				} else if common.SimulationMode == common.EthaneMode {
					// jsonFileName += "Ethane_" + params[1] + "_" + params[2] + "_" + strconv.FormatUint(deleteEpoch, 10) + "_" + strconv.FormatUint(inactivateEpoch, 10) + "_" + strconv.FormatUint(inactivateCriterion, 10) + ".json"
				} else if common.SimulationMode == common.EthanosMode {
					// jsonFileName += "Ethanos_" + params[1] + "_" + params[2] + "_" + strconv.FormatUint(sweepEpoch, 10) + ".json"
				} else {
					os.Exit(1)
				}
				fmt.Println("json file name:", jsonFileName)

				// open json file
				file, err := os.ReadFile(simBlocksPath + jsonFileName)
				if err != nil {
					fmt.Println("Error opening file:", err)
					os.Exit(1)
				}

				// load SimBlocks
				loadedSimBlocks := make(map[string]*common.SimBlock)
				json.Unmarshal([]byte(file), &loadedSimBlocks)
				common.SimBlocks = make(map[string]*common.SimBlock)
				for blockNumStr, simBlock := range loadedSimBlocks {
					if blockNumStr <= lastBlockNumToLoadStr {
						common.SimBlocks[blockNumStr] = simBlock
					}
				}

				// set current state
				latestSimBlock := common.SimBlocks[lastBlockNumToLoadStr]
				currentStateRoot = latestSimBlock.StateRoot
				fmt.Println("current state root:", currentStateRoot)
				currentBlockNum = latestSimBlock.Number + 1

				if common.LoggingReadStats {
					hashdb.ResetCacheStat()
					pathdb.ResetCacheStat()
					leveldb.ResetCacheStat(currentBlockNum)
				}
				if common.LoggingOpcodeStats {
					common.ResetOpcodeStat(currentBlockNum)
				}

				fmt.Println("load state success")
				response = []byte("success")

			case "setStateRootAndTargetBlockNum":
				fmt.Println("execute setStateRootAndTargetBlockNum()")

				// starBlockNum, _ := strconv.ParseUint(params[1], 10, 64)
				// endBlockNum, _ := strconv.ParseUint(params[2], 10, 64)
				lastBlockNumToLoad, _ := strconv.ParseUint(params[3], 10, 64)
				lastBlockNumToLoadStr := fmt.Sprintf("%08d", lastBlockNumToLoad)
				targetBlockNum, _ := strconv.ParseUint(params[4], 10, 64)
				fmt.Println("last block num to load:", lastBlockNumToLoad)
				fmt.Println("targetBlockNum:", targetBlockNum)

				jsonFileName := "evm_simulation_result_"
				jsonFileName += "Ethereum_" + params[1] + "_" + params[2] + ".json"
				fmt.Println("json file name:", jsonFileName)

				// open json file
				file, err := os.ReadFile(simBlocksPath + jsonFileName)
				if err != nil {
					fmt.Println("Error opening file:", err)
					os.Exit(1)
				}

				// load SimBlocks
				loadedSimBlocks := make(map[string]*common.SimBlock)
				json.Unmarshal([]byte(file), &loadedSimBlocks)

				// set current state
				latestSimBlock := loadedSimBlocks[lastBlockNumToLoadStr]
				currentStateRoot = latestSimBlock.StateRoot
				fmt.Println("current state root:", currentStateRoot)

				// set target block number (= decide EVM version for DoS attack)
				currentBlockNum = targetBlockNum

				// set cache stats of Geth trie and LevelDB
				if common.LoggingReadStats {
					hashdb.ResetCacheStat()
					pathdb.ResetCacheStat()
					leveldb.ResetCacheStat(currentBlockNum)
				}
				if common.LoggingOpcodeStats {
					common.ResetOpcodeStat(currentBlockNum)
				}

				fmt.Println("set state root and target block number complete")
				response = []byte("success")

			case "simulateDoSAttack":
				fmt.Println("\nexecute simulateDoSAttack()")

				// receive large msg
				if params[len(params)-1] != "@" {
					finalBuf := make([]byte, 0)
					finalBuf = append(finalBuf, recvBuf[:n]...)
					cnt := 0
					ns := make([]int, 0)
					for {
						cnt++
						n, err := conn.Read(recvBuf)
						if err != nil {
							if err == io.EOF {
								log.Println(err)
								return
							}
							log.Println(err)
							return
						}
						ns = append(ns, n)
						finalBuf = append(finalBuf, recvBuf[:n]...)

						request := string(finalBuf)

						if request[len(request)-1] == '@' {
							params = strings.Split(request, ",")
							break
						}
					}
				}

				attackerAddr := common.HexToAddress(params[1])
				attackerBalance := new(uint256.Int)
				attackerBalance.SetFromDecimal(params[2])
				// attackerBalance, _ = attackerBalance.SetString(params[2], 10)
				contractAddr := common.HexToAddress(params[3])
				contractBytecode := common.Hex2Bytes(params[4])

				//
				// open statedb
				//
				if common.EnableSnapshot && mySnaps == nil {
					mySnapconfig := snapshot.Config{
						CacheSize: 256,
						// CacheSize:  bc.cacheConfig.SnapshotLimit,
						// Recovery:   recover,
						// NoBuild:    bc.cacheConfig.SnapshotNoBuild,
						// AsyncBuild: !bc.cacheConfig.SnapshotWait,
					}
					mySnaps, err = snapshot.New(mySnapconfig, diskdb, mainTrieDB, currentStateRoot)
					if err != nil {
						fmt.Println("err: snapshot is not made")
						os.Exit(1)
					} else {
						fmt.Println("snapshot enabled!")
					}
				}
				stateDB, err := state.New(currentStateRoot, stateCache, mySnaps)
				if err != nil {
					fmt.Println("ERROR: state.New() err:", err)
					os.Exit(1)
				}

				// increase attacker's balance
				stateDB.AddBalance(attackerAddr, attackerBalance)

				// deploy attack contract
				// stateDB.CreateAccount(contractAddr) // this is not essential
				stateDB.SetCode(contractAddr, contractBytecode)

				//
				// execute transactionArgsList
				//

				// get header
				// TODO(jmlee): set current block num
				currentBlockNum = 5600000
				header := myChainContext.GetHeader(common.Hash{}, currentBlockNum)
				fmt.Println("start attack execution at block", currentBlockNum)

				// reset attack stat
				common.CurrentAttackStat = common.NewAttackStat()

				attackTxGasCost := uint64(0)
				stateDB.StartPrefetcher("miner") // when snapshot is enabled, read needed trie nodes at background
				gasPool := new(core.GasPool).AddGas(header.GasLimit)
				gasPool.AddGas(uint64(30000000))                            // TODO(jmlee): add enough gas for attack tx, is this ok to do so?
				deleteEmptyObjects := myChainConfig.IsEIP158(header.Number) // blockNum > 2,675,000
				_ = deleteEmptyObjects

				// TODO(jmlee): execute normal transactions before attacking

				attackStartTime := time.Now()
				common.IsDoSAttacking = true
				for txIndex, txArg := range txArgsList {
					receipt, err := core.ApplyTransactionArgs(myChainConfig, &myChainContext, &header.Coinbase, gasPool, stateDB,
						header, txArg, &header.GasUsed, vm.Config{})
					if err != nil {
						fmt.Println("ApplyTransaction() err:", err)
						fmt.Println("at block", header.Number, "/ tx index:", txIndex)
						txArg.Print()

						// stateDB.RevertToSnapshot(snap) // do not needed maybe, since we do not have to rollback state
						os.Exit(1)
					}
					attackTxGasCost = receipt.GasUsed
				}
				common.IsDoSAttacking = false

				//
				// print attack result
				//
				attackTxExecutionTime := time.Since(attackStartTime)
				totalOpcodeExecutionTime := int64(0)
				totalOpcodeGasCost := uint64(0)
				for i := 0; i < len(common.CurrentAttackStat.OpcodeNames); i++ {
					totalOpcodeExecutionTime += common.CurrentAttackStat.ExecutionTimes[i]
					totalOpcodeGasCost += common.CurrentAttackStat.GasCosts[i]
				}
				fmt.Println("attack tx finished, print attack results")
				fmt.Println("  # of opcodes:", len(common.CurrentAttackStat.OpcodeNames))
				fmt.Println("  tx execution time:", attackTxExecutionTime.Nanoseconds(), "ns")
				fmt.Println("  opcode execution time:", totalOpcodeExecutionTime, "ns")
				fmt.Println("  tx gas cost:", attackTxGasCost)
				fmt.Println("    opcode gas cost:", totalOpcodeGasCost)
				fmt.Println("    starting gas cost:", common.CurrentAttackStat.StartingGasCost)
				fmt.Println("    tx.data gas cost:", common.CurrentAttackStat.TxDataGasCost)
				fmt.Println("    access list gas cost:", common.CurrentAttackStat.AccessListGasCost)
				fmt.Println("    refund gas cost:", common.CurrentAttackStat.RefundAmount)
				measuredAttackTxGasCost := totalOpcodeGasCost + common.CurrentAttackStat.StartingGasCost + common.CurrentAttackStat.TxDataGasCost + common.CurrentAttackStat.AccessListGasCost - common.CurrentAttackStat.RefundAmount
				if attackTxGasCost != measuredAttackTxGasCost {
					fmt.Println("ERROR: tx gas cost is weird")
					fmt.Println("  correct tx cost:", attackTxGasCost)
					fmt.Println("  wrongly measured tx cost:", measuredAttackTxGasCost)

					// TODO(jmlee): why this happen in RL DoS attack simulation
					// os.Exit(1)
				}

				// common.CurrentAttackStat.Print()

				// fmt.Println("stateRoot before attack:", currentStateRoot.Hex())
				// stateRoot := stateDB.IntermediateRoot(deleteEmptyObjects)
				// fmt.Println("stateRoot after attack:", stateRoot.Hex())

				//
				// cleanups
				//

				// clear txArgsList
				txArgsList = make([]*core.TransactionArgs, 0)

				// return attack results
				responseStr := ""
				responseStr += strconv.FormatInt(int64(len(common.CurrentAttackStat.OpcodeNames)), 10) + ","
				responseStr += strconv.FormatInt(totalOpcodeExecutionTime, 10) + ","
				responseStr += strconv.FormatInt(attackTxExecutionTime.Nanoseconds(), 10) + ","
				responseStr += strconv.FormatUint(totalOpcodeGasCost, 10) + ","
				responseStr += strconv.FormatUint(attackTxGasCost, 10)
				response = []byte(responseStr)

			case "benchmarkSync":
				fmt.Println("execute benchmarkSync()")

				// reset cache stats
				hashdb.ResetCacheStat()
				pathdb.ResetCacheStat()
				leveldb.ResetCacheStat(0)

				// mimic fast sync
				stateRootToSend := common.HexToHash(params[1])
				trieToSend, _ := trie.New(trie.StateTrieID(stateRootToSend), mainTrieDB)
				trieToSend.MimicFastSync()

				// print trie node read stats
				// fmt.Println()
				// fmt.Println("Geth trie cache size:", trieCacheSize, "MB")
				// hashdb.PrintReadStats()
				fmt.Println()
				fmt.Println("LevelDB cache size:", leveldbCache, "MB")
				leveldb.PrintReadStats()
				// leveldb.PrintTotalCacheStat()

				response = []byte("success")

			case "inspectAndCopyState":
				fmt.Println("execute inspectAndCopyState()")

				// get params
				blockNum, _ := strconv.ParseUint(params[1], 10, 64)
				stateRootToInspect := common.HexToHash(params[2])

				copyHash, _ := strconv.ParseInt(params[3], 10, 64)
				copyStateHash := (copyHash != 0)

				copyHashSnap, _ := strconv.ParseInt(params[4], 10, 64)
				copyStateHashSnapshot := (copyHashSnap != 0)

				copyPath, _ := strconv.ParseInt(params[5], 10, 64)
				copyStatePath := (copyPath != 0)

				copyPathSnap, _ := strconv.ParseInt(params[6], 10, 64)
				copyStatePathSnapshot := (copyPathSnap != 0)

				trie.InspectAndCopyState(blockNum, stateRootToInspect, frdiskdb, copyStateHash, copyStateHashSnapshot, copyStatePath, copyStatePathSnapshot)

				response = []byte("success")

			case "convertKeyalues":
				fmt.Println("execute convertKeyalues()")

				// set options
				setRandomKey := false
				setRandomValue := false
				fmt.Println("  setRandomKey:", setRandomKey, "/ setRandomValue:", setRandomValue)

				start := time.Now()

				// open new db
				_, newFrdb := openLevelDB("_convert")

				var (
					trieNodeNum       int
					trieNodeKeySize   common.StorageSize
					trieNodeValueSize common.StorageSize
				)

				// hasher for new key
				sha := sha3.NewLegacyKeccak256().(crypto.KeccakState)

				// iterate original db
				it := frdiskdb.NewIterator(nil, nil)
				defer it.Release()
				for it.Next() {
					if trieNodeNum%100000 == 0 {
						fmt.Print("\r  show intermediate result -> node num: ", trieNodeNum, " / key size: ", trieNodeKeySize, " / value size: ", trieNodeValueSize, " / elapsed: ", time.Since(start))
					}

					trieNodeNum++
					trieNodeKeySize += common.StorageSize(len(it.Key()))
					trieNodeValueSize += common.StorageSize(len(it.Value()))

					// generate unique random hash key
					newKey := make([]byte, len(it.Key()))
					copy(newKey, it.Key())
					if setRandomKey {
						sha.Reset()
						sha.Write(append(it.Value(), []byte(strconv.Itoa(trieNodeNum))...))
						sha.Read(newKey)
					}
					// fmt.Println("origin key:", common.Bytes2Hex(it.Key()))
					// fmt.Println("   new key:", common.Bytes2Hex(newKey))

					// generate random value
					newValue := make([]byte, len(it.Value()))
					copy(newValue, it.Value())
					if setRandomValue {
						newValue = generateRandomBytes(len(it.Value()))
					}
					// fmt.Println("origin value:", common.Bytes2Hex(it.Value()))
					// fmt.Println("   new value:", common.Bytes2Hex(newValue))

					// insert original value into new db with new key
					err := newFrdb.Put(newKey, newValue)
					if err != nil {
						fmt.Println("Failed to write to DB:", err)
						os.Exit(1)
					}
				}

				fmt.Println("\n\nfinal result -> node num: ", trieNodeNum, " / key size: ", trieNodeKeySize, " / value size: ", trieNodeValueSize, " / elapsed: ", time.Since(start))
				fmt.Println("  setRandomKey:", setRandomKey, "/ setRandomValue:", setRandomValue)

				response = []byte("success")

			case "stopSimulation":
				fmt.Println("stop simulation")
				os.Exit(1)

			case "test":
				trie.ReloadTrieNodes()

				response = []byte("success")
				os.Exit(1)

			default:
				fmt.Println("ERROR: there is no matching request")
				fmt.Println("  => params:", params)
				response = []byte("ERROR: there is no matching request")
			}

			// send response to client
			_, err = conn.Write(response[:])
			// fmt.Println("")
			if err != nil {
				log.Println(err)
				return
			}
		}
	}
}

func generateRandomBytes(size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(rand.Intn(256))
	}
	return b
}

// get directory's size in bytes
func getDirectorySize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return size, nil
}

// get directory size from "du -b" command
func getDirectorySizeV2(path string) (int64, error) {
	// call "du -b" command
	cmd := exec.Command("du", "-b", path)
	stdout, err := cmd.Output()
	if err != nil {
		// this err can be ignored
		fmt.Println("exec.Command's Output err:", err)
		fmt.Println("  stdout:", string(stdout))
		// return 0, err
	}
	fmt.Println(string(stdout))

	results := strings.Fields(string(stdout))
	fmt.Println("results:", results)
	if len(results) < 2 {
		fmt.Println("ERROR: du -b result is wierd")
		return 0, errors.New("du -b result is wierd")
	}

	sizeStr := results[len(results)-2]
	fmt.Println("sizeStr:", sizeStr)

	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		fmt.Println("ERROR: strconv.ParseInt err:", err)
		return 0, err
	}
	fmt.Println("size:", size, "B")
	return size, nil
}

// actual main() function
func StartStateSimulator() {

	fmt.Println("start state simulator")
	metrics.EnabledExpensive = enabledExpensive

	// create dir if not exist for log files
	err := os.MkdirAll(simBlocksPath, os.ModePerm)
	if err != nil {
		fmt.Println("ERROR: mkdirAll failed")
		os.Exit(1)
	}
	err = os.MkdirAll(cacheStatsPath, os.ModePerm)
	if err != nil {
		fmt.Println("ERROR: mkdirAll failed")
		os.Exit(1)
	}
	err = os.MkdirAll(opcodeStatsPath, os.ModePerm)
	if err != nil {
		fmt.Println("ERROR: mkdirAll failed")
		os.Exit(1)
	}
	err = os.MkdirAll(leveldbStatsPath, os.ModePerm)
	if err != nil {
		fmt.Println("ERROR: mkdirAll failed")
		os.Exit(1)
	}
	err = os.MkdirAll(errLogPath, os.ModePerm)
	if err != nil {
		fmt.Println("ERROR: mkdirAll failed")
		os.Exit(1)
	}

	// open tcp socket
	fmt.Println("open socket")
	listener, err := net.Listen("tcp", ":"+ServerPort)
	fmt.Println(listener.Addr())
	if nil != err {
		log.Println(err)
	}
	defer listener.Close()

	// wait for requests
	for {
		fmt.Println("  Modify Hash method:", common.ModifyHashMethod)
		fmt.Println("  ReadAllChildNodes:", common.ReadAllChildNodes)
		fmt.Println("\nwait for requests...")
		conn, err := listener.Accept()
		if err != nil {
			log.Println(err)
			continue
		}
		defer conn.Close()

		// go ConnHandler(conn) // asynchronous
		connHandler(conn) // synchronous
	}

}
