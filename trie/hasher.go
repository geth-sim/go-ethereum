// Copyright 2016 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package trie

import (
	"encoding/hex"
	"fmt"
	"math/bits"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"golang.org/x/crypto/sha3"
)

// for prefixing trie node hashes
var (
	CurrentBlockNum     = uint64(0)
	currentRunPathID    uint64
	runPathPendingNodes uint64
)

func SetCurrentBlockNum(blockNum uint64) {
	CurrentBlockNum = blockNum
	// for testing TH's performance when version num is rotating
	// CurrentBlockNum %= 65535
	// CurrentBlockNum %= 1048575
}

// ResetRunPath resets the logical write-run state before opening a new
// database. The simulator advances it only between blocks.
func ResetRunPath() {
	currentRunPathID = 0
	runPathPendingNodes = 0
}

// AdvanceRunPath accounts for one completed block. Keys for that block have
// already been constructed, so the next run ID becomes visible only to the
// following block and never changes while hashers run in parallel.
func AdvanceRunPath(nodes uint64) {
	if common.ModifyHashMethod != "RunPath" || nodes == 0 {
		return
	}
	if common.RunPathTargetNodes == 0 {
		fmt.Println("ERROR: RunPathTargetNodes must be nonzero")
		os.Exit(1)
	}
	runPathPendingNodes += nodes
	for runPathPendingNodes >= common.RunPathTargetNodes {
		runPathPendingNodes -= common.RunPathTargetNodes
		currentRunPathID++
	}
}

// hasher is a type used for the trie Hash operation. A hasher has some
// internal preallocated temp space
type hasher struct {
	sha          crypto.KeccakState
	tmp          []byte
	encbuf       rlp.EncoderBuffer
	parallel     bool          // Whether to use parallel threads when hashing
	modifyHashes time.Duration // sum of per-node elapsed durations; merged after parallel children finish
}

// hasherPool holds pureHashers
var hasherPool = sync.Pool{
	New: func() interface{} {
		return &hasher{
			tmp:    make([]byte, 0, 550), // cap is as large as a full fullNode.
			sha:    sha3.NewLegacyKeccak256().(crypto.KeccakState),
			encbuf: rlp.NewEncoderBuffer(nil),
		}
	},
}

func newHasher(parallel bool) *hasher {
	h := hasherPool.Get().(*hasher)
	h.parallel = parallel
	h.modifyHashes = 0
	if common.MeasureChildStats || common.MeasureReadStats {
		h.parallel = false // for measure MyHash stats correctly (jmlee)
	}
	return h
}

func addAdditionalNodeRead() {
	if common.MeasureReadStats {
		atomic.AddUint64(&common.AdditionalNodeReadFuncCnt, 1)
		return
	}
	common.AdditionalNodeReadFuncCnt++
}

func returnHasherToPool(h *hasher) {
	hasherPool.Put(h)
}

// hash collapses a node down into a hash node, also returning a copy of the
// original node initialized with the computed hash to replace the original one.
func (h *hasher) hash(n node, force bool, tnd common.TrieNodeData) (hashed node, cached node) {
	// Return the cached hash if it's available
	if hash, _ := n.cache(); hash != nil {
		return hash, n
	}
	// Trie not processed yet, walk the children
	switch n := n.(type) {
	case *shortNode:
		collapsed, cached := h.hashShortNodeChildren(n, tnd)
		hashed := h.shortnodeToHash(collapsed, force)
		// We need to retain the possibly _not_ hashed node, in case it was too
		// small to be hashed
		if hn, ok := hashed.(hashNode); ok {
			cached.flags.hash = hn

			if common.ReadAllChildNodes {
				// this node's hash was (re)computed and is representable as hashNode
				refreshAdditionalReadMarkerIfPresent(tnd.Path)
			}

			if common.PathLength+common.VersionLength > 0 {
				start := time.Now()
				modifiedHash := modifyHashV5(n, hn, CurrentBlockNum, tnd)
				cached.flags.hash = modifiedHash
				hashed = modifiedHash
				h.modifyHashes += time.Since(start)
			}

		} else {
			cached.flags.hash = nil
		}
		return hashed, cached
	case *fullNode:
		collapsed, cached := h.hashFullNodeChildren(n, tnd)
		hashed = h.fullnodeToHash(collapsed, force)
		if hn, ok := hashed.(hashNode); ok {
			cached.flags.hash = hn

			if common.ReadAllChildNodes {
				// this node's hash was (re)computed and is representable as hashNode
				refreshAdditionalReadMarkerIfPresent(tnd.Path)
			}

			if common.PathLength+common.VersionLength > 0 {
				start := time.Now()
				modifiedHash := modifyHashV5(n, hn, CurrentBlockNum, tnd)
				cached.flags.hash = modifiedHash
				hashed = modifiedHash
				h.modifyHashes += time.Since(start)
			}

		} else {
			cached.flags.hash = nil
		}
		return hashed, cached
	default:
		// Value and hash nodes don't have children, so they're left as were
		return n, n
	}
}

