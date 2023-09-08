package evmsim

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

var (
	//
	// system params
	//
	deleteEpoch         = uint64(1)
	inactivateEpoch     = uint64(1)
	inactivateCriterion = uint64(1)

	//
	// common (for Ethanos & Ethane)
	//
	// option: how many nodes to omit in Merkle proofs (ex. 1: omit root node)
	restoreProofFromLevel = uint(0)
	// addresses to restore for the latest block
	restoreAddrs    = make([]common.Address, 0)
	accessAddrs     = make([]common.Address, 0) // addresses which will be read or written in current block
	totalRestoreNum = 0
)

// might be deprecated: use v2 if there is no problem
func restoreEthaneAddrs() int {
	fmt.Println("restoreEthaneAddrs() start")

	activeTrie, err := trie.New(trie.StateTrieID(currentStateRoot), indepTrieDB)
	if err != nil {
		fmt.Println("cannot open active trie")
		fmt.Println("  trie.New() error:", err)
		fmt.Println("  root:", currentStateRoot.Hex())
		os.Exit(1)
	}
	inactiveTrie, err := trie.New(trie.StateTrieID(common.InactiveTrieRoot), indepTrieDB)
	if err != nil {
		fmt.Println("cannot open inactive trie")
		fmt.Println("  trie.New() error:", err)
		fmt.Println("  root:", common.InactiveTrieRoot.Hex())
		os.Exit(1)
	}
	// fmt.Println("  active root:", currentStateRoot.Hex())
	// fmt.Println("  inactive root: ", common.InactiveTrieRoot.Hex())

	// open new log file
	fileName := "restore_errors_Ethane_" + strconv.FormatUint(sweepEpoch, 10) + ".txt"
	// os.Remove(errLogPath+fileName) // delete prev log file if exist
	// f, _ := os.OpenFile(errLogPath+fileName, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)

	// restore addresses
	restoredNum := 0
	for _, addr := range restoreAddrs {
		restoreSuccess := false
		addrState := ""

		isValidRestoreTarget := true

		// check if this address is in current trie
		activeAddrKey, exist := common.AddrToKeyActive[addr]
		if !exist {
			activeAddrKey = common.MaxHash
		}
		activeEnc, err := activeTrie.Get(activeAddrKey[:])
		if err == nil && len(activeEnc) != 0 {
			fmt.Println("restore err for addr:", addr)
			fmt.Println("this account is already active -> len(enc):", len(activeEnc))
			isValidRestoreTarget = false
			addrState += "active | "
		}

		// check if this address is not in inactive trie
		var inactiveAddrKey common.Hash
		inactiveAddrKeys, exist := common.AddrToKeyInactive[addr]
		if !exist {
			inactiveAddrKey = common.MaxHash
		}
		if len(inactiveAddrKeys) != 1 {
			fmt.Println("this cannot happen within conservative restoration scenario")
			fmt.Println("  addr:", addr)
			fmt.Println("  inactiveAddrKeys:", inactiveAddrKeys)
			fmt.Println("  len(inactiveAddrKeys):", len(inactiveAddrKeys))
			os.Exit(1)
		}
		inactiveAddrKey = inactiveAddrKeys[0]
		inactiveEnc, err := inactiveTrie.Get(inactiveAddrKey[:])
		if err == nil && len(inactiveEnc) == 0 {
			fmt.Println("this account is not in inactive trie -> len(enc):", len(inactiveEnc))
			isValidRestoreTarget = false
			addrState += "not found | "
		}

		// restore inactive account
		if isValidRestoreTarget {
			activeKey := common.HexToHash(strconv.FormatUint(common.NextKey, 16))
			common.NextKey++
			err := activeTrie.Update(activeKey[:], inactiveEnc)
			if err != nil {
				fmt.Println("trie update err:", err)
				os.Exit(1)
			}
			fmt.Println("  success restore -> addr:", addr.Hex())
			restoreSuccess = true
			restoredNum++

			// update K_A, K_I, D_I
			common.AddrToKeyActive[addr] = activeKey
			delete(common.AddrToKeyInactive, addr)
			common.RestoredKeys = append(common.RestoredKeys, inactiveAddrKey)
		}

		// if restoration failed, write error log
		if !restoreSuccess {
			fmt.Println("Ethane restore err: something goes wrong")
			fmt.Println("  -> addr:", addr.Hex())
			fmt.Println("  -> blockNum:", currentBlockNum)
			fmt.Println("  -> addrState:", addrState)
			fmt.Println("  -> restoreSuccess:", restoreSuccess)

			activeAcc := new(types.EthaneStateAccount)
			if len(activeEnc) != 0 {
				// decode account
				if err := rlp.DecodeBytes(activeEnc, &activeAcc); err != nil {
					fmt.Println("Failed to decode state object:", err)
					fmt.Println("restoreAddr:", addr)
					fmt.Println("activeEnc:", activeEnc)
					os.Exit(1)
				}
				activeAcc.Print()
			}

			// save error log
			f, _ := os.OpenFile(errLogPath+fileName, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
			log := ""
			log += "\nat blocknum: " + strconv.FormatUint(currentBlockNum, 10)
			log += "\naddr: " + addr.Hex()
			log += "\naddress state: " + addrState
			if activeAcc != nil {
				log += "\n  current account info"
				log += "\n" + activeAcc.ToString()
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

	// flush to trie.Database (memdb) (i.e., trie.Commit() & trieDB.Update())
	newStateRoot, nodes, err := activeTrie.Commit(true)
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

func restoreEthaneAddrsV2() int {
	fmt.Println("restoreEthaneAddrsV2() start")
	// fmt.Println("access addrs:", accessAddrs)

	activeTrie, err := trie.New(trie.StateTrieID(currentStateRoot), indepTrieDB)
	if err != nil {
		fmt.Println("cannot open active trie")
		fmt.Println("  trie.New() error:", err)
		fmt.Println("  root:", currentStateRoot.Hex())
		os.Exit(1)
	}
	inactiveTrie, err := trie.New(trie.StateTrieID(common.InactiveTrieRoot), indepTrieDB)
	if err != nil {
		fmt.Println("cannot open inactive trie")
		fmt.Println("  trie.New() error:", err)
		fmt.Println("  root:", common.InactiveTrieRoot.Hex())
		os.Exit(1)
	}
	// fmt.Println("  active root:", currentStateRoot.Hex())
	// fmt.Println("  inactive root: ", common.InactiveTrieRoot.Hex())

	// open new log file
	// fileName := "restore_errors_Ethane_" + strconv.FormatUint(sweepEpoch, 10) + ".txt"
	// os.Remove(errLogPath+fileName) // delete prev log file if exist
	// f, _ := os.OpenFile(errLogPath+fileName, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)

	// restore addresses
	restoredNum := 0
	for _, addr := range accessAddrs {
		// restoreSuccess := false
		// addrState := ""

		isValidRestoreTarget := true

		// check if this address is not in inactive trie
		var inactiveAddrKey common.Hash
		inactiveAddrKeys, exist := common.AddrToKeyInactive[addr]
		if !exist {
			// this address does not have inactive account, so no need to restore
			continue
		}
		if len(inactiveAddrKeys) > 1 {
			fmt.Println("ERROR: this cannot happen in conservative restoration scenario")
			fmt.Println("  addr:", addr)
			fmt.Println("  inactiveAddrKeys:", inactiveAddrKeys)
			fmt.Println("  len(inactiveAddrKeys):", len(inactiveAddrKeys))
			os.Exit(1)
		}
		inactiveAddrKey = inactiveAddrKeys[0]
		inactiveEnc, err := inactiveTrie.Get(inactiveAddrKey[:])
		if len(inactiveEnc) == 0 {
			fmt.Println("ERROR: inactive key is wrong. there is no account in this inactive key")
			fmt.Println("  addr:", addr)
			fmt.Println("  InactiveTrieRoot:", common.InactiveTrieRoot)
			fmt.Println("  inactiveAddrKey:", inactiveAddrKey.Big())
			fmt.Println("  inactiveEnc:", inactiveEnc)
			fmt.Println("  err:", err)
			os.Exit(1)
		}
		if common.BytesToAddress(inactiveEnc) != addr {
			fmt.Println("ERROR: address is not matched with the address written in the account")
			fmt.Println("  addr:", addr)
			fmt.Println("  addr in account:", common.BytesToAddress(inactiveEnc))
			os.Exit(1)
		}

		// check if this address is in current trie
		activeAddrKey, exist := common.AddrToKeyActive[addr]
		var activeEnc []byte
		if exist {
			activeEnc, err = activeTrie.Get(activeAddrKey[:])
			if len(activeEnc) != 0 {
				fmt.Println("ERROR: this address have both active and inactive account")
				fmt.Println("  addr:", addr)
				fmt.Println("  active addr key:", activeAddrKey.Big())
				fmt.Println("  inactive addr key:", inactiveAddrKey.Big())
				fmt.Println("  activeEnc:", activeEnc)
				fmt.Println("  inactiveEnc:", inactiveEnc)
				fmt.Println("  err:", err)
				os.Exit(1)
			} else {
				fmt.Println("ERROR: active key is wrong. there is no account in this active key")
				fmt.Println("  addr:", addr)
				fmt.Println("  ActiveTrieRoot:", currentStateRoot)
				fmt.Println("  activeAddrKey:", activeAddrKey.Big())
				fmt.Println("  activeEnc:", activeEnc)
				fmt.Println("  inactiveEnc:", inactiveEnc)
				fmt.Println("  err:", err)
				os.Exit(1)
			}
		}

		// restore inactive account
		if isValidRestoreTarget {
			activeKey := common.HexToHash(strconv.FormatUint(common.NextKey, 16))
			common.NextKey++
			err := activeTrie.Update(activeKey[:], inactiveEnc)
			if err != nil {
				fmt.Println("trie update err:", err)
				os.Exit(1)
			}
			fmt.Println("  success restore -> addr:", addr.Hex())
			// restoreSuccess = true
			restoredNum++

			// update K_A, K_I, D_I
			common.AddrToKeyActive[addr] = activeKey
			delete(common.AddrToKeyInactive, addr)
			common.RestoredKeys = append(common.RestoredKeys, inactiveAddrKey)
		}

		// // if restoration failed, write error log
		// if !restoreSuccess {
		// 	fmt.Println("Ethane restore err: something goes wrong")
		// 	fmt.Println("  -> addr:", addr.Hex())
		// 	fmt.Println("  -> blockNum:", currentBlockNum)
		// 	fmt.Println("  -> addrState:", addrState)

		// 	activeAcc := new(types.EthaneStateAccount)
		// 	if len(activeEnc) != 0 {
		// 		// decode account
		// 		if err := rlp.DecodeBytes(activeEnc, &activeAcc); err != nil {
		// 			fmt.Println("Failed to decode state object:", err)
		// 			fmt.Println("restoreAddr:", addr)
		// 			fmt.Println("activeEnc:", activeEnc)
		// 			os.Exit(1)
		// 		}
		// 		activeAcc.Print()
		// 	}

		// 	// save error log
		// 	f, _ := os.OpenFile(errLogPath+fileName, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
		// 	log := ""
		// 	log += "\nat blocknum: " + strconv.FormatUint(currentBlockNum, 10)
		// 	log += "\naddr: " + addr.Hex()
		// 	log += "\naddress state: " + addrState
		// 	if activeAcc != nil {
		// 		log += "\n  current account info"
		// 		log += "\n" + activeAcc.ToString()
		// 	}
		// 	log += "\n"
		// 	// write log to file
		// 	fmt.Fprintln(f, log)
		// 	f.Close()
		// }
	}

	//
	// commit restored accounts
	//

	accessAddrs = make([]common.Address, 0)
	if restoredNum == 0 {
		// no need to commit
		return 0
	}

	// flush to trie.Database (memdb) (i.e., trie.Commit() & trieDB.Update())
	newStateRoot, nodes, err := activeTrie.Commit(true)
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
