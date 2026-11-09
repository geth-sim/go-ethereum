package common

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/bits"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ReplayConfig is local to one research database, never a process-wide switch.
// Freeze it before opening the chain. Version is advanced only by chain insertion.
// Custom encoders must give Codec a unique, versioned ID including all parameters.
// They are called concurrently, and must be deterministic and concurrency-safe.
type ReplayConfig struct {
	runID                atomic.Uint64
	runPending           uint64
	KeyScheme            string
	Authentication       string // none, keccak, ristretto255, p256, gls254
	AuthMode             string // full or incremental
	AuthWorkers          int
	AuthCacheMiB         int
	AuthCacheMode        string
	Measure              bool // detailed counters; performance runs leave this disabled
	StateOnly            bool // chain records stay in bounded memory; state data reaches disk
	Codec                string
	EpochSize            uint64
	ShardPrefix          int
	TileBlocks           uint64
	TilePathPrefix       int
	RunTargetNodes       uint64
	StateDepth           int
	StorageDepth         int // -1 leaves every storage node in the version stream
	SkipIntermediateRoot bool
	SkipBlockRootChecks  bool                                // skip tx/uncle/receipt commitments, never state/auth calculation
	EncodeKey            func(TrieNodeContext) (Hash, error) `json:"-"`
	version              atomic.Uint64
	runtimeOnce          sync.Once
	runtimeValue         any
	runtimeErr           error
	counters             ReplayCounters
}

// TrieNodeContext describes the location BEFORE a short node's own key fragment.
// Owner is the full account-address hash for storage, zero for the account trie.
type TrieNodeContext struct {
	Version  uint64
	Owner    Hash
	Path     []byte // nibbles, borrowed for this call only
	Depth    int    // structural hops, used only by legacy DepthSplit/DepthEpoch
	LeafPath []byte // complete account key nibbles for ForestVP
	Digest   Hash   // original digest for hybrid legacy layouts
	Branch   bool
}

// CanonicalHashing leaves Ethereum commitments and proofs unchanged.
func (c *ReplayConfig) CanonicalHashing() bool { return c.KeyScheme == "H" || c.KeyScheme == "PBSS" }

func (c *ReplayConfig) Version() uint64     { return c.version.Load() }
func (c *ReplayConfig) SetVersion(v uint64) { c.version.Store(v) }

