package statesim

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

const (
	defaultTotalCacheMB = 4096
	myHashBytes         = 32
)

// SimulatorConfig contains the reviewer-facing simulator options. Low-level
// key-layout fields remain scheme presets in setDatabase so invalid key
// combinations cannot be assembled from the command line.
type SimulatorConfig struct {
	Port                 string  `json:"port"`
	Scheme               string  `json:"scheme"`
	StateMode            string  `json:"state_mode"`
	PathDBHistory        bool    `json:"pathdb_history"`
	Database             string  `json:"database"`
	Compression          string  `json:"compression"`
	MyHash               bool    `json:"myhash"`
	MyHashCacheMB        int     `json:"myhash_cache_mb"`
	MyHashCacheMode      string  `json:"myhash_cache_mode"`
	DiskSizeMultiplier   float64 `json:"disk_size_multiplier"`
	VersionWrap          string  `json:"version_wrap"`
	AccurateReadCounters bool    `json:"accurate_read_counters"`
	ChildStats           bool    `json:"child_stats"`
	DiskSizeInterval     uint64  `json:"disk_size_interval"`
	LevelDBStatsInterval uint64  `json:"leveldb_stats_interval"`
}

var experimentID string

func DefaultSimulatorConfig() SimulatorConfig {
	return SimulatorConfig{
		Port:                 ServerPort,
		Scheme:               "H",
		StateMode:            "auto",
		PathDBHistory:        true,
		Database:             dbTypeLevelDB,
		Compression:          common.DatabaseCompressionSnappy,
		MyHashCacheMode:      "unified",
		DiskSizeMultiplier:   1.0,
		VersionWrap:          "none",
		DiskSizeInterval:     diskSizeMeasureEpoch,
		LevelDBStatsInterval: saveLevelDBStatsEpoch,
	}
}

func normalizeScheme(scheme string) (paperName, method string, err error) {
	normalized := strings.ToLower(strings.TrimSpace(scheme))
	normalized = strings.ReplaceAll(normalized, "*", "star")
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, "_", "")
	switch normalized {
	case "h":
		return "H", "none", nil
	case "p":
		return "P", "PBSS", nil
	case "ph":
		return "PH", "HalfPath", nil
	case "pv":
		return "PV", "PrefixTree", nil
	case "pvstar":
		return "PVstar", "PrefixTree_fixed", nil
	case "vh":
		return "VH", "TH", nil
	case "vp":
		return "VP", "JMT", nil
	case "vpstar":
		return "VPstar", "JMT_fixed", nil
	default:
		return "", "", fmt.Errorf("unknown scheme %q (available: H, P, PH, PV, PVstar, VH, VP, VPstar)", scheme)
	}
}

func normalizeStateMode(mode string, isPBSS bool) (string, bool, error) {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	if normalized == "" {
		normalized = "auto"
	}
	switch normalized {
	case "auto":
		if isPBSS {
			return "non-archive", false, nil
		}
		return "archive", true, nil
	case "archive":
		if isPBSS {
			return "", false, fmt.Errorf("scheme P is intrinsically non-archive; use --state-mode auto or non-archive")
		}
		return "archive", true, nil
	case "non-archive", "nonarchive", "full":
		return "non-archive", false, nil
	default:
		return "", false, fmt.Errorf("unknown state mode %q (available: auto, archive, non-archive)", mode)
	}
}

func normalizeDatabase(database string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(database))
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, "_", "")
	switch normalized {
	case "leveldb":
		return dbTypeLevelDB, nil
	case "pebble", "pebbledb":
		return dbTypePebble, nil
	default:
		return "", fmt.Errorf("unknown database %q (available: leveldb, pebbledb)", database)
	}
}

func normalizeVersionWrap(value string) (string, uint64, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", "none", "off", "0":
		return "none", 0, nil
	case "0xffff":
		return "0xffff", 65535, nil
	case "0xfffff":
		return "0xfffff", 1048575, nil
	default:
		return "", 0, fmt.Errorf("unknown version wrap %q (available: none, 0xffff, 0xfffff)", value)
	}
}

func normalizeMyHashCacheMode(value string) (string, bool, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		normalized = "unified"
	}
	switch normalized {
	case "unified":
		return "unified", true, nil
	case "split":
		return "split", false, nil
	default:
		return "", false, fmt.Errorf("unknown myHash cache mode %q (available: unified, split)", value)
	}
}

func floatToken(value float64) string {
	token := strconv.FormatFloat(value, 'f', -1, 64)
	token = strings.ReplaceAll(token, ".", "p")
	return token
}

// buildExperimentID returns the stable, filename-safe identifier shared by
// every output from one configuration. All separators are underscores.
func buildExperimentID(config SimulatorConfig) string {
	mode := strings.ReplaceAll(config.StateMode, "-", "")
	database := config.Database
	if database == dbTypePebble {
		database = "pebbledb"
	}
	parts := []string{
		config.Scheme,
		mode,
		database,
		config.Compression,
		simulatorBuildVariant,
	}
	if config.MyHash {
		parts = append(parts, "myhash")
		if config.MyHashCacheMB > 0 {
			cacheMode := config.MyHashCacheMode
			if cacheMode == "" {
				cacheMode = "unified"
			}
			parts = append(parts, "cache", strconv.Itoa(config.MyHashCacheMB)+"mb", cacheMode)
		}
	}
	if config.DiskSizeMultiplier > 1.0 {
		parts = append(parts, "padding", floatToken(config.DiskSizeMultiplier))
	}
	if config.VersionWrap != "none" {
		parts = append(parts, "wrap", strings.TrimPrefix(config.VersionWrap, "0x"))
	}
	if !config.PathDBHistory {
		parts = append(parts, "nohistory")
	}
	if config.AccurateReadCounters {
		parts = append(parts, "accurate", "reads")
	}
	if config.ChildStats {
		parts = append(parts, "child", "stats")
	}
	return strings.Join(parts, "_")
}