// (jmlee) modify nodeHash as I want
func modifyHashV5(n node, hash hashNode, blockNum uint64, tnd common.TrieNodeData) hashNode {
	// fmt.Println("\nin modifyHashV5()")
	// fmt.Println("  original path:", tnd.Path)
	// fmt.Println("  version:", blockNum)
	// fmt.Println("  original hash:", hash)

	if common.HashingStateTrie && common.HashingStorageTrie {
		fmt.Println("ERROR: HashingStateTrie and HashingStorageTrie could not be both true")
		fmt.Println("  current block number:", blockNum)
		os.Exit(1)
	}
	if !common.HashingStateTrie && !common.HashingStorageTrie && blockNum != 0 {
		fmt.Println("ERROR: HashingStateTrie and HashingStorageTrie could not be both false")
		fmt.Println("  current block number:", blockNum)
		os.Exit(1)
	}

	switch n.(type) {
	case *shortNode, *fullNode:
		if common.ModifyHashMethod == "OutwardStorage" {
			newHash, err := modifyOutwardStorageKey(blockNum, tnd, n)
			if err != nil {
				fmt.Println("ERROR: failed to construct OutwardStorage key:", err)
				os.Exit(1)
			}
			return newHash
		}
		if common.ModifyHashMethod == "ForestVP" {
			versionStr, err := structuredVersion(blockNum)
			if err != nil {
				fmt.Println("ERROR: failed to construct ForestVP version:", err)
				os.Exit(1)
			}
			newHash, err := modifyForestVPKey(versionStr, n, tnd)
			if err != nil {
				fmt.Println("ERROR: failed to construct ForestVP key:", err)
				os.Exit(1)
			}
			return newHash
		}
		if usesStructuredKeyScheme(common.ModifyHashMethod) {
			newHash, err := modifyStructuredKey(blockNum, tnd)
			if err != nil {
				fmt.Println("ERROR: failed to construct", common.ModifyHashMethod, "key:", err)
				os.Exit(1)
			}
			return newHash
		}

		//
		// Convert path to fixed-length hex string (each byte -> single hex digit)
		//

		// Adjust path length to match PathLength (Trim or Pad)
		path := tnd.Path
		pathLen := len(path)
		if len(path) > common.PathLength {
			path = path[:common.PathLength] // Keep leftmost PathLength elements
			// fmt.Println("  path trimmed to:", path)
		} else if len(path) < common.PathLength && common.FixedPathLength {
			missing := common.PathLength - len(path)
			padding := make([]byte, missing)
			if common.PathPaddingAtEnd {
				path = append(path, padding...) // Append padding at the end
			} else {
				path = append(padding, path...) // Prepend padding at the front
			}
			// fmt.Println("  path padded to:", path)
		}
		// Convert path bytes (0~15) to hex string using lookup table
		var indices = []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "a", "b", "c", "d", "e", "f"}
		pathStr := ""
		for _, b := range path {
			pathStr += indices[b] // Faster than fmt.Sprintf or Builder
		}
		// fmt.Println("  path prefix:", pathStr)

		//
		// Convert blockNum to fixed-length hex string
		//
		blockStr := ""
		if common.VersionLength > 0 {
			if common.EnableVersionPadding {
				blockStr = fmt.Sprintf("%0*x", common.VersionLength, blockNum)
			} else {
				blockStr = fmt.Sprintf("%x", blockNum)
			}
		}
		// fmt.Println("  block prefix:", blockStr)

		//
		// Merge pathStr and blockStr into a single string
		//
		var prefixStr string
		if common.AppendPathFirst {
			prefixStr = pathStr + blockStr
		} else {
			prefixStr = blockStr + pathStr
		}

		sectionStr := ""
		if common.AppendTrieType {
			if common.HashingStateTrie && !common.HashingStorageTrie {
				if common.ModifyHashMethod == "HalfPath" && pathLen > 5 {
					sectionStr = "e"
				} else {
					sectionStr = "d"
				}
			} else if !common.HashingStateTrie && common.HashingStorageTrie {
				sectionStr = "f"
			} else {
				fmt.Println("ERROR: HashingStateTrie and HashingStorageTrie could not be both true")
				fmt.Println("  HashingStateTrie:", common.HashingStateTrie)
				fmt.Println("  HashingStorageTrie:", common.HashingStorageTrie)
				fmt.Println("  current block number:", blockNum)
				os.Exit(1)
			}
		}
		// fmt.Println("  sectionStr:", sectionStr)

		addrHashStr := ""
		if common.AppendContractAddrHash {
			if !common.HashingStateTrie && common.HashingStorageTrie {
				addrHashHex := common.AddrHashOfCurrentStorageTrie.Hex()[2:]
				addrHashStr = addrHashHex[:common.AddrHashPrefixLen]
				// fmt.Println("  common.AddrHashPrefixLen:", common.AddrHashPrefixLen)
				// fmt.Println("  addrHashHex:", addrHashHex)
				// fmt.Println("  addrHashStr:", addrHashStr)

				// TODO(jmlee): improve this corner case handling
				if common.ModifyHashMethod == "PrefixTree_fixed" {
					prefixStr = strings.Replace(prefixStr, strings.Repeat("0", common.AddrHashPrefixLen), "", 1)
					// fmt.Println("  0-padding removed: remove ", common.AddrHashPrefixLen, "zeros")
				}
			}
		}
		// fmt.Println("  addrHashStr:", addrHashStr)
		if common.ModifyHashMethod != "JMT_fixed" {
			prefixStr = sectionStr + addrHashStr + prefixStr
		} else {
			pathStr = strings.Replace(pathStr, strings.Repeat("0", len(addrHashStr)), "", 1)
			prefixStr = blockStr + sectionStr + addrHashStr + pathStr
		}

		if len(prefixStr) < common.LastPaddingBound {
			prefixStr += strings.Repeat("0", common.LastPaddingBound-len(prefixStr))
		}
		// fmt.Println("  prefix str:", prefixStr)

		//
		// Overwrite the front part of newHashHex with prefixStr
		//
		newHashHex := prefixStr + hex.EncodeToString(hash)[len(prefixStr):]

		if common.AppendPathLen {
			pathLenHex := fmt.Sprintf("%0*x", common.LenOfPathLen, pathLen)
			newHashHex = newHashHex[:len(newHashHex)-common.LenOfPathLen] + pathLenHex
		}
		// fmt.Println("  modified hex hash:", newHashHex)
		if len(newHashHex) != 64 {
			fmt.Println("  ERROR: newHashHex len is not 64")
			fmt.Println("  len(newHashHex):", len(newHashHex))
			os.Exit(1)
		}

		// check new hash
		// fmt.Println("\n<<<<<< modify hash >>>>>>")
		// fmt.Println("  blockStr:", blockStr)
		// fmt.Println("  sectionStr:", sectionStr)
		// fmt.Println("  addrHashStr:", addrHashStr)
		// fmt.Println("  addrHashHex:", common.AddrHashOfCurrentStorageTrie.Hex())
		// fmt.Println("  pathStr:", pathStr, "-> len:", len(pathStr))
		// fmt.Println("  pathLen:", pathLen)
		// fmt.Println("  prefixStr:", prefixStr, "-> len:", len(prefixStr))
		// fmt.Println("\n  original hash:", hash)
		// fmt.Println("  newHashHex:", newHashHex)

		// Convert the modified hex string back to bytes efficiently
		newHash, err := hex.DecodeString(newHashHex)
		if err != nil {
			fmt.Println("  hex.Decode error:", err)
			return nil
		}

		// fmt.Println("  modified hash:", common.Bytes2Hex(newHash), "\n")
		return newHash
	default:
		return nil
	}
}

func usesStructuredKeyScheme(method string) bool {
	switch method {
	case "EpochPath", "TPV", "SplitPVHot", "OutwardSplit", "VPRight", "DepthSplit", "DepthEpoch", "ShardVP", "DualVP", "RunPath", "ATileVP":
		return true
	default:
		return false
	}
}

// modifyStructuredKey constructs a complete 32-byte database key without using
// any bits from the authenticated node hash. Authentication is intentionally out
// of scope for these experimental schemes, matching the existing PV*/VP* setup.
func modifyStructuredKey(blockNum uint64, tnd common.TrieNodeData) (hashNode, error) {
	if common.VersionLength != 8 {
		return nil, fmt.Errorf("VersionLength must be 8, got %d", common.VersionLength)
	}
	if common.LenOfPathLen <= 0 {
		return nil, fmt.Errorf("LenOfPathLen must be positive, got %d", common.LenOfPathLen)
	}
	if common.ModifyHashMethod == "EpochPath" {
		return modifyEpochPathKey(blockNum, tnd)
	}
	if common.ModifyHashMethod == "SplitPVHot" {
		return modifySplitPVHotKey(blockNum, tnd)
	}
	if common.ModifyHashMethod == "OutwardSplit" {
		return modifyOutwardSplitKey(blockNum, tnd)
	}
	if common.ModifyHashMethod == "VPRight" {
		return modifyVPRightKey(blockNum, tnd)
	}
	if common.ModifyHashMethod == "TPV" {
		return modifyTPVKey(blockNum, tnd)
	}
	if common.ModifyHashMethod == "ATileVP" {
		return modifyATileVPKey(blockNum, tnd)
	}

	versionStr, err := structuredVersion(blockNum)
	if err != nil {
		return nil, err
	}
	if common.ModifyHashMethod == "ShardVP" {
		return modifyShardVPKey(versionStr, tnd)
	}
	if common.ModifyHashMethod == "DualVP" {
		return modifyDualVPKey(versionStr, tnd)
	}
	if common.ModifyHashMethod == "RunPath" {
		return modifyRunPathKey(versionStr, tnd)
	}

	trieID, err := structuredTrieID()
	if err != nil {
		return nil, err
	}

	bandStr := ""
	if common.ModifyHashMethod == "DepthSplit" || common.ModifyHashMethod == "DepthEpoch" {
		if tnd.Depth <= common.DepthThreshold {
			bandStr = "0"
		} else {
			bandStr = "1"
		}
	}

	pathWidth := 64 - common.VersionLength - len(trieID) - len(bandStr) - common.LenOfPathLen
	pathStr, pathLenStr, err := structuredPath(tnd.Path, pathWidth)
	if err != nil {
		return nil, err
	}

	var keyStr string
	switch common.ModifyHashMethod {
	case "DepthSplit":
		if bandStr == "0" {
			keyStr = bandStr + versionStr + trieID + pathStr + pathLenStr
		} else {
			keyStr = bandStr + trieID + pathStr + versionStr + pathLenStr
		}
	case "DepthEpoch":
		epochStr, offsetStr, err := splitVersionByEpoch(versionStr)
		if err != nil {
			return nil, err
		}
		if bandStr == "0" {
			keyStr = epochStr + bandStr + offsetStr + trieID + pathStr + pathLenStr
		} else {
			keyStr = epochStr + bandStr + trieID + pathStr + offsetStr + pathLenStr
		}
	default:
		return nil, fmt.Errorf("unsupported structured key scheme %q", common.ModifyHashMethod)
	}

	if len(keyStr) != 64 {
		return nil, fmt.Errorf("constructed key has %d hex digits, want 64", len(keyStr))
	}
	key, err := hex.DecodeString(keyStr)
	if err != nil {
		return nil, fmt.Errorf("decode constructed key: %w", err)
	}
	return key, nil
}