func (c *ReplayConfig) Validate() error {
	if c.AuthEnabled() {
		if c.CanonicalHashing() {
			return fmt.Errorf("H/PBSS use canonical Keccak; select a structured key for experimental auth")
		}
		switch c.Authentication {
		case "keccak", "ristretto255", "p256", "gls254":
		default:
			return fmt.Errorf("unknown authentication %q", c.Authentication)
		}
		if c.AuthMode != "full" && c.AuthMode != "incremental" {
			return fmt.Errorf("auth mode must be full or incremental")
		}
		if c.Authentication == "keccak" && c.AuthCacheMiB > 0 && c.AuthCacheMode != "unified" {
			return fmt.Errorf("Keccak commitment cache uses unified mode")
		}
		if c.Authentication == "keccak" && c.AuthMode != "full" {
			return fmt.Errorf("keccak requires full authentication")
		}
		if c.AuthWorkers < 1 || c.AuthWorkers > 16 {
			return fmt.Errorf("auth workers must be in [1,16]")
		}
		if c.AuthCacheMiB < 0 || c.AuthCacheMiB > 1<<20 {
			return fmt.Errorf("invalid auth cache budget")
		}
		switch c.AuthCacheMode {
		case "unified", "accumulator", "contribution":
		default:
			return fmt.Errorf("unknown auth cache mode")
		}
	}
	if c.Codec == "" {
		return fmt.Errorf("research codec ID is required")
	}
	switch c.KeyScheme {
	case "H", "PBSS", "VP", "PV", "JMT_fixed", "PrefixTree_fixed", "VPRight", "SplitPVHot", "DepthSplit", "ShardVP", "DualVP", "RunPath", "ForestVP", "ATileVP", "JMT", "PrefixTree", "JMT_balanced", "PrefixTree_balanced", "HalfPath", "TH":
	case "Outward", "OutwardSplit", "OutwardStorage", "EpochPath", "TPV", "DepthEpoch":
		if c.EpochSize == 0 || c.EpochSize > 1<<32 || c.EpochSize&(c.EpochSize-1) != 0 {
			return fmt.Errorf("epoch must be a power of two in [1, 2^32]")
		}
		if c.StateDepth < 0 || c.StateDepth > 53 || c.StorageDepth < -1 || c.StorageDepth > 29 {
			return fmt.Errorf("invalid Outward depth threshold")
		}
	default:
		if c.EncodeKey == nil {
			return fmt.Errorf("unknown key scheme %q", c.KeyScheme)
		}
	}
	if c.KeyScheme == "DepthEpoch" && bits.Len64(c.EpochSize-1)%4 != 0 {
		return fmt.Errorf("DepthEpoch requires an epoch power of 16")
	}
	if c.KeyScheme == "ShardVP" && c.ShardPrefix != 1 && c.ShardPrefix != 2 && c.ShardPrefix != 4 {
		return fmt.Errorf("shard prefix must be 1, 2 or 4 nibbles")
	}
	if c.KeyScheme == "RunPath" && c.RunTargetNodes == 0 {
		return fmt.Errorf("run target must be positive")
	}
	if c.KeyScheme == "ATileVP" && (c.TileBlocks == 0 || c.TileBlocks > 1<<32 || c.TileBlocks&(c.TileBlocks-1) != 0 || c.TilePathPrefix < 0 || c.TilePathPrefix > 29) {
		return fmt.Errorf("invalid tile options")
	}
	if !c.CanonicalHashing() && !c.SkipIntermediateRoot {
		return fmt.Errorf("structured keys require block-end hashing (skip intermediate root)")
	}
	return nil
}

