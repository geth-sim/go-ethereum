//go:build leveldbstats

package statesim

import "github.com/syndtr/goleveldb/leveldb"

const detailedLevelDBStatsEnabled = true
const simulatorBuildVariant = "stats"

func saveDetailedLevelDBReadStats(filePath string) {
	leveldb.SaveMyReadStats(filePath)
}
