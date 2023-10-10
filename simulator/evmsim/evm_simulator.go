package evmsim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

var (
	//
	// simulator server
	//
	// port for requests
	// 8998: develop
	// 8997: Ethereum ~5M test
	// 8996: Ethanos ~5M test
	// 8995: Ethane ~5M test
	// 8994: test
	serverPort = "8994"
	// maximum byte length of response
	maxResponseLen = 4097

	//
	// log files
	//
	// simulation result log file path
	logFilePath   = "./logFiles/evm/"
	simBlocksPath = logFilePath + "simBlocks/"
	errLogPath    = logFilePath + "errLogs/"

	//
	// etc
	//
	big8                 = big.NewInt(8)
	big32                = big.NewInt(32)
	diskSizeMeasureEpoch = uint64(10000)
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

			case "switchSimulationMode":
				// fmt.Println("execute switchSimulationMode()")
				mode, _ := strconv.ParseUint(params[1], 10, 64)

				if mode == 0 || mode == 1 || mode == 2 {
					common.SimulationMode = int(mode)
					fmt.Println("current mode:", common.SimulationMode)
				} else {
					fmt.Println("Error switchSimulationMode: invalid mode ->", mode)
					os.Exit(1)
				}

				response = []byte("success")

			case "setEthanosParams":
				// fmt.Println("execute setEthanosParams()")
				sweepEpoch, _ = strconv.ParseUint(params[1], 10, 64)
				fromLevel, _ := strconv.ParseUint(params[2], 10, 64)
				restoreProofFromLevel = uint(fromLevel)

				response = []byte("success")

			case "setEthaneParams":
				// fmt.Println("execute setEthaneParams()")
				deleteEpoch, _ = strconv.ParseUint(params[1], 10, 64)
				inactivateEpoch, _ = strconv.ParseUint(params[2], 10, 64)
				inactivateCriterion, _ = strconv.ParseUint(params[3], 10, 64)
				fromLevel, _ := strconv.ParseUint(params[4], 10, 64)
				restoreProofFromLevel = uint(fromLevel)

				response = []byte("success")

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

			// TODO(jmlee): implement this
			case "insertTransaction":
				// get params
				// fmt.Println("execute insertTransaction()")
				tx := types.Transaction{}
				_ = tx

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
				}

				if params[9] != "None" {
					maxPriorityFeePerGas, _ := strconv.ParseInt(params[9], 10, 64)
					maxPriorityFeePerGasBig := big.NewInt(maxPriorityFeePerGas)
					txArgs.MaxPriorityFeePerGas = (*hexutil.Big)(maxPriorityFeePerGasBig)
				}

				txArgsList = append(txArgsList, txArgs)

				response = []byte("success")

			case "clearTransactionArgsList":
				txArgsList = make([]*core.TransactionArgs, 0)

				response = []byte("success")

			case "insertRestoreAddressList":

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

				for _, addr := range params[1 : len(params)-1] {
					addrToRestore := common.HexToAddress(addr)
					restoreAddrs = append(restoreAddrs, addrToRestore)
					// fmt.Println("add address to restore:", addrToRestore.Hex())
				}
				// fmt.Println("received", len(restoreAddrs), "addresses to restore")

				response = []byte("success")

			case "insertAccessList":
				// get params
				fmt.Println("execute insertAccessList()")

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

				for _, addr := range params[1 : len(params)-1] {
					accessAddr := common.HexToAddress(addr)
					accessAddrs = append(accessAddrs, accessAddr)
					// fmt.Println("add access address:", accessAddr.Hex())
				}
				// fmt.Println("received", len(accessAddrs), "addresses to access")

				response = []byte("success")

			case "executeTransactionArgsList":
				// get params
				// fmt.Println("execute executeTransactionArgsList()")

				if currentBlockNum == 0 {
					fmt.Println("set genesis state")

					// set (mainnet's) genesis state
					mainnetGenesis := core.DefaultGenesisBlock()
					mainnetGenesis.ToBlock().Hash()
					generatedBlockHeader, _ := mainnetGenesis.Commit(frdiskdb, mainTrieDB)

					// check validity
					genesisHeader := myChainContext.GetHeader(common.Hash{}, currentBlockNum)
					if common.SimulationMode == common.EthereumMode && generatedBlockHeader.Root() != genesisHeader.Root {
						fmt.Println("genesis state is wrong")
						fmt.Println("generated state root:\t", generatedBlockHeader.Root().Hex())
						fmt.Println("genesis header.Root:\t", genesisHeader.Root.Hex())
						os.Exit(1)
					}

					// prepare next block
					currentStateRoot = generatedBlockHeader.Root()
					fmt.Println("set genesis state complete -> currentStateRoot:", currentStateRoot.Hex())

					blockNumStr := fmt.Sprintf("%08d", currentBlockNum)
					simBlock := new(common.SimBlock) // save simulation result
					simBlock.Number = currentBlockNum
					simBlock.StateRoot = currentStateRoot
					simBlock.LastActiveKey = common.NextKey - 1
					common.SimBlocks[blockNumStr] = simBlock

					currentBlockNum++
					response = []byte("success")
					break
				}

				//
				// get header
				//
				header := myChainContext.GetHeader(common.Hash{}, currentBlockNum)
				fmt.Println("start block execution for block", currentBlockNum)

				blockNumStr := fmt.Sprintf("%08d", currentBlockNum)
				simBlock := new(common.SimBlock) // save simulation result
				simBlock.Number = currentBlockNum

				//
				// sweep state trie for Ethaons
				//
				if common.SimulationMode == common.EthanosMode && currentBlockNum%sweepEpoch == 0 {
					sweep()
				}

				//
				// restore inactive accounts which will be needed in this block
				//
				if len(restoreAddrs) != 0 || len(accessAddrs) != 0 {
					start := time.Now()
					if common.SimulationMode == common.EthaneMode {
						restoreEthaneAddrsV2(simBlock)
					} else if common.SimulationMode == common.EthanosMode {
						restoreEthanosAddrs(simBlock)
					}
					simBlock.AccountRestores += time.Since(start)
				}

				//
				// set stateDB
				//
				blockStartTime := time.Now()
				// TODO(jmlee): implement snapshot
				// bc.snaps, _ = snapshot.New(bc.db, bc.stateCache.TrieDB(), bc.cacheConfig.SnapshotLimit, head.Root(), !bc.cacheConfig.SnapshotWait, true, recover)
				// snaps := nil
				// stateDB, err := state.New(emptyRoot, stateCache, snaps)
				stateDB, err := state.New(currentStateRoot, stateCache, nil)
				if err != nil {
					fmt.Println("ERROR: state.New() err:", err)
					os.Exit(1)
				}

				//
				// execute transactionArgsList
				//
				stateDB.StartPrefetcher("miner") // TODO(jmlee): is it good to do for fast commit? -> needed for snapshot
				gasPool := new(core.GasPool).AddGas(header.GasLimit)
				deleteEmptyObjects := myChainConfig.IsEIP158(header.Number) // blockNum > 2,675,000
				for txIndex, txArg := range txArgsList {

					start := time.Now()
					// snap := stateDB.Snapshot()
					receipt, err := core.ApplyTransactionArgs(myChainConfig, &myChainContext, &header.Coinbase, gasPool, stateDB,
						header, txArg, &header.GasUsed, vm.Config{})
					if metrics.EnabledExpensive {
						txExecute := time.Since(start)
						if txArg.Input == nil || len(*txArg.Input) == 0 {
							// payment tx
							simBlock.PaymentTxLen++
							simBlock.PaymentTxExecutes += txExecute
						} else {
							// contract call tx
							simBlock.CallTxLen++
							simBlock.CallTxExecutes += txExecute
						}
					}
					if err != nil {
						fmt.Println("ApplyTransaction() err:", err)
						fmt.Println("at block", header.Number, "/ tx index:", txIndex)
						txArg.Print()

						// stateDB.RevertToSnapshot(snap) // do not needed maybe, since we do not have to rollback state
						os.Exit(1)
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
				reward := new(big.Int).Set(blockReward)
				r := new(big.Int)
				for _, uncleInfo := range uncleInfos[currentBlockNum] {
					r.Add(uncleInfo.UncleHeight, big8)
					r.Sub(r, header.Number)
					r.Mul(r, blockReward)
					r.Div(r, big8)
					stateDB.AddBalance(uncleInfo.Coinbase, r)

					r.Div(blockReward, big32)
					reward.Add(reward, r)
				}
				stateDB.AddBalance(header.Coinbase, reward)
				// stateDB.IntermediateRoot(myChainConfig.IsEIP158(header.Number)) // 밑에서 stateDB.Commit 할거면 해줄 필요 없음

				//
				// deal with DAO hard fork
				//
				if myChainConfig.DAOForkSupport && myChainConfig.DAOForkBlock != nil && myChainConfig.DAOForkBlock.Cmp(header.Number) == 0 {
					misc.ApplyDAOHardFork(stateDB)
				}

				//
				// delete previous accounts or inactivate old accounts for Ethane
				//
				if common.SimulationMode == common.EthaneMode {
					// fmt.Println("blockNum:", currentBlockNum, "/ deleteEpoch:", deleteEpoch)
					if (currentBlockNum+1)%deleteEpoch == 0 {
						stateDB.IntermediateRoot(deleteEmptyObjects) // apply remained modifications before deletion
						stateDB.DeletePreviousAccounts()
					}
					if (currentBlockNum+1)%inactivateEpoch == 0 && currentBlockNum >= inactivateCriterion {
						blockNumStr := fmt.Sprintf("%08d", currentBlockNum-inactivateCriterion)
						lastKeyToCheck := common.HexToHash(strconv.FormatUint(common.SimBlocks[blockNumStr].LastActiveKey, 16))
						// fmt.Println("lastKeyToCheck:", lastKeyToCheck.Big())
						diskCommits, lastInactiveKey := stateDB.InactivateOldAccounts(currentBlockNum, lastKeyToCheck)
						simBlock.DiskCommits += diskCommits
						simBlock.LastInactiveKey = lastInactiveKey
					}
				}

				//
				// commit final state
				//
				// TODO(jmlee): update bloom filter for Ethanos
				currentStateRoot, err = stateDB.Commit(currentBlockNum, deleteEmptyObjects)
				if err != nil {
					fmt.Println("stateDB.Commit() err:", err)
					os.Exit(1)
				}
				simBlock.StateRoot = currentStateRoot
				if common.SimulationMode == common.EthanosMode {
					// save cached trie root
					simBlock.SubStateRoot = common.CachedTrieRoot
				}
				if common.SimulationMode == common.EthaneMode {
					// save inactive trie root
					simBlock.SubStateRoot = common.InactiveTrieRoot
					// save last written key (checkpointKey)
					simBlock.LastActiveKey = common.NextKey - 1
				}

				// flush to disk
				start := time.Now()
				stateDB.Database().TrieDB().Commit(currentStateRoot, false)
				if metrics.EnabledExpensive {
					diskCommits := time.Since(start)
					fmt.Println("trie.Database.Commit() time:", diskCommits.Nanoseconds(), "ns")
					simBlock.DiskCommits += diskCommits

					// collect performance metrics
					stateDB.SaveMeters(simBlock)
					// stateDB.PrintMeters()
				}

				// check results
				simBlock.BlockExecuteTime = time.Since(blockStartTime)
				fmt.Println("<<< execution success for block", header.Number, ">>>", "( mode:", common.SimulationModeNames[common.SimulationMode], "/ port:", ServerPort, ")")
				fmt.Println("  current state root:", currentStateRoot.Hex())
				fmt.Println("  sub state root:", simBlock.SubStateRoot.Hex())
				fmt.Println("  mainnet header.Root:", header.Root.Hex())
				fmt.Println("  executed txArgs len:", len(txArgsList))
				totalRestoreNum += simBlock.AccountRestoreNum
				fmt.Println("  restored accounts num:", simBlock.AccountRestoreNum, "/ total:", totalRestoreNum)
				if common.SimulationMode == common.EthereumMode && currentStateRoot != header.Root {
					fmt.Println("ERR: executeTransactionArgsList: Ethereum state not match")
					os.Exit(1)
				}
				// myNewNormTrie, _ := trie.New(trie.StateTrieID(currentStateRoot), myTrieDB)
				// myNewNormTrie.Print()

				// measure disk usage
				if currentBlockNum%diskSizeMeasureEpoch == 0 {
					start := time.Now()
					size, err := getDirectorySize(leveldbPath)
					if err != nil {
						fmt.Println("Error at getDirectorySize():", err)
						os.Exit(1)
					}
					simBlock.DiskSize = size
					fmt.Println("Directory Size (in bytes):", size)
					fmt.Println("elapsed:", time.Since(start))
				}

				//
				// cleanups
				//

				// save this block's simulation result
				common.SimBlocks[blockNumStr] = simBlock

				// clear txArgsList
				txArgsList = make([]*core.TransactionArgs, 0)

				// clear restore address list
				restoreAddrs = make([]common.Address, 0)

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

			// TODO(jmlee): implement this
			case "checkTxExecutionResult":
				// get params
				// fmt.Println("execute checkTxExecutionResult()")

				nonce, _ := strconv.ParseUint(params[1], 10, 64)
				balance := new(big.Int)
				balance, _ = balance.SetString(params[2], 10)
				root := common.HexToHash(params[3])
				codeHash := common.Hex2Bytes(params[4])
				addr := common.HexToAddress(params[5])
				addrHash := crypto.Keccak256Hash(addr[:])

				expectedAcc := new(types.StateAccount)
				expectedAcc.Nonce = nonce
				expectedAcc.Balance = balance
				expectedAcc.Root = root
				expectedAcc.CodeHash = codeHash

				isCorrect := false
				deleted := params[3] == "0x00" && params[4] == "0x00"

				currentTrie, err := trie.New(trie.StateTrieID(currentStateRoot), indepTrieDB)
				if err != nil {
					fmt.Println("at checkTxExecutionResult() -> trie.New() error:", err)
					os.Exit(1)
				}

				var position common.Hash
				if common.SimulationMode == common.EthaneMode {
					addrKey, exist := common.AddrToKeyActive[addr]
					if exist {
						position = addrKey
					} else {
						position = common.MaxHash
					}
				} else {
					position = addrHash
				}

				enc, err := currentTrie.Get(position[:])
				// fmt.Println("enc:", enc, "/ err:", err)

				var acc0 types.StateAccount
				var acc1 types.EthaneStateAccount
				var acc2 types.EthanosStateAccount
				if err == nil {
					if len(enc) != 0 {
						if common.SimulationMode == common.EthereumMode {
							// decode account
							if err := rlp.DecodeBytes(enc, &acc0); err != nil {
								fmt.Println("Failed to decode state object:", err)
								fmt.Println("addr:", addr)
								fmt.Println("enc:", enc)
								os.Exit(1)
							}
							if acc0.Equal(expectedAcc) {
								isCorrect = true
							}
						} else if common.SimulationMode == common.EthaneMode {
							// decode account
							if err := rlp.DecodeBytes(enc, &acc1); err != nil {
								fmt.Println("Failed to decode state object:", err)
								fmt.Println("addr:", addr)
								fmt.Println("enc:", enc)
								os.Exit(1)
							}
							if acc1.Equal(expectedAcc) {
								isCorrect = true
							}
						} else if common.SimulationMode == common.EthanosMode {
							// decode account
							if err := rlp.DecodeBytes(enc, &acc2); err != nil {
								fmt.Println("Failed to decode state object:", err)
								fmt.Println("addr:", addr)
								fmt.Println("enc:", enc)
								os.Exit(1)
							}
							if acc2.Equal(expectedAcc) {
								isCorrect = true
							} else if deleted && acc2.Root == common.DeletedContractRoot {
								// Ethanos does not delete CA, but just set invalid Root value
								// this account is deleted account, so ignore it
								isCorrect = true
							}
						} else {
							fmt.Println("this must not happen")
							fmt.Println("at checkTxExecutionResult() -> wrong mode:", common.SimulationMode)
							os.Exit(1)
						}
					} else {
						if deleted {
							// this account is deleted
							isCorrect = true
						}
					}
				}

				if isCorrect {
					response = []byte("correct")

					// fmt.Println("### for addr", addr, "at block", currentBlockNum-1, "###")
					// fmt.Println("* actual account state * ( len:", len(enc), ")")
					// if common.SimulationMode == common.EthereumMode {
					// 	acc0.Print()
					// } else if common.SimulationMode == common.EthaneMode {
					// 	acc1.Print()
					// } else if common.SimulationMode == common.EthanosMode {
					// 	acc2.Print()
					// } else {
					// 	fmt.Println("this must not happen")
					// 	fmt.Println("at checkTxExecutionResult() -> wrong mode:", common.SimulationMode)
					// 	os.Exit(1)
					// }
					// fmt.Println("=> checkTxExecutionResult: CORRECT / addr:", addr)
				} else {
					// write err log
					fileName := "tx_execution_errors_"
					if common.SimulationMode == common.EthereumMode {
						// open log file
						fileName += "Ethereum.txt"
						// os.Remove(errLogPath+fileName) // delete prev log file if exist
						f, _ := os.OpenFile(errLogPath+fileName, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)

						log := ""
						log += "at block " + strconv.FormatUint(currentBlockNum-1, 10) + " for address " + addr.Hex() + "\n"
						log += "expected account state ( should be deleted: " + strconv.FormatBool(deleted) + " )\n"
						log += expectedAcc.ToString()
						log += "actual account state\n"
						log += acc0.ToString()

						// write log to file
						fmt.Fprintln(f, log)
						f.Close()

					} else if common.SimulationMode == common.EthaneMode {
						// open log file
						fileName += "Ethane_" + strconv.FormatUint(deleteEpoch, 10) + "_" + strconv.FormatUint(inactivateEpoch, 10) + "_" + strconv.FormatUint(inactivateCriterion, 10) + ".txt"
						// os.Remove(errLogPath+fileName) // delete prev log file if exist
						f, _ := os.OpenFile(errLogPath+fileName, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)

						log := ""
						log += "at block " + strconv.FormatUint(currentBlockNum-1, 10) + " for address " + addr.Hex() + "\n"
						log += "expected account state ( should be deleted: " + strconv.FormatBool(deleted) + " )\n"
						log += expectedAcc.ToString()
						log += "actual account state\n"
						log += acc1.ToString()

						// write log to file
						fmt.Fprintln(f, log)
						f.Close()

					} else if common.SimulationMode == common.EthanosMode {
						// open log file
						fileName += "Ethanos_" + strconv.FormatUint(sweepEpoch, 10) + ".txt"
						// os.Remove(errLogPath+fileName) // delete prev log file if exist
						f, _ := os.OpenFile(errLogPath+fileName, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)

						log := ""
						log += "at block " + strconv.FormatUint(currentBlockNum-1, 10) + " for address " + addr.Hex() + "\n"
						log += "expected account state ( should be deleted: " + strconv.FormatBool(deleted) + " )\n"
						log += expectedAcc.ToString()
						log += "actual account state\n"
						log += acc2.ToString()

						// write log to file
						fmt.Fprintln(f, log)
						f.Close()
					}

					fmt.Println("### for addr", addr, "at block", currentBlockNum-1, "###")
					fmt.Println("* expected account state *")
					if deleted {
						fmt.Println("  this account is deleted")
					} else {
						expectedAcc.Print()
					}
					fmt.Println("* actual account state * ( len:", len(enc), ")")
					if common.SimulationMode == common.EthereumMode {
						acc0.Print()
					} else if common.SimulationMode == common.EthaneMode {
						acc1.Print()
					} else if common.SimulationMode == common.EthanosMode {
						acc2.Print()
					} else {
						fmt.Println("this must not happen")
						fmt.Println("at checkTxExecutionResult() -> wrong mode:", common.SimulationMode)
						os.Exit(1)
					}
					fmt.Println("=> checkTxExecutionResult: WRONG")

					response = []byte("wrong")
				}

			// TODO(jmlee): implement this
			case "setEnvForEVM":
				// get params
				// fmt.Println("execute setEnvForEVM()")

				// set current block state
				currentBlockNum, _ = strconv.ParseUint(params[1], 10, 64)
				currentStateRoot = common.HexToHash(params[2])
				fmt.Println("next block num:", currentBlockNum, "/ state root:", currentStateRoot.Hex())

				response = []byte("success")

			case "saveSimBlocks":
				// get params
				fmt.Println("execute saveSimBlocks()")

				fileName := params[1]

				// encoding map to json
				jsonData, err := json.MarshalIndent(common.SimBlocks, "", "  ")
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

				// save Ethane's additional indices
				if common.SimulationMode == common.EthaneMode {
					indices := map[string]interface{}{
						"K_A": common.AddrToKeyActive,
						"K_I": common.AddrToKeyInactive,
						"D_A": common.KeysToDelete,
						"D_I": common.RestoredKeys,
					}

					jsonData, err = json.MarshalIndent(indices, "", "  ")
					if err != nil {
						fmt.Println("JSON marshaling error:", err)
						return
					}

					indiciesFileName := "ethane_indices_" + strconv.FormatUint(currentBlockNum-1, 10) + "_" + strconv.FormatUint(deleteEpoch, 10) + "_" + strconv.FormatUint(inactivateEpoch, 10) + "_" + strconv.FormatUint(inactivateCriterion, 10) + ".json"
					err = os.WriteFile(simBlocksPath+indiciesFileName, jsonData, 0644)
					if err != nil {
						fmt.Println("File write error:", err)
						return
					}
					fmt.Println("  saved indices file name:", indiciesFileName)
				}

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
					jsonFileName += "Ethereum_" + params[1] + "_" + params[2] + ".json"
				} else if common.SimulationMode == common.EthaneMode {
					jsonFileName += "Ethane_" + params[1] + "_" + params[2] + "_" + strconv.FormatUint(deleteEpoch, 10) + "_" + strconv.FormatUint(inactivateEpoch, 10) + "_" + strconv.FormatUint(inactivateCriterion, 10) + ".json"
				} else if common.SimulationMode == common.EthanosMode {
					jsonFileName += "Ethanos_" + params[1] + "_" + params[2] + "_" + strconv.FormatUint(sweepEpoch, 10) + ".json"
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
				if common.SimulationMode == common.EthanosMode {
					// set cached trie root
					common.CachedTrieRoot = latestSimBlock.SubStateRoot
					// fill cachedTrieRoots
					cachedTrieRoots = make([]common.Hash, 0)
					for blockNum := uint64(sweepEpoch); blockNum <= lastBlockNumToLoad; blockNum += sweepEpoch {
						blockNumStr := fmt.Sprintf("%08d", blockNum)
						simBlock := common.SimBlocks[blockNumStr]
						cachedTrieRoots = append(cachedTrieRoots, simBlock.SubStateRoot)
					}
				}
				if common.SimulationMode == common.EthaneMode {
					// TODO(jmlee): implement here

					common.NextKey = latestSimBlock.LastActiveKey + 1
					common.InactiveTrieRoot = latestSimBlock.SubStateRoot

					indiciesFileName := "ethane_indices_" + strconv.FormatUint(currentBlockNum-1, 10) + "_" + strconv.FormatUint(deleteEpoch, 10) + "_" + strconv.FormatUint(inactivateEpoch, 10) + "_" + strconv.FormatUint(inactivateCriterion, 10) + ".json"
					indiciesFile, err := os.ReadFile(simBlocksPath + indiciesFileName)
					if err != nil {
						fmt.Println("Error opening Ethane's indicies file:", err)
						os.Exit(1)
					}

					var data map[string]interface{}
					err = json.Unmarshal(indiciesFile, &data)
					if err != nil {
						fmt.Println("unmarshal error:", err)
						os.Exit(1)
					}

					addrToKeyActive, ok1 := data["K_A"].(map[string]interface{})
					addrToKeyInactive, ok2 := data["K_I"].(map[string]interface{})
					keysToDelete, ok3 := data["D_A"].([]interface{})
					restoredKeys, ok4 := data["D_I"].([]interface{})
					if !ok1 || !ok2 || !ok3 || !ok4 {
						fmt.Println("wrong indices:", ok1, ok2, ok3, ok4)
						os.Exit(1)
					}

					// 각 필드를 원하는 타입으로 변환
					common.AddrToKeyActive = make(map[common.Address]common.Hash)
					for k, v := range addrToKeyActive {
						addr := common.HexToAddress(k)
						hash := common.HexToHash(v.(string))
						common.AddrToKeyActive[addr] = hash
					}

					common.AddrToKeyInactive = make(map[common.Address][]common.Hash)
					for k, v := range addrToKeyInactive {
						addr := common.HexToAddress(k)
						hashes := make([]common.Hash, len(v.([]interface{})))
						for i, h := range v.([]interface{}) {
							hash := common.HexToHash(h.(string))
							hashes[i] = hash
						}
						common.AddrToKeyInactive[addr] = hashes
					}

					common.KeysToDelete = make([]common.Hash, len(keysToDelete))
					for i, v := range keysToDelete {
						hash := common.HexToHash(v.(string))
						common.KeysToDelete[i] = hash
					}

					common.RestoredKeys = make([]common.Hash, len(restoredKeys))
					for i, v := range restoredKeys {
						hash := common.HexToHash(v.(string))
						common.RestoredKeys[i] = hash
					}
				}

				fmt.Println("load state success")
				response = []byte("success")

			case "stopSimulation":
				fmt.Println("stop simulation")
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

// actual main() function
func StartEvmSimulator() {

	fmt.Println("start evm simulator")
	metrics.EnabledExpensive = enabledExpensive

	// create dir if not exist for log files
	err := os.MkdirAll(errLogPath, os.ModePerm)
	if err != nil {
		fmt.Println("ERROR: mkdirAll failed")
		os.Exit(1)
	}
	err = os.MkdirAll(simBlocksPath, os.ModePerm)
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