// NodeKey preserves the original fixed 32-byte VP*, PV* and Outward layouts.
// The caller keeps upstream encoding/Keccak work, replacing only the locator.
// Oversized paths fail instead of silently truncating them into colliding keys.
func (c *ReplayConfig) NodeKey(n TrieNodeContext) (Hash, error) {
	if c.EncodeKey != nil {
		return c.EncodeKey(n)
	}
	if n.Version >= 1<<32 {
		return Hash{}, fmt.Errorf("version exceeds 32 bits")
	}
	if c.KeyUsesDigest() {
		return c.hybridKey(n)
	}
	storage := n.Owner != (Hash{})
	width := 53
	if storage {
		width = 29
	}
	if c.KeyScheme == "DepthSplit" || c.KeyScheme == "DepthEpoch" {
		width--
	}
	if c.KeyScheme == "RunPath" {
		width -= 8
	}
	if len(n.Path) > width {
		return Hash{}, fmt.Errorf("path depth %d exceeds codec capacity %d", len(n.Path), width)
	}
	for _, b := range n.Path {
		if b > 15 {
			return Hash{}, fmt.Errorf("non-nibble path")
		}
	}
	var key Hash
	pos := 0
	put := func(value uint64, width int) {
		for i := width - 1; i >= 0; i-- {
			key[pos/8] |= byte((value>>i)&1) << (7 - pos%8)
			pos++
		}
	}
	location := func() {
		if storage {
			put(15, 4)
			for _, b := range n.Owner[:12] {
				put(uint64(b), 8)
			}
		} else {
			put(13, 4)
		}
		for _, b := range n.Path {
			put(uint64(b), 4)
		}
		pos += (width - len(n.Path)) * 4
	}
	switch c.KeyScheme {
	case "VP", "JMT_fixed":
		put(n.Version, 32)
		location()
		put(uint64(len(n.Path)), 8)
	case "PV", "PrefixTree_fixed":
		location()
		put(n.Version, 32)
		put(uint64(len(n.Path)), 8)
	case "Outward", "OutwardSplit", "OutwardStorage":
		cold := !storage && len(n.Path) <= c.StateDepth || storage && c.KeyScheme != "OutwardSplit" && n.Branch && len(n.Path) <= c.StorageDepth
		if cold {
			w := bits.Len64(c.EpochSize - 1)
			put(0, 2)
			put(((uint64(1)<<(32-w))-1)-(n.Version>>w), 32-w)
			location()
			put(n.Version&(c.EpochSize-1), w)
		} else {
			put(2, 2)
			put(n.Version, 32)
			location()
		}
		put(uint64(len(n.Path)), 6)

	case "VPRight":
		put(2, 2)
		put(n.Version, 32)
		location()
		put(uint64(len(n.Path)), 6)
	case "EpochPath":
		w := bits.Len64(c.EpochSize - 1)
		put(n.Version>>w, 32-w)
		location()
		put(n.Version&(c.EpochSize-1), w)
		put(uint64(len(n.Path)), 8)
	case "TPV":
		w := bits.Len64(c.EpochSize - 1)
		upper := !storage && len(n.Path) <= c.StateDepth
		put(n.Version>>w, 32-w)
		if upper {
			put(0, 1)
		} else {
			put(1, 1)
			put(n.Version&(c.EpochSize-1), w)
		}
		location()
		if upper {
			put(n.Version&(c.EpochSize-1), w)
		}
		put(uint64(len(n.Path)), 7)
	case "SplitPVHot":
		cold := !storage && len(n.Path) <= c.StateDepth
		if cold {
			put(1, 1)
			location()
			put(n.Version, 32)
		} else {
			put(0, 1)
			put(n.Version, 32)
			location()
		}
		put(uint64(len(n.Path)), 7)
	case "DepthSplit", "DepthEpoch":
		shallow := n.Depth <= c.StateDepth
		w := 32
		if c.KeyScheme == "DepthEpoch" {
			w = bits.Len64(c.EpochSize - 1)
			put(n.Version>>w, 32-w)
		}
		band := uint64(1)
		if shallow {
			band = 0
		}
		put(band, 4)
		low := n.Version & ((uint64(1) << w) - 1)
		if shallow {
			put(low, w)
			location()
		} else {
			location()
			put(low, w)
		}
		put(uint64(len(n.Path)), 8)
	case "DualVP":
		section := uint64(13)
		if storage {
			section = 15
		}
		put(section, 4)
		put(n.Version, 32)
		if storage {
			for _, b := range n.Owner[:12] {
				put(uint64(b), 8)
			}
		}
		for _, b := range n.Path {
			put(uint64(b), 4)
		}
		pos += (width - len(n.Path)) * 4
		put(uint64(len(n.Path)), 8)
	case "ShardVP":
		if !storage {
			put(n.Version, 32)
			location()
		} else {
			shift := c.ShardPrefix * 4
			owner := uint64(n.Owner[0])<<8 | uint64(n.Owner[1])
			put(owner>>(16-shift), shift)
			put(n.Version>>shift, 32-shift)
			put(15, 4)
			put(n.Version&((1<<shift)-1), shift)
			for i := c.ShardPrefix; i < 24; i++ {
				put(uint64(n.Owner[i/2]>>uint(4*(1-i%2)))&15, 4)
			}
			for _, b := range n.Path {
				put(uint64(b), 4)
			}
			pos += (width - len(n.Path)) * 4
		}
		put(uint64(len(n.Path)), 8)
	case "RunPath":
		if c.runID.Load() >= 1<<32 {
			return Hash{}, fmt.Errorf("run ID overflow")
		}
		put(c.runID.Load(), 32)
		location()
		put(n.Version, 32)
		put(uint64(len(n.Path)), 8)
	case "ForestVP":
		put(n.Version, 32)
		section, length := uint64(13), len(n.Path)
		path := n.Path
		if storage {
			section = 15
			for _, b := range n.Owner[:12] {
				put(uint64(b), 8)
			}
		} else if len(n.LeafPath) > 0 {
			if len(n.LeafPath) != 64 {
				return Hash{}, fmt.Errorf("invalid account leaf key")
			}
			section = 14
			path = n.LeafPath[:24]
			length = 24
		}
		for _, b := range path {
			put(uint64(b), 4)
		}
		pos += (width - len(path)) * 4
		put(section, 4)
		put(uint64(length), 8)
	case "ATileVP":
		w := bits.Len64(c.TileBlocks - 1)
		put(n.Version>>w, 32-w)
		if !storage {
			put(13, 4)
			put(n.Version&(c.TileBlocks-1), w)
			for _, b := range n.Path {
				put(uint64(b), 4)
			}
			pos += (width - len(n.Path)) * 4
		} else {
			put(15, 4)
			for _, b := range n.Owner[:12] {
				put(uint64(b), 8)
			}
			for i := 0; i < c.TilePathPrefix; i++ {
				b := byte(0)
				if i < len(n.Path) {
					b = n.Path[i]
				}
				put(uint64(b), 4)
			}
			put(n.Version&(c.TileBlocks-1), w)
			for i := c.TilePathPrefix; i < width; i++ {
				b := byte(0)
				if i < len(n.Path) {
					b = n.Path[i]
				}
				put(uint64(b), 4)
			}
		}
		put(uint64(len(n.Path)), 8)
	default:
		return Hash{}, fmt.Errorf("no structured encoder for %q", c.KeyScheme)
	}
	if pos != 256 {
		return Hash{}, fmt.Errorf("codec emitted %d bits", pos)
	}
	return key, nil
}

