package statesim

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/syndtr/goleveldb/leveldb"
)

const (
	simBlocksSchemaVersion       = 15
	keySchemeCodecVersion        = 1
	oldestSimBlocksSchemaVersion = 2
)

// simulationConfig records effective runtime settings alongside measured blocks.
// Zero and false values are deliberately retained in JSON for later auditing.
type simulationConfig struct {
	Simulation  simulationOptions  `json:"simulation"`
	KeyScheme   keySchemeOptions   `json:"key_scheme"`
	Database    databaseOptions    `json:"database"`
	Cache       cacheOptions       `json:"cache"`
	Features    featureOptions     `json:"features"`
	Measurement measurementOptions `json:"measurement"`
	Software    softwareOptions    `json:"software"`
}

type simulationOptions struct {
	SimulationMode     int    `json:"simulation_mode"`
	SimulationModeName string `json:"simulation_mode_name"`
	ChainConfig        string `json:"chain_config"`
	ServerPort         string `json:"server_port"`
	IsArchiveMode      bool   `json:"is_archive_mode"`
	IsPathScheme       bool   `json:"is_path_scheme"`
	EnableSnapshot     bool   `json:"enable_snapshot"`
	IsDoSAttacking     bool   `json:"is_dos_attacking"`
}

// keySchemeOptions records the effective encoder options and scheme parameters.
type keySchemeOptions struct {
	CodecVersion              int    `json:"codec_version"`
	ModifyHashMethod          string `json:"modify_hash_method"`
	EpochSize                 uint64 `json:"epoch_size"`
	DepthThreshold            int64  `json:"depth_threshold"`
	StorageDepthThreshold     int64  `json:"storage_depth_threshold"`
	ShardOwnerPrefixLen       int    `json:"shard_owner_prefix_len"`
	RunPathTargetNodes        uint64 `json:"run_path_target_nodes"`
	ATileBlocks               uint64 `json:"atile_blocks"`
	ATileStoragePathPrefixLen int    `json:"atile_storage_path_prefix_len"`
	VersionLength             int    `json:"version_length"`
	EnableVersionPadding      bool   `json:"enable_version_padding"`
	PathLength                int    `json:"path_length"`
	FixedPathLength           bool   `json:"fixed_path_length"`
	PathPaddingAtEnd          bool   `json:"path_padding_at_end"`
	AppendPathFirst           bool   `json:"append_path_first"`
	LastPaddingBound          int    `json:"last_padding_bound"`
	AppendPathLen             bool   `json:"append_path_len"`
	LenOfPathLen              int    `json:"len_of_path_len"`
	AppendTrieType            bool   `json:"append_trie_type"`
	AppendContractAddrHash    bool   `json:"append_contract_addr_hash"`
	AddrHashPrefixLen         int    `json:"addr_hash_prefix_len"`
}

type databaseOptions struct {
	Configured          bool   `json:"configured"`
	DeleteDisk          bool   `json:"delete_disk"`
	Backend             string `json:"backend"`
	Path                string `json:"path"`
	Compression         string `json:"compression"`
	PebbleEphemeral     bool   `json:"pebble_ephemeral"`
	LevelDBNamespace    string `json:"leveldb_namespace"`
	LevelDBReadonly     bool   `json:"leveldb_readonly"`
	LevelDBHandles      int    `json:"leveldb_handles"`
	LevelDBLoggingBuild bool   `json:"leveldb_logging_build"`
	TriePreimages       bool   `json:"trie_preimages"`
	PathStateHistory    uint64 `json:"path_state_history"`
	TrieFlushIntervalNS int64  `json:"trie_flush_interval_ns"`
}

type cacheOptions struct {
	TotalCacheMB            int  `json:"total_cache_mb"`
	LevelDBCacheMB          int  `json:"leveldb_cache_mb"`
	DirtyCacheMB            int  `json:"dirty_cache_mb"`
	SnapshotCacheMB         int  `json:"snapshot_cache_mb"`
	TrieCacheMB             int  `json:"trie_cache_mb"`
	UseUnifiedChildCache    bool `json:"use_unified_child_cache"`
	StateChildReadCacheMB   int  `json:"state_child_read_cache_mb"`
	StorageChildReadCacheMB int  `json:"storage_child_read_cache_mb"`
}

