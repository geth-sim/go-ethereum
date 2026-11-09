package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestReplayOSCachePreparation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux cache preparation")
	}
	// Never reset the test host's caches. Substitute only the privileged helper.
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	log := filepath.Join(dir, "arguments")
	t.Setenv("STATESIM_CACHE_RESET_TEST_LOG", log)
	helper := filepath.Join(dir, "sudo")
	for _, exitCode := range []string{"7", "0"} {
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$STATESIM_CACHE_RESET_TEST_LOG\"\nexit " + exitCode + "\n"
		if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		err := dropReplayOSCaches()
		if (err == nil) != (exitCode == "0") {
			t.Fatalf("cache preparation must propagate helper failure: %v", err)
		}
	}
	args, err := os.ReadFile(log)
	if err != nil || string(args) != "-n\n/bin/sh\n-c\nsync && echo 3 > /proc/sys/vm/drop_caches\n" {
		t.Fatalf("cache reset must be noninteractive and sync first: %q, %v", args, err)
	}
}

func TestReplayDatabase(t *testing.T) {
	for _, engine := range []string{"leveldb", "pebble"} {
		for _, compression := range []string{"snappy", "none", "zstd"} {
			t.Run(engine+"/"+compression, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "chaindata")
				db, options, err := openReplayDatabase(path, engine, compression, 16, 16)
				if engine == "leveldb" && compression == "zstd" {
					if err == nil {
						db.Close()
						t.Fatal("accepted unsupported compression")
					}
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatal("invalid options created a database")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(strings.ToLower(options), map[string]string{"snappy": "snappy", "none": "none", "zstd": "zstd"}[compression]) && !(compression == "none" && strings.Contains(options, "NoCompression")) {
					t.Fatalf("effective compression absent from options: %s", options)
				}
				key := common.Hash{1}
				value := bytes.Repeat([]byte("compressible-node"), 4096)
				if err := db.Put(key[:], value); err != nil {
					t.Fatal(err)
				}
				checkpoint := replayCheckpoint{Schema: 1, Head: core.ReplayResult{Number: 1, ReplayRoot: core.ReplayRoot{PhysicalRoot: key}}}
				if err := saveReplayCheckpoint(filepath.Dir(path), db, checkpoint); err != nil {
					t.Fatal(err)
				}
				if err := db.Compact(nil, nil); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if rawdb.PreexistingDatabase(path) != engine {
					t.Fatal("wrong engine on disk")
				}
				db, _, err = openReplayDatabase(path, engine, compression, 16, 16)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				got, err := db.Get(key[:])
				if err != nil || !bytes.Equal(got, value) {
					t.Fatalf("lost data after reopen: %v", err)
				}
				it := db.NewIterator(nil, nil)
				defer it.Release()
				if !it.Next() || !bytes.Equal(it.Key(), key[:]) || it.Next() || it.Error() != nil {
					t.Fatal("checkpoint introduced a non-state key")
				}
				other := map[string]string{"pebble": "leveldb", "leveldb": "pebble"}[engine]
				if _, _, err := openReplayDatabase(path, other, "snappy", 16, 16); err == nil {
					t.Fatal("accepted a different engine")
				}
			})
		}
	}
}

func TestReplayEmptyCheckpoint(t *testing.T) {
	for _, engine := range []string{"leveldb", "pebble"} {
		t.Run(engine, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "chaindata")
			db, _, err := openReplayDatabase(path, engine, "snappy", 16, 16)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := replayCheckpoint{Schema: 1, Head: core.ReplayResult{ReplayRoot: core.ReplayRoot{PhysicalRoot: types.EmptyRootHash}}}
			if err := saveReplayCheckpoint(dir, db, checkpoint); err != nil {
				db.Close()
				t.Fatal("checkpoint of an entirely empty database", err)
			}
			codeHash, code := common.Hash{1}, []byte("retained contract code")
			rawdb.WriteCode(db, codeHash, code)
			if err := saveReplayCheckpoint(dir, db, checkpoint); err != nil {
				db.Close()
				t.Fatal("empty state with earlier DB writes", err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, _, err = openReplayDatabase(path, engine, "snappy", 16, 16)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if exists, err := db.Has(types.EmptyRootHash[:]); exists || err != nil {
				t.Fatal("checkpoint persisted a non-existent root node", err)
			}
			if !bytes.Equal(rawdb.ReadCode(db, codeHash), code) {
				t.Fatal("earlier code write lost")
			}
			if data, err := os.ReadFile(filepath.Join(dir, "checkpoint.json")); err != nil || !json.Valid(data) {
				t.Fatal("checkpoint not published", err)
			}
		})
	}
}