// bitKeyBuilder packs fields from most-significant to least-significant bit.
// ATileVP uses a six-bit within-tile version, so a nibble-only string encoder
// would either waste two bits or change the intended field order.
type bitKeyBuilder struct {
	key    [common.HashLength]byte
	bitPos int
}

func (builder *bitKeyBuilder) appendUint(value uint64, width int) error {
	if width < 0 || width > 64 {
		return fmt.Errorf("invalid bit-field width %d", width)
	}
	if width == 0 {
		if value != 0 {
			return fmt.Errorf("value %d does not fit in zero bits", value)
		}
		return nil
	}
	if width < 64 && value >= uint64(1)<<width {
		return fmt.Errorf("value %d does not fit in %d bits", value, width)
	}
	if builder.bitPos+width > common.HashLength*8 {
		return fmt.Errorf("key fields exceed %d bits", common.HashLength*8)
	}
	remaining := width
	for remaining > 0 {
		byteOffset := builder.bitPos / 8
		used := builder.bitPos % 8
		available := 8 - used
		take := remaining
		if take > available {
			take = available
		}
		shift := remaining - take
		mask := uint64(1<<take) - 1
		chunk := byte((value >> shift) & mask)
		builder.key[byteOffset] |= chunk << (available - take)
		builder.bitPos += take
		remaining -= take
	}
	return nil
}

func (builder *bitKeyBuilder) appendNibbles(path []byte, width int) error {
	if len(path) > width {
		return fmt.Errorf("path has %d nibbles, exceeds field width %d", len(path), width)
	}
	for _, nibble := range path {
		if nibble > 0xf {
			return fmt.Errorf("path contains non-nibble value %d", nibble)
		}
		if err := builder.appendUint(uint64(nibble), 4); err != nil {
			return err
		}
	}
	paddingBits := (width - len(path)) * 4
	if builder.bitPos+paddingBits > common.HashLength*8 {
		return fmt.Errorf("key fields exceed %d bits", common.HashLength*8)
	}
	builder.bitPos += paddingBits
	return nil
}

func (builder *bitKeyBuilder) finish() (hashNode, error) {
	if builder.bitPos != common.HashLength*8 {
		return nil, fmt.Errorf("constructed key has %d bits, want %d", builder.bitPos, common.HashLength*8)
	}
	key := make(hashNode, common.HashLength)
	copy(key, builder.key[:])
	return key, nil
}

// modifyATileVPKey retains a coarse global version prefix, but optimizes the
// remainder independently for state and storage tries. With ATileBlocks=64:
//
// State:   versionHigh[26] | d[4] | versionLow[6] | path[212] | pathLen[8]
// Storage: versionHigh[26] | f[4] | owner[96] | pathPrefix[4] |
//
//	versionLow[6] | pathRest[112] | pathLen[8]
//
// The common versionHigh keeps newly written state and storage in the current
// LSM range. State retains VP-like chronological ordering within each tile,
// while storage groups repeated versions of the same owner/path closely.
func modifyATileVPKey(blockNum uint64, tnd common.TrieNodeData) (hashNode, error) {
	if common.VersionLength != 8 {
		return nil, fmt.Errorf("ATileVP requires VersionLength=8, got %d", common.VersionLength)
	}
	if common.LenOfPathLen != 2 {
		return nil, fmt.Errorf("ATileVP requires LenOfPathLen=2, got %d", common.LenOfPathLen)
	}
	if common.AddrHashPrefixLen != 24 {
		return nil, fmt.Errorf("ATileVP requires AddrHashPrefixLen=24, got %d", common.AddrHashPrefixLen)
	}
	if common.ATileBlocks == 0 || common.ATileBlocks > uint64(1)<<32 || common.ATileBlocks&(common.ATileBlocks-1) != 0 {
		return nil, fmt.Errorf("ATileBlocks must be a power of two in [1, 2^32], got %d", common.ATileBlocks)
	}
	if blockNum >= uint64(1)<<32 {
		return nil, fmt.Errorf("block number %d does not fit in 32 bits", blockNum)
	}
	offsetBits := bits.Len64(common.ATileBlocks - 1)
	highBits := 32 - offsetBits
	versionHigh := blockNum >> offsetBits
	versionLow := blockNum & (common.ATileBlocks - 1)

	var builder bitKeyBuilder
	if err := builder.appendUint(versionHigh, highBits); err != nil {
		return nil, err
	}
	switch {
	case common.HashingStateTrie && !common.HashingStorageTrie:
		if len(tnd.Path) > 0xff {
			return nil, fmt.Errorf("state path length %d does not fit in 8 bits", len(tnd.Path))
		}
		if err := builder.appendUint(0xd, 4); err != nil {
			return nil, err
		}
		if err := builder.appendUint(versionLow, offsetBits); err != nil {
			return nil, err
		}
		if err := builder.appendNibbles(tnd.Path, 53); err != nil {
			return nil, err
		}
		if err := builder.appendUint(uint64(len(tnd.Path)), 8); err != nil {
			return nil, err
		}

	case !common.HashingStateTrie && common.HashingStorageTrie:
		const storagePathWidth = 29
		prefixLen := common.ATileStoragePathPrefixLen
		if prefixLen < 0 || prefixLen > storagePathWidth {
			return nil, fmt.Errorf("ATileStoragePathPrefixLen must be in [0, %d], got %d", storagePathWidth, prefixLen)
		}
		if len(tnd.Path) > storagePathWidth {
			return nil, fmt.Errorf("storage path has %d nibbles, exceeds field width %d", len(tnd.Path), storagePathWidth)
		}
		if err := builder.appendUint(0xf, 4); err != nil {
			return nil, err
		}
		owner := common.AddrHashOfCurrentStorageTrie
		for _, ownerByte := range owner[:common.AddrHashPrefixLen/2] {
			if err := builder.appendUint(uint64(ownerByte), 8); err != nil {
				return nil, err
			}
		}
		pathPrefixEnd := prefixLen
		if pathPrefixEnd > len(tnd.Path) {
			pathPrefixEnd = len(tnd.Path)
		}
		if err := builder.appendNibbles(tnd.Path[:pathPrefixEnd], prefixLen); err != nil {
			return nil, err
		}
		if err := builder.appendUint(versionLow, offsetBits); err != nil {
			return nil, err
		}
		if err := builder.appendNibbles(tnd.Path[pathPrefixEnd:], storagePathWidth-prefixLen); err != nil {
			return nil, err
		}
		if err := builder.appendUint(uint64(len(tnd.Path)), 8); err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("exactly one of HashingStateTrie and HashingStorageTrie must be true")
	}
	return builder.finish()
}

