package trie

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/fdlimit"
	"github.com/ethereum/go-ethereum/common/prque"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
)

// TODO(jmlee): save sync results as a json file (ex. elapsed time, read node size) (per round)
//              and also leveldb's stats (cnt, time)
// TODO(jmlee): implement fast sync simulator
// TODO(jmlee): sync storage tries
// CAUTION: need to drop page caches before experiment
//
//	-> $ sudo sh -c "echo 1 > /proc/sys/vm/drop_caches"
func (t *Trie) MimicFastSync() {
	startTime := time.Now()

	fmt.Println("execute MimicFastSync()")

	//
	// choose strategy for priority: "depth", "hash"
	//
	// TODO(jmlee): add "path" option as a priority

	priorityOption := "depth"
	// priorityOption := "hash"

	hashPriorityLen := 2 + 8 // including "0x"

	//
	// parameters
	//

	batchSize := 384 // # of max nodes to send at once (default: 384)

	//
	// initialize
	//

	// queue := prque.New[int, common.Hash](nil)
	queue := prque.New[int64, []byte](nil) // higher priority pops out first
	rootHash := t.Hash()
	priority := int64(0)
	queue.Push(rootHash.Bytes(), priority)

	//
	// start read trie nodes
	//

	fmt.Println("start fast sync")
	roundNum := int64(0)
	readNodeCnt := 0
	readNodeSizeSum := 0
	queueSizeSum := 0
	queueSizeMax := 0
	queuePopTimeSum := int64(0)
	queuePushTimeSum := int64(0)
	nodeReadTimeSum := int64(0)
	for {
		roundNum++

		if queue.Size() == 0 {
			break
		}

		// update max queue size
		queueSize := queue.Size()
		queueSizeSum += queueSize
		if queueSizeMax < queueSize {
			queueSizeMax = queueSize
		}

		// get # of nodes to read: min(queueSize, batchSize)
		nodeNum := batchSize
		if queue.Size() < batchSize {
			nodeNum = queue.Size()
		}

		// print intermediate results
		if roundNum%100 == 0 {
			elapsedTime := time.Since(startTime)
			fmt.Println("\nat round", roundNum, "-> elapsed time:", elapsedTime)
			fmt.Println("  total read node size:", readNodeSizeSum, "B")
			if int(elapsedTime.Seconds()) !=  0{
				fmt.Println("  performance:", (readNodeSizeSum/1000)/int(elapsedTime.Seconds()), "KB/sec")
			}
			fmt.Println("  # of nodes in the queue:", queueSize)
			fmt.Println("  read", nodeNum, "trie nodes from the disk")
		}

		// read trie nodes to send
		var childHashes [][]byte
		var priorities []int64
		for i := 0; i < nodeNum; i++ {

			// select which node to read
			popStartTime := time.Now()
			nodeBytesToRead, priority := queue.Pop()
			queuePopTimeSum += time.Since(popStartTime).Nanoseconds()
			nodeHashToRead := common.BytesToHash(nodeBytesToRead)
			_ = priority

			// read trie node
			// fmt.Println("  read node for sync:", nodeHashToRead.Hex(), "/ priority:", priority)
			nodeReadStartTime := time.Now()
			blob, _ := t.reader.node(nil, nodeHashToRead)
			nodeReadTimeSum += time.Since(nodeReadStartTime).Nanoseconds()
			if len(blob) != 0 {
				readNodeCnt++
				readNodeSizeSum += len(blob)
				decNode := mustDecodeNode(nodeHashToRead.Bytes(), blob)

				// get child node hashes
				var children [][]byte
				switch node := decNode.(type) {
				case *shortNode:
					key := node.Key
					if hasTerm(key) {
						// this is leaf node
						// TODO(jmlee): decode account and get storage trie's root
						key = key[:len(key)-1]
					}

					if childHash, ok := (node.Val).(hashNode); ok {
						children = append(children, childHash)
					}

				case *fullNode:
					for i := 0; i < 17; i++ {
						if node.Children[i] != nil {
							if childHash, ok := (node.Children[i]).(hashNode); ok {
								children = append(children, childHash)
							}
						}
					}

				default:
					panic(fmt.Sprintf("unknown node: %+v", decNode))
				}

				// push child node hashes to queue
				for _, childBytes := range children {

					var childPriority int64
					// var err error
					switch priorityOption {
					case "depth":
						childPriority = priority + 1
					case "hash":
						childHash := common.BytesToHash(childBytes)
						childHashStr := childHash.String()[2:hashPriorityLen]
						// fmt.Println("  childHash:", childHash.String())
						// fmt.Println("  childHashStr:", childHashStr)
						childPriority, _ = strconv.ParseInt(childHashStr, 16, 64)
					default:
						childPriority = 0
					}

					childHashes = append(childHashes, childBytes)
					priorities = append(priorities, childPriority)
				}
			} else {
				fmt.Println("node not found:", nodeHashToRead.Hex())
				os.Exit(1)
			}

		}

		// push newly known child nodes to receive
		// fmt.Println("  # of newly pushed nodes:", len(childHashes))
		pushStartTime := time.Now()
		for i := 0; i < len(childHashes); i++ {
			queue.Push(childHashes[i], priorities[i])
		}
		queuePushTimeSum += time.Since(pushStartTime).Nanoseconds()

		// for test, delete this later
		// if roundNum > 10000 {
		// 	break
		// }

	}

	fmt.Println()
	fmt.Println("fast sync finished!")
	fmt.Println("trie root hash:", t.Hash().Hex())
	fmt.Println("priority option:", priorityOption)
	if priorityOption == "hash" {
		fmt.Println("  hash prefix len:", hashPriorityLen)
	}
	fmt.Println("batch size:", batchSize)
	elapsedTime := time.Since(startTime).Nanoseconds()
	fmt.Println("elapsed time:", elapsedTime, "ns (", time.Since(startTime), ")")
	fmt.Println("  roundNum:", roundNum)
	fmt.Println("  readNodeCnt:", readNodeCnt)
	fmt.Println("  readNodeSizeSum:", readNodeSizeSum, "bytes (", float64(readNodeSizeSum)/float64(1000000000), "GB )")

	fmt.Println("  avg queueSize:", int64(queueSizeSum)/roundNum, "( max:", queueSizeMax, ")")

	// fmt.Println("  avg queuePopTime:", queuePopTimeSum/roundNum, "ns per round -> portion:", float64(queuePopTimeSum)/float64(elapsedTime)*100, "%")
	// fmt.Println("  avg queuePushTime:", queuePushTimeSum/roundNum, "ns per round -> portion:", float64(queuePushTimeSum)/float64(elapsedTime)*100, "%")
	// fmt.Println("  avg nodeReadTime:", nodeReadTimeSum/roundNum, "ns per round -> portion:", float64(nodeReadTimeSum)/float64(elapsedTime)*100, "%")
	fmt.Println("  total queuePopTime:", queuePopTimeSum, "(avg time:", queuePopTimeSum/roundNum, "ns per round -> portion:", float64(queuePopTimeSum)/float64(elapsedTime)*100, "%")
	fmt.Println("  total queuePushTime:", queuePushTimeSum, "(avg time:", queuePushTimeSum/roundNum, "ns per round -> portion:", float64(queuePushTimeSum)/float64(elapsedTime)*100, "%")
	fmt.Println("  total nodeReadTime:", nodeReadTimeSum, "(avg time:", nodeReadTimeSum/roundNum, "ns per round -> portion:", float64(nodeReadTimeSum)/float64(elapsedTime)*100, "%")

	fmt.Println("\nfor excel logging")
	fmt.Println(readNodeCnt, readNodeSizeSum, elapsedTime, nodeReadTimeSum, queuePopTimeSum, queuePushTimeSum)
}