// AuthRuntime is initialized once per experiment, shared by account and storage
// tries. The implementation lives in trie to keep common free of crypto cycles.
func (c *ReplayConfig) AuthRuntime(init func() (any, error)) (any, error) {
	c.runtimeOnce.Do(func() { c.runtimeValue, c.runtimeErr = init() })
	return c.runtimeValue, c.runtimeErr
}
func (c *ReplayConfig) AuthEnabled() bool {
	return c != nil && c.Authentication != "" && c.Authentication != "none"
}
func (c *ReplayConfig) Counters() *ReplayCounters { return &c.counters }

// Hash times are sums of per-trie/per-worker elapsed work. With parallelism
// they overlap; they must not be blindly subtracted from block wall time.
// Lookup times cover the complete request up to the result, including earlier
// cache probes. Bytes are returned encoded node bytes, not physical disk I/O.
type ReplayReadStats struct {
	Count, Bytes, LayerDepthSum uint64
	TimeNS                      int64
}
type replayReadCounters struct {
	count, bytes, depth atomic.Uint64
	nanos               atomic.Int64
}

func (r *replayReadCounters) take() ReplayReadStats {
	return ReplayReadStats{r.count.Swap(0), r.bytes.Swap(0), r.depth.Swap(0), r.nanos.Swap(0)}
}
func (m *ReplayCounters) RecordNodeRead(location string, start time.Time, size, depth int) {
	var r *replayReadCounters
	switch location {
	case "clean":
		m.CleanHits.Add(1)
		r = &m.cleanReads
	case "dirty":
		m.DirtyHits.Add(1)
		r = &m.dirtyReads
	case "disk":
		m.DiskHits.Add(1)
		r = &m.diskReads
	case "diff":
		r = &m.diffReads
	default:
		m.MissingNodes.Add(1)
		r = &m.missingReads
	}
	r.nanos.Add(int64(time.Since(start)))
	r.count.Add(1)
	r.bytes.Add(uint64(size))
	r.depth.Add(uint64(depth))
}

