package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/simulator/statesim"
)

func main() {
	config := statesim.DefaultSimulatorConfig()
	flags := flag.NewFlagSet("state-simulator", flag.ExitOnError)

	flags.StringVar(&config.Port, "port", config.Port, "TCP port for simulator requests")
	flags.StringVar(&config.Scheme, "scheme", config.Scheme, "key scheme: H, P, PH, PV, PVstar, VH, VP, or VPstar")
	flags.StringVar(&config.StateMode, "state-mode", config.StateMode, "state retention mode: auto, archive, or non-archive")
	flags.BoolVar(&config.PathDBHistory, "pathdb-history", config.PathDBHistory, "persist PathDB reverse-diff history (scheme P only)")
	flags.StringVar(&config.Database, "db", config.Database, "database backend: leveldb or pebbledb")
	flags.StringVar(&config.Compression, "compression", config.Compression, "database compression: snappy, none, or zstd")
	flags.BoolVar(&config.MyHash, "myhash", config.MyHash, "model myHash authentication overhead")
	flags.IntVar(&config.MyHashCacheMB, "myhash-cache-mb", config.MyHashCacheMB, "total myHash cache size in MB (requires --myhash)")
	flags.StringVar(&config.MyHashCacheMode, "myhash-cache-mode", config.MyHashCacheMode, "myHash cache layout: unified or split")
	flags.Float64Var(&config.DiskSizeMultiplier, "disk-size-multiplier", config.DiskSizeMultiplier, "append random bytes to multiply stored trie-node size")
	flags.StringVar(&config.VersionWrap, "version-wrap", config.VersionWrap, "VH version wrap: none, 0xffff, or 0xfffff")
	flags.BoolVar(&config.AccurateReadCounters, "accurate-read-counters", config.AccurateReadCounters, "collect accurate node and myHash read counters")
	flags.BoolVar(&config.ChildStats, "child-stats", config.ChildStats, "collect detailed trie child statistics")
	flags.Uint64Var(&config.DiskSizeInterval, "disk-size-interval", config.DiskSizeInterval, "block interval for measuring database size")
	flags.Uint64Var(&config.LevelDBStatsInterval, "leveldb-stats-interval", config.LevelDBStatsInterval, "block interval for LevelDB statistics")

	if err := flags.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "failed to parse simulator options:", err)
		os.Exit(2)
	}
	// Preserve the original `go run main.go <port>` interface. When flags are
	// used, a trailing positional port is accepted as well.
	switch flags.NArg() {
	case 0:
	case 1:
		config.Port = flags.Arg(0)
	default:
		fmt.Fprintln(os.Stderr, "usage: state-simulator [options] [legacy-port]")
		os.Exit(2)
	}
	if err := statesim.ConfigureSimulator(config); err != nil {
		fmt.Fprintln(os.Stderr, "invalid simulator configuration:", err)
		os.Exit(2)
	}

	statesim.SetDbPath("")

	statesim.StartStateSimulator()
	// triesim.StartTrieSimulator()
}
