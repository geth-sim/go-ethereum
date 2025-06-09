package common

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	// temp vars for test
	TruncateFromTailCnt = 0
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

	// LevelDB stats, LevelDBStats[blockNumStr] = LevelDBStat
	LevelDBStats = make(map[string]*LevelDBStat)

	// TODO(jmlee): set archive mode or not
	IsArchiveMode = true

	// TODO(jmlee): implement path-based scheme
	// state scheme is path-based or hash-based
	IsPathScheme = false

	// enable snapshot or not
	EnableSnapshot = false

	//
	// modify nodeHash options
	//

	// length for each prefixes, sum of lengths must be <= 64 (= hash's hex string length)
	VersionLength        = 0    // hex string length when overwriting version number to nodeHash (recommanded: 8)
	EnableVersionPadding = true // option: 0-padding version string

	PathLength       = 0     // hex string length when overwriting path to nodeHash, max(len(path)) = 16 until 6M blocks, so this should be >= 16
	FixedPathLength  = false // option: path length is fixed or not
	PathPaddingAtEnd = true  // option: 0-padding position for path -> end or front

	AppendPathFirst = false // option: path-version vs version-path

	LastPaddingBound = 0 // padding prefix until len(prefix) = LastPaddingBound (max: 64, to disable: 0)

	AppendPathLen = false // option: overwrite len(path) to nodeHash (at the end)
	LenOfPathLen  = 2     // hex string length when overwriting len(path) to nodeHash, max(len(path)) = 16 until 6M blocks, so 2 is enough to present path len

	AppendTrieType = false // option: distinguish state trie node vs storage trie node -> state trie node: "d" or "e", storage trie node: "f"

	AppendContractAddrHash = false // option: overwrite CA's addrHash to nodeHash
	AddrHashPrefixLen      = 16    // hex string length when overwriting CA's addrHash to nodeHash, maybe should be >= 16 until 6M blocks

	HashingStateTrie             = false // flag: now hashing state trie
	HashingStorageTrie           = false // flag: now hashing storage tries
	AddrHashOfCurrentStorageTrie Hash    // addrHash of CA whose storage trie is being hashed

	ReadAllChildNodes = false // option: read all full node's child nodes when hashing
	NodeReadFuncCnt = 0 // # of trie.hashdb.Database.node() execution
	AdditionalNodeReadFuncCnt = 0 // # of trie.hashdb.Database.node() execution due to ReadAllChildNodes option
	// detailed read stats (these might not be 100% accurate due to goroutines)
	CleanHitCnt = 0
	DirtyHitCnt = 0 // this should be 0 in archive mode
	DiskHitCnt = 0
	NotFoundHitCnt = 0 // this should be 0

	// CAUTION: maybe need to remote disk before re-run simulator when modifying nodeHash
	// CAUTION: modified (root) node hash must not be common.Hash{} (= 0x000...0), this is treated as types.EmptyRootHash

	ModifyHashes time.Duration // execution time of modifyHash() in the current block

	ModifyHashMethod = "none" // option: JMT, JMT_fixed, PrefixTree, PrefixTree_fixed, HalfPath, PBSS, TH, none

	GenesisStateRoot Hash // state root of genesis block

	// opcode stats (opcode execution num/time/cost)
	LoggingOpcodeStats = false
	OpcodeStats        = make(map[string]*OpcodeStat)
	CurrentOpcodeStat  = NewOpcodeStat()

	// TODO(jmlee): choose whether logging leveldb stats or not
	// this logging have less impact on performance,
	// but might be incorrect when snapshot is enabled (due to concurrent trie node prefetching)
	// additionally, may have impact on "DiskCommits" time (need to check this)
	LoggingReadStats = false

	// flag for DoS attack
	IsDoSAttacking    = false
	CurrentAttackStat = NewAttackStat()
)

