//go:build !leveldbstats

package statesim

const detailedLevelDBStatsEnabled = false
const simulatorBuildVariant = "fast"

func saveDetailedLevelDBReadStats(string) {}