func structuredVersion(blockNum uint64) (string, error) {
	versionStr := fmt.Sprintf("%0*x", common.VersionLength, blockNum)
	if len(versionStr) != common.VersionLength {
		return "", fmt.Errorf("block number %d does not fit in %d hex digits", blockNum, common.VersionLength)
	}
	return versionStr, nil
}

// modifyRunPathKey groups several blocks into a logical write run sized by
// persisted trie-node volume. The monotonically increasing run remains the
// outermost field; within a run, trie/path locality and the exact block version
// determine the key.
//
// State:   run[8] | d | path[45] | version[8] | pathLen[2]
// Storage: run[8] | f | owner[24] | path[21] | version[8] | pathLen[2]
func modifyRunPathKey(versionStr string, tnd common.TrieNodeData) (hashNode, error) {
	if common.RunPathTargetNodes == 0 {
		return nil, fmt.Errorf("RunPathTargetNodes must be nonzero")
	}
	runStr := fmt.Sprintf("%08x", currentRunPathID)
	if len(runStr) != 8 {
		return nil, fmt.Errorf("RunPath run ID %d does not fit in 8 hex digits", currentRunPathID)
	}
	trieID, err := structuredTrieID()
	if err != nil {
		return nil, err
	}
	pathWidth := 64 - len(runStr) - len(trieID) - len(versionStr) - common.LenOfPathLen
	pathStr, pathLenStr, err := structuredPath(tnd.Path, pathWidth)
	if err != nil {
		return nil, err
	}
	keyStr := runStr + trieID + pathStr + versionStr + pathLenStr
	if len(keyStr) != 64 {
		return nil, fmt.Errorf("constructed RunPath key has %d hex digits, want 64", len(keyStr))
	}
	key, err := hex.DecodeString(keyStr)
	if err != nil {
		return nil, fmt.Errorf("decode constructed RunPath key: %w", err)
	}
	return key, nil
}

// modifyForestVPKey treats all storage tries as subtrees nested beneath their
// owning account in one global path namespace, while retaining version as the
// outermost field. State account leaves and their storage roots consequently
// have adjacent keys.
//
// State internal: version[8] | statePath[53]          | d | pathLen[2]
// State leaf:     version[8] | owner[24] | zero[29]   | e | 18
// Storage:        version[8] | owner[24] | path[29]   | f | pathLen[2]
func modifyForestVPKey(versionStr string, n node, tnd common.TrieNodeData) (hashNode, error) {
	var globalPath, pathLenStr, trieType string
	switch {
	case common.HashingStateTrie && !common.HashingStorageTrie:
		if short, ok := n.(*shortNode); ok {
			if _, leaf := short.Val.(valueNode); leaf {
				fullKey := make([]byte, 0, len(tnd.Path)+len(short.Key))
				fullKey = append(fullKey, tnd.Path...)
				fullKey = append(fullKey, short.Key...)
				if !hasTerm(fullKey) {
					return nil, fmt.Errorf("state leaf key has no terminator")
				}
				fullKey = fullKey[:len(fullKey)-1]
				if len(fullKey) < common.AddrHashPrefixLen {
					return nil, fmt.Errorf("state leaf path has %d nibbles, need owner anchor %d", len(fullKey), common.AddrHashPrefixLen)
				}
				anchor, _, err := structuredPath(fullKey[:common.AddrHashPrefixLen], common.AddrHashPrefixLen)
				if err != nil {
					return nil, err
				}
				globalPath = anchor + strings.Repeat("0", 53-common.AddrHashPrefixLen)
				pathLenStr = fmt.Sprintf("%0*x", common.LenOfPathLen, common.AddrHashPrefixLen)
				trieType = "e"
				break
			}
		}
		var err error
		globalPath, pathLenStr, err = structuredPath(tnd.Path, 53)
		if err != nil {
			return nil, err
		}
		trieType = "d"

	case !common.HashingStateTrie && common.HashingStorageTrie:
		if common.AddrHashPrefixLen != 24 {
			return nil, fmt.Errorf("ForestVP requires AddrHashPrefixLen=24, got %d", common.AddrHashPrefixLen)
		}
		owner := common.AddrHashOfCurrentStorageTrie.Hex()[2:]
		pathStr, length, err := structuredPath(tnd.Path, 29)
		if err != nil {
			return nil, err
		}
		globalPath = owner[:common.AddrHashPrefixLen] + pathStr
		pathLenStr = length
		trieType = "f"

	default:
		return nil, fmt.Errorf("exactly one of HashingStateTrie and HashingStorageTrie must be true")
	}
	keyStr := versionStr + globalPath + trieType + pathLenStr
	if len(keyStr) != 64 {
		return nil, fmt.Errorf("constructed ForestVP key has %d hex digits, want 64", len(keyStr))
	}
	key, err := hex.DecodeString(keyStr)
	if err != nil {
		return nil, fmt.Errorf("decode constructed ForestVP key: %w", err)
	}
	return key, nil
}

// modifyDualVPKey creates two type-partitioned version streams in the same
// database. This is a single-key field permutation, not a separate database,
// cache, or storage tier.
//
// State:   d | version[8] | path[53] | pathLen[2]
// Storage: f | version[8] | owner[24] | path[29] | pathLen[2]
func modifyDualVPKey(versionStr string, tnd common.TrieNodeData) (hashNode, error) {
	trieID, err := structuredTrieID()
	if err != nil {
		return nil, err
	}
	pathWidth := 64 - len(trieID) - len(versionStr) - common.LenOfPathLen
	pathStr, pathLenStr, err := structuredPath(tnd.Path, pathWidth)
	if err != nil {
		return nil, err
	}
	keyStr := trieID[:1] + versionStr + trieID[1:] + pathStr + pathLenStr
	if len(keyStr) != 64 {
		return nil, fmt.Errorf("constructed DualVP key has %d hex digits, want 64", len(keyStr))
	}
	key, err := hex.DecodeString(keyStr)
	if err != nil {
		return nil, fmt.Errorf("decode constructed DualVP key: %w", err)
	}
	return key, nil
}