// return simulation mode and its options
//
// Ethereum mode options: AH, AHS, P, PS, FH, FHS
//
//	A: archive mode     vs F: non-archive mode (full)
//	H: hash-based state vs P: path-based state
//	S: snapshot enabled
//
// Logging options: O, R, OR
//
//	R: read stats
//	O: opcode stats
func GetSimulationTypeName() string {
	// simulation mode name
	simModeName := SimulationModeNames[SimulationMode]

	//
	// get blockchain node options
	//

	chainOptions := make([]string, 0)
	if IsPathScheme {
		// path-based should be non-archive (so omit "A" or "F")
		// node prefixing is not allowed in path-based
		chainOptions = append(chainOptions, "P")
	} else {
		chainOptions = append(chainOptions, "H")
		if IsArchiveMode {
			chainOptions = append(chainOptions, "A")
		} else {
			chainOptions = append(chainOptions, "F")
		}
		if VersionLength+PathLength > 0 {
			chainOptions = append(chainOptions, "N")
		}
	}
	if EnableSnapshot {
		chainOptions = append(chainOptions, "S")
	}

	sort.Strings(chainOptions)
	chainOptionName := ""
	for _, char := range chainOptions {
		chainOptionName += char
	}

	//
	// get logging options
	//

	logOptionName := ""
	if LoggingOpcodeStats {
		logOptionName += "O"
	}
	if LoggingReadStats {
		logOptionName += "R"
	}

	//
	// return name
	//

	name := simModeName + chainOptionName
	if logOptionName != "" {
		name += "_" + logOptionName
	}

	// fmt.Println("name:", name)
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
	ModifyHashes           time.Duration
	AccountUpdated         int
	StorageUpdated         int
	AccountDeleted         int
	StorageDeleted         int

	DiskSize         int64         // disk usage (i.e., result of du -b)
	DiskCommits      time.Duration // flush time to disk
	BlockExecuteTime time.Duration // elapsed time to execute this block

	HistorySize int64 // size of state history for path-based state
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

func (as *AttackStat) Print() {
	fmt.Println("Print AttackStat")
	for k, _ := range as.OpcodeNames {
		fmt.Println("opcode name:", as.OpcodeNames[k], "/ exec time:", as.ExecutionTimes[k], "/ gas:", as.GasCosts[k])
	}
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
func SaveOpcodeLogs(filePath string) {
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
	if len(mapKeys) == 0 {
		fmt.Println("there is no OpcodeLogs to save")
		return
	}
	firstBlockNum := OpcodeStats[mapKeys[0]].StartBlockNum
	lastBlockNum := OpcodeStats[mapKeys[len(mapKeys)-1]].EndBlockNum
	fileName := "opcode_stats_" + GetSimulationTypeName() + "_" + strconv.FormatUint(firstBlockNum, 10) + "_" + strconv.FormatUint(lastBlockNum, 10) + ".json"
	err = os.WriteFile(filePath+fileName, jsonData, 0644)
	if err != nil {
		fmt.Println("File write error:", err)
		return
	}
	fmt.Println("  saved file name:", fileName)
}

// metadata to modify nodeHash (jmlee)
type TrieNodeData struct {
	NodeHash Hash

	Path []byte

	AddrHash Hash

	Depth int64

	NodeType string

	EncodedNode []byte
}

//
// LevelDB stats (from LevelDB's GetProperty(name string) function)
// (CAUTION: The statistics are valid only when the simulator is executed from block 0 to the end without interruption. 
// Restarting the simulator resets all previously accumulated statistics.)
//
type CompactionStat struct {
	Level   int     `json:"level"`
	Tables  int     `json:"tables"`
	SizeMB  float64 `json:"size_mb"`
	TimeSec float64 `json:"time_sec"`
	ReadMB  float64 `json:"read_mb"`
	WriteMB float64 `json:"write_mb"`
}

type CompactionStatsOutput struct {
	Stats []CompactionStat `json:"stats"`
	Total CompactionStat   `json:"total"`
}

type IOStats struct {
	ReadMB  float64 `json:"read_mb"`
	WriteMB float64 `json:"write_mb"`
}

type WriteDelay struct {
	DelayN   int     `json:"delay_n"`
	Delay    string  `json:"delay"`
	DelaySec float64 `json:"delay_sec"`
	Paused   bool    `json:"paused"`
}

type CompCount struct {
	MemComp       int `json:"mem_comp"`
	Level0Comp    int `json:"level0_comp"`
	NonLevel0Comp int `json:"non_level0_comp"`
	SeekComp      int `json:"seek_comp"`
}

type LevelDBStat struct {
	BlockNum        uint64                `json:"block_num"`
	Compaction      CompactionStatsOutput `json:"compaction"`
	IO              IOStats               `json:"io"`
	WriteDelay      WriteDelay            `json:"write_delay"`
	CompactionCount CompCount             `json:"compaction_count"`
	OpenedTables    int                   `json:"opened_tables"`
}

// LevelDB print MiB as MB, so need to convert them for accuracy
func MiBtoMB(mib float64) float64 {
	const miToMb = 1.048576
	mb := mib * miToMb
	rounded, _ := strconv.ParseFloat(fmt.Sprintf("%.5f", mb), 64)
	return rounded
}

func ParseStats(raw string) CompactionStatsOutput {
	// e.g.,
	// 	Compactions
	// 	Level |   Tables   |    Size(MB)   |    Time(sec)  |    Read(MB)   |   Write(MB)
	//    -------+------------+---------------+---------------+---------------+---------------
	// 	  0   |          0 |       0.00000 |    2006.46115 |       0.00000 | 1281697.25207
	// 	  1   |        528 |    1050.89133 |    8159.66828 | 1315175.55528 | 1313873.02606
	// 	  2   |       1068 |    2091.47431 |    1213.24795 |  120415.45560 |  120414.95415
	// 	  3   |       4943 |    9999.38435 |       0.00000 |       0.00000 |       0.00000
	// 	  4   |      49424 |   99998.67841 |       0.00000 |       0.00000 |       0.00000
	// 	  5   |     494279 |  999998.49897 |       0.00000 |       0.00000 |       0.00000
	// 	  6   |      82605 |  167255.29403 |       0.00000 |       0.00000 |       0.00000
	//    -------+------------+---------------+---------------+---------------+---------------
	// 	Total |     632847 | 1280394.22141 |   11379.37738 | 1435591.01087 | 2715985.23228

	lines := strings.Split(raw, "\n")
	stats := []CompactionStat{}
	var total CompactionStat

	start := false
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "-------") {
			start = true
			continue
		}
		if !start || line == "" {
			continue
		}

		if strings.HasPrefix(line, "Total") {
			parts := strings.Split(line, "|")
			if len(parts) < 6 {
				continue
			}
			total.Level = -1
			total.Tables, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
			total.SizeMB, _ = strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
			total.TimeSec, _ = strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
			total.ReadMB, _ = strconv.ParseFloat(strings.TrimSpace(parts[4]), 64)
			total.WriteMB, _ = strconv.ParseFloat(strings.TrimSpace(parts[5]), 64)

			total.SizeMB = MiBtoMB(total.SizeMB)
			total.ReadMB = MiBtoMB(total.ReadMB)
			total.WriteMB = MiBtoMB(total.WriteMB)
			continue
		}

		if strings.Contains(line, "|") {
			parts := strings.Split(line, "|")
			if len(parts) < 6 {
				continue
			}
			level, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
			tables, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
			sizeMB, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
			timeSec, _ := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
			readMB, _ := strconv.ParseFloat(strings.TrimSpace(parts[4]), 64)
			writeMB, _ := strconv.ParseFloat(strings.TrimSpace(parts[5]), 64)

			stats = append(stats, CompactionStat{
				Level:   level,
				Tables:  tables,
				SizeMB:  MiBtoMB(sizeMB),
				TimeSec: timeSec,
				ReadMB:  MiBtoMB(readMB),
				WriteMB: MiBtoMB(writeMB),
			})
		}
	}

	return CompactionStatsOutput{
		Stats: stats,
		Total: total,
	}
}

