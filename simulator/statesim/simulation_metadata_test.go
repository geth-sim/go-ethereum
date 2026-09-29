package statesim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestKeySchemeMetadataRoundTrip(t *testing.T) {
	restore := saveMetadataTestGlobals()
	defer restore()
	for _, method := range []string{
		"JMT", "JMT_fixed", "JMT_balanced", "PrefixTree", "PrefixTree_fixed", "PrefixTree_balanced",
		"HalfPath", "PBSS", "TH", "none", "EpochPath", "TPV", "SplitPVHot", "OutwardSplit", "OutwardStorage", "VPRight",
		"DepthSplit", "DepthEpoch", "ShardVP", "DualVP", "RunPath", "ForestVP", "ATileVP",
	} {
		t.Run(method, func(t *testing.T) {
			common.ModifyHashMethod, common.EpochSize = method, 256
			common.DepthThreshold, common.StorageDepthThreshold = 3, 2
			common.ShardOwnerPrefixLen, common.RunPathTargetNodes = 1, 524288
			common.ATileBlocks, common.ATileStoragePathPrefixLen = 128, 1
			if err := configureKeyScheme(); err != nil {
				t.Fatal(err)
			}
			config := captureSimulationConfig()
			data, err := json.Marshal(simulationResultsFile{SchemaVersion: simBlocksSchemaVersion, Config: config, Blocks: map[string]*common.SimBlock{}})
			if err != nil {
				t.Fatal(err)
			}
			got, legacy, err := decodeSimulationResultsJSON(data)
			if err != nil || legacy || !reflect.DeepEqual(got.Config, config) {
				t.Fatalf("effective configuration lost: legacy=%v err=%v", legacy, err)
			}
		})
	}
}

func TestKeySchemeParameterValidation(t *testing.T) {
	restore := saveMetadataTestGlobals()
	defer restore()
	for _, tc := range []struct {
		method string
		change func()
	}{
		{"unknown", func() {}},
		{"EpochPath", func() { common.EpochSize = 0 }},
		{"TPV", func() { common.EpochSize = 1000 }},
		{"OutwardSplit", func() { common.EpochSize = 1<<32 + 1 }},
		{"DepthEpoch", func() { common.EpochSize = 128 }},
		{"TPV", func() { common.DepthThreshold = -1 }},
		{"OutwardSplit", func() { common.DepthThreshold = 54 }},
		{"OutwardStorage", func() { common.StorageDepthThreshold = -1 }},
		{"OutwardStorage", func() { common.StorageDepthThreshold = 30 }},
		{"ShardVP", func() { common.ShardOwnerPrefixLen = 3 }},
		{"RunPath", func() { common.RunPathTargetNodes = 0 }},
		{"ATileVP", func() { common.ATileBlocks = 48 }},
		{"ATileVP", func() { common.ATileStoragePathPrefixLen = 30 }},
	} {
		common.ModifyHashMethod, common.EpochSize = tc.method, 256
		common.DepthThreshold, common.StorageDepthThreshold = 3, 2
		common.ShardOwnerPrefixLen, common.RunPathTargetNodes = 1, 524288
		common.ATileBlocks, common.ATileStoragePathPrefixLen = 128, 1
		tc.change()
		if err := configureKeyScheme(); err == nil {
			t.Errorf("%s accepted invalid parameters", tc.method)
		}
	}
	common.ModifyHashMethod, common.DepthThreshold = "EpochPath", 3
	for _, size := range []uint64{1, 64, 128, 1 << 32} {
		common.EpochSize = size
		if err := configureKeyScheme(); err != nil {
			t.Fatalf("valid epoch %d rejected: %v", size, err)
		}
	}
}