func TestReplayDescriptor(t *testing.T) {
	d := replayDescriptor{Replay: &common.ReplayConfig{KeyScheme: "VP", Codec: "fixed32-v1"}, Engine: "leveldb", Compression: "snappy", DBMiB: 16}
	next := d
	next.DBMiB = 32
	if !d.compatible(next) {
		t.Fatal("runtime cache change rejected")
	}
	next.NonArchive = true
	if d.compatible(next) {
		t.Fatal("retention policy change accepted")
	}
	next.NonArchive = false
	next.Prefetch = true
	if !d.compatible(next) {
		t.Fatal("prefetch change rejected")
	}
	next.SnapshotMiB = 4
	if d.compatible(next) {
		t.Fatal("mixed snapshot storage history accepted")
	}
	snapped := next
	next.SnapshotMiB = 8
	if !snapped.compatible(next) {
		t.Fatal("snapshot cache budget change rejected")
	}
	next = d
	next.Engine = "pebble"
	if d.compatible(next) {
		t.Fatal("engine change accepted")
	}
	next = d
	next.Compression = "none"
	if d.compatible(next) {
		t.Fatal("mixed compression history accepted")
	}
	var old replayDescriptor
	if err := json.Unmarshal([]byte(`{"Replay":{"KeyScheme":"VP","Codec":"fixed32-v1"},"PebbleMiB":16}`), &old); err != nil {
		t.Fatal(err)
	}
	next = d
	next.Engine = "pebble"
	if !old.compatible(next) || old.compatible(d) {
		t.Fatal("legacy Pebble manifest misclassified")
	}
	pbss := replayDescriptor{Replay: &common.ReplayConfig{KeyScheme: "PBSS", Codec: "fixed32-v1"}, Engine: "pebble", Compression: "snappy"}
	next = pbss
	next.SnapshotMiB = 409
	if !pbss.compatible(next) {
		t.Fatal("PBSS flat-state read cache change rejected")
	}
}

func TestReplayCacheSizes(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		total, db, trie, snapshot int
		pbss                      bool
		want                      [3]int
	}{
		{"archive-off", 4096, -1, -1, 0, false, [3]int{2048, 2048, 0}},
		{"archive-on", 4096, -1, -1, 819, false, [3]int{2048, 1229, 0}},
		{"archive-1g-snapshot", 4096, -1, -1, 1024, false, [3]int{2048, 1024, 0}},
		{"pbss-no-flat-cache", 4096, -1, -1, 0, true, [3]int{2048, 1024, 1024}},
		{"pbss-flat-cache", 4096, -1, -1, 409, true, [3]int{2048, 615, 1024}},
		{"explicit", 4096, 64, 64, 8, false, [3]int{64, 64, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, trie, dirty, err := replayCacheSizes(tc.total, tc.db, tc.trie, tc.snapshot, tc.pbss)
			if err != nil || [3]int{db, trie, dirty} != tc.want {
				t.Fatalf("got %d/%d/%d: %v", db, trie, dirty, err)
			}
		})
	}
	for _, sizes := range [][4]int{{4096, -1, -1, 4096}, {16, -1, -1, 0}, {4096, -2, -1, 0}, {4096, -1, -2, 0}, {4096, -1, -1, -1}} {
		if _, _, _, err := replayCacheSizes(sizes[0], sizes[1], sizes[2], sizes[3], false); err == nil {
			t.Fatalf("accepted invalid sizes %v", sizes)
		}
	}
}