func ParseIOStats(raw string) IOStats {
	// e.g., "Read(MB):8455544.72528 Write(MB):3986768.26486"
	fields := strings.Fields(raw)
	read, _ := strconv.ParseFloat(strings.Split(fields[0], ":")[1], 64)
	write, _ := strconv.ParseFloat(strings.Split(fields[1], ":")[1], 64)
	return IOStats{MiBtoMB(read), MiBtoMB(write)}
}

func ParseWriteDelay(raw string) WriteDelay {
	// e.g., "DelayN:0 Delay:0s Paused:false"
	parts := strings.Fields(raw)
	n, _ := strconv.Atoi(strings.Split(parts[0], ":")[1])
	delay := strings.Split(parts[1], ":")[1]
	delaySec := 0.0
	delayDur, err := time.ParseDuration(delay)
	if err == nil {
		delaySec = delayDur.Seconds()
	}
	paused, _ := strconv.ParseBool(strings.Split(parts[2], ":")[1])
	return WriteDelay{n, delay, delaySec, paused}
}

func ParseCompCount(raw string) CompCount {
	// e.g., "MemComp:2508 Level0Comp:627 NonLevel0Comp:30090 SeekComp:0"
	parts := strings.Fields(raw)
	m, _ := strconv.Atoi(strings.Split(parts[0], ":")[1])
	l0, _ := strconv.Atoi(strings.Split(parts[1], ":")[1])
	nl0, _ := strconv.Atoi(strings.Split(parts[2], ":")[1])
	seek, _ := strconv.Atoi(strings.Split(parts[3], ":")[1])
	return CompCount{m, l0, nl0, seek}
}
