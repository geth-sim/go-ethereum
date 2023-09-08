package evmsim

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

var (
	//
	// system params
	//
	sweepEpoch = uint64(1)
	// option: how many nodes to omit in Merkle proofs (ex. 1: omit root node)
	// restoreProofFromLevel = uint(0)

	// cached state trie's root
	// cachedTrieRoot  common.Hash // moved to common package
	cachedTrieRoots = make([]common.Hash, 0)
)

func sweep() {
	fmt.Println("Ethanos sweep() executed")
	common.CachedTrieRoot = currentStateRoot
	cachedTrieRoots = append(cachedTrieRoots, common.CachedTrieRoot)
	currentStateRoot = types.EmptyRootHash

	fmt.Println("after sweep")
	fmt.Println("  -> current root:", currentStateRoot.Hex())
	fmt.Println("  -> cached root:", common.CachedTrieRoot.Hex())
}

func restoreEthanosAddrs() int {
	// fmt.Println("restoreEthanosAddrs() start")

	currentTrie, err := trie.New(trie.StateTrieID(currentStateRoot), indepTrieDB)
	if err != nil {
		fmt.Println("trie.New() error:", err)
		os.Exit(1)
	}
	cachedTrie, err := trie.New(trie.StateTrieID(common.CachedTrieRoot), indepTrieDB)
	if err != nil {
		fmt.Println("cannot open cached trie")
		fmt.Println("root:", common.CachedTrieRoot.Hex())
		os.Exit(1)
	}
	// fmt.Println("  current root:", currentStateRoot.Hex())
	// fmt.Println("  cached root: ", common.CachedTrieRoot.Hex())

	// open new log file
	fileName := "restore_errors_Ethanos_" + strconv.FormatUint(sweepEpoch, 10) + ".txt"
	// os.Remove(errLogPath+fileName) // delete prev log file if exist
	// f, _ := os.OpenFile(errLogPath+fileName, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)

	// restore addresses
	restoredNum := 0
	for _, addr := range restoreAddrs {
		restoreSuccess := false
		addrHash := crypto.Keccak256Hash(addr[:])
		addrState := ""

		isValidRestoreTarget := true

		// check if this address is in current trie
		currentEnc, err := currentTrie.Get(addrHash[:])
		if err == nil && len(currentEnc) != 0 {
			fmt.Println("this account is already active -> len(enc):", len(currentEnc))
			isValidRestoreTarget = false
			addrState += "active | "
		}
		// check if this address is in cached trie
		cachedEnc, err := cachedTrie.Get(addrHash[:])
		if err == nil && len(cachedEnc) != 0 {
			fmt.Println("this account is cached -> len(enc):", len(cachedEnc))
			isValidRestoreTarget = false
			addrState += "cached | "
		}

		// do restore
		var deletedEnc []byte
		if isValidRestoreTarget {
			cachedTrieNum := len(cachedTrieRoots)
			for i := cachedTrieNum - 2; i >= 0; i-- {
				// checkpointTrie, err := trie.New(trie.StateTrieID(cachedTrieRoots[i]), subTrieDB)
				checkpointTrie, err := trie.New(trie.StateTrieID(cachedTrieRoots[i]), indepTrieDB)
				if err != nil {
					fmt.Println("restoreAccountForEthanos() error 3:", err)
					os.Exit(1)
				}
				enc, err := checkpointTrie.Get(addrHash[:])
				if err != nil {
					fmt.Println("checkpointTrie.TryGet err:", err)
					fmt.Println("restoreAddr:", addr)
					fmt.Println("enc:", enc)
					os.Exit(1)
				}
				if len(enc) != 0 {
					var latestAcc types.EthanosStateAccount
					// decode account
					if err := rlp.DecodeBytes(enc, &latestAcc); err != nil {
						fmt.Println("Failed to decode state object:", err)
						fmt.Println("restoreAddr:", addr)
						fmt.Println("enc:", enc)
						os.Exit(1)
					}

					// ignore deleted CA in Ethanos
					if latestAcc.Root == common.DeletedContractRoot {
						addrState += "deleted | "
						deletedEnc = enc
						break
					}

					// set restored flag
					latestAcc.Restored = true
					// encode restored account
					data, _ := rlp.EncodeToBytes(&latestAcc)

					err := currentTrie.Update(addrHash[:], data)
					if err != nil {
						fmt.Println("trie update err:", err)
						os.Exit(1)
					}

					// fmt.Println("  success restore -> addr:", addr.Hex())
					// fmt.Println("cachedTrieRoots:", cachedTrieRoots)
					// fmt.Println("find account at", i, "index checkpoint trie:", cachedTrieRoots[i])
					// latestAcc.Print()
					restoreSuccess = true
					restoredNum++
					break
				}
			}

			if !restoreSuccess {
				addrState += "not found"
			}

		}

		// if restoration failed, write error log
		if !restoreSuccess {
			fmt.Println("Ethanos restore err: something goes wrong")
			fmt.Println("  -> addr:", addr.Hex())
			fmt.Println("  -> blockNum:", currentBlockNum)
			fmt.Println("  -> addrState:", addrState)
			fmt.Println("  -> restoreSuccess:", restoreSuccess)

			currentAcc := new(types.EthanosStateAccount)
			if len(currentEnc) != 0 {
				// decode account
				if err := rlp.DecodeBytes(currentEnc, &currentAcc); err != nil {
					fmt.Println("Failed to decode state object:", err)
					fmt.Println("restoreAddr:", addr)
					fmt.Println("currentEnc:", currentEnc)
					os.Exit(1)
				}
				currentAcc.Print()
			}
			cachedAcc := new(types.EthanosStateAccount)
			if len(cachedEnc) != 0 {
				// decode account
				if err := rlp.DecodeBytes(cachedEnc, &cachedAcc); err != nil {
					fmt.Println("Failed to decode state object:", err)
					fmt.Println("restoreAddr:", addr)
					fmt.Println("cachedEnc:", cachedEnc)
					os.Exit(1)
				}
				cachedAcc.Print()
			}
			deletedAcc := new(types.EthanosStateAccount)
			if len(deletedEnc) != 0 {
				// decode account
				if err := rlp.DecodeBytes(deletedEnc, &deletedAcc); err != nil {
					fmt.Println("Failed to decode state object:", err)
					fmt.Println("restoreAddr:", addr)
					fmt.Println("deletedEnc:", deletedEnc)
					os.Exit(1)
				}
				deletedAcc.Print()
			}

			// save error log
			f, _ := os.OpenFile(errLogPath+fileName, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
			log := ""
			log += "\nat blocknum: " + strconv.FormatUint(currentBlockNum, 10)
			log += "\naddr: " + addr.Hex()
			log += "\naddress state: " + addrState
			if currentAcc != nil {
				log += "\n  current account info"
				log += "\n" + currentAcc.ToString()
			}
			if cachedAcc != nil {
				log += "\n  cached account info"
				log += "\n" + cachedAcc.ToString()
			}
			if deletedAcc != nil {
				log += "\n  deleted account info"
				log += "\n" + deletedAcc.ToString()
			}
			log += "\n"
			// write log to file
			fmt.Fprintln(f, log)
			f.Close()
		}
	}

	//
	// commit restored accounts
	//
	// TODO(jmlee): StateDB 기반으로 갱신해두고 commit은 나중에 tx 수행 끝나고 몰아서 하는게 맞으려나?
	// ethane에서도 restore, delete, inactivate도 그런 식으로 하는게 맞을 것 같기도 하고?
	// SetNonce() 같은 걸로 restore 하는 꼼수를 부릴 수 있을 것 같음
	if restoredNum == 0 {
		// no need to commit
		return 0
	}

	// flush to trie.Database (memdb) (i.e., trie.Commit() & trieDB.Update())
	newStateRoot, nodes, err := currentTrie.Commit(true)
	if err != nil {
		fmt.Println("at restoreEthanosAddrs(): trie.Commit() failed")
		fmt.Println("  err:", err)
		os.Exit(1)
	}
	// note: last field is for path-based scheme, the field is ok to be nil when using hash-based scheme
	indepTrieDB.Update(newStateRoot, currentStateRoot, currentBlockNum, trienode.NewWithNodeSet(nodes), nil)

	// flush to disk
	err = indepTrieDB.Commit(newStateRoot, false)
	if err != nil {
		fmt.Println("at Ethanos restoration: err:", err)
	}

	currentStateRoot = newStateRoot
	fmt.Println("finish restoration -> current root:", currentStateRoot.Hex())

	return restoredNum
}
