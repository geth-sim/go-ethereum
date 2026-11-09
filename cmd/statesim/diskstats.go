package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Array positions follow Linux /proc/diskstats, excluding major/minor/name.
// Older kernels omit the discard/flush suffix; absent fields are not zeroes.
var replayDiskStatsFields = []string{
	"read_ios", "read_merges", "read_sectors", "read_ms",
	"write_ios", "write_merges", "write_sectors", "write_ms",
	"in_flight", "io_ms", "weighted_io_ms",
	"discard_ios", "discard_merges", "discard_sectors", "discard_ms",
	"flush_ios", "flush_ms",
}

const replayDiskStatsFlushInterval = 10 * time.Second

type replayDiskStatsSample struct {
	Type, RunID, Phase string
	TimestampUTC       time.Time
	ElapsedNS          time.Duration
	Number             uint64 // last imported block, from Geth's atomic head pointer
	Devices            map[string][]uint64
}

func readReplayDiskStats(path string, devices map[string]bool) (map[string][]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stats := make(map[string][]uint64, len(devices))
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || !devices[fields[2]] {
			continue
		}
		count := min(len(fields)-3, len(replayDiskStatsFields))
		if count != 11 && count != 15 && count != 17 {
			return nil, fmt.Errorf("invalid diskstats field count for %s: %d", fields[2], count)
		}
		values := make([]uint64, count)
		for i := range values {
			values[i], err = strconv.ParseUint(fields[i+3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("diskstats %s/%s: %w", fields[2], replayDiskStatsFields[i], err)
			}
		}
		stats[fields[2]] = values
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for device := range devices {
		if _, ok := stats[device]; !ok {
			return nil, fmt.Errorf("diskstats device disappeared: %s", device)
		}
	}
	return stats, nil
}

// Collect independently of block execution so stalls still produce samples.
// One goroutine owns the buffered log; no sample history or per-I/O tracing is
// retained. stop flushes the final sample and is safe to call more than once.
func startReplayDiskStats(path, runID string, interval time.Duration, runInfo any, head func() uint64, cancel func()) (stop func() error, err error) {
	if interval == 0 {
		return func() error { return nil }, nil
	}
	if interval < 0 || runtime.GOOS != "linux" {
		return nil, errors.New("diskstats requires Linux and a positive interval (0 disables)")
	}
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}
	devices := make(map[string]bool)
	for _, entry := range entries {
		name := entry.Name()
		// /sys/block contains whole devices, including RAID/dm, not partitions.
		if !strings.HasPrefix(name, "loop") && !strings.HasPrefix(name, "ram") && !strings.HasPrefix(name, "zram") {
			devices[name] = true
		}
	}
	if len(devices) == 0 {
		return nil, errors.New("diskstats: no block devices found")
	}
	// Preserve earlier runs on resume, but remove an incomplete crash tail.
	if err = truncateReplayResults(path, ^uint64(0)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	log, err := openReplayLog(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, log.Close())
		}
	}()
	if err = log.Encode(runInfo); err != nil {
		return nil, err
	}
	if err = log.Encode(struct {
		Type, RunID, Source         string
		SchemaVersion, SectorBytes  int
		IntervalNS, FlushIntervalNS time.Duration
		Fields                      []string
	}{"diskstats-schema", runID, "/proc/diskstats", 1, 512, interval, replayDiskStatsFlushInterval, replayDiskStatsFields}); err != nil {
		return nil, err
	}
	started := time.Now()
	sample := func(phase string) error {
		values, err := readReplayDiskStats("/proc/diskstats", devices)
		if err != nil {
			return err
		}
		now := time.Now()
		return log.Encode(replayDiskStatsSample{"diskstats", runID, phase, now.UTC(), now.Sub(started), head(), values})
	}
	if err = sample("start"); err != nil {
		return nil, err
	}
	if err = log.writer.Flush(); err != nil {
		return nil, err
	}
	quit, done := make(chan struct{}), make(chan error, 1)
	go func() {
		var err error
		defer func() {
			err = errors.Join(err, log.Close())
			if err != nil {
				err = fmt.Errorf("diskstats collection: %w", err)
				cancel() // stop replay cleanly instead of silently losing measurements
			}
			done <- err
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		flush := time.NewTicker(replayDiskStatsFlushInterval)
		defer flush.Stop()
		for {
			select {
			case <-quit:
				err = sample("end")
				return
			case <-ticker.C:
				err = sample("sample")
			case <-flush.C:
				err = log.writer.Flush()
			}
			if err != nil {
				return
			}
		}
	}()
	return sync.OnceValue(func() error { close(quit); return <-done }), nil
}
