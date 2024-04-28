package common

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"
)

var (
	//
	// for evm simulation
	//

	// option: simulation mode (0: original ethereum, 1: Ethane, 2: Ethanos)
	SimulationMode      = 0
	EthereumMode        = 0
	EthaneMode          = 1
	EthanosMode         = 2
	SimulationModeNames = []string{"Ethereum", "Ethane", "Ethanos"}

	// simulation results, SimBlocks[blockNumStr] = SimBlock
	SimBlocks = make(map[string]*SimBlock)

	// enable snapshot or not
	EnableSnapshot = false

	// prefixing trie node's hash value with block number
	EnableNodePrefixing = false
	// actually, this may be prefix bytes (ex. PrefixLength = 3 -> prefixes 6 characters)
	PrefixLength = 0

	// opcode stats (opcode execution num/time/cost)
	LoggingOpcodeStats = false
	OpcodeStats        = make(map[string]*OpcodeStat)
	CurrentOpcodeStat  = NewOpcodeStat()

	// choose whether logging leveldb stats or not
	// this logging may have impact on performance
	// and might be largely incorrect when snapshot is enabled (due to concurrent trie node prefetching)
	// additionally, may have impact on "DiskCommits" time (need to check this)
	LoggingReadStats = false

	// flag for DoS attack
	IsDoSAttacking    = false
	CurrentAttackStat = NewAttackStat()

	//
	// Ethanos
	//
	// to deal with SELF_DESTRUCTed contract accounts
	DeletedContractRoot = HexToHash("0xdeadffffffffffffffffffffffffffffffffffffffffffffffffffffffffdead")
	// cached trie's root
	CachedTrieRoot Hash

	//
	// Ethane
	//
	AddrToKeyActive   = make(map[Address]Hash)   // K_A
	AddrToKeyInactive = make(map[Address][]Hash) // K_I
	KeysToDelete      = make([]Hash, 0)          // D_A
	RestoredKeys      = make([]Hash, 0)          // D_I

	InactiveTrieRoot Hash

	// next key to insert new account in active trie
	NextKey  = uint64(1)
	ZeroHash = Hash{}
	MaxHash  = HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	// INF epoch length for "deleteEpoch" & "inactivateEpoch" & "sweepEpoch"
	InfiniteEpoch = uint64(100000000)
)

// return simulation mode and its options
func GetSimulationTypeName() string {
	// simulation mode name
	name := SimulationModeNames[SimulationMode]

	// enable node prefixing: N
	if EnableNodePrefixing {
		name += "N"
	}

	// logging opcode stats: O
	if LoggingOpcodeStats {
		name += "O"
	}

	// TODO(jmlee): implement path-based scheme
	// enable path-based: P

	// logging read stats: R
	if LoggingReadStats {
		name += "R"
	}

	// enable snapshot: S
	if EnableSnapshot {
		name += "S"
	}

	return name
}