func TestCaptureSimulationConfigUsesEffectiveKeyOptions(t *testing.T) {
	restore := saveMetadataTestGlobals()
	defer restore()

	common.ModifyHashMethod = "DepthEpoch"
	common.EpochSize = 65536
	common.DepthThreshold = 6
	common.StorageDepthThreshold = 2
	common.ShardOwnerPrefixLen = 2
	common.RunPathTargetNodes = 123456
	common.ATileBlocks = 128
	common.ATileStoragePathPrefixLen = 2
	common.VersionLength = 8
	common.EnableVersionPadding = false
	common.PathLength = 51
	common.FixedPathLength = true
	common.PathPaddingAtEnd = false
	common.AppendPathFirst = true
	common.LastPaddingBound = 7
	common.AppendPathLen = true
	common.LenOfPathLen = 3
	common.AppendTrieType = true
	common.AppendContractAddrHash = true
	common.AddrHashPrefixLen = 23

	want := keySchemeOptions{
		CodecVersion:              keySchemeCodecVersion,
		ModifyHashMethod:          "DepthEpoch",
		EpochSize:                 65536,
		DepthThreshold:            6,
		StorageDepthThreshold:     2,
		ShardOwnerPrefixLen:       2,
		RunPathTargetNodes:        123456,
		ATileBlocks:               128,
		ATileStoragePathPrefixLen: 2,
		VersionLength:             8,
		EnableVersionPadding:      false,
		PathLength:                51,
		FixedPathLength:           true,
		PathPaddingAtEnd:          false,
		AppendPathFirst:           true,
		LastPaddingBound:          7,
		AppendPathLen:             true,
		LenOfPathLen:              3,
		AppendTrieType:            true,
		AppendContractAddrHash:    true,
		AddrHashPrefixLen:         23,
	}
	if got := captureSimulationConfig().KeyScheme; !reflect.DeepEqual(got, want) {
		t.Fatalf("effective key options mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestSimulationResultsWriterAndLegacyDecode(t *testing.T) {
	restore := saveMetadataTestGlobals()
	defer restore()
	simBlocksPath = t.TempDir() + string(os.PathSeparator)
	common.ModifyHashMethod, common.EpochSize = "OutwardStorage", 128
	common.DepthThreshold, common.StorageDepthThreshold = 3, 2
	if err := configureKeyScheme(); err != nil {
		t.Fatal(err)
	}
	common.SimBlocks = map[string]*common.SimBlock{
		"00000000": {Number: 0, StateRoot: common.HexToHash("0x01")},
		"00000001": {Number: 1, StateRoot: common.HexToHash("0x02")},
	}
	if err := writeSimBlocksJSON("result.json", []string{"00000000", "00000001"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(simBlocksPath, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"config_sha256"`)) || bytes.Contains(data, []byte(`"key_layout"`)) || bytes.Contains(data, []byte(`"experiment"`)) || bytes.Contains(data, []byte(`"workload"`)) {
		t.Fatal("retired metadata was written")
	}
	got, legacy, err := decodeSimulationResultsJSON(data)
	if err != nil || legacy || !reflect.DeepEqual(got.Blocks, common.SimBlocks) || !reflect.DeepEqual(got.Config, captureSimulationConfig()) {
		t.Fatalf("written results did not round trip: legacy=%v err=%v", legacy, err)
	}
	for schema := oldestSimBlocksSchemaVersion; schema < simBlocksSchemaVersion; schema++ {
		// Key order does not matter. Retired fields remain readable without copying
		// the old schema definitions into the runtime.
		old := fmt.Sprintf(`{"config":{"key_scheme":{"modify_hash_method":"JMT_fixed","key_layout":"old-layout"},"measurement":{"experiment":{"scheme":"JMT_fixed","label":"archived"}},"workload":{"query_pool_size":1}},"config_sha256":"retired","schema_version":%d,"blocks":{"00000001":{"Number":1}}}`, schema)
		got, legacy, err := decodeSimulationResultsJSON([]byte(old))
		if err != nil || legacy || got.Blocks["00000001"].Number != 1 || got.Config.KeyScheme.ModifyHashMethod != "JMT_fixed" {
			t.Fatalf("schema %d rejected: %v", schema, err)
		}
	}
	got, legacy, err = decodeSimulationResultsJSON([]byte(`{"00000001":{"Number":1}}`))
	if err != nil || !legacy || got.Blocks["00000001"].Number != 1 {
		t.Fatalf("legacy map rejected: %v", err)
	}
	for _, invalid := range []string{`[]`, `{`, `{} extra`, `{"schema_version":13}`, `{"schema_version":999,"blocks":{}}`} {
		if _, _, err := decodeSimulationResultsJSON([]byte(invalid)); err == nil {
			t.Errorf("invalid results accepted: %s", invalid)
		}
	}
}

func TestKeySchemeFileLabels(t *testing.T) {
	restore := saveMetadataTestGlobals()
	defer restore()
	common.EpochSize, common.DepthThreshold, common.StorageDepthThreshold = 128, 3, 2
	common.ATileBlocks, common.ATileStoragePathPrefixLen = 128, 1
	for _, tc := range []struct{ method, want string }{
		{"JMT_fixed", "JMT_fixed"},
		{"EpochPath", "EpochPath-E128"},
		{"TPV", "TPV-E128-D3"},
		{"OutwardSplit", "OutwardSplit-E128-D3"},
		{"OutwardStorage", "OutwardStorage-E128-A3-S2B"},
		{"VPRight", "VPRight"},
		{"ATileVP", "ATileVP-B128-P1"},
	} {
		common.ModifyHashMethod = tc.method
		if got := keySchemeFileLabel(); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.method, got, tc.want)
		}
	}
	before := keySchemeFileLabel()
	common.ATileStoragePathPrefixLen++
	if before == keySchemeFileLabel() {
		t.Fatal("different storage path prefixes share a filename")
	}
}

func setMetadataTestKeyOptions(options keySchemeOptions) {
	common.ModifyHashMethod = options.ModifyHashMethod
	common.EpochSize = options.EpochSize
	common.DepthThreshold = options.DepthThreshold
	common.StorageDepthThreshold = options.StorageDepthThreshold
	common.ShardOwnerPrefixLen = options.ShardOwnerPrefixLen
	common.RunPathTargetNodes = options.RunPathTargetNodes
	common.ATileBlocks = options.ATileBlocks
	common.ATileStoragePathPrefixLen = options.ATileStoragePathPrefixLen
	common.VersionLength = options.VersionLength
	common.EnableVersionPadding = options.EnableVersionPadding
	common.PathLength = options.PathLength
	common.FixedPathLength = options.FixedPathLength
	common.PathPaddingAtEnd = options.PathPaddingAtEnd
	common.AppendPathFirst = options.AppendPathFirst
	common.LastPaddingBound = options.LastPaddingBound
	common.AppendPathLen = options.AppendPathLen
	common.LenOfPathLen = options.LenOfPathLen
	common.AppendTrieType = options.AppendTrieType
	common.AppendContractAddrHash = options.AppendContractAddrHash
	common.AddrHashPrefixLen = options.AddrHashPrefixLen
}

func saveMetadataTestGlobals() func() {
	options := currentKeySchemeOptions()
	oldPath, oldBlocks := simBlocksPath, common.SimBlocks
	oldArchive, oldPathScheme := common.IsArchiveMode, common.IsPathScheme
	return func() {
		setMetadataTestKeyOptions(options)
		simBlocksPath, common.SimBlocks = oldPath, oldBlocks
		common.IsArchiveMode, common.IsPathScheme = oldArchive, oldPathScheme
	}
}