type featureOptions struct {
	ReadAllChildNodes  bool    `json:"read_all_child_nodes"`
	AdditionalByteLen  int     `json:"additional_byte_len"`
	DiskSizeMultiplier float64 `json:"disk_size_multiplier"`
}

type measurementOptions struct {
	EnabledExpensive      bool   `json:"enabled_expensive"`
	MetricsExpensive      bool   `json:"metrics_expensive"`
	MeasureReadStats      bool   `json:"measure_read_stats"`
	MeasureChildStats     bool   `json:"measure_child_stats"`
	LoggingReadStats      bool   `json:"logging_read_stats"`
	LoggingOpcodeStats    bool   `json:"logging_opcode_stats"`
	DiskSizeMeasureEpoch  uint64 `json:"disk_size_measure_epoch"`
	SaveLevelDBStatsEpoch uint64 `json:"save_leveldb_stats_epoch"`
}

type softwareOptions struct {
	GoVersion              string `json:"go_version"`
	GethRevision           string `json:"geth_revision"`
	GethModified           bool   `json:"geth_modified"`
	GoLevelDBModuleVersion string `json:"goleveldb_module_version"`
	GoLevelDBReplacement   string `json:"goleveldb_replacement"`
	GoLevelDBRevision      string `json:"goleveldb_revision"`
	GoLevelDBModified      bool   `json:"goleveldb_modified"`
}

type simulationResultsFile struct {
	SchemaVersion int                         `json:"schema_version"`
	Config        simulationConfig            `json:"config"`
	Blocks        map[string]*common.SimBlock `json:"blocks"`
}

var (
	softwareConfigOnce sync.Once
	softwareConfig     softwareOptions
)

func captureSimulationConfig() simulationConfig {
	modeName := "unknown"
	if common.SimulationMode >= 0 && common.SimulationMode < len(common.SimulationModeNames) {
		modeName = common.SimulationModeNames[common.SimulationMode]
	}
	softwareConfigOnce.Do(func() {
		softwareConfig = captureSoftwareOptions()
	})
	return simulationConfig{
		Simulation: simulationOptions{
			SimulationMode:     common.SimulationMode,
			SimulationModeName: modeName,
			ChainConfig:        "mainnet",
			ServerPort:         ServerPort,
			IsArchiveMode:      common.IsArchiveMode,
			IsPathScheme:       common.IsPathScheme,
			EnableSnapshot:     common.EnableSnapshot,
			IsDoSAttacking:     common.IsDoSAttacking,
		},
		KeyScheme: currentKeySchemeOptions(),
		Database: databaseOptions{
			Configured:          databaseConfigured,
			DeleteDisk:          databaseDeleteDisk,
			Backend:             dbType,
			Path:                leveldbPath,
			Compression:         common.DatabaseCompression,
			PebbleEphemeral:     pebbleEphemeral,
			LevelDBNamespace:    leveldbNamespace,
			LevelDBReadonly:     leveldbReadonly,
			LevelDBHandles:      leveldbHandles,
			LevelDBLoggingBuild: leveldb.IsLogging,
			TriePreimages:       triePreimages,
			PathStateHistory:    pathStateHistory,
			TrieFlushIntervalNS: myFlushInterval.Load(),
		},
		Cache: cacheOptions{
			TotalCacheMB:            totalCacheSize,
			LevelDBCacheMB:          leveldbCache,
			DirtyCacheMB:            dirtyCacheSize,
			SnapshotCacheMB:         snapshotCacheSize,
			TrieCacheMB:             trieCacheSize,
			UseUnifiedChildCache:    common.UseUnifiedCache,
			StateChildReadCacheMB:   common.StateChildReadCacheSize,
			StorageChildReadCacheMB: common.StorageChildReadCacheSize,
		},
		Features: featureOptions{
			ReadAllChildNodes:  common.ReadAllChildNodes,
			AdditionalByteLen:  common.AdditionalByteLen,
			DiskSizeMultiplier: common.DiskSizeMultiplier,
		},
		Measurement: measurementOptions{
			EnabledExpensive:      enabledExpensive,
			MetricsExpensive:      metrics.EnabledExpensive,
			MeasureReadStats:      common.MeasureReadStats,
			MeasureChildStats:     common.MeasureChildStats,
			LoggingReadStats:      common.LoggingReadStats,
			LoggingOpcodeStats:    common.LoggingOpcodeStats,
			DiskSizeMeasureEpoch:  diskSizeMeasureEpoch,
			SaveLevelDBStatsEpoch: saveLevelDBStatsEpoch,
		},
		Software: softwareConfig,
	}
}

