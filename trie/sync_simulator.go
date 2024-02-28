package trie

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/prque"
)

// TODO(jmlee): implement fast sync simulator
// CAUTION: need to drop page caches before experiment
//
//	-> $ sudo sh -c "echo 1 > /proc/sys/vm/drop_caches"
func (t *Trie) MimicFastSync() {
	startTime := time.Now()

	fmt.Println("execute MimicFastSync()")

	//
	// choose strategy for priority: "depth", "hash"
	//

	priorityOption := "depth"
	// priorityOption := "hash"

	hashPriorityLen := 2 + 8 // including "0x"

	//
	// parameters
	//

	batchSize := 256 // # of max nodes to send at once (default: 256)

	//
	// initialize
	//

	// queue := prque.New[int, common.Hash](nil)
	queue := prque.New[int64, []byte](nil)
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

		queueSize := queue.Size()
		queueSizeSum += queueSize
		if queueSizeMax < queueSize {
			queueSizeMax = queueSize
		}
		nodeNum := batchSize
		if queue.Size() < batchSize {
			nodeNum = queue.Size()
		}
		fmt.Println("\nat round", roundNum, "-> # of nodes in the queue:", queueSize)
		fmt.Println("read", nodeNum, "trie nodes from the disk")

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

					// queue.Push(childBytes, childPriority)
				}
			} else {
				fmt.Println("node not found:", nodeHashToRead.Hex())
				os.Exit(1)
			}

		}

		// push newly known child nodes to receive
		fmt.Println("  # of newly pushed nodes:", len(childHashes))
		pushStartTime := time.Now()
		for i := 0; i < len(childHashes); i++ {
			queue.Push(childHashes[i], priorities[i])
		}
		queuePushTimeSum += time.Since(pushStartTime).Nanoseconds()

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
	fmt.Println("elapsed time:", time.Since(startTime))
	fmt.Println("  roundNum:", roundNum)
	fmt.Println("  readNodeCnt:", readNodeCnt)
	fmt.Println("  readNodeSizeSum:", readNodeSizeSum, "bytes (", float64(readNodeSizeSum)/float64(1000000000), "GB )")
	fmt.Println("  avg queueSize:", int64(queueSizeSum)/roundNum, "( max:", queueSizeMax, ")")
	fmt.Println("  avg queuePopTime:", queuePopTimeSum/roundNum, "ns per round -> portion:", float64(queuePopTimeSum)/float64(elapsedTime)*100, "%")
	fmt.Println("  avg queuePushTime:", queuePushTimeSum/roundNum, "ns per round -> portion:", float64(queuePushTimeSum)/float64(elapsedTime)*100, "%")
	fmt.Println("  avg nodeReadTime:", nodeReadTimeSum/roundNum, "ns per round -> portion:", float64(nodeReadTimeSum)/float64(elapsedTime)*100, "%")
	// fmt.Println("  avg nodeReadTime:", nodeReadTimeSum/int64(readNodeCnt), "ns per node")
}
