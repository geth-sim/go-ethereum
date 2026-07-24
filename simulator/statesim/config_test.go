package statesim

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestNormalizeScheme(t *testing.T) {
	tests := map[string]struct {
		paper  string
		method string
	}{
		"H":       {"H", "none"},
		"P":       {"P", "PBSS"},
		"PH":      {"PH", "HalfPath"},
		"PV":      {"PV", "PrefixTree"},
		"PVstar":  {"PVstar", "PrefixTree_fixed"},
		"PV*":     {"PVstar", "PrefixTree_fixed"},
		"VH":      {"VH", "TH"},
		"VP":      {"VP", "JMT"},
		"VPstar":  {"VPstar", "JMT_fixed"},
		"vp-star": {"VPstar", "JMT_fixed"},
	}
	for input, want := range tests {
		paper, method, err := normalizeScheme(input)
		if err != nil {
			t.Fatalf("normalizeScheme(%q): %v", input, err)
		}
		if paper != want.paper || method != want.method {
			t.Fatalf("normalizeScheme(%q) = (%q, %q), want (%q, %q)", input, paper, method, want.paper, want.method)
		}
	}
}

func TestNormalizeStateMode(t *testing.T) {
	if mode, archive, err := normalizeStateMode("auto", false); err != nil || mode != "archive" || !archive {
		t.Fatalf("normal auto mode = (%q, %v, %v)", mode, archive, err)
	}
	if mode, archive, err := normalizeStateMode("auto", true); err != nil || mode != "non-archive" || archive {
		t.Fatalf("PBSS auto mode = (%q, %v, %v)", mode, archive, err)
	}
	if _, _, err := normalizeStateMode("archive", true); err == nil {
		t.Fatal("PBSS archive mode should be rejected")
	}
}

func TestNormalizeVersionWrap(t *testing.T) {
	tests := map[string]uint64{
		"none":    0,
		"0xffff":  65535,
		"0xfffff": 1048575,
	}
	for input, want := range tests {
		_, got, err := normalizeVersionWrap(input)
		if err != nil {
			t.Fatalf("normalizeVersionWrap(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("normalizeVersionWrap(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestNormalizeMyHashCacheMode(t *testing.T) {
	if mode, unified, err := normalizeMyHashCacheMode("unified"); err != nil || mode != "unified" || !unified {
		t.Fatalf("unified cache mode = (%q, %v, %v)", mode, unified, err)
	}
	if mode, unified, err := normalizeMyHashCacheMode("split"); err != nil || mode != "split" || unified {
		t.Fatalf("split cache mode = (%q, %v, %v)", mode, unified, err)
	}
	if _, _, err := normalizeMyHashCacheMode("invalid"); err == nil {
		t.Fatal("invalid cache mode should be rejected")
	}
}

func TestPathDBHistoryValidation(t *testing.T) {
	config := DefaultSimulatorConfig()
	config.PathDBHistory = false
	if err := ConfigureSimulator(config); err == nil {
		t.Fatal("disabling PathDB history for H should be rejected")
	}

	config.Scheme = "P"
	if err := ConfigureSimulator(config); err != nil {
		t.Fatalf("disabling PathDB history for P should be accepted: %v", err)
	}
	if !common.IsArchiveMode || common.IsPathScheme {
		t.Fatalf(
			"P must retain the original pre-database cache state; got archive=%v path=%v",
			common.IsArchiveMode,
			common.IsPathScheme,
		)
	}
}

func TestBuildExperimentID(t *testing.T) {
	tests := []struct {
		name   string
		config SimulatorConfig
		want   string
	}{
		{
			name: "baseline",
			config: SimulatorConfig{
				Scheme: "H", StateMode: "archive", Database: dbTypeLevelDB,
				Compression: "snappy", DiskSizeMultiplier: 1,
				VersionWrap: "none", PathDBHistory: true,
			},
			want: "H_archive_leveldb_snappy_" + simulatorBuildVariant,
		},
		{
			name: "P without history",
			config: SimulatorConfig{
				Scheme: "P", StateMode: "non-archive", Database: dbTypeLevelDB,
				Compression: "snappy", DiskSizeMultiplier: 1,
				VersionWrap: "none", PathDBHistory: false,
			},
			want: "P_nonarchive_leveldb_snappy_" + simulatorBuildVariant + "_nohistory",
		},
		{
			name: "myhash cache",
			config: SimulatorConfig{
				Scheme: "PVstar", StateMode: "archive", Database: dbTypeLevelDB,
				Compression: "snappy", MyHash: true, MyHashCacheMB: 4096,
				MyHashCacheMode:    "unified",
				DiskSizeMultiplier: 1, VersionWrap: "none", PathDBHistory: true,
			},
			want: "PVstar_archive_leveldb_snappy_" + simulatorBuildVariant + "_myhash_cache_4096mb_unified",
		},
		{
			name: "myhash split cache",
			config: SimulatorConfig{
				Scheme: "VPstar", StateMode: "archive", Database: dbTypeLevelDB,
				Compression: "snappy", MyHash: true, MyHashCacheMB: 4096,
				MyHashCacheMode:    "split",
				DiskSizeMultiplier: 1, VersionWrap: "none", PathDBHistory: true,
			},
			want: "VPstar_archive_leveldb_snappy_" + simulatorBuildVariant + "_myhash_cache_4096mb_split",
		},
		{
			name: "pebble padding and counters",
			config: SimulatorConfig{
				Scheme: "VPstar", StateMode: "non-archive", Database: dbTypePebble,
				Compression: "zstd", DiskSizeMultiplier: 1.125,
				VersionWrap: "none", AccurateReadCounters: true, ChildStats: true,
				PathDBHistory: true,
			},
			want: "VPstar_nonarchive_pebbledb_zstd_" + simulatorBuildVariant + "_padding_1p125_accurate_reads_child_stats",
		},
		{
			name: "version wrap",
			config: SimulatorConfig{
				Scheme: "VH", StateMode: "archive", Database: dbTypeLevelDB,
				Compression: "snappy", DiskSizeMultiplier: 1,
				VersionWrap: "0xffff", PathDBHistory: true,
			},
			want: "VH_archive_leveldb_snappy_" + simulatorBuildVariant + "_wrap_ffff",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := buildExperimentID(test.config); got != test.want {
				t.Fatalf("buildExperimentID() = %q, want %q", got, test.want)
			}
		})
	}
}