// store simulation results as a block with performance metrics
type SimBlock struct {
	Number          uint64 // block number
	StateRoot       Hash   // state trie root
	SubStateRoot    Hash   // cached trie root (Ethanos) or inactive trie root (Ethane)
	LastActiveKey   uint64 // last used key in active trie in this block (similar to CheckpointKey)
	LastInactiveKey uint64 // last used key in inactive trie in this block (similar to InactiveBoundaryKey)

	// payment tx num & execution time
	PaymentTxLen      uint64
	PaymentTxExecutes time.Duration
	// contract call tx num & execution time
	CallTxLen      uint64
	CallTxExecutes time.Duration
	GasUsed        uint64

	// from StateDB struct
	// Measurements gathered during execution for debugging purposes
	AccountReads           time.Duration
	AccountReadNum         int
	NonExistAccountReadNum int
	AccountHashes          time.Duration
	AccountUpdates         time.Duration
	AccountCommits         time.Duration
	StorageReads           time.Duration
	StorageHashes          time.Duration
	StorageUpdates         time.Duration
	StorageCommits         time.Duration
	SnapshotAccountReads   time.Duration
	SnapshotStorageReads   time.Duration
	SnapshotCommits        time.Duration
	TrieDBCommits          time.Duration
	AccountUpdated         int
	StorageUpdated         int
	AccountDeleted         int
	StorageDeleted         int

	DiskSize         int64         // disk usage (i.e., result of du -b)
	DiskCommits      time.Duration // flush time to disk
	BlockExecuteTime time.Duration // elapsed time to execute this block

	// restoration for Ethanos & Ethane
	AccountRestoreNum    int
	AccountRestores      time.Duration
	RestoreReads         time.Duration
	RestoreSubReads      time.Duration // void read for Ethane, cached trie read for Ethanos
	RestoreHashes        time.Duration
	RestoreUpdates       time.Duration
	RestoreCommits       time.Duration
	RestoreTrieDBCommits time.Duration
	RestoreDiskCommits   time.Duration

	// for Ethanos
	CachedAccountReads   time.Duration
	CachedAccountReadNum int

	// for Ethane
	ActiveIndexReads   time.Duration
	VoidAccountReadNum int
	VoidAccountReads   time.Duration // for fair comparison, Ethane reads random account when desired account does not exist
	// do not measure commit times independently for deletion and inactivation
	// just add to other metrics (AccountCommits, TrieDBCommits, DiskCommits)
	DeleteNum         int
	DeleteUpdates     time.Duration
	DeleteHashes      time.Duration
	InactivateNum     int
	InactivateUpdates time.Duration
	InactivateHashes  time.Duration
	UsedProofNum      int
	UsedProofUpdtaes  time.Duration
}

// buffer to save StateDB's metrics
var (
	AccountUpdated int
	StorageUpdated int
	AccountDeleted int
	StorageDeleted int
)

// Result of executing DoS attack contract
type AttackStat struct {
	// these are arranged in order of opcode execution
	OpcodeNames       []string
	ExecutionTimes    []int64
	GasCosts          []uint64
	RefundAmount      uint64 // refunded gas amount due to SSTORE, SELFDESTRUCT
	StartingGasCost   uint64
	TxDataGasCost     uint64
	AccessListGasCost uint64

	// TODO(jmlee): need this? (# of executing interpreter.Run())
	ContractCallNum uint64

	// TODO(jmlee): implement this later for storage attack
	IncTrieNodeNum  uint64
	IncTrieNodeSize uint64
}

func NewAttackStat() *AttackStat {
	as := new(AttackStat)
	as.OpcodeNames = make([]string, 0)
	as.ExecutionTimes = make([]int64, 0)
	as.GasCosts = make([]uint64, 0)
	return as
}

// store opcode related stat
type OpcodeStat struct {
	StartBlockNum uint64
	EndBlockNum   uint64

	ContractCallNum uint64

	OpcodeNums     map[string]int64
	OpcodeExecutes map[string]int64
	OpcodeCosts    map[string]uint64
}

func NewOpcodeStat() *OpcodeStat {
	os := new(OpcodeStat)
	os.OpcodeNums = make(map[string]int64)
	os.OpcodeExecutes = make(map[string]int64)
	os.OpcodeCosts = make(map[string]uint64)
	return os
}

func (ops *OpcodeStat) Add(otherOS *OpcodeStat) {

	if ops.StartBlockNum == 0 && ops.EndBlockNum == 0 {
		ops.StartBlockNum = otherOS.StartBlockNum
	} else if ops.EndBlockNum+1 != otherOS.StartBlockNum {
		fmt.Println("ERROR: cannot add these cache stats")
		fmt.Println("os.EndBlockNum:", ops.EndBlockNum)
		fmt.Println("otherOS.StartBlockNum:", otherOS.StartBlockNum)
		os.Exit(1)
	}
	ops.EndBlockNum = otherOS.EndBlockNum

	ops.ContractCallNum += otherOS.ContractCallNum

	for k, v := range otherOS.OpcodeNums {
		ops.OpcodeNums[k] += v
		ops.OpcodeExecutes[k] += otherOS.OpcodeExecutes[k]
		ops.OpcodeCosts[k] += otherOS.OpcodeCosts[k]
	}
}