type ReplayCounters struct {
	cleanReads, dirtyReads, diskReads, diffReads, missingReads                                 replayReadCounters
	SnapshotAccountReads, SnapshotStorageReads, SnapshotCommits                                atomic.Int64
	SnapshotAccountLookups, SnapshotStorageLookups                                             atomic.Uint64
	WrittenNodes                                                                               atomic.Uint64
	AccountHashes, StorageHashes, ModifyHashes, AuthHashes, DiskCommits                        atomic.Int64
	AuthNodeReads, AuthBranchUpdates, AuthBranchBuilds, AuthParallelBatches, AuthParallelTasks atomic.Uint64
	CleanHits, DirtyHits, DiskHits, MissingNodes                                               atomic.Uint64
	ShortNodes, FullNodes, LeafNodes, CleanChildren, DirtyChildren, NilChildren                atomic.Uint64
	ModifiedChildren                                                                           [17]atomic.Uint64
}
type ReplayStats struct {
	NodeReads                                                                                  struct{ Clean, Dirty, Disk, Diff, Missing ReplayReadStats }
	SnapshotAccountReads, SnapshotStorageReads, SnapshotCommits                                int64
	SnapshotAccountLookups, SnapshotStorageLookups                                             uint64
	WrittenNodes                                                                               uint64
	AccountHashes, StorageHashes, ModifyHashes, AuthHashes, DiskCommits                        int64
	AuthNodeReads, AuthBranchUpdates, AuthBranchBuilds, AuthParallelBatches, AuthParallelTasks uint64
	CleanHits, DirtyHits, DiskHits, MissingNodes                                               uint64
	ShortNodes, FullNodes, LeafNodes, CleanChildren, DirtyChildren, NilChildren                uint64
	ModifiedChildren                                                                           [17]uint64
}

func (c *ReplayConfig) TakeStats() ReplayStats {
	m := c.Counters()
	out := ReplayStats{WrittenNodes: m.WrittenNodes.Swap(0),
		AccountHashes: m.AccountHashes.Swap(0), StorageHashes: m.StorageHashes.Swap(0), ModifyHashes: m.ModifyHashes.Swap(0), AuthHashes: m.AuthHashes.Swap(0), DiskCommits: m.DiskCommits.Swap(0),
		AuthNodeReads: m.AuthNodeReads.Swap(0), AuthBranchUpdates: m.AuthBranchUpdates.Swap(0), AuthBranchBuilds: m.AuthBranchBuilds.Swap(0), AuthParallelBatches: m.AuthParallelBatches.Swap(0), AuthParallelTasks: m.AuthParallelTasks.Swap(0),
		CleanHits: m.CleanHits.Swap(0), DirtyHits: m.DirtyHits.Swap(0), DiskHits: m.DiskHits.Swap(0), MissingNodes: m.MissingNodes.Swap(0),
		ShortNodes: m.ShortNodes.Swap(0), FullNodes: m.FullNodes.Swap(0), LeafNodes: m.LeafNodes.Swap(0), CleanChildren: m.CleanChildren.Swap(0), DirtyChildren: m.DirtyChildren.Swap(0), NilChildren: m.NilChildren.Swap(0)}
	out.NodeReads.Clean, out.NodeReads.Dirty, out.NodeReads.Disk = m.cleanReads.take(), m.dirtyReads.take(), m.diskReads.take()
	out.NodeReads.Diff, out.NodeReads.Missing = m.diffReads.take(), m.missingReads.take()
	out.SnapshotAccountReads, out.SnapshotStorageReads, out.SnapshotCommits = m.SnapshotAccountReads.Swap(0), m.SnapshotStorageReads.Swap(0), m.SnapshotCommits.Swap(0)
	out.SnapshotAccountLookups, out.SnapshotStorageLookups = m.SnapshotAccountLookups.Swap(0), m.SnapshotStorageLookups.Swap(0)
	for i := range out.ModifiedChildren {
		out.ModifiedChildren[i] = m.ModifiedChildren[i].Swap(0)
	}
	return out
}