func TestCheckpointResults(t *testing.T) {
	keep := "{\"Type\":\"run\"}\n{\"Number\":10}\n{\"Type\":\"batch\",\"Last\":10}\n"
	for _, tail := range []string{"", "{\"Number\":11}\n", "{\"Type\":\"progress\",\"Number\":11}\n", "{\"Type\":\"auth-cache\",\"Number\":11}\n", "{\"Type\":\"batch\",\"Last\":11}\n", "{\"Type\":\"complete\",\"Head\":11}\n", "{\"Num", "broken\n"} {
		path := filepath.Join(t.TempDir(), "simblocks.jsonl")
		if err := os.WriteFile(path, []byte(keep+tail), 0600); err != nil {
			t.Fatal(err)
		}
		if err := truncateReplayResults(path, 10); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != keep {
			t.Fatalf("lost durable results or retained uncommitted results: %q (%v)", got, err)
		}
	}
}

func TestReplayProgress(t *testing.T) {
	var buf bytes.Buffer
	start := time.Now().Add(-time.Minute)
	if err := recordProgress(json.NewEncoder(&buf), "run-id", 100, "replay", start, start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var row struct {
		Type, RunID, Phase                string
		Number                            uint64
		TimestampUTC                      time.Time
		ProcessElapsedNS, ReplayElapsedNS time.Duration
	}
	if err := json.Unmarshal(buf.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row.Type != "progress" || row.RunID != "run-id" || row.Number != 100 || row.Phase != "replay" || row.TimestampUTC.Location() != time.UTC || row.ProcessElapsedNS-row.ReplayElapsedNS != time.Second || row.ReplayElapsedNS < 59*time.Second {
		t.Fatalf("incorrect UTC/monotonic progress: %+v", row)
	}
}

func TestReplayContext(t *testing.T) {
	genesis := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(0)})
	var blocks types.Blocks
	var input bytes.Buffer
	last := genesis.Hash()
	for i := uint64(1); i <= 520; i++ {
		block := types.NewBlockWithHeader(&types.Header{Number: new(big.Int).SetUint64(i), ParentHash: last})
		last = block.Hash()
		blocks = append(blocks, block)
		if err := rlp.Encode(&input, block); err != nil {
			t.Fatal(err)
		}
	}
	head := core.ReplayResult{Number: 520, BlockHash: last}
	recent, err := readReplayContext(rlp.NewStream(bytes.NewReader(input.Bytes()), 0), genesis, head)
	if err != nil || len(recent) != 512 || recent[0].NumberU64() != 9 || recent[511].Hash() != last {
		t.Fatalf("bounded restart context: %d (%v)", len(recent), err)
	}
	head.BlockHash[0] ^= 1
	if _, err := readReplayContext(rlp.NewStream(bytes.NewReader(input.Bytes()), 0), genesis, head); err == nil {
		t.Fatal("accepted a different checkpoint chain")
	}
	input.Reset()
	for _, block := range blocks[1:] {
		if err := rlp.Encode(&input, block); err != nil {
			t.Fatal(err)
		}
	}
	head.BlockHash = last
	if _, err := readReplayContext(rlp.NewStream(&input, 0), genesis, head); err == nil {
		t.Fatal("accepted input with a missing block")
	}
}

