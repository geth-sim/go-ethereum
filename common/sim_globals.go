package common

import (
	"crypto/rand"
	"fmt"
	mrand "math/rand"
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

	// LevelDB stats, LevelDBStats[blockNumStr] = LevelDBStat
	LevelDBStats = make(map[string]*LevelDBStat)

	// TODO(jmlee): set archive mode or not
	IsArchiveMode = true

	// TODO(jmlee): implement path-based scheme
	// state scheme is path-based or hash-based
	IsPathScheme = false

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
	HashingInactiveTrie          = false // flag: now hashing inactive trie of Ethane (TODO(jmlee): implement this)

	ReadAllChildNodes         = false // option: read all full node's child nodes when hashing
	NodeReadFuncCnt           = 0    // # of trie.hashdb.Database.node() execution
	AdditionalNodeReadFuncCnt = 0    // # of trie.hashdb.Database.node() execution due to ReadAllChildNodes option
	// detailed read stats (these might not be 100% accurate due to goroutines)
	CleanHitCnt    = 0
	DirtyHitCnt    = 0 // this should be 0 in archive mode
	DiskHitCnt     = 0
	NotFoundHitCnt = 0 // this should be 0

	// implement MyHash (as PrefixTree)
	AdditionalByteLen = 0 // 0 means that MyHash is disabled
	prng              = mrand.New(mrand.NewSource(time.Now().UnixNano()))

	// measure MyHash stats (for accurate measure, need to set hasher.parallel = false)
	MeasureChildStats  = true // option: if this is enabled, parallel trie node hashing is disabled for accurate measure
	NilChildNum        = 0
	DirtyChildNum      = 0
	CleanChildNum      = 0
	ModifiedChildNum   = make([]int, 17) // ex. ModifiedChildNum[x] = y, meaning that there are y branch nodes in which x child nodes are modified
	WrittenTrieNodeNum = 0               // this should be HashedFullNodeNum + HashedShortNodeNum
	HashedFullNodeNum  = 0
	HashedShortNodeNum = 0
	HashedLeafNodeNum  = 0

	// CAUTION: maybe need to remote disk before re-run simulator when modifying nodeHash
	// CAUTION: modified (root) node hash must not be common.Hash{} (= 0x000...0), this is treated as types.EmptyRootHash

	ModifyHashes time.Duration // execution time of modifyHash() in the current block

	// Ethane + newScheme error list (ED: 10, EI: 10, THI: 1000): JMT_fixed at block 1009 / PrefixTree_fixed at block 1009 / PBSS는 왜 되는거지?
	// _fixed의 경우 최초의 inactivate 시에 계속 같은 addr을 inactivate 하게 되는데 왜그런지 모르겠네
	ModifyHashMethod = "PrefixTree_fixed" // option: JMT, JMT_fixed, JMT_balanced, PrefixTree, PrefixTree_fixed, PrefixTree_balanced, HalfPath, PBSS, TH, none

	GenesisStateRoot Hash // state root of genesis block

	// temp vars for measurement
	WrittenNodeHashes = make(map[Hash]bool)
	ModifiedNodeHashes = make(map[string]bool)
	SpecialDirtyChildCnt = 0
	SpecialCleanChildCnt = 0

)

func ClearDirtyStats() {
	NilChildNum = 0
	DirtyChildNum = 0
	CleanChildNum = 0
	ModifiedChildNum = make([]int, 17) // ex. ModifiedChildNum[x] = y, meaning that there are y branch nodes in which x child nodes are modified

	WrittenTrieNodeNum = 0
	HashedFullNodeNum = 0
	HashedShortNodeNum = 0
	HashedLeafNodeNum = 0
}

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

// metadata to modify nodeHash (jmlee)
type TrieNodeData struct {
	NodeHash Hash

	Path []byte

	AddrHash Hash

	Depth int64

	NodeType string

	EncodedNode []byte
}

// LevelDB stats (from LevelDB's GetProperty(name string) function)
// (CAUTION: The statistics are valid only when the simulator is executed from block 0 to the end without interruption.
// Restarting the simulator resets all previously accumulated statistics.)
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

// RandomBytes returns a cryptographically secure random slice of given length.
// It uses crypto/rand, which is slower but suitable for security-sensitive use.
func RandomBytes(length int) ([]byte, error) {
	b := make([]byte, length)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// FastRandomBytes returns a pseudo-random slice of given length.
// It uses math/rand with a pre-seeded PRNG, which is much faster than crypto/rand,
// but not cryptographically secure. Suitable when only "random-looking" data is needed.
func FastRandomBytes(length int) []byte {
	b := make([]byte, length)
	for i := 0; i < length; i++ {
		b[i] = byte(prng.Intn(256))
	}
	return b
}