// KeyUsesDigest identifies layouts that need the physical node RLP digest.
// A logical auth commitment cannot replace it: equal logical subtries can have
// different child locators. Custom callbacks also receive the original digest.
func (c *ReplayConfig) KeyUsesDigest() bool {
	if c.EncodeKey != nil {
		return true
	}
	switch c.KeyScheme {
	case "JMT", "PrefixTree", "JMT_balanced", "PrefixTree_balanced", "HalfPath", "TH":
		return true
	}
	return false
}

// Hybrid controls intentionally retain the old digest suffix and path truncation.
func (c *ReplayConfig) hybridKey(n TrieNodeContext) (Hash, error) {
	for _, b := range n.Path {
		if b > 15 {
			return Hash{}, fmt.Errorf("non-nibble path")
		}
	}
	versionWidth, pathWidth := 8, 46
	if strings.HasSuffix(c.KeyScheme, "_balanced") {
		versionWidth, pathWidth = 27, 27
	}
	if c.KeyScheme == "TH" {
		pathWidth = 0
	}
	if c.KeyScheme == "HalfPath" {
		versionWidth, pathWidth = 0, 24
	}
	path := ""
	for _, b := range n.Path[:min(len(n.Path), pathWidth)] {
		path += string("0123456789abcdef"[b])
	}
	path += strings.Repeat("0", pathWidth-len(path))
	version := ""
	if versionWidth > 0 {
		version = fmt.Sprintf("%0*x", versionWidth, n.Version)
	}
	prefix := version + path
	if strings.HasPrefix(c.KeyScheme, "PrefixTree") {
		prefix = path + version
	}
	if c.KeyScheme == "HalfPath" {
		section := "d"
		owner := ""
		if n.Owner != (Hash{}) {
			section = "f"
			owner = hex.EncodeToString(n.Owner[:12])
		} else if len(n.Path) > 5 {
			section = "e"
		}
		prefix = section + owner + path
	}
	digest := hex.EncodeToString(n.Digest[:])
	encoded := prefix + digest[len(prefix):]
	if c.KeyScheme == "HalfPath" {
		encoded = encoded[:62] + fmt.Sprintf("%02x", len(n.Path))
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		return Hash{}, err
	}
	return BytesToHash(raw), nil
}

type ReplayRun struct{ ID, Pending uint64 }

func (c *ReplayConfig) RunState() ReplayRun    { return ReplayRun{c.runID.Load(), c.runPending} }
func (c *ReplayConfig) RestoreRun(r ReplayRun) { c.runID.Store(r.ID); c.runPending = r.Pending }
func (c *ReplayConfig) AdvanceRun(nodes uint64) {
	if c.KeyScheme != "RunPath" {
		return
	}
	c.runPending += nodes
	c.runID.Add(c.runPending / c.RunTargetNodes)
	c.runPending %= c.RunTargetNodes
}

// PersistenceID excludes execution-only cache/worker/measurement settings, so
// the same state checkpoint can benchmark them without rebuilding from genesis.
func (c *ReplayConfig) PersistenceID() string {
	data, _ := json.Marshal(struct {
		Scheme, Codec, Authentication                         string
		Epoch, Tile, RunTarget                                uint64
		StateDepth, StorageDepth, ShardPrefix, TilePathPrefix int
		StateOnly, SkipIntermediateRoot                       bool
	}{c.KeyScheme, c.Codec, c.Authentication, c.EpochSize, c.TileBlocks, c.RunTargetNodes, c.StateDepth, c.StorageDepth, c.ShardPrefix, c.TilePathPrefix, c.StateOnly, c.SkipIntermediateRoot})
	return string(data)
}
