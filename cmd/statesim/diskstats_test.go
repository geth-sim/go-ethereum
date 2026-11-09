package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReplayDiskStatsParser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diskstats")
	devices := map[string]bool{"nvme0n1": true, "md0": true}
	input := "259 0 nvme0n1 10 2 30 40 50 6 70 80 9 100 110 120 13 140 150 160 170\n" +
		"259 1 nvme0n1p1 1 0 2 3 4 0 5 6 0 7 8\n" +
		"9 0 md0 21 22 23 24 25 26 27 28 29 30 31\n"
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readReplayDiskStats(path, devices)
	want := map[string][]uint64{
		"nvme0n1": {10, 2, 30, 40, 50, 6, 70, 80, 9, 100, 110, 120, 13, 140, 150, 160, 170},
		"md0":     {21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("misattributed device/counter or fabricated optional field: %v, %v", got, err)
	}
	for _, bad := range []string{
		strings.Replace(input, " 170\n", " invalid\n", 1),
		strings.Replace(input, "9 0 md0", "9 0 missing", 1),
		"259 0 nvme0n1 1 2 3\n",
	} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readReplayDiskStats(path, devices); err == nil {
			t.Fatal("accepted corrupt/incomplete device statistics")
		}
	}
}

func TestReplayDiskStatsLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux device counters")
	}
	path := filepath.Join(t.TempDir(), "disk_io.jsonl")
	// A previous run may contain blocks beyond a restored checkpoint and a
	// partially written final line. Keep its valid samples when appending.
	if err := os.WriteFile(path, []byte("{\"Type\":\"diskstats\",\"RunID\":\"old\",\"Number\":999}\n{\"Type\":"), 0600); err != nil {
		t.Fatal(err)
	}
	var head atomic.Uint64
	head.Store(10)
	sampled := make(chan struct{}, 8)
	readHead := func() uint64 {
		select {
		case sampled <- struct{}{}:
		default:
		}
		return head.Load()
	}
	var canceled atomic.Bool
	stop, err := startReplayDiskStats(path, "new", 5*time.Millisecond, map[string]any{"Type": "run", "RunID": "new", "PerformanceMode": true}, readHead, func() { canceled.Store(true) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop() })
	// Baseline is immediately visible even before the periodic buffer flush.
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"Phase":"start"`) {
		t.Fatalf("missing initial sample: %s, %v", data, err)
	}
	// The block head deliberately stays fixed: samples must continue in a stall.
	for i := 0; i < 3; i++ {
		select {
		case <-sampled:
		case <-time.After(2 * time.Second):
			t.Fatal("sampler stalled with the block processor")
		}
	}
	head.Store(11)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var samples []replayDiskStatsSample
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var sample replayDiskStatsSample
		if err := json.Unmarshal([]byte(line), &sample); err != nil {
			t.Fatal("partial tail corrupted appended run", err)
		}
		if sample.Type == "diskstats" && sample.RunID == "new" {
			samples = append(samples, sample)
		}
	}
	if len(samples) < 4 || samples[0].Phase != "start" || samples[len(samples)-1].Phase != "end" || samples[len(samples)-1].Number != 11 {
		t.Fatalf("lost baseline, periodic or final sample: %+v", samples)
	}
	for i, sample := range samples {
		if len(sample.Devices) == 0 || sample.TimestampUTC.IsZero() || (i > 0 && sample.ElapsedNS <= samples[i-1].ElapsedNS) {
			t.Fatalf("invalid sample: %+v", sample)
		}
	}
	if !strings.Contains(string(data), `"Number":999`) || canceled.Load() {
		t.Fatal("lost previous run or canceled successful replay")
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatal("repeated stop wrote duplicate samples", err)
	}
}

func TestReplayDiskStatsDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk_io.jsonl")
	stop, err := startReplayDiskStats(path, "off", 0, nil, nil, nil)
	if err != nil || stop() != nil {
		t.Fatal("disabled collector failed", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("disabled collector created output", err)
	}
	if _, err := startReplayDiskStats(path, "invalid", -time.Second, nil, nil, nil); err == nil {
		t.Fatal("accepted a negative interval")
	}
}

func BenchmarkReplayDiskStats(b *testing.B) {
	if runtime.GOOS != "linux" {
		b.Skip("Linux device counters")
	}
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		b.Fatal(err)
	}
	devices := make(map[string]bool)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "loop") && !strings.HasPrefix(name, "ram") && !strings.HasPrefix(name, "zram") {
			devices[name] = true
		}
	}
	writer := bufio.NewWriterSize(io.Discard, 256*1024)
	encoder := json.NewEncoder(writer)
	started := time.Now()
	sample := replayDiskStatsSample{Type: "diskstats", RunID: started.UTC().Format(time.RFC3339Nano), Phase: "sample", Number: 6000000}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sample.Devices, err = readReplayDiskStats("/proc/diskstats", devices)
		if err != nil {
			b.Fatal(err)
		}
		now := time.Now()
		sample.TimestampUTC, sample.ElapsedNS = now.UTC(), now.Sub(started)
		if err := encoder.Encode(sample); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	data, err := json.Marshal(sample)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(len(data)+1), "bytes/sample")
	b.ReportMetric(float64(len(data)+1)*86400/(1<<20), "MiB/day@1Hz")
}
