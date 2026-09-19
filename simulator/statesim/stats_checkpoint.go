package statesim

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
)

var (
	statsCheckpointSet  bool
	lastStatsCheckpoint uint64
)

// recordExperimentStats captures cumulative counters at the same block as a
// saved simulator prefix. Periodic calls and explicit saves can coincide; do
// not append duplicate child snapshots or sample background activity twice.
func recordExperimentStats(currentBlockNum uint64) {
	if statsCheckpointSet && lastStatsCheckpoint == currentBlockNum {
		return
	}
	blockNumStr := fmt.Sprintf("%08d", currentBlockNum)
	if common.MeasureChildStats {
		// check child stats validity
		hashedNodeNum := 0
		for _, v := range common.ModifiedChildNum {
			hashedNodeNum += v
		}
		if common.HashedFullNodeNum != hashedNodeNum {
			fmt.Println("ERROR: common.HashedFullNodeNum != hashedNodeNum")
			fmt.Println("  common.HashedFullNodeNum:", common.HashedFullNodeNum)
			fmt.Println("  hashedNodeNum:", hashedNodeNum)
			os.Exit(1)
		}

		// save child stats
		f, err := os.OpenFile("additional_node_stats.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			panic(err)
		}
		defer f.Close()

		fmt.Fprintf(f, "\nCurrent Block Number: %d\n"+
			"HashedFullNodeNum: %d\n"+
			"  nil   childs: %d -> %.2f%%\n"+
			"  clean childs: %d -> %.2f%%\n"+
			"  dirty childs: %d -> %.2f%%\n",
			currentBlockNum,
			common.HashedFullNodeNum,
			common.NilChildNum, float64(common.NilChildNum)*100/float64(common.HashedFullNodeNum)/16,
			common.CleanChildNum, float64(common.CleanChildNum)*100/float64(common.HashedFullNodeNum)/16,
			common.DirtyChildNum, float64(common.DirtyChildNum)*100/float64(common.HashedFullNodeNum)/16,
		)

		// Log ModifiedChildNum values
		fmt.Fprintf(f, "common.ModifiedChildNum:\n")
		for x, y := range common.ModifiedChildNum {
			percent := float64(y) * 100 / float64(hashedNodeNum)
			fmt.Fprintf(f, "  [%2d]\t= %6d\t-> %6.2f%%\n", x, y, percent)
		}

		fmt.Fprintf(f, "\nWrittenTrieNodeNum: %d\n", common.WrittenTrieNodeNum)
		fmt.Fprintf(f, "  HashedLeafNodeNum: %d\n", common.HashedLeafNodeNum)
		fmt.Fprintf(f, "  HashedShortNodeNum: %d\n", common.HashedShortNodeNum)
		fmt.Fprintf(f, "  HashedFullNodeNum: %d\n", common.HashedFullNodeNum)
	}

	// Save cumulative detailed LevelDB read stats (cache hit rate,
	// negative lookups, and bloom filter behavior). The fast build
	// compiles this path out; the leveldbstats build links the
	// measureReadStats goleveldb revision. Console printing remains
	// disabled because it is intended only for interactive debugging.
	if dbType == dbTypeLevelDB &&
		detailedLevelDBStatsEnabled {
		readStatFileName := "read_stats_" +
			experimentID + "_" +
			strconv.FormatUint(currentBlockNum, 10) + ".json"
		saveDetailedLevelDBReadStats(leveldbStatsPath + readStatFileName)
	}

	// save leveldb stats
	if dbType == dbTypeLevelDB {
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
	lastStatsCheckpoint = currentBlockNum
	statsCheckpointSet = true
}