// modifyShardVPKey keeps state-trie keys identical to VP* and moves a prefix
// of the storage-trie owner hash ahead of the version. The trie-type nibble
// remains at offset 8 in both layouts, preserving the existing collision-free
// state/storage namespace.
//
// State:   version[8] | d | path[53] | pathLen[2]
// Storage: owner[:s] | version[:8-s] | f | version[8-s:] |
//
//	owner[s:24] | path[29] | pathLen[2]
func modifyShardVPKey(versionStr string, tnd common.TrieNodeData) (hashNode, error) {
	if err := validateShardOwnerPrefixLen(common.ShardOwnerPrefixLen); err != nil {
		return nil, err
	}

	var keyStr string
	switch {
	case common.HashingStateTrie && !common.HashingStorageTrie:
		pathStr, pathLenStr, err := structuredPath(tnd.Path, 64-common.VersionLength-1-common.LenOfPathLen)
		if err != nil {
			return nil, err
		}
		keyStr = versionStr + "d" + pathStr + pathLenStr

	case !common.HashingStateTrie && common.HashingStorageTrie:
		addrHashHex := common.AddrHashOfCurrentStorageTrie.Hex()[2:]
		if common.AddrHashPrefixLen != 24 {
			return nil, fmt.Errorf("ShardVP requires AddrHashPrefixLen=24, got %d", common.AddrHashPrefixLen)
		}
		if common.AddrHashPrefixLen > len(addrHashHex) {
			return nil, fmt.Errorf("AddrHashPrefixLen %d exceeds owner hash length %d", common.AddrHashPrefixLen, len(addrHashHex))
		}
		ownerStr := addrHashHex[:common.AddrHashPrefixLen]
		s := common.ShardOwnerPrefixLen
		versionSplit := common.VersionLength - s
		pathWidth := 64 - common.VersionLength - 1 - len(ownerStr) - common.LenOfPathLen
		pathStr, pathLenStr, err := structuredPath(tnd.Path, pathWidth)
		if err != nil {
			return nil, err
		}
		keyStr = ownerStr[:s] + versionStr[:versionSplit] + "f" + versionStr[versionSplit:] + ownerStr[s:] + pathStr + pathLenStr

	default:
		return nil, fmt.Errorf("exactly one of HashingStateTrie and HashingStorageTrie must be true")
	}
	if len(keyStr) != 64 {
		return nil, fmt.Errorf("constructed ShardVP key has %d hex digits, want 64", len(keyStr))
	}
	key, err := hex.DecodeString(keyStr)
	if err != nil {
		return nil, fmt.Errorf("decode constructed ShardVP key: %w", err)
	}
	return key, nil
}

func validateShardOwnerPrefixLen(prefixLen int) error {
	switch prefixLen {
	case 1, 2, 4:
		return nil
	default:
		return fmt.Errorf("ShardOwnerPrefixLen must be one of 1, 2, or 4 hex nibbles, got %d", prefixLen)
	}
}

func structuredTrieID() (string, error) {
	switch {
	case common.HashingStateTrie && !common.HashingStorageTrie:
		return "d", nil
	case !common.HashingStateTrie && common.HashingStorageTrie:
		addrHashHex := common.AddrHashOfCurrentStorageTrie.Hex()[2:]
		if common.AddrHashPrefixLen < 0 || common.AddrHashPrefixLen > len(addrHashHex) {
			return "", fmt.Errorf("AddrHashPrefixLen must be in [0, %d], got %d", len(addrHashHex), common.AddrHashPrefixLen)
		}
		return "f" + addrHashHex[:common.AddrHashPrefixLen], nil
	default:
		return "", fmt.Errorf("exactly one of HashingStateTrie and HashingStorageTrie must be true")
	}
}

func structuredPath(path []byte, width int) (string, string, error) {
	if width < 0 {
		return "", "", fmt.Errorf("negative path width %d", width)
	}
	if len(path) > width {
		return "", "", fmt.Errorf("path length %d exceeds key capacity %d", len(path), width)
	}

	const digits = "0123456789abcdef"
	var pathBuilder strings.Builder
	pathBuilder.Grow(width)
	for _, nibble := range path {
		if nibble > 0x0f {
			return "", "", fmt.Errorf("path contains non-nibble value %d", nibble)
		}
		pathBuilder.WriteByte(digits[nibble])
	}
	pathBuilder.WriteString(strings.Repeat("0", width-len(path)))

	pathLenStr := fmt.Sprintf("%0*x", common.LenOfPathLen, len(path))
	if len(pathLenStr) != common.LenOfPathLen {
		return "", "", fmt.Errorf("path length %d does not fit in %d hex digits", len(path), common.LenOfPathLen)
	}
	return pathBuilder.String(), pathLenStr, nil
}

func splitVersionByEpoch(versionStr string) (string, string, error) {
	offsetWidth, err := epochOffsetWidth(common.EpochSize)
	if err != nil {
		return "", "", err
	}
	if offsetWidth > len(versionStr) {
		return "", "", fmt.Errorf("EpochSize %d exceeds the %d-hex-digit version space", common.EpochSize, len(versionStr))
	}
	epochWidth := len(versionStr) - offsetWidth
	return versionStr[:epochWidth], versionStr[epochWidth:], nil
}

func epochOffsetWidth(epochSize uint64) (int, error) {
	if epochSize == 0 {
		return 0, fmt.Errorf("EpochSize must be nonzero")
	}
	width := 0
	for epochSize > 1 {
		if epochSize%16 != 0 {
			return 0, fmt.Errorf("EpochSize must be a power of 16, got %d", common.EpochSize)
		}
		epochSize /= 16
		width++
	}
	return width, nil
}