// TODO(jmlee): reload trie nodes in certain orders
// TODO(jmlee): maybe
func ReloadTrieNodes() {

	startTime := time.Now()

	leveldbPathPrefix := "/ethereum/th_plus/stateTries/"
	// protocol := "ethereum"
	protocol := "trie-hashimoto"
	blockNum := uint64(7000000)
	rootHash := common.HexToHash("0x006acfc0b01994385fa35e5298147e6c586929561d275e33746fe0abbca1fa5e")
	dbScheme := "hash"
	// dbScheme := "path"
	sortMode := "sorted"
	// sortMode := "random"
	dbPathSuffix := "_" + dbScheme + "_" + sortMode

	//
	// open leveldb
	//
	fmt.Println("open leveldb for reloading")

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
	leveldbPath := leveldbPathPrefix + protocol + "/" + strconv.FormatUint(blockNum, 10) + "_" + rootHash.Hex() + dbPathSuffix
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

	//
	// open trie node infos file
	//
	fmt.Println("open trie node infos file")
	filePath := leveldbPathPrefix + protocol + "/trieNodeInfos/"
	fileName := "trieNodeInfos_" + strconv.FormatUint(blockNum, 10) + "_" + rootHash.Hex() + "_" + sortMode
	fmt.Println("trie node infos:", filePath+fileName)
	file, err := os.Open(filePath + fileName)
	if err != nil {
		fmt.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	//
	// reload trie nodes
	//
	fmt.Println("start trie node reloading")
	scanner := bufio.NewScanner(file)
	cnt := 0
	// batch := frdb.NewBatch()
	for scanner.Scan() {
		cnt++
		if cnt%10000000 == 0 {
			fmt.Println("  # of written nodes:", cnt)
		}

		line := scanner.Text()
		parts := strings.Split(line, ",")
		parts = parts[:len(parts)-1] // remove empty part

		nodeHash := common.HexToHash(parts[0])
		encodedNode, err := hex.DecodeString(parts[len(parts)-1])
		if err != nil {
			fmt.Println("ERROR: in hex.DecodeString() ->", err)
			os.Exit(1)
		}
		// fmt.Println("cnt:", cnt, "/ nodeHash:", nodeHash.Hex(), "/ value:", encodedNode)

		//
		// TODO(jmlee): maybe this naive frdb.Put() makes db inefficient, find other ways to write (ex. batch write)
		//
		err = frdb.Put(nodeHash[:], encodedNode)
		if err != nil {
			fmt.Println("ERROR: in frdb.Put() ->", err)
			os.Exit(1)
		}

		// rawdb.WriteLegacyTrieNode(batch, nodeHash, encodedNode)
		// if batch.ValueSize() >= ethdb.IdealBatchSize {
		// 	if err := batch.Write(); err != nil {
		// 		fmt.Println("ERROR: batch.Write() ->", err)
		// 		os.Exit(1)
		// 	}
		// 	// TODO(jmlee): why replay is needed? check this later
		// 	// err := batch.Replay(uncacher)
		// 	// if err != nil {
		// 	// 	return err
		// 	// }
		// 	batch.Reset()
		// }

	}

	// // Trie mostly committed to disk, flush any batch leftovers
	// if err := batch.Write(); err != nil {
	// 	fmt.Println("ERROR: Failed to write trie to disk", "err", err)
	// 	os.Exit(1)
	// }
	// // TODO(jmlee): why replay is needed? check this later
	// // Uncache any leftovers in the last batch
	// // if err := batch.Replay(uncacher); err != nil {
	// // 	return err
	// // }
	// batch.Reset()

	err = frdb.Sync()
	if err != nil {
		fmt.Println("ERROR: frdb.Sync error:", err)
		os.Exit(1)
	}
	err = frdb.Close()
	if err != nil {
		fmt.Println("ERROR: frdb.Close error ->", err)
		os.Exit(1)
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("Error reading file:", err)
	}

	fmt.Println("ReloadTrieNodes() finished -> elapsed:", time.Since(startTime))
}

// TODO(jmlee): count trie nodes num per prefix/blockNum

// TODO(jmlee): convert hash-based state trie to path-based state trie

// TODO(jmlee):