func currentKeySchemeOptions() keySchemeOptions {
	return keySchemeOptions{
		CodecVersion:              keySchemeCodecVersion,
		ModifyHashMethod:          common.ModifyHashMethod,
		EpochSize:                 common.EpochSize,
		DepthThreshold:            common.DepthThreshold,
		StorageDepthThreshold:     common.StorageDepthThreshold,
		ShardOwnerPrefixLen:       common.ShardOwnerPrefixLen,
		RunPathTargetNodes:        common.RunPathTargetNodes,
		ATileBlocks:               common.ATileBlocks,
		ATileStoragePathPrefixLen: common.ATileStoragePathPrefixLen,
		VersionLength:             common.VersionLength,
		EnableVersionPadding:      common.EnableVersionPadding,
		PathLength:                common.PathLength,
		FixedPathLength:           common.FixedPathLength,
		PathPaddingAtEnd:          common.PathPaddingAtEnd,
		AppendPathFirst:           common.AppendPathFirst,
		LastPaddingBound:          common.LastPaddingBound,
		AppendPathLen:             common.AppendPathLen,
		LenOfPathLen:              common.LenOfPathLen,
		AppendTrieType:            common.AppendTrieType,
		AppendContractAddrHash:    common.AppendContractAddrHash,
		AddrHashPrefixLen:         common.AddrHashPrefixLen,
	}
}

// Older envelopes may contain config_sha256 and retired metadata fields. They
// are ignored; schema and JSON structure are checked without rebuilding a digest.
func decodeSimulationResultsJSON(data []byte) (simulationResultsFile, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return simulationResultsFile{}, false, fmt.Errorf("simulation result must be a JSON object: %v", err)
	}
	legacy := !decoder.More()
	if !legacy {
		key, err := decoder.Token()
		if err != nil {
			return simulationResultsFile{}, false, err
		}
		_, err = strconv.ParseUint(key.(string), 10, 64)
		legacy = err == nil // legacy objects have block numbers as top-level keys
	}
	if legacy {
		var blocks map[string]*common.SimBlock
		err := json.Unmarshal(data, &blocks)
		return simulationResultsFile{SchemaVersion: 1, Blocks: blocks}, true, err
	}
	var result simulationResultsFile
	if err := json.Unmarshal(data, &result); err != nil {
		return result, false, err
	}
	if result.SchemaVersion < oldestSimBlocksSchemaVersion || result.SchemaVersion > simBlocksSchemaVersion {
		return result, false, fmt.Errorf("unsupported simulation result schema %d", result.SchemaVersion)
	}
	if result.Blocks == nil {
		return result, false, fmt.Errorf("simulation result has no blocks object")
	}
	return result, false, nil
}

