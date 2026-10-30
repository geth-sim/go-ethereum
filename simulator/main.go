package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common"

	"github.com/ethereum/go-ethereum/simulator/statesim"
)

func main() {
	fmt.Println("run main.go")

	if len(os.Args) > 1 {
		args := os.Args[1:]
		statesim.ServerPort = args[0]
		fmt.Println("set new server port:", statesim.ServerPort)
	} else {
		fmt.Println("using default server port:", statesim.ServerPort)
	}
	// Optional flags follow the existing positional port. Defaults preserve the
	// original source-configured simulator behavior.
	opts := flag.NewFlagSet("state simulator", flag.ExitOnError)
	opts.StringVar(&common.TrieAuthentication, "auth", common.TrieAuthentication, "authentication: ristretto255, p256, keccak, gls254 (optional build); empty disables")
	opts.StringVar(&common.TrieAuthMode, "auth-mode", common.TrieAuthMode, "incremental or full")
	opts.IntVar(&common.TrieAuthWorkers, "auth-workers", common.TrieAuthWorkers, "authentication workers: 16=geth branch slots, 1=serial, 2..15=scaling controls")
	opts.IntVar(&common.TrieAuthCacheMB, "auth-cache-mb", common.TrieAuthCacheMB, "additional authentication computation cache in MiB; 0 disables")
	opts.StringVar(&common.TrieAuthCacheMode, "auth-cache-mode", common.TrieAuthCacheMode, "unified, accumulator, contribution; one budget across both trie types")
	opts.StringVar(&common.ModifyHashMethod, "key-scheme", common.ModifyHashMethod, "existing key scheme")
	opts.Uint64Var(&common.EpochSize, "epoch-size", common.EpochSize, "key scheme epoch")
	opts.Int64Var(&common.DepthThreshold, "state-depth", common.DepthThreshold, "state path nibble cutoff")
	opts.Int64Var(&common.StorageDepthThreshold, "storage-depth", common.StorageDepthThreshold, "storage branch nibble cutoff")
	if len(os.Args) > 2 {
		opts.Parse(os.Args[2:])
	}
	if opts.NArg() != 0 {
		panic("unexpected simulator arguments")
	}
	statesim.SetDbPath("")

	statesim.StartStateSimulator()
	// triesim.StartTrieSimulator()
}