func TestReplayDiskHistory(t *testing.T) {
	dir := t.TempDir()
	files := map[string]int{"000001.ldb": 100, "ancient/state/meta.0000.rdat": 20, "ancient/trienode/node.0000.cdat": 30, "ancient/chain/header.0000.cdat": 40}
	for name, size := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := recordDiskSize(json.NewEncoder(&out), dir, 15, nil); err != nil {
		t.Fatal(err)
	}
	var got replayDiskSample
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Number != 15 || got.DiskSize != 190 || got.HistorySize != 50 || got.StateHistorySize != 20 || got.TrienodeHistorySize != 30 {
		t.Fatalf("wrong history subset: %+v", got)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got.Timestamp.IsZero() || len(got.Filesystems) != 2 {
		t.Fatalf("missing filesystem sample: %+v", got)
	}
	for label, path := range map[string]string{"home": home, "database": dir} {
		usage := got.Filesystems[label]
		if usage == nil || usage.Path != path || usage.Total <= uint64(got.DiskSize) || usage.Used > usage.Total || usage.Free > usage.Total || usage.UsedPercent < 0 || usage.UsedPercent > 100 {
			t.Fatalf("invalid filesystem usage for %s: %+v", label, usage)
		}
	}
	if _, err := replayFilesystemUsage(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("failed filesystem lookup must not produce zero-valued statistics")
	}
}

func TestReplayConsole(t *testing.T) {
	var out bytes.Buffer
	c := newReplayConsole(&out, 5000, 6003, 1, time.Now().Add(-25*time.Hour))
	for i := uint64(5001); i <= 6003; i++ {
		c.block(core.ReplayResult{Number: i, PaymentTxLen: 1, CallTxLen: 2, GasUsed: 80000, Stats: core.ExecuteStats{TotalTime: time.Millisecond}})
	}
	c.mu.Lock()
	c.progress(time.Now())
	c.mu.Unlock()
	c.printf("disk sample saved\n")
	c.close()
	text := out.String()
	if strings.Contains(text, "tx=") || strings.Contains(text, "gas=") ||
		!strings.Contains(text, "Block: 6,000\tSpeed (blocks/s): total 0.01 / recent 0.01") ||
		!strings.Contains(text, "Block: 6,003\tSpeed (blocks/s):") || strings.Contains(text, "Last ") ||
		!strings.Contains(text, "\tElapsed: 1d 01h") ||
		!strings.Contains(text, " / Remaining: 4m 30s") || !strings.Contains(text, " / Remaining: 0s") ||
		!strings.Contains(text, "Current block: 6,003 / 6,003\r\033[K") ||
		!strings.Contains(text, "\r\033[Kdisk sample saved\n") {
		t.Fatal(text)
	}
	if got := replayNumber(1002347); got != "1,002,347" {
		t.Fatal(got)
	}
	if got := replayElapsed(49*time.Hour + 2*time.Minute + 3*time.Second); got != "2d 01h 02m 03s" {
		t.Fatal(got)
	}
	for duration, want := range map[time.Duration]string{
		0: "0s", 5 * time.Second: "5s", 169 * time.Second: "2m 49s",
		time.Hour + 3*time.Second: "1h 00m 03s",
	} {
		if got := replayElapsed(duration); got != want {
			t.Fatalf("duration %s: got %q, want %q", duration, got, want)
		}
	}
	// A slow first window affects ETA until it leaves the 10K rolling window;
	// Recent still describes only the latest 1K, not an average of rates.
	out.Reset()
	now := time.Unix(1, 0)
	c = &replayConsole{out: bufio.NewWriter(&out), start: now, summaryAt: now, target: 20000}
	for i := 1; i <= 11; i++ {
		period := 10 * time.Second
		if i == 1 {
			period = 100 * time.Second
		}
		now = now.Add(period)
		c.head = uint64(i * 1000)
		c.summary(now)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.HasSuffix(lines[0], "Remaining: 31m 40s") ||
		!strings.Contains(lines[9], "/ recent 100.00") || !strings.HasSuffix(lines[9], "Remaining: 3m 10s") ||
		!strings.HasSuffix(lines[10], "Remaining: 1m 30s") {
		t.Fatal(out.String())
	}
	// Exercise the asynchronous flush/heartbeat with a deliberately old clock.
	out.Reset()
	c = newReplayConsole(&out, 7, 10, 0, time.Now().Add(-11*time.Second))
	time.Sleep(1200 * time.Millisecond)
	c.close()
	if !strings.Contains(out.String(), "last-completed=7/10") {
		t.Fatal(out.String())
	}
}