func configureOutputPaths() {
	runPath := logFilePath + "runs/" + experimentID + "/"
	simBlocksPath = runPath + "simBlocks/"
	cacheStatsPath = runPath + "cacheStats/"
	opcodeStatsPath = runPath + "opcodeStats/"
	leveldbStatsPath = runPath + "leveldbStats/"
	errLogPath = runPath + "errLogs/"
}

// ConfigureSimulator validates and applies all process-wide options before the
// simulator accepts a client. Database and key-layout initialization still
// occurs in setDatabase after the client supplies its database path.
func ConfigureSimulator(config SimulatorConfig) error {
	port, err := strconv.Atoi(config.Port)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port must be an integer between 1 and 65535, got %q", config.Port)
	}
	paperScheme, modifyHashMethod, err := normalizeScheme(config.Scheme)
	if err != nil {
		return err
	}
	stateMode, archive, err := normalizeStateMode(config.StateMode, paperScheme == "P")
	if err != nil {
		return err
	}
	database, err := normalizeDatabase(config.Database)
	if err != nil {
		return err
	}
	compression, err := common.NormalizeDatabaseCompression(config.Compression)
	if err != nil {
		return err
	}
	if database == dbTypeLevelDB && compression == common.DatabaseCompressionZstd {
		return fmt.Errorf("zstd compression is supported only by pebbledb")
	}
	if config.MyHashCacheMB < 0 {
		return fmt.Errorf("myHash cache size cannot be negative")
	}
	if config.MyHashCacheMB > 0 && !config.MyHash {
		return fmt.Errorf("--myhash-cache-mb requires --myhash")
	}
	cacheMode, unifiedCache, err := normalizeMyHashCacheMode(config.MyHashCacheMode)
	if err != nil {
		return err
	}
	if cacheMode == "split" && config.MyHashCacheMB == 0 {
		return fmt.Errorf("--myhash-cache-mode split requires --myhash-cache-mb greater than zero")
	}
	if config.MyHash && paperScheme != "PVstar" && paperScheme != "VPstar" {
		return fmt.Errorf("--myhash is supported for PVstar and VPstar paper configurations")
	}
	if !config.PathDBHistory && paperScheme != "P" {
		return fmt.Errorf("--pathdb-history=false is supported only with scheme P")
	}
	if math.IsNaN(config.DiskSizeMultiplier) ||
		math.IsInf(config.DiskSizeMultiplier, 0) ||
		config.DiskSizeMultiplier < 1.0 {
		return fmt.Errorf("--disk-size-multiplier must be at least 1.0")
	}
	if config.MyHash && config.DiskSizeMultiplier > 1.0 {
		return fmt.Errorf("--myhash and --disk-size-multiplier greater than 1.0 are mutually exclusive")
	}
	versionWrap, versionModulo, err := normalizeVersionWrap(config.VersionWrap)
	if err != nil {
		return err
	}
	if versionModulo > 0 && paperScheme != "VH" {
		return fmt.Errorf("--version-wrap is supported only with scheme VH")
	}
	if config.DiskSizeInterval == 0 {
		return fmt.Errorf("--disk-size-interval must be greater than zero")
	}
	if config.LevelDBStatsInterval == 0 {
		return fmt.Errorf("--leveldb-stats-interval must be greater than zero")
	}

	config.Scheme = paperScheme
	config.StateMode = stateMode
	config.Database = database
	config.Compression = compression
	config.MyHashCacheMode = cacheMode
	config.VersionWrap = versionWrap

	ServerPort = config.Port
	common.ModifyHashMethod = modifyHashMethod
	if paperScheme == "P" {
		// Preserve the original PBSS initialization order. setDatabase first
		// allocates the archive-mode cache split, then the PBSS preset switches
		// execution to non-archive PathDB mode.
		common.IsArchiveMode = true
		common.IsPathScheme = false
	} else {
		common.IsArchiveMode = archive
		common.IsPathScheme = false
	}
	common.EnableSnapshot = false
	common.UseUnifiedCache = unifiedCache
	common.ReadAllChildNodes = config.MyHash
	common.AdditionalByteLen = 0
	common.StateChildReadCacheSize = 0
	common.StorageChildReadCacheSize = 0
	if config.MyHash {
		common.AdditionalByteLen = myHashBytes
		// The unified cache implementation uses the sum of these two values.
		common.StateChildReadCacheSize = config.MyHashCacheMB / 2
		common.StorageChildReadCacheSize = config.MyHashCacheMB - common.StateChildReadCacheSize
	}
	common.DiskSizeMultiplier = config.DiskSizeMultiplier
	if err := common.SetDatabaseCompression(config.Compression); err != nil {
		return err
	}
	common.VersionModulo = versionModulo
	common.MeasureReadStats = config.AccurateReadCounters
	common.MeasureChildStats = config.ChildStats
	common.LoggingReadStats = false

	dbType = config.Database
	totalCacheSize = defaultTotalCacheMB
	pathDBHistory = config.PathDBHistory
	diskSizeMeasureEpoch = config.DiskSizeInterval
	saveLevelDBStatsEpoch = config.LevelDBStatsInterval
	experimentID = buildExperimentID(config)
	configureOutputPaths()
	return nil
}