// modifyEpochPathKey is experiment B (EP). Both trie sides use exactly the
// same order: epoch | section | [owner] | padded path | offset | path length.
// The returned 32 bytes are used as both the child reference and database key.
// For nibble-aligned epochs this preserves the original EpochPath encoding.
func modifyEpochPathKey(version uint64, tnd common.TrieNodeData) (hashNode, error) {
	if common.VersionLength != 8 || common.LenOfPathLen != 2 || common.AddrHashPrefixLen != 24 {
		return nil, fmt.Errorf("EpochPath requires VersionLength=8, LenOfPathLen=2 and AddrHashPrefixLen=24")
	}
	width, err := common.EpochOffsetBits(common.EpochSize)
	if err != nil {
		return nil, err
	}
	if version >= uint64(1)<<32 {
		return nil, fmt.Errorf("block number %d does not fit in 32 bits", version)
	}
	var b bitKeyBuilder
	if err := b.appendUint(version>>width, 32-width); err != nil {
		return nil, err
	}
	pathWidth := 53
	switch {
	case common.HashingStateTrie && !common.HashingStorageTrie:
		if err := b.appendUint(0xd, 4); err != nil {
			return nil, err
		}
	case !common.HashingStateTrie && common.HashingStorageTrie:
		pathWidth = 29
		if err := b.appendUint(0xf, 4); err != nil {
			return nil, err
		}
		owner := common.AddrHashOfCurrentStorageTrie
		for _, value := range owner[:12] {
			if err := b.appendUint(uint64(value), 8); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("exactly one of HashingStateTrie and HashingStorageTrie must be true")
	}
	if err := b.appendNibbles(tnd.Path, pathWidth); err != nil {
		return nil, err
	}
	if err := b.appendUint(version&(common.EpochSize-1), width); err != nil {
		return nil, err
	}
	if err := b.appendUint(uint64(len(tnd.Path)), 8); err != nil {
		return nil, err
	}
	return b.finish()
}

// modifyTPVKey implements experiment T with identical 32-byte child references
// and database keys. G=0 is state path-depth <= cutoff; all other nodes use G=1.
// Upper: E | G=0 | section | path | u | length.
// Body:  E | G=1 | u | section | [owner] | path | length.
// One bit is moved from the existing 8-bit length suffix to G. The existing
// path and owner capacities are unchanged. Depth is nibble path length, not
// traversal hops (TrieNodeData.Depth). This scheme does not include C4.
func modifyTPVKey(version uint64, tnd common.TrieNodeData) (hashNode, error) {
	if common.VersionLength != 8 || common.LenOfPathLen != 2 || common.AddrHashPrefixLen != 24 {
		return nil, fmt.Errorf("TPV requires VersionLength=8, LenOfPathLen=2 and AddrHashPrefixLen=24")
	}
	width, err := common.EpochOffsetBits(common.EpochSize)
	if err != nil {
		return nil, err
	}
	if common.DepthThreshold < 0 || common.DepthThreshold > 53 {
		return nil, fmt.Errorf("TPV DepthThreshold must be in [0, 53], got %d", common.DepthThreshold)
	}
	if version >= uint64(1)<<32 {
		return nil, fmt.Errorf("block number %d does not fit in 32 bits", version)
	}
	if common.HashingStateTrie == common.HashingStorageTrie {
		return nil, fmt.Errorf("exactly one of HashingStateTrie and HashingStorageTrie must be true")
	}
	upper := common.HashingStateTrie && int64(len(tnd.Path)) <= common.DepthThreshold
	group := uint64(1)
	if upper {
		group = 0
	}
	var b bitKeyBuilder
	if err := b.appendUint(version>>width, 32-width); err != nil {
		return nil, err
	}
	if err := b.appendUint(group, 1); err != nil {
		return nil, err
	}
	if !upper {
		if err := b.appendUint(version&(common.EpochSize-1), width); err != nil {
			return nil, err
		}
	}
	pathWidth, section := 53, uint64(0xd)
	if common.HashingStorageTrie {
		pathWidth, section = 29, 0xf
	}
	if err := b.appendUint(section, 4); err != nil {
		return nil, err
	}
	if common.HashingStorageTrie {
		for _, value := range common.AddrHashOfCurrentStorageTrie[:12] {
			if err := b.appendUint(uint64(value), 8); err != nil {
				return nil, err
			}
		}
	}
	if err := b.appendNibbles(tnd.Path, pathWidth); err != nil {
		return nil, err
	}
	if upper {
		if err := b.appendUint(version&(common.EpochSize-1), width); err != nil {
			return nil, err
		}
	}
	if err := b.appendUint(uint64(len(tnd.Path)), 7); err != nil {
		return nil, err
	}
	return b.finish()
}

// modifySplitPVHotKey partitions one keyspace globally, without epochs.
// State path depths <= DepthThreshold use the trailing PV region (group 1).
// All other state nodes and ALL storage nodes use the leading VP region (0).
// As in TPV, the class bit comes from the length suffix; owner/path capacities
// and the full 32-bit birth version are preserved. Child ID equals DB key.
func modifySplitPVHotKey(version uint64, tnd common.TrieNodeData) (hashNode, error) {
	if common.VersionLength != 8 || common.LenOfPathLen != 2 || common.AddrHashPrefixLen != 24 {
		return nil, fmt.Errorf("SplitPVHot requires VersionLength=8, LenOfPathLen=2 and AddrHashPrefixLen=24")
	}
	if common.DepthThreshold < 0 || common.DepthThreshold > 53 {
		return nil, fmt.Errorf("SplitPVHot DepthThreshold must be in [0, 53], got %d", common.DepthThreshold)
	}
	if version >= uint64(1)<<32 {
		return nil, fmt.Errorf("block number %d does not fit in 32 bits", version)
	}
	if common.HashingStateTrie == common.HashingStorageTrie {
		return nil, fmt.Errorf("exactly one of HashingStateTrie and HashingStorageTrie must be true")
	}
	cold := common.HashingStateTrie && int64(len(tnd.Path)) <= common.DepthThreshold
	group := uint64(0)
	if cold {
		group = 1
	}
	var b bitKeyBuilder
	if err := b.appendUint(group, 1); err != nil {
		return nil, err
	}
	if !cold {
		if err := b.appendUint(version, 32); err != nil {
			return nil, err
		}
	}
	pathWidth, section := 53, uint64(0xd)
	if common.HashingStorageTrie {
		pathWidth, section = 29, 0xf
	}
	if err := b.appendUint(section, 4); err != nil {
		return nil, err
	}
	if common.HashingStorageTrie {
		for _, value := range common.AddrHashOfCurrentStorageTrie[:12] {
			if err := b.appendUint(uint64(value), 8); err != nil {
				return nil, err
			}
		}
	}
	if err := b.appendNibbles(tnd.Path, pathWidth); err != nil {
		return nil, err
	}
	if cold {
		if err := b.appendUint(version, 32); err != nil {
			return nil, err
		}
	}
	if err := b.appendUint(uint64(len(tnd.Path)), 7); err != nil {
		return nil, err
	}
	return b.finish()
}

// modifyVPRightKey preserves VP order for every node, wholly above the existing
// code namespace. It is byte-identical to OutwardSplit's body representation.
func modifyVPRightKey(version uint64, tnd common.TrieNodeData) (hashNode, error) {
	if common.VersionLength != 8 || common.LenOfPathLen != 2 || common.AddrHashPrefixLen != 24 {
		return nil, fmt.Errorf("VPRight requires VersionLength=8, LenOfPathLen=2 and AddrHashPrefixLen=24")
	}
	if version >= uint64(1)<<32 {
		return nil, fmt.Errorf("block number %d does not fit in 32 bits", version)
	}
	if common.HashingStateTrie == common.HashingStorageTrie {
		return nil, fmt.Errorf("exactly one of HashingStateTrie and HashingStorageTrie must be true")
	}
	var b bitKeyBuilder
	if err := b.appendUint(2, 2); err != nil {
		return nil, err
	}
	if err := b.appendUint(version, 32); err != nil {
		return nil, err
	}
	pathWidth, section := 53, uint64(0xd)
	if common.HashingStorageTrie {
		pathWidth, section = 29, 0xf
	}
	if err := b.appendUint(section, 4); err != nil {
		return nil, err
	}
	if common.HashingStorageTrie {
		for _, value := range common.AddrHashOfCurrentStorageTrie[:12] {
			if err := b.appendUint(uint64(value), 8); err != nil {
				return nil, err
			}
		}
	}
	if err := b.appendNibbles(tnd.Path, pathWidth); err != nil {
		return nil, err
	}
	if err := b.appendUint(uint64(len(tnd.Path)), 6); err != nil {
		return nil, err
	}
	return b.finish()
}

// modifyOutwardSplitKey places cold state history before code and body after
// code. Across epochs cold grows left; body grows right with full birth version.
// Shared L0 ranges can still span both classes. Child IDs are these same keys.
func modifyOutwardSplitKey(version uint64, tnd common.TrieNodeData) (hashNode, error) {
	if common.VersionLength != 8 || common.LenOfPathLen != 2 || common.AddrHashPrefixLen != 24 {
		return nil, fmt.Errorf("OutwardSplit requires VersionLength=8, LenOfPathLen=2 and AddrHashPrefixLen=24")
	}
	width, err := common.EpochOffsetBits(common.EpochSize)
	if err != nil {
		return nil, err
	}
	if common.DepthThreshold < 0 || common.DepthThreshold > 53 {
		return nil, fmt.Errorf("OutwardSplit DepthThreshold must be in [0, 53], got %d", common.DepthThreshold)
	}
	if version >= uint64(1)<<32 {
		return nil, fmt.Errorf("block number %d does not fit in 32 bits", version)
	}
	if common.HashingStateTrie == common.HashingStorageTrie {
		return nil, fmt.Errorf("exactly one of HashingStateTrie and HashingStorageTrie must be true")
	}
	cold := common.HashingStateTrie && int64(len(tnd.Path)) <= common.DepthThreshold
	group := uint64(2)
	if cold {
		group = 0
	}
	var b bitKeyBuilder
	if err := b.appendUint(group, 2); err != nil {
		return nil, err
	}
	if cold {
		reverseEpoch := (uint64(1)<<(32-width) - 1) - (version >> width)
		if err := b.appendUint(reverseEpoch, 32-width); err != nil {
			return nil, err
		}
	} else if err := b.appendUint(version, 32); err != nil {
		return nil, err
	}
	pathWidth, section := 53, uint64(0xd)
	if common.HashingStorageTrie {
		pathWidth, section = 29, 0xf
	}
	if err := b.appendUint(section, 4); err != nil {
		return nil, err
	}
	if common.HashingStorageTrie {
		for _, value := range common.AddrHashOfCurrentStorageTrie[:12] {
			if err := b.appendUint(uint64(value), 8); err != nil {
				return nil, err
			}
		}
	}
	if err := b.appendNibbles(tnd.Path, pathWidth); err != nil {
		return nil, err
	}
	if cold {
		if err := b.appendUint(version&(common.EpochSize-1), width); err != nil {
			return nil, err
		}
	}
	if err := b.appendUint(uint64(len(tnd.Path)), 6); err != nil {
		return nil, err
	}
	return b.finish()
}

// modifyOutwardStorageKey extends OutwardSplit only for shallow storage
// branches. Existing account keys and all unselected storage keys are identical
// to OutwardSplit. Classification uses the actual node, not TrieNodeData.NodeType
// (which is not populated on the hashing path). Historical IDs remain immutable.
func modifyOutwardStorageKey(version uint64, tnd common.TrieNodeData, n node) (hashNode, error) {
	if common.StorageDepthThreshold < 0 || common.StorageDepthThreshold > 29 {
		return nil, fmt.Errorf("OutwardStorage StorageDepthThreshold must be in [0, 29], got %d", common.StorageDepthThreshold)
	}
	_, branch := n.(*fullNode)
	if !common.HashingStorageTrie || !branch || int64(len(tnd.Path)) > common.StorageDepthThreshold {
		return modifyOutwardSplitKey(version, tnd)
	}
	if common.VersionLength != 8 || common.LenOfPathLen != 2 || common.AddrHashPrefixLen != 24 {
		return nil, fmt.Errorf("OutwardStorage requires VersionLength=8, LenOfPathLen=2 and AddrHashPrefixLen=24")
	}
	if common.DepthThreshold < 0 || common.DepthThreshold > 53 || common.HashingStateTrie == common.HashingStorageTrie {
		return nil, fmt.Errorf("invalid OutwardStorage state depth or trie side")
	}
	if version >= uint64(1)<<32 {
		return nil, fmt.Errorf("block number %d does not fit in 32 bits", version)
	}
	width, err := common.EpochOffsetBits(common.EpochSize)
	if err != nil {
		return nil, err
	}
	var b bitKeyBuilder
	if err := b.appendUint(0, 2); err != nil {
		return nil, err
	}
	reverseEpoch := (uint64(1)<<(32-width) - 1) - (version >> width)
	if err := b.appendUint(reverseEpoch, 32-width); err != nil {
		return nil, err
	}
	if err := b.appendUint(15, 4); err != nil {
		return nil, err
	}
	for _, value := range common.AddrHashOfCurrentStorageTrie[:12] {
		if err := b.appendUint(uint64(value), 8); err != nil {
			return nil, err
		}
	}
	if err := b.appendNibbles(tnd.Path, 29); err != nil {
		return nil, err
	}
	if err := b.appendUint(version&(common.EpochSize-1), width); err != nil {
		return nil, err
	}
	if err := b.appendUint(uint64(len(tnd.Path)), 6); err != nil {
		return nil, err
	}
	return b.finish()
}

// hashShortNodeChildren collapses the short node. The returned collapsed node
// holds a live reference to the Key, and must not be modified.
func (h *hasher) hashShortNodeChildren(n *shortNode, tnd common.TrieNodeData) (collapsed, cached *shortNode) {
	// Hash the short node's child, caching the newly hashed subtree
	collapsed, cached = n.copy(), n.copy()
	// Previously, we did copy this one. We don't seem to need to actually
	// do that, since we don't overwrite/reuse keys
	// cached.Key = common.CopyBytes(n.Key)
	collapsed.Key = hexToCompact(n.Key)
	// Unless the child is a valuenode or hashnode, hash it
	switch n.Val.(type) {
	case *fullNode, *shortNode:
		var childTnd common.TrieNodeData
		childTnd.Path = append(tnd.Path, n.Key...)
		childTnd.Depth = tnd.Depth + 1
		collapsed.Val, cached.Val = h.hash(n.Val, false, childTnd)
	}
	return collapsed, cached
}

func (h *hasher) hashFullNodeChildren(n *fullNode, tnd common.TrieNodeData) (collapsed *fullNode, cached *fullNode) {
	modifiedChildNum := 0
	unmodifiedChildNum := 0
	// Hash the full node's children, caching the newly hashed subtrees
	cached = n.copy()
	collapsed = n.copy()
	if h.parallel {
		var wg sync.WaitGroup
		var childModifyHashes [16]time.Duration
		wg.Add(16)
		for i := 0; i < 16; i++ {
			go func(i int) {
				hasher := newHasher(false)
				if child := n.Children[i]; child != nil {
					// set TrieNodeData
					var childTnd common.TrieNodeData
					childTnd.Path = make([]byte, len(tnd.Path)+1)
					copy(childTnd.Path, tnd.Path)
					childTnd.Path[len(tnd.Path)] = byte(i)
					childTnd.Depth = tnd.Depth + 1

					// check if child hash is cached
					if hash, _ := child.cache(); hash != nil {
						// this is clean child
						// fmt.Println("  check child", i, "-> clean")
						collapsed.Children[i], cached.Children[i] = hash, child

						// additionally read this clean child node (to get childHash)
						if common.ReadAllChildNodes && shouldAdditionalReadChild(childTnd.Path) {
							// fmt.Println("    additional read occurs for", common.BytesToHash(hash))
							addAdditionalNodeRead()
							blob, err := CurrentTrie.reader.node(childTnd.Path, common.BytesToHash(hash))
							if err == nil {
								// CurrentTrie.tracer.onRead(childTnd.Path, blob) // comment out this to avoid current map write issue
								mustDecodeNode(hash, blob)
								markAdditionalReadDone(childTnd.Path)
							}
						}
					} else {
						// this child hash is not cached, need to compute it
						collapsed.Children[i], cached.Children[i] = hasher.hash(child, false, childTnd)

						// additionally read this clean child node (to get childHash)
						if common.ReadAllChildNodes {
							switch c := child.(type) {
							case hashNode:
								if shouldAdditionalReadChild(childTnd.Path) {
									addAdditionalNodeRead()
									blob, err := CurrentTrie.reader.node(childTnd.Path, common.BytesToHash(c))
									if err == nil {
										// CurrentTrie.tracer.onRead(childTnd.Path, blob) // comment out this to avoid current map write issue
										mustDecodeNode(c, blob)
										markAdditionalReadDone(childTnd.Path)
									}
								}
							}
						}

					}
				} else {
					collapsed.Children[i] = nilValueNode
				}
				childModifyHashes[i] = hasher.modifyHashes
				returnHasherToPool(hasher)
				wg.Done()
			}(i)
		}
		wg.Wait()
		for _, elapsed := range childModifyHashes {
			h.modifyHashes += elapsed
		}
	} else {
		for i := 0; i < 16; i++ {
			if child := n.Children[i]; child != nil {
				// set TrieNodeData
				var childTnd common.TrieNodeData
				childTnd.Path = append(tnd.Path, byte(i))
				childTnd.Depth = tnd.Depth + 1

				// check if child hash is cached
				if hash, isDirty := child.cache(); hash != nil {
					// this is clean child
					if isDirty {
						// this is not called until 10M blocks
						fmt.Println("I think this is clean node, but its dirty")
						fmt.Println("  isDirty:", isDirty)
						os.Exit(1)
					}
					// fmt.Println("  check child", i, "-> clean")
					collapsed.Children[i], cached.Children[i] = hash, child

					// additionally read this clean child node (to get childHash)
					if common.ReadAllChildNodes && shouldAdditionalReadChild(childTnd.Path) {
						addAdditionalNodeRead()
						blob, err := CurrentTrie.reader.node(childTnd.Path, common.BytesToHash(hash))
						if err == nil {
							// CurrentTrie.tracer.onRead(childTnd.Path, blob) // comment out this to avoid current map write issue
							mustDecodeNode(hash, blob)
							markAdditionalReadDone(childTnd.Path)
						}
					}
					common.CleanChildNum++
					unmodifiedChildNum++
				} else {

					switch child.(type) {

					case hashNode:
						// fmt.Println("this is hash node -> clean")
						common.CleanChildNum++
						unmodifiedChildNum++

					case valueNode:
						// fmt.Println("this is value node -> clean or dirty")
						// valueNode cannot be a full node's child (this is not called until 10M blocks)
						// just treat this as a nil
						common.NilChildNum++
						unmodifiedChildNum++
						fmt.Println("ERROR: full node can have valueNode as a child")
						os.Exit(1)

					case *shortNode, *fullNode:
						// fmt.Println("this is short/full node -> clean or dirty")
						// this can be a node which is smaller than 32B, so no hash is cached
						// but this case would be very rare
						if isDirty {
							common.DirtyChildNum++
							modifiedChildNum++
						} else {
							// this is clean but cannot be seen as an independent node
							// so just treat this as a nil
							// this case occurred 25,077 times until 10M blocks
							common.NilChildNum++
							unmodifiedChildNum++
						}

					default:
						// this is not called until 10M blocks
						fmt.Println("ERROR: how child node can be wierd type?")
						os.Exit(1)
					}

					// this child hash is not cached, need to compute it
					collapsed.Children[i], cached.Children[i] = h.hash(child, false, childTnd)

					// additionally read this clean child node (to get childHash)
					if common.ReadAllChildNodes {
						switch c := child.(type) {
						case hashNode:
							if shouldAdditionalReadChild(childTnd.Path) {
								addAdditionalNodeRead()
								blob, err := CurrentTrie.reader.node(childTnd.Path, common.BytesToHash(c))
								if err == nil {
									// CurrentTrie.tracer.onRead(childTnd.Path, blob) // comment out this to avoid current map write issue
									mustDecodeNode(c, blob)
									markAdditionalReadDone(childTnd.Path)
								}
							}
						}
					}

				}
			} else {
				collapsed.Children[i] = nilValueNode

				// TODO(jmlee): need to distinguish this is nil originally or modified to nil (ex. due to trie.Delete())
				common.NilChildNum++
				unmodifiedChildNum++
			}
		}
	}

	if common.MeasureChildStats && modifiedChildNum+unmodifiedChildNum != 16 {
		// this is not called until 10M blocks
		fmt.Println("EROR: modifiedChildNum + unmodifiedChildNum is not 16")
		fmt.Println("  modifiedChildNum:", modifiedChildNum)
		fmt.Println("  unmodifiedChildNum:", unmodifiedChildNum)
		os.Exit(1)
	}
	common.ModifiedChildNum[modifiedChildNum]++

	// if modifiedChildNum == 0 {
	// 	// this can happen, maybe due to read-only account (read the account but it is not updated)
	// 	fmt.Println("ERROR? modified child num is 0")
	// 	os.Exit(1)
	// }

	return collapsed, cached
}

// shortnodeToHash creates a hashNode from a shortNode. The supplied shortnode
// should have hex-type Key, which will be converted (without modification)
// into compact form for RLP encoding.
// If the rlp data is smaller than 32 bytes, `nil` is returned.
func (h *hasher) shortnodeToHash(n *shortNode, force bool) node {
	n.encode(h.encbuf)
	enc := h.encodedBytes()

	if len(enc) < 32 && !force {
		return n // Nodes smaller than 32 bytes are stored inside their parent
	}

	// fmt.Println("\n\nin shortnodeToHash() -> myhash:", h.hashData(enc))
	common.HashedShortNodeNum++
	switch n.Val.(type) {
	case valueNode:
		common.HashedLeafNodeNum++
	}

	return h.hashData(enc)
}

// fullnodeToHash is used to create a hashNode from a fullNode, (which
// may contain nil values)
func (h *hasher) fullnodeToHash(n *fullNode, force bool) node {
	n.encode(h.encbuf)
	enc := h.encodedBytes()

	if len(enc) < 32 && !force {
		return n // Nodes smaller than 32 bytes are stored inside their parent
	}

	// fmt.Println("fullnodeToHash() -> myhash:", h.hashData(enc))
	common.HashedFullNodeNum++

	return h.hashData(enc)
}

// encodedBytes returns the result of the last encoding operation on h.encbuf.
// This also resets the encoder buffer.
//
// All node encoding must be done like this:
//
//	node.encode(h.encbuf)
//	enc := h.encodedBytes()
//
// This convention exists because node.encode can only be inlined/escape-analyzed when
// called on a concrete receiver type.
func (h *hasher) encodedBytes() []byte {
	h.tmp = h.encbuf.AppendToBytes(h.tmp[:0])
	h.encbuf.Reset(nil)
	return h.tmp
}

// hashData hashes the provided data
func (h *hasher) hashData(data []byte) hashNode {
	n := make(hashNode, 32)
	h.sha.Reset()
	h.sha.Write(data)
	h.sha.Read(n)
	return n
}

// TODO(jmlee): This function will not work correctly if the nodeHash has been modified with.
// Keep this in mind and either avoid using this function or take appropriate measures.
//
// proofHash is used to construct trie proofs, and returns the 'collapsed'
// node (for later RLP encoding) as well as the hashed node -- unless the
// node is smaller than 32 bytes, in which case it will be returned as is.
// This method does not do anything on value- or hash-nodes.
func (h *hasher) proofHash(original node) (collapsed, hashed node) {
	switch n := original.(type) {
	case *shortNode:
		var tnd common.TrieNodeData
		sn, _ := h.hashShortNodeChildren(n, tnd)
		return sn, h.shortnodeToHash(sn, false)
	case *fullNode:
		var tnd common.TrieNodeData
		fn, _ := h.hashFullNodeChildren(n, tnd)
		return fn, h.fullnodeToHash(fn, false)
	default:
		// Value and hash nodes don't have children, so they're left as were
		return n, n
	}
}