func captureSoftwareOptions() softwareOptions {
	options := softwareOptions{GoVersion: runtime.Version()}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				options.GethRevision = setting.Value
			case "vcs.modified":
				options.GethModified = setting.Value == "true"
			}
		}
		for _, dependency := range info.Deps {
			if dependency.Path != "github.com/syndtr/goleveldb" {
				continue
			}
			options.GoLevelDBModuleVersion = dependency.Version
			if dependency.Replace != nil {
				options.GoLevelDBReplacement = dependency.Replace.Path
			}
		}
	}

	// The binary's geth revision comes from build info, never from the runtime
	// working tree. A local LevelDB replacement is recorded as a checkout observation.
	if root := gitOutput(".", "rev-parse", "--show-toplevel"); root != "" && options.GoLevelDBReplacement != "" {
		repo := options.GoLevelDBReplacement
		if !filepath.IsAbs(repo) {
			repo = filepath.Join(root, repo)
		}
		options.GoLevelDBRevision = gitOutput(repo, "rev-parse", "HEAD")
		options.GoLevelDBModified = gitOutput(repo, "status", "--porcelain", "--untracked-files=normal") != ""
	}
	return options
}

func gitOutput(repoPath string, args ...string) string {
	commandArgs := append([]string{"-C", repoPath}, args...)
	output, err := exec.Command("git", commandArgs...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// Only active scheme parameters belong in the filename; full settings are in JSON.
func keySchemeFileLabel() string {
	label := common.ModifyHashMethod
	switch common.ModifyHashMethod {
	case "EpochPath":
		label += fmt.Sprintf("-E%d", common.EpochSize)
	case "TPV", "OutwardSplit", "DepthEpoch":
		label += fmt.Sprintf("-E%d-D%d", common.EpochSize, common.DepthThreshold)
	case "OutwardStorage":
		label += fmt.Sprintf("-E%d-A%d-S%dB", common.EpochSize, common.DepthThreshold, common.StorageDepthThreshold)
	case "SplitPVHot", "DepthSplit":
		label += fmt.Sprintf("-D%d", common.DepthThreshold)
	case "ShardVP":
		label += fmt.Sprintf("-O%d", common.ShardOwnerPrefixLen)
	case "RunPath":
		label += fmt.Sprintf("-N%d", common.RunPathTargetNodes)
	case "ATileVP":
		label += fmt.Sprintf("-B%d-P%d", common.ATileBlocks, common.ATileStoragePathPrefixLen)
	}
	return label
}

func writeSimBlocksJSON(fileName string, mapKeys []string) error {
	file, err := os.Create(simBlocksPath + fileName)
	if err != nil {
		return err
	}
	closeFile := true
	defer func() {
		if closeFile {
			file.Close()
		}
	}()

	writer := bufio.NewWriterSize(file, 1024*1024)
	config := captureSimulationConfig()
	if err := validateKeySchemeOptions(config.KeyScheme); err != nil {
		return fmt.Errorf("refuse to save invalid key scheme configuration: %w", err)
	}
	configData, err := json.MarshalIndent(config, "  ", "  ")
	if err != nil {
		return err
	}

	if _, err := writer.WriteString("{\n  \"schema_version\": " + strconv.Itoa(simBlocksSchemaVersion) + ",\n"); err != nil {
		return err
	}
	if _, err := writer.WriteString("  \"config\": "); err != nil {
		return err
	}
	if _, err := writer.Write(configData); err != nil {
		return err
	}
	if _, err := writer.WriteString(",\n  \"blocks\": {\n"); err != nil {
		return err
	}
	for i, blockNumStr := range mapKeys {
		keyData, err := json.Marshal(blockNumStr)
		if err != nil {
			return err
		}
		blockData, err := json.MarshalIndent(common.SimBlocks[blockNumStr], "    ", "  ")
		if err != nil {
			return err
		}
		if _, err := writer.WriteString("    "); err != nil {
			return err
		}
		if _, err := writer.Write(keyData); err != nil {
			return err
		}
		if _, err := writer.WriteString(": "); err != nil {
			return err
		}
		if _, err := writer.Write(blockData); err != nil {
			return err
		}
		if i != len(mapKeys)-1 {
			if _, err := writer.WriteString(","); err != nil {
				return err
			}
		}
		if _, err := writer.WriteString("\n"); err != nil {
			return err
		}
	}
	if _, err := writer.WriteString("  }\n}\n"); err != nil {
		return err
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	closeFile = false
	return file.Close()
}