func (os *OpcodeStat) Print() {
	fmt.Println("print OpcodeStat -> start block num:", os.StartBlockNum, "/ end block num:", os.EndBlockNum)
	fmt.Println("  contract call tx num:", os.ContractCallNum)

	mapKeys := make([]string, 0)
	for k, _ := range os.OpcodeNums {
		mapKeys = append(mapKeys, k)
	}
	sort.Strings(mapKeys)
	for _, opcode := range mapKeys {
		fmt.Println("  opcode:", opcode)
		fmt.Println("    -> avg:", uint64(os.OpcodeExecutes[opcode])/os.OpcodeCosts[opcode], "ns/gas ( num:", os.OpcodeNums[opcode], "/ execute time:", os.OpcodeExecutes[opcode], "ns / gas cost:", os.OpcodeCosts[opcode], ")")
	}
}

func PrintTotalOpcodeStat() {

	totalOpcodeStat := NewOpcodeStat()

	mapKeys := make([]string, 0)
	for k, _ := range OpcodeStats {
		mapKeys = append(mapKeys, k)
	}
	sort.Strings(mapKeys)
	for _, endBlockNum := range mapKeys {
		opcodeStat := OpcodeStats[endBlockNum]

		totalOpcodeStat.Add(opcodeStat)
		// opcodeStat.Print()
	}

	fmt.Println("print total opcode stats")
	totalOpcodeStat.Print()
}

func SaveOpcodeStat(endBlockNum uint64) {
	CurrentOpcodeStat.EndBlockNum = endBlockNum

	blockNumStr := fmt.Sprintf("%08d", endBlockNum)
	OpcodeStats[blockNumStr] = CurrentOpcodeStat
}

func ResetOpcodeStat(startBlockNum uint64) {
	fmt.Println("ResetOpcodeStat() executed")

	CurrentOpcodeStat = NewOpcodeStat()
	CurrentOpcodeStat.StartBlockNum = startBlockNum
}

// save OpcodeStats as a json file
func SaveOpcodeLogs(filePath string, deleteEpoch, inactivateEpoch, inactivateCriterion, sweepEpoch uint64) {
	// encoding map to json
	var jsonData []byte
	var err error

	// save all CacheStats at once
	jsonData, err = json.MarshalIndent(OpcodeStats, "", "  ")
	if err != nil {
		fmt.Println("JSON marshaling error:", err)
		return
	}

	// save as a json file
	mapKeys := make([]string, 0)
	for k, _ := range OpcodeStats {
		mapKeys = append(mapKeys, k)
	}
	sort.Strings(mapKeys)
	firstBlockNum := OpcodeStats[mapKeys[0]].StartBlockNum
	lastBlockNum := OpcodeStats[mapKeys[len(mapKeys)-1]].EndBlockNum
	var fileName string
	if SimulationMode == EthaneMode {
		fileName = "opcode_stats_" + GetSimulationTypeName() + "_" + strconv.FormatUint(firstBlockNum, 10) + "_" + strconv.FormatUint(lastBlockNum, 10) + "_" + strconv.FormatUint(deleteEpoch, 10) + "_" + strconv.FormatUint(inactivateEpoch, 10) + "_" + strconv.FormatUint(inactivateCriterion, 10) + ".json"
	} else if SimulationMode == EthanosMode {
		fileName = "opcode_stats_" + GetSimulationTypeName() + "_" + strconv.FormatUint(firstBlockNum, 10) + "_" + strconv.FormatUint(lastBlockNum, 10) + "_" + strconv.FormatUint(sweepEpoch, 10) + ".json"
	} else {
		fileName = "opcode_stats_" + GetSimulationTypeName() + "_" + strconv.FormatUint(firstBlockNum, 10) + "_" + strconv.FormatUint(lastBlockNum, 10) + ".json"
	}
	err = os.WriteFile(filePath+fileName, jsonData, 0644)
	if err != nil {
		fmt.Println("File write error:", err)
		return
	}
	fmt.Println("  saved file name:", fileName)
}
