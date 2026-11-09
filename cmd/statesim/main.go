// statesim replays original signed blocks through Geth's persistent import path.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/fdlimit"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/internal/version"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
	"github.com/shirou/gopsutil/disk"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	processStarted := time.Now()
	input := flag.String("blocks", "", "external concatenated block RLP file (signed transactions, complete uncles)")
	dir := flag.String("datadir", "", "dedicated experiment directory")
	genesisFile := flag.String("genesis", "", "genesis JSON; empty selects Ethereum mainnet")
	until := flag.Uint64("until", 0, "last block to execute, inclusive")
	verify := flag.Bool("verify-state", false, "rebuild canonical state after import (untimed correctness check)")
	resume := flag.Bool("resume", false, "continue the same configuration/database")
	dropOSCache := flag.Bool("drop-os-cache", false, "Linux: sync and drop system-wide page/dentry/inode caches before DB open; requires noninteractive sudo")
	archive := flag.Bool("archive", true, "retain all hashdb states; false uses native dirty-cache GC (PBSS defaults to false)")
	scheme := flag.String("scheme", "H", "H, PBSS (native pathdb), VP, PV, Outward and legacy candidate names (see README)")
	shard := flag.Int("shard-prefix", 1, "ShardVP owner prefix nibbles: 1,2,4")
	tile := flag.Uint64("tile-blocks", 128, "ATileVP version tile size")
	tilePrefix := flag.Int("tile-path-prefix", 1, "ATileVP storage path prefix nibbles")
	runTarget := flag.Uint64("run-target-nodes", 524288, "RunPath written-node target per run")
	epoch := flag.Uint64("epoch", 128, "Outward epoch size")
	a := flag.Int("state-depth", 3, "Outward account depth, inclusive")
	s := flag.Int("storage-depth", 2, "Outward storage branch depth, inclusive; -1 disables")
	skip := flag.Bool("skip-intermediate-root", true, "hash only at block end before Byzantium, uniformly across schemes")
	verifyRoots := flag.Bool("verify-block-roots", false, "recompute/verify transaction, uncle and receipt roots; state/auth calculation always remains enabled")
	prefetch := flag.Bool("prefetch", false, "enable both speculative transaction and trie-path prefetching")
	snapshotCache := flag.Int("snapshot-cache", 0, "flat-state read cache MiB; 0 disables hashdb snapshots (PBSS keeps flat-state storage)")
	auth := flag.String("auth", "none", "none, keccak, ristretto255, p256, gls254")
	authMode := flag.String("auth-mode", "auto", "auto, full, incremental")
	authWorkers := flag.Int("auth-workers", 16, "authentication branch workers (1..16)")
	authCache := flag.Int("auth-cache", 0, "additional authentication cache MiB, separate from trie cache")
	authCacheMode := flag.String("auth-cache-mode", "unified", "unified, accumulator, contribution")
	measure := flag.Bool("measure", false, "detailed trie/cache statistics; serial hashing for measurement")
	dbMeasure := flag.Bool("db-measure", false, "detailed LevelDB read/cache counters (requires instrumented build); diagnostic timings")
	chainData := flag.Bool("store-chain-data", false, "persist blocks/transactions/receipts/indexes as well as trie nodes and code")
	handles := flag.Int("handles", 0, "DB open files; 0 raises OS limit and uses half, like legacy simulator")
	checkpointInterval := flag.Uint64("checkpoint-interval", 10000, "durable resume checkpoint interval in blocks; 0 disables periodic checkpoints")
	diskInterval := flag.Uint64("disk-size-interval", 10000, "disk-size and progress interval in blocks; 0 disables periodic samples")
	dbStatsInterval := flag.Uint64("db-stats-interval", 100000, "LevelDB/Pebble statistics interval in blocks; 0 disables periodic samples")
	cache := flag.Int("cache", 4096, "base cache budget MiB for automatic allocation; explicit per-cache sizes override it")
	trieCache := flag.Int("trie-cache", -1, "clean-node cache MiB; -1 assigns the remaining budget after DB, snapshot and write buffer")
	dbCache := flag.Int("db-cache", -1, "database cache allowance MiB; -1 assigns 50% of -cache")
	dbEngine := flag.String("db-engine", "pebble", "leveldb or pebble")
	compression := flag.String("compression", "snappy", "snappy, none, or zstd (Pebble only)")
	batchSize := flag.Int("batch", 256, "blocks per InsertChain batch")
	blockDelay := flag.Duration("block-delay", 0, "extra wait after each imported block; included in replay wall time, excluded from block processing time")
	cpuProfile := flag.String("cpuprofile", "", "optional CPU profile of replay, excluding input-prefix scan and final verification")
	pprofPort := flag.Int("pprof-port", 0, "optional localhost-only diagnostic pprof port; 0 disables")
	logInterval := flag.Uint64("log-interval", 1, "update progress every Nth completed block; display refreshes at 10Hz; 0 disables progress (summaries remain)")
	flag.Parse()
	if *pprofPort < 0 || *pprofPort > 65535 {
		return errors.New("pprof-port must be in [0,65535]")
	}
	if *pprofPort != 0 {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *pprofPort))
		if err != nil {
			return err
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", httppprof.Index)
		mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
		server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		defer server.Close()
		go server.Serve(listener)
	}
	if err := validateDatabaseOptions(*dbEngine, *compression); err != nil {
		return err
	}
	if *dir == "" || *input == "" || *until == 0 || *until >= 1<<32 || *batchSize < 1 || *blockDelay < 0 {
		return errors.New("require -datadir, -blocks, -until (1..2^32-1), valid batch/cache sizes")
	}
	if *dbMeasure && *dbEngine != "leveldb" {
		return errors.New("-db-measure currently requires LevelDB; detailed Pebble profiling is not implemented")
	}
	if *authMode == "auto" {
		if *auth == "keccak" {
			*authMode = "full"
		} else {
			*authMode = "incremental"
		}
	}
	if *handles == 0 {
		limit, err := fdlimit.Maximum()
		if err != nil {
			return err
		}
		raised, err := fdlimit.Raise(uint64(limit))
		if err != nil {
			return err
		}
		*handles = int(raised / 2)
	}
	if *handles < 16 {
		return errors.New("require at least 16 file handles")
	}
	replay := &common.ReplayConfig{KeyScheme: *scheme, ShardPrefix: *shard, TileBlocks: *tile, TilePathPrefix: *tilePrefix, RunTargetNodes: *runTarget, Authentication: *auth, Codec: "fixed32-v1", EpochSize: *epoch, StateDepth: *a, StorageDepth: *s, SkipIntermediateRoot: *skip, AuthMode: *authMode, AuthWorkers: *authWorkers, AuthCacheMiB: *authCache, AuthCacheMode: *authCacheMode, Measure: *measure, StateOnly: !*chainData}
	replay.SkipBlockRootChecks = !*verifyRoots
	if _, err := trie.AuthenticationConfig(replay); err != nil {
		return err
	}
	if err := replay.Validate(); err != nil {
		return err
	}
	pbss := replay.KeyScheme == "PBSS"
	if pbss {
		explicitArchive := false
		flag.Visit(func(f *flag.Flag) { explicitArchive = explicitArchive || f.Name == "archive" })
		if explicitArchive && *archive {
			return errors.New("PBSS replay currently supports native full mode only; use -archive=false")
		}
		*archive = false
	}
	cleanStop := !*archive
	var dirtyCache int
	var err error
	*dbCache, *trieCache, dirtyCache, err = replayCacheSizes(*cache, *dbCache, *trieCache, *snapshotCache, !*archive)
	if err != nil {
		return err
	}
	if pbss {
		replay.Codec = "geth-pathdb"
	}
	if cleanStop {
		// Periodic forced flushes would change native non-archive retention.
		// PBSS journals are likewise only taken at clean shutdown.
		*checkpointInterval = 0
	}
	genesis := core.DefaultGenesisBlock()
	if *genesisFile != "" {
		data, err := os.ReadFile(*genesisFile)
		if err != nil {
			return err
		}
		genesis = new(core.Genesis)
		if err := json.Unmarshal(data, genesis); err != nil {
			return err
		}
	}
	if genesis.Config == nil {
		return errors.New("missing genesis chain config")
	}
	f, err := os.Open(*input)
	if err != nil {
		return err
	}
	defer f.Close()
	dbPath := filepath.Join(*dir, "chaindata")
	_, statErr := os.Stat(dbPath)
	if !*resume && !os.IsNotExist(statErr) {
		return errors.New("chaindata already exists; use -resume only for the same experiment")
	}
	if *resume && statErr != nil {
		return fmt.Errorf("resume database: %w", statErr)
	}
	if err := os.MkdirAll(*dir, 0755); err != nil {
		return err
	}
	// Check the persistent format before opening the engine; execution-only cache
	// settings can change on resume and are recorded per run. No source data is copied into this database upfront.
	description := replayDescriptor{Replay: replay, NonArchive: !pbss && !*archive, CacheMiB: *cache, DBMiB: *dbCache, TrieMiB: *trieCache, DirtyMiB: dirtyCache, Handles: *handles, SnapshotMiB: *snapshotCache, Prefetch: *prefetch, Genesis: genesis.ToBlock().Hash(), Engine: *dbEngine, Compression: *compression}
	descriptor, err := json.Marshal(description)
	if err != nil {
		return err
	}
	manifest := filepath.Join(*dir, "replay.json")
	activeFile := filepath.Join(*dir, "hashdb-running")
	if pbss {
		activeFile = filepath.Join(*dir, "pbss-running")
	}
	if cleanStop && *resume {
		if _, err := os.Stat(activeFile); !os.IsNotExist(err) {
			return errors.New("non-archive replay was not cleanly stopped; restore a clean DB/checkpoint copy")
		}
		// Reject an invalid target before opening pathdb or marking it active.
		data, err := os.ReadFile(filepath.Join(*dir, "checkpoint.json"))
		if err != nil {
			return err
		}
		var saved replayCheckpoint
		if err := json.Unmarshal(data, &saved); err != nil {
			return err
		}
		if saved.Head.Number > *until {
			return errors.New("target is below the existing head")
		}
	}
	if *resume {
		old, err := os.ReadFile(manifest)
		if err != nil {
			return err
		}
		var previous replayDescriptor
		if err := json.Unmarshal(old, &previous); err != nil {
			return err
		}
		if !previous.compatible(description) {
			return errors.New("replay persistent format/genesis/engine/compression mismatch")
		}
	} else if err := os.WriteFile(manifest, descriptor, 0644); err != nil {
		return err
	}
	// Finish the potentially long input-prefix scan while the DB is closed.
	// Otherwise background compaction changes checkpoint clones before replay
	// starts, by an amount depending on input scanning speed.
	stream := rlp.NewStream(bufio.NewReaderSize(f, 4*1024*1024), 0)
	var checkpoint replayCheckpoint
	var recent types.Blocks
	if *resume && replay.StateOnly {
		data, err := os.ReadFile(filepath.Join(*dir, "checkpoint.json"))
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &checkpoint); err != nil {
			return err
		}
		if checkpoint.Schema != 1 || checkpoint.Head.Number > *until {
			return errors.New("invalid checkpoint schema or target below checkpoint head")
		}
		fmt.Fprintf(os.Stderr, "checking input through block %d before opening DB\n", checkpoint.Head.Number)
		recent, err = readReplayContext(stream, genesis.ToBlock(), checkpoint.Head)
		if err != nil {
			return err
		}
	}
	var osCacheDroppedAt time.Time
	var osCachePreparationTime time.Duration
	if *dropOSCache {
		fmt.Fprintln(os.Stderr, "preparing OS caches before DB open (sync + drop_caches=3)")
		start := time.Now()
		if err := dropReplayOSCaches(); err != nil {
			return err
		}
		osCachePreparationTime = time.Since(start)
		osCacheDroppedAt = time.Now().UTC()
	}
	kv, options, err := openReplayDatabase(dbPath, *dbEngine, *compression, *dbCache, *handles)
	if err != nil {
		return err
	}
	var db ethdb.Database = rawdb.NewDatabase(kv)
	if pbss {
		db = core.NewReplayPathDatabase(kv, filepath.Join(dbPath, "ancient"), replay.StateOnly)
	} else if replay.StateOnly {
		db = core.NewReplayDatabase(kv)
	}
	defer db.Close()
	if *dbMeasure {
		if err := kv.(*leveldb.Database).EnableReadStats(!pbss); err != nil {
			return err
		}
	}
	engineFilename := *dbEngine + "_metrics.jsonl"
	if *resume && replay.StateOnly {
		if err := truncateReplayResults(filepath.Join(*dir, "simblocks.jsonl"), checkpoint.Head.Number); err != nil {
			return err
		}
		// Also trim legacy split diagnostics if present; new runs no longer create them.
		for _, name := range []string{engineFilename, "detailed_metrics.jsonl"} {
			if err := truncateReplayResults(filepath.Join(*dir, name), checkpoint.Head.Number); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	output, err := openReplayLog(filepath.Join(*dir, "simblocks.jsonl"))
	if err != nil {
		return err
	}
	defer output.Close()
	enc := output.Encoder
	engineLog, err := openReplayLog(filepath.Join(*dir, engineFilename))
	if err != nil {
		return err
	}
	defer engineLog.Close()
	logs := []*replayLog{output, engineLog}
	flushLogs := func() error {
		for _, log := range logs {
			if err := log.writer.Flush(); err != nil {
				return err
			}
		}
		return nil
	}
	build, _ := debug.ReadBuildInfo()
	authFormat, err := trie.AuthenticationConfig(replay)
	if err != nil {
		return err
	}

	cfg := core.DefaultConfig()
	cfg.StateScheme = rawdb.HashScheme
	cfg.ArchiveMode = *archive
	cfg.Preimages = false // state/key experiments do not need address/slot preimages
	cfg.SnapshotLimit = *snapshotCache
	cfg.SnapshotWait = true // finish genesis/reopen snapshot generation before timing
	cfg.NoPrefetch = !*prefetch
	cfg.TrieCleanLimit = *trieCache
	cfg.TrieDirtyLimit = dirtyCache
	cfg.Replay = replay
	if pbss {
		cfg.StateScheme, cfg.ArchiveMode = rawdb.PathScheme, false
		cfg.StateHistory = pathdb.Defaults.StateHistory
		cfg.TrienodeHistory = pathdb.Defaults.TrienodeHistory
	}
	var outputErr error
	var lastResult core.ReplayResult
	var console *replayConsole
	var pacingTime time.Duration
	cfg.OnReplayBlock = func(result core.ReplayResult) {
		lastResult = result
		if console != nil {
			console.block(result)
		}
		if outputErr == nil {
			outputErr = enc.Encode(result)
		}
		// The native processing timer has already stopped. Keep the normal
		// InsertChain batch size while varying only the inter-block wait.
		if *blockDelay > 0 {
			start := time.Now()
			time.Sleep(*blockDelay)
			pacingTime += time.Since(start)
		}
	}
	engine := beacon.New(ethash.NewFaker()) // replay validates headers/uncles, excludes historical PoW seal computation
	defer engine.Close()
	if *resume && replay.StateOnly {
		if err := core.RestoreReplay(db.(*core.ReplayDatabase), genesis, cfg, checkpoint.Genesis, checkpoint.Head, recent, checkpoint.RecentRoots); err != nil {
			return err
		}
	}
	if cleanStop {
		f, err := os.OpenFile(activeFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			return err
		}
		if err := syncReplayDirectory(*dir); err != nil {
			return err
		}
	}
	chain, err := core.NewBlockChain(db, genesis, engine, cfg)
	if err != nil {
		return err
	}
	defer chain.Stop()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	head := chain.CurrentBlock().Number.Uint64()
	if pbss && *resume && replay.StateOnly && (head != checkpoint.Head.Number || !chain.HasState(checkpoint.Head.PhysicalRoot)) {
		return errors.New("PBSS journal does not contain the checkpoint state")
	}
	if *resume && !replay.StateOnly {
		for _, name := range []string{"simblocks.jsonl", engineFilename, "detailed_metrics.jsonl"} {
			if err := truncateReplayResults(filepath.Join(*dir, name), head); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	genesisRoot, err := chain.ReplayRoot(genesis.ToBlock().Hash())
	if err != nil {
		return err
	}
	checkpoint.Genesis = genesisRoot
	checkpoint.Schema = 1
	checkpoint.StateScheme = cfg.StateScheme
	replay.TakeStats() // exclude genesis and reopen setup from block counters
	if head > *until {
		return errors.New("target is below the existing head")
	}
	replayStarted := time.Now()
	stopProfile := func() {}
	if *cpuProfile != "" {
		f, err := os.OpenFile(*cpuProfile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			f.Close()
			return err
		}
		stopProfile = sync.OnceFunc(func() { pprof.StopCPUProfile(); f.Close() })
		defer stopProfile()
	}
	runID := replayStarted.UTC().Format(time.RFC3339Nano)
	workingDir, err := os.Getwd()
	if err != nil {
		return err
	}
	// VisitAll includes defaults, false/zero values and future flags. Capture
	// after resolving auto auth mode and the OS-dependent file-handle limit.
	runFlags := make(map[string]string)
	flag.VisitAll(func(f *flag.Flag) { runFlags[f.Name] = f.Value.String() })
	filesystems, err := replayFilesystemUsage(dbPath)
	if err != nil {
		return err
	}
	runInfo := struct {
		SchemaVersion                                                     int
		ConsoleInterval                                                   uint64
		Preimages                                                         bool
		TxTimingScope                                                     string
		TxClassification                                                  string
		Type, Version, Go                                                 string
		OS, Arch, WorkingDirectory                                        string
		RunID                                                             string
		ProcessStartedAt, ReplayStartedAt                                 time.Time
		OSCacheDroppedAt                                                  time.Time `json:",omitempty"`
		OSCachePreparationTime                                            time.Duration
		Filesystems                                                       map[string]*disk.UsageStat
		Flags                                                             map[string]string
		PerformanceMode                                                   bool
		DetailedMetrics                                                   []string
		Target, StartingHead                                              uint64
		BatchSize, GOMAXPROCS                                             int
		CheckpointInterval, DiskSizeInterval, DBStatsInterval             uint64
		AuthFormat, AuthImplementation, AuthParallelPolicy, EngineOptions string
		ChainConfig                                                       *params.ChainConfig
		StateScheme                                                       string
		ArchiveMode, VerifyPoW                                            bool
		SnapshotLimit                                                     int
		Prefetch, SnapshotWait                                            bool
		StateHistory                                                      uint64
		TrienodeHistory                                                   int64
		TrieDirtyMiB                                                      int
		EffectiveTrieDirtyMiB                                             int
		CheckpointPolicy                                                  string
		Config                                                            json.RawMessage
		Build                                                             *debug.BuildInfo
	}{SchemaVersion: 4, ConsoleInterval: *logInterval, Preimages: cfg.Preimages, TxTimingScope: "apply-transaction-including-reads-finalise-excluding-sender-async-bloom", TxClassification: "receipt-gas-used-21000", Type: "run", Version: version.WithMeta, Go: runtime.Version(),
		OS: runtime.GOOS, Arch: runtime.GOARCH, WorkingDirectory: workingDir,
		RunID: runID, ProcessStartedAt: processStarted.UTC(), ReplayStartedAt: replayStarted.UTC(), Flags: runFlags, PerformanceMode: !*measure && !*dbMeasure, DetailedMetrics: []string{},
		OSCacheDroppedAt: osCacheDroppedAt, OSCachePreparationTime: osCachePreparationTime,
		Filesystems: filesystems,
		Target:      *until, StartingHead: head, BatchSize: *batchSize, GOMAXPROCS: runtime.GOMAXPROCS(0),
		CheckpointInterval: *checkpointInterval, DiskSizeInterval: *diskInterval, DBStatsInterval: *dbStatsInterval, AuthFormat: authFormat,
		AuthImplementation: trie.AuthImplementation, AuthParallelPolicy: trie.AuthParallelPolicy,
		EngineOptions: options, ChainConfig: chain.Config(), StateScheme: cfg.StateScheme,
		ArchiveMode: cfg.ArchiveMode, VerifyPoW: false, SnapshotLimit: cfg.SnapshotLimit,
		Prefetch: !cfg.NoPrefetch, SnapshotWait: cfg.SnapshotWait,
		StateHistory: cfg.StateHistory, TrienodeHistory: cfg.TrienodeHistory, TrieDirtyMiB: cfg.TrieDirtyLimit,
		EffectiveTrieDirtyMiB: dirtyCache,
		CheckpointPolicy:      "periodic-archive-root",
		Config:                descriptor, Build: build}
	if pbss {
		runInfo.EffectiveTrieDirtyMiB = min(dirtyCache, 256) // native pathdb cap
		runInfo.CheckpointPolicy = "clean-stop-native-journal"
	} else if !*archive {
		runInfo.CheckpointPolicy = "clean-stop-native-gc"
	}
	if *measure {
		// Pebble lookup/block-kind profiling is not implemented yet. Do not
		// represent those unavailable measurements as measured zeroes.
		runInfo.DetailedMetrics = []string{"trie", "key-codec", "trie-read-locations"}
		if !pbss && cfg.SnapshotLimit > 0 {
			runInfo.DetailedMetrics = append(runInfo.DetailedMetrics, "snapshot")
		}
	}
	if *dbMeasure {
		runInfo.DetailedMetrics = append(runInfo.DetailedMetrics, "leveldb-readstats-v3")
	}
	if *cpuProfile != "" {
		runInfo.PerformanceMode = false
		runInfo.DetailedMetrics = append(runInfo.DetailedMetrics, "cpu-profile")
	}
	if *pprofPort != 0 {
		runInfo.DetailedMetrics = append(runInfo.DetailedMetrics, "localhost-pprof-on-request")
	}
	for _, log := range logs {
		if err := log.Encode(runInfo); err != nil {
			return err
		}
	}
	console = newReplayConsole(os.Stderr, head, *until, *logInterval, replayStarted)
	defer console.close()
	console.printf("replay %d -> %d | scheme=%s epoch=%d A=%d S=%d | auth=%s/%s cache=%dMiB workers=%d\n", head, *until, *scheme, *epoch, *a, *s, *auth, *authMode, *authCache, *authWorkers)
	console.printf("engine=%s/%s archive=%t state-only=%t prefetch=%t snapshot-cache=%dMiB | cache DB=%d trie=%d dirty=%dMiB | detailed trie=%t DB=%t\n", *dbEngine, *compression, cfg.ArchiveMode, replay.StateOnly, *prefetch, *snapshotCache, *dbCache, *trieCache, runInfo.EffectiveTrieDirtyMiB, *measure, *dbMeasure)
	console.printf("results=%s | input=%s | progress every=%d blocks (10Hz); summary=1000 blocks; disk sample=%d; DB stats=%d blocks\n", *dir, *input, *logInterval, *diskInterval, *dbStatsInterval)
	recordEngine := func(number uint64) error {
		var snapshot any
		var detailed json.RawMessage
		switch engine := kv.(type) {
		case *pebble.Database:
			snapshot = engine.Metrics()
		case *leveldb.Database:
			stats, err := engine.Metrics()
			if err != nil {
				return err
			}
			snapshot = stats
			if *dbMeasure {
				detailed, err = engine.ReadStatsJSON()
				if err != nil {
					return err
				}
			}
		}
		return engineLog.Encode(struct {
			Type, RunID string
			Number      uint64
			Metrics     any
			Detailed    json.RawMessage `json:",omitempty"`
		}{*dbEngine, runID, number, snapshot, detailed})
	}
	recordProgressAndCache := func(number uint64, phase string) error {
		if err := recordProgress(enc, runID, number, phase, processStarted, replayStarted); err != nil {
			return err
		}
		if *blockDelay > 0 {
			if err := enc.Encode(struct {
				Type, RunID string
				Number      uint64
				WaitTimeNS  time.Duration
			}{"pacing", runID, number, pacingTime}); err != nil {
				return err
			}
		}
		if err := enc.Encode(struct {
			Type, RunID string
			Number      uint64
			Stats       any
		}{"auth-cache", runID, number, trie.AuthenticationCacheStats(replay)}); err != nil {
			return err
		}
		if !pbss && cfg.SnapshotLimit > 0 {
			return enc.Encode(struct {
				Type, RunID string
				Number      uint64
				Counters    map[string]int64
			}{"snapshot", runID, number, snapshotCounters()})
		}
		return nil
	}
	// Baseline after genesis/reopen. Subtract snapshots within this RunID;
	// engine counters restart on every DB open, including checkpoint resume.
	if err := recordEngine(head); err != nil {
		return err
	}
	if err := recordProgressAndCache(head, "start"); err != nil {
		return err
	}
	lastCheckpoint, lastSample, lastDBSample := head, head, head
	finalCheckpoint := func() error {
		if !pbss && !*archive {
			checkpoint.RecentRoots, err = chain.FlushReplayState()
			if err != nil {
				return err
			}
		}
		if pbss {
			if err := chain.TrieDB().Journal(chain.CurrentBlock().Root); err != nil {
				return err
			}
		}
		if lastResult.Number > 0 {
			checkpoint.Head = lastResult
		} else {
			current := chain.CurrentBlock()
			root, err := chain.ReplayRoot(current.Hash())
			if err != nil {
				return err
			}
			checkpoint.Head = core.ReplayResult{Number: current.Number.Uint64(), BlockHash: current.Hash(), CanonicalRoot: current.Root, ReplayRoot: root}
		}
		if err := saveReplayCheckpoint(*dir, kv, checkpoint); err != nil {
			return err
		}
		if cleanStop {
			if err := os.Remove(activeFile); err != nil {
				return err
			}
			return syncReplayDirectory(*dir)
		}
		return nil
	}
	batch := make(types.Blocks, 0, *batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		start := time.Now()
		if _, err := chain.InsertChain(batch); err != nil {
			return err
		}
		if outputErr != nil {
			return outputErr
		}
		importElapsed := time.Since(start)
		if err := enc.Encode(struct {
			Type         string
			First, Last  uint64
			ImportTimeNS time.Duration
		}{"batch", batch[0].NumberU64(), batch[len(batch)-1].NumberU64(), importElapsed}); err != nil {
			return err
		}
		if err := flushLogs(); err != nil {
			return err
		}
		current := chain.CurrentBlock().Number.Uint64()
		if err := chain.PruneReplayHistory(head, 512); err != nil {
			return err
		}
		if *checkpointInterval > 0 && current-lastCheckpoint >= *checkpointInterval {
			checkpoint.Head = lastResult
			if err := saveReplayCheckpoint(*dir, kv, checkpoint); err != nil {
				return err
			}
			lastCheckpoint = current
		}
		if *diskInterval > 0 && current-lastSample >= *diskInterval {
			if err := recordDiskSize(enc, dbPath, current, console); err != nil {
				return err
			}
			if err := recordProgressAndCache(current, "replay"); err != nil {
				return err
			}
			if err := flushLogs(); err != nil {
				return err
			}
			lastSample = current
		}
		if *dbStatsInterval > 0 && current-lastDBSample >= *dbStatsInterval {
			if err := recordEngine(current); err != nil {
				return err
			}
			if err := engineLog.writer.Flush(); err != nil {
				return err
			}
			lastDBSample = current
		}

		batch = batch[:0]
		return nil
	}
	for head < *until {
		if ctx.Err() != nil {
			if lastResult.Number > 0 || cleanStop {
				if err := finalCheckpoint(); err != nil {
					return err
				}
			}
			if err := enc.Encode(struct {
				Type string
				Head uint64
			}{"stopped", head}); err != nil {
				return err
			}
			if err := recordDiskSize(enc, dbPath, head, console); err != nil {
				return err
			}
			if err := recordEngine(head); err != nil {
				return err
			}
			if err := recordProgressAndCache(head, "stopped"); err != nil {
				return err
			}
			console.printf("stopped at block=%d; checkpoint saved\n", head)
			return flushLogs()
		} // finish the current batch before stopping
		var block types.Block
		if err := stream.Decode(&block); err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("input ended before target block %d", *until)
			}
			return err
		}
		if block.NumberU64() <= head {
			if rawdb.ReadCanonicalHash(db, block.NumberU64()) != block.Hash() {
				return fmt.Errorf("input chain differs at block %d", block.NumberU64())
			}
			continue
		}
		expected := head + uint64(len(batch)) + 1
		if block.NumberU64() != expected {
			return fmt.Errorf("input block %d, expected %d", block.NumberU64(), expected)
		}
		batch = append(batch, &block)
		if len(batch) == *batchSize || block.NumberU64() == *until || (*checkpointInterval > 0 && block.NumberU64()%*checkpointInterval == 0) || (*diskInterval > 0 && block.NumberU64()%*diskInterval == 0) || (*dbStatsInterval > 0 && block.NumberU64()%*dbStatsInterval == 0) {
			if err := flush(); err != nil {
				return err
			}
			head = chain.CurrentBlock().Number.Uint64()
		}
	}
	if (head > 0 && lastResult.Number == head) || cleanStop {
		if err := finalCheckpoint(); err != nil {
			return err
		}
	}
	if err := recordDiskSize(enc, dbPath, head, console); err != nil {
		return err
	}
	if err := recordEngine(head); err != nil {
		return err
	}
	if err := recordProgressAndCache(head, "replay-end"); err != nil {
		return err
	}
	if err := flushLogs(); err != nil {
		return err
	}

	stopProfile()
	if *verify {
		start := time.Now()
		console.setPhase("verify-authentication")
		if err := chain.VerifyReplayAuthentication(chain.GetBlockByNumber(head)); err != nil {
			return err
		}
		console.setPhase("verify-state")
		if err := chain.VerifyReplayState(chain.GetBlockByNumber(head)); err != nil {
			return err
		}
		console.printf("verification passed block=%d elapsed=%s\n", head, time.Since(start).Round(time.Second))
		if err := enc.Encode(struct {
			Type         string
			Head         uint64
			VerifyTimeNS time.Duration
		}{"verified", head, time.Since(start)}); err != nil {
			return err
		}
	}
	console.printf("completed block=%d/%d verified=%t\n", head, *until, *verify)
	if err := recordProgress(enc, runID, head, "complete", processStarted, replayStarted); err != nil {
		return err
	}
	if err := enc.Encode(struct {
		Type string
		Head uint64
	}{"complete", head}); err != nil {
		return err
	}
	return flushLogs()
}

type replayLog struct {
	*json.Encoder
	writer *bufio.Writer
	file   *os.File
}

func openReplayLog(path string) (*replayLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriterSize(f, 256*1024)
	return &replayLog{json.NewEncoder(w), w, f}, nil
}

func (l *replayLog) Close() error {
	return errors.Join(l.writer.Flush(), l.file.Close())
}

type replayCheckpoint struct {
	Schema      int
	StateScheme string `json:",omitempty"`
	Genesis     core.ReplayRoot
	Head        core.ReplayResult
	RecentRoots map[common.Hash]core.ReplayRoot `json:",omitempty"` // hashdb non-archive GC context
}

func saveReplayCheckpoint(dir string, db ethdb.KeyValueStore, checkpoint replayCheckpoint) error {
	var err error
	if level, ok := db.(*leveldb.Database); ok {
		// Hashdb checkpoint roots have been flushed and are immutable. LevelDB's
		// native SyncKeyValue is a no-op, so sync an existing root without adding
		// a non-state entry. This extra write is outside import timing.
		key := checkpoint.Head.PhysicalRoot[:]
		if checkpoint.StateScheme == rawdb.PathScheme {
			key = []byte("TrieJournal") // native journal is immutable after Journal
		}
		if checkpoint.StateScheme != rawdb.PathScheme && checkpoint.Head.PhysicalRoot == types.EmptyRootHash {
			// Empty tries have no root node. A synchronous deletion of that
			// absent key orders earlier writes without creating a live DB key.
			err = level.SyncAbsentKey(key)
		} else {
			err = level.SyncImmutableKey(key)
		}
	} else {
		err = db.SyncKeyValue()
	}
	if err != nil {
		return err
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	name := filepath.Join(dir, "checkpoint.json")
	f, err := os.OpenFile(name+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(name+".tmp", name); err != nil {
		return err
	}
	return syncReplayDirectory(dir)
}

func syncReplayDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Progress uses UTC for correlation and monotonic durations for elapsed time.
// Replay samples include input, import, logging and checkpoint work. Completion
// adds the optional state verification; neither includes later deferred shutdown.
func recordProgress(enc *json.Encoder, runID string, head uint64, phase string, processStarted, replayStarted time.Time) error {
	now := time.Now()
	return enc.Encode(struct {
		Type, RunID, Phase                string
		Number                            uint64
		TimestampUTC                      time.Time
		ProcessElapsedNS, ReplayElapsedNS time.Duration
	}{"progress", runID, phase, head, now.UTC(), now.Sub(processStarted), now.Sub(replayStarted)})
}

// HistorySize is a subset of DiskSize: state/trienode freezer files only.
// History indexes stored inside the LSM cannot be separated by a file walk.
type replayDiskSample struct {
	Type                                                         string
	Number                                                       uint64
	DiskSize, HistorySize, StateHistorySize, TrienodeHistorySize int64
	Timestamp                                                    time.Time
	Filesystems                                                  map[string]*disk.UsageStat
}

// Filesystem occupancy is distinct from the DB file sizes. Usage queries statfs
// on Linux; it does not scan the home directory or unrelated experiment files.
func replayFilesystemUsage(dbPath string) (map[string]*disk.UsageStat, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	usage := make(map[string]*disk.UsageStat, 2)
	for label, path := range map[string]string{"home": home, "database": dbPath} {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		usage[label], err = disk.Usage(absolute)
		if err != nil {
			return nil, fmt.Errorf("filesystem usage for %s: %w", label, err)
		}
	}
	return usage, nil
}

func recordDiskSize(enc *json.Encoder, path string, head uint64, console *replayConsole) error {
	sample := replayDiskSample{Type: "disk", Number: head}
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		sample.DiskSize += info.Size()
		relative, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if strings.HasPrefix(relative, "ancient/"+rawdb.MerkleStateFreezerName+"/") {
			sample.StateHistorySize += info.Size()
		}
		if strings.HasPrefix(relative, "ancient/"+rawdb.MerkleTrienodeFreezerName+"/") {
			sample.TrienodeHistorySize += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	sample.Filesystems, err = replayFilesystemUsage(path)
	if err != nil {
		return err
	}
	sample.Timestamp = time.Now().UTC()
	sample.HistorySize = sample.StateHistorySize + sample.TrienodeHistorySize
	if console != nil {
		console.printf("disk block=%d total=%.3fGiB history=%.3fGiB (included)\n", head, float64(sample.DiskSize)/(1<<30), float64(sample.HistorySize)/(1<<30))
	}
	return enc.Encode(sample)
}
func readReplayContext(stream *rlp.Stream, genesis *types.Block, head core.ReplayResult) (types.Blocks, error) {
	recent := make(types.Blocks, 0, 513)
	last := genesis.Hash()
	expected := uint64(1)
	if head.Number == 0 {
		return types.Blocks{genesis}, nil
	}
	for {
		var b types.Block
		if err := stream.Decode(&b); err != nil {
			return nil, err
		}
		if b.NumberU64() == 0 {
			if expected != 1 || b.Hash() != genesis.Hash() {
				return nil, errors.New("input genesis mismatch")
			}
			continue
		}
		if b.NumberU64() != expected || b.ParentHash() != last {
			return nil, fmt.Errorf("input chain mismatch at %d", b.NumberU64())
		}
		last = b.Hash()
		expected++
		recent = append(recent, &b)
		if len(recent) > 512 {
			copy(recent, recent[1:])
			recent = recent[:512]
		}
		if b.NumberU64() == head.Number {
			if b.Hash() != head.BlockHash {
				return nil, errors.New("checkpoint block hash mismatch")
			}
			return recent, nil
		}
	}
}

type replayDescriptor struct {
	Replay                  *common.ReplayConfig
	DBMiB, TrieMiB, Handles int
	CacheMiB, DirtyMiB      int
	SnapshotMiB             int
	Prefetch                bool
	NonArchive              bool `json:",omitempty"`
	Genesis                 common.Hash
	Engine, Compression     string
}

func (d replayDescriptor) compatible(next replayDescriptor) bool {
	// Pre-engine-selection manifests always used Pebble's Snappy default.
	if d.Engine == "" {
		d.Engine, d.Compression = "pebble", "snappy"
	}
	return d.Replay != nil && next.Replay != nil && d.Genesis == next.Genesis &&
		d.Replay.PersistenceID() == next.Replay.PersistenceID() && d.NonArchive == next.NonArchive &&
		(d.Replay.KeyScheme == "PBSS" || (d.SnapshotMiB > 0) == (next.SnapshotMiB > 0)) &&
		d.Engine == next.Engine && d.Compression == next.Compression
}

// Match the legacy/Geth split: 50% DB, 50% state in hashdb archive;
// Non-archive mode reserves 25% for dirty nodes (native pathdb applies its own cap).
// Snapshot memory is taken from clean-node memory, not added on top.
func replayCacheSizes(total, db, trie, snapshot int, nonArchive bool) (int, int, int, error) {
	if total < 32 || total > 1<<20 || db < -1 || trie < -1 || snapshot < 0 || snapshot > 1<<20 {
		return 0, 0, 0, errors.New("invalid cache budget; require -cache >=32, per-cache sizes >=0 or -1 for auto")
	}
	dirty := 0
	if nonArchive {
		dirty = total * 25 / 100
	}
	if db == -1 {
		db = total * 50 / 100
	}
	if trie == -1 {
		trie = total - db - dirty - snapshot
	}
	if db < 16 || db > 1<<20 || trie < 0 || trie > 1<<20 {
		return 0, 0, 0, errors.New("cache budget exhausted or invalid: require DB >=16 MiB and nonnegative remaining trie cache")
	}
	return db, trie, dirty, nil
}

func validateDatabaseOptions(engine, compression string) error {
	if engine != "leveldb" && engine != "pebble" {
		return fmt.Errorf("unknown database engine %q", engine)
	}
	if compression != "snappy" && compression != "none" && !(compression == "zstd" && engine == "pebble") {
		return fmt.Errorf("unsupported %s compression %q", engine, compression)
	}
	return nil
}

func dropReplayOSCaches() error {
	if runtime.GOOS != "linux" {
		return errors.New("-drop-os-cache requires Linux")
	}
	// Keep the simulator unprivileged; fail rather than silently measuring with
	// a different cache state. No workload or database is open at this point.
	out, err := exec.Command("sudo", "-n", "/bin/sh", "-c", "sync && echo 3 > /proc/sys/vm/drop_caches").CombinedOutput()
	if err != nil {
		return fmt.Errorf("OS cache reset failed (requires noninteractive sudo): %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func openReplayDatabase(path, engine, compression string, cache, handles int) (ethdb.KeyValueStore, string, error) {
	if err := validateDatabaseOptions(engine, compression); err != nil {
		return nil, "", err
	}
	if existing := rawdb.PreexistingDatabase(path); existing != "" && existing != engine {
		return nil, "", fmt.Errorf("requested %s but found %s database", engine, existing)
	}
	if engine == "pebble" {
		db, err := pebble.NewWithCompression(path, cache, handles, "statesim", false, compression)
		if err != nil {
			return nil, "", err
		}
		options, err := databaseOptions(path)
		if err != nil {
			db.Close()
			return nil, "", err
		}
		return db, options, nil
	}
	var applied *opt.Options
	db, err := leveldb.NewCustom(path, "statesim", func(o *opt.Options) {
		o.OpenFilesCacheCapacity = max(handles, 16)
		o.BlockCacheCapacity = max(cache, 16) / 2 * opt.MiB
		o.WriteBuffer = max(cache, 16) / 4 * opt.MiB
		o.Compression = opt.SnappyCompression
		if compression == "none" {
			o.Compression = opt.NoCompression
		}
		applied = o
	})
	if err != nil {
		return nil, "", err
	}
	// Record effective defaults as well as the explicit overrides. LevelDB has
	// no Pebble-style OPTIONS file. Cache split matches the legacy Geth wrapper.
	o := applied
	options, _ := json.Marshal(map[string]any{
		"Compression": o.GetCompression().String(), "BlockSize": o.GetBlockSize(),
		"BlockRestartInterval": o.GetBlockRestartInterval(), "BloomBitsPerKey": 10,
		"BlockCacheCapacity": o.GetBlockCacheCapacity(), "WriteBuffer": o.GetWriteBuffer(),
		"OpenFilesCacheCapacity": o.GetOpenFilesCacheCapacity(), "CompactionL0Trigger": o.GetCompactionL0Trigger(),
		"CompactionTableSize": o.GetCompactionTableSize(0), "CompactionTotalSize": o.GetCompactionTotalSize(0),
		"DisableSeeksCompaction": o.GetDisableSeeksCompaction(), "NoSync": o.GetNoSync(),
		"CheckpointSync": "immutable-root-rewrite",
	})
	return db, string(options), nil
}

// Trim only timings beyond the durable state checkpoint, never state data.
// A truncated last JSON line after a crash is also removed before appending.
func truncateReplayResults(path string, head uint64) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	var offset int64
	for {
		line, readErr := reader.ReadBytes('\n')
		if errors.Is(readErr, io.EOF) {
			if len(line) == 0 {
				return nil
			}
			return f.Truncate(offset)
		}
		var row struct {
			Number *uint64
			Last   *uint64
			Head   *uint64
		}
		if json.Unmarshal(line, &row) != nil || (row.Number != nil && *row.Number > head) || (row.Last != nil && *row.Last > head) || (row.Head != nil && *row.Head > head) {
			return f.Truncate(offset)
		}
		offset += int64(len(line))
		if readErr != nil {
			return readErr
		}
	}
}
func databaseOptions(path string) (string, error) {
	files, err := filepath.Glob(filepath.Join(path, "OPTIONS-*"))
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", errors.New("Pebble effective options unavailable")
	}
	data, err := os.ReadFile(files[len(files)-1])
	return string(data), err
}

// These native counters include background snapshot activity. Take differences
// within one RunID; do not confuse them with foreground per-block read timers.
func snapshotCounters() map[string]int64 {
	out := make(map[string]int64)
	metrics.DefaultRegistry.Each(func(name string, value interface{}) {
		if !strings.HasPrefix(name, "state/snapshot/") {
			return
		}
		switch value := value.(type) {
		case *metrics.Meter:
			out[name] = value.Snapshot().Count()
		case *metrics.Counter:
			out[name] = value.Snapshot().Count()
		}
	})
	return out
}

// Only the current progress line is redrawn; summaries and messages remain in
// scrollback. No state/DB queries are made here, and block callbacks only update
// progress after the measured ProcessBlock completes.
type replayConsole struct {
	mu                                           sync.Mutex
	out                                          *bufio.Writer
	start, last, summaryAt                       time.Time
	initial, head, target, interval, summaryHead uint64
	progressHead                                 uint64
	progressLine                                 bool
	etaNext                                      int
	etaWindow                                    [10]struct {
		head uint64
		at   time.Time
	}
	phase      string
	phaseAt    time.Time
	stop, done chan struct{}
}

func newReplayConsole(out io.Writer, head, target, interval uint64, start time.Time) *replayConsole {
	c := &replayConsole{out: bufio.NewWriterSize(out, 64*1024), start: start, last: start, summaryAt: start,
		initial: head, head: head, progressHead: head, target: target, interval: interval, summaryHead: head, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		var heartbeat time.Time
		for {
			select {
			case <-c.stop:
				return
			case now := <-ticker.C:
				c.mu.Lock()
				if c.interval > 0 {
					c.progress(now)
				} else if now.Sub(c.last) >= 10*time.Second && now.Sub(heartbeat) >= 10*time.Second {
					if c.phase != "" {
						fmt.Fprintf(c.out, "%s phase=%s elapsed=%s | last-completed=%d/%d\n", now.Format("15:04:05"), c.phase, now.Sub(c.phaseAt).Round(time.Second), c.head, c.target)
					} else {
						fmt.Fprintf(c.out, "%s waiting | last-completed=%d/%d | no completed block for=%s\n", now.Format("15:04:05"), c.head, c.target, now.Sub(c.last).Round(time.Second))
					}
					heartbeat = now
				}
				c.out.Flush()
				c.mu.Unlock()
			}
		}
	}()
	return c
}
func (c *replayConsole) setPhase(phase string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.phase, c.phaseAt = phase, time.Now()
	c.clearProgress()
	fmt.Fprintf(c.out, "%s phase=%s starting | last-completed=%d/%d\n", c.phaseAt.Format("15:04:05"), phase, c.head, c.target)
	c.out.Flush()
}
func (c *replayConsole) printf(format string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearProgress()
	fmt.Fprintf(c.out, format, args...)
	c.out.Flush()
}
func (c *replayConsole) block(r core.ReplayResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.head, c.last = r.Number, now
	if c.interval > 0 && (r.Number%c.interval == 0 || r.Number == c.target) {
		c.progressHead = r.Number
	}
	if r.Number-c.summaryHead >= 1000 {
		c.summary(now)
	}
}
func (c *replayConsole) summary(now time.Time) {
	blocks := c.head - c.summaryHead
	if blocks == 0 {
		return
	}
	total := float64(c.head-c.initial) / max(now.Sub(c.start).Seconds(), 1e-9)
	recent := float64(blocks) / max(now.Sub(c.summaryAt).Seconds(), 1e-9)
	// Ten 1K summary boundaries give a rolling 10K-block rate. During
	// warmup, use all completed blocks since this replay started.
	etaHead, etaAt := c.initial, c.start
	sample := &c.etaWindow[c.etaNext]
	if !sample.at.IsZero() {
		etaHead, etaAt = sample.head, sample.at
	}
	etaSpeed := float64(c.head-etaHead) / max(now.Sub(etaAt).Seconds(), 1e-9)
	sample.head, sample.at = c.head, now
	c.etaNext = (c.etaNext + 1) % len(c.etaWindow)
	var remaining time.Duration
	if c.head < c.target {
		remaining = time.Duration(float64(c.target-c.head) / etaSpeed * float64(time.Second))
	}
	block := "Block: " + replayNumber(c.head)
	speed := fmt.Sprintf("Speed (blocks/s): total %.2f / recent %.2f", total, recent)
	elapsed := "Elapsed: " + replayElapsed(now.Sub(c.start))
	c.clearProgress()
	// Pad column ends, not values: a bare tab jumps to a different stop when
	// a speed loses a digit (e.g. 1025.72 -> 808.72).
	fmt.Fprintf(c.out, "%-*s\t%-55s\t%s / Remaining: %s\n", len("Block: ")+len(replayNumber(c.target)), block, speed, elapsed, replayElapsed(remaining))
	c.summaryAt, c.summaryHead = now, c.head
	c.out.Flush()
}

// Call with mu held, so a progress refresh never overwrites a permanent log line.
func (c *replayConsole) clearProgress() {
	if c.progressLine {
		fmt.Fprint(c.out, "\r\033[K")
		c.progressLine = false
	}
}

func (c *replayConsole) progress(now time.Time) {
	fmt.Fprintf(c.out, "\r\033[KCurrent block: %s / %s", replayNumber(c.progressHead), replayNumber(c.target))
	if c.phase != "" {
		fmt.Fprintf(c.out, "\tPhase: %s", c.phase)
	} else if now.Sub(c.last) >= 10*time.Second {
		fmt.Fprintf(c.out, "\tWaiting: %s", now.Sub(c.last).Truncate(time.Second))
	}
	c.progressLine = true
}

func replayNumber(n uint64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func replayElapsed(d time.Duration) string {
	seconds := int64(max(d, 0) / time.Second)
	switch {
	case seconds >= 86400:
		return fmt.Sprintf("%dd %02dh %02dm %02ds", seconds/86400, seconds/3600%24, seconds/60%60, seconds%60)
	case seconds >= 3600:
		return fmt.Sprintf("%dh %02dm %02ds", seconds/3600, seconds/60%60, seconds%60)
	case seconds >= 60:
		return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

func (c *replayConsole) close() {
	close(c.stop)
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearProgress()
	c.summary(time.Now())
	c.out.Flush()
}
