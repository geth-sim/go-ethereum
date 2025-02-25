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
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"golang.org/x/crypto/sha3"
)

// for prefixing trie node hashes
var (
	CurrentBlockNum = uint64(0)
)

func SetCurrentBlockNum(blockNum uint64) {
	CurrentBlockNum = blockNum
}

// hasher is a type used for the trie Hash operation. A hasher has some
// internal preallocated temp space
type hasher struct {
	sha      crypto.KeccakState
	tmp      []byte
	encbuf   rlp.EncoderBuffer
	parallel bool // Whether to use parallel threads when hashing
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
	return h
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

			if common.PathLength+common.VersionLength > 0 {
				modifiedHash := modifyHashV4(n, hn, CurrentBlockNum, tnd)
				cached.flags.hash = modifiedHash
				hashed = modifiedHash
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

			if common.PathLength+common.VersionLength > 0 {
				modifiedHash := modifyHashV4(n, hn, CurrentBlockNum, tnd)
				cached.flags.hash = modifiedHash
				hashed = modifiedHash
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

// modifyHash returns a new hashNode without finding proper nonce (jmlee)
// just overlap the hash prefix with what we want
func modifyHash(n node, hash hashNode, blockNum uint64) hashNode {
	bs := make([]byte, 8)
	binary.BigEndian.PutUint64(bs, blockNum)

	newHash := hashNode(hash)
	copy(newHash, hash)
	switch n.(type) {
	case *shortNode, *fullNode:
		copy(newHash[:common.PrefixLength], bs[8-common.PrefixLength:])
		return newHash
	default:
		return nil
	}
}

// (jmlee) modify nodeHash as I want
func modifyHashV4(n node, hash hashNode, blockNum uint64, tnd common.TrieNodeData) hashNode {
	fmt.Println("in modifyHashV5()")
	// fmt.Println("  original path:", tnd.Path)
	// fmt.Println("  version:", blockNum)
	// fmt.Println("  original hash:", hash)
	// fmt.Println("  HashingStateTrie:", common.HashingStateTrie)
	// fmt.Println("  HashingStorageTrie:", common.HashingStorageTrie)
	if common.HashingStateTrie && common.HashingStorageTrie {
		fmt.Println("ERROR: HashingStateTrie and HashingStorageTrie could not be both true")
		os.Exit(1)
	}
	if common.MaxPathLen < len(tnd.Path) {
		common.MaxPathLen = len(tnd.Path)
		common.MaxPathLenBlockNum = blockNum
	}
	// fmt.Println("  max path len:", common.MaxPathLen)
	// fmt.Println("  max path len at block:", common.MaxPathLenBlockNum)

	switch n.(type) {
	case *shortNode, *fullNode:
		//
		// Convert path to fixed-length hex string (each byte -> single hex digit)
		//

		// Adjust path length to match PathLength (Trim or Pad)
		path := tnd.Path
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
		pathHex := ""
		for _, b := range path {
			pathHex += indices[b] // Faster than fmt.Sprintf or Builder
		}
		// fmt.Println("  path prefix:", pathHex)

		//
		// Convert blockNum to fixed-length hex string
		//
		blockHex := fmt.Sprintf("%0*x", common.VersionLength, blockNum)
		// fmt.Println("  block prefix:", blockHex)

		//
		// Merge pathHex and blockHex into a single string
		//
		var prefixStr string
		if common.AppendPathFirst {
			prefixStr = pathHex + blockHex
		} else {
			prefixStr = blockHex + pathHex
		}
		if len(prefixStr) < common.LastPaddingBound {
			prefixStr += strings.Repeat("0", common.LastPaddingBound-len(prefixStr))
		}
		// fmt.Println("  prefix str:", prefixStr)

		//
		// Overwrite the front part of newHashHex with prefixStr
		//
		newHashHex := prefixStr + hex.EncodeToString(hash)[len(prefixStr):]
		// fmt.Println("  modified hex hash:", newHashHex)

		// Convert the modified hex string back to bytes efficiently
		newHash, err := hex.DecodeString(newHashHex)
		if err != nil {
			fmt.Println("  hex.Decode error:", err)
			return nil
		}

		// fmt.Println("  modified hash (bytes):", newHash, "\n")
		return newHash
	default:
		return nil
	}
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
	// Hash the full node's children, caching the newly hashed subtrees
	cached = n.copy()
	collapsed = n.copy()
	if h.parallel {
		var wg sync.WaitGroup
		wg.Add(16)
		for i := 0; i < 16; i++ {
			go func(i int) {
				hasher := newHasher(false)
				if child := n.Children[i]; child != nil {
					var childTnd common.TrieNodeData
					childTnd.Path = append(tnd.Path, byte(i))
					childTnd.Depth = tnd.Depth + 1
					collapsed.Children[i], cached.Children[i] = hasher.hash(child, false, childTnd)
				} else {
					collapsed.Children[i] = nilValueNode
				}
				returnHasherToPool(hasher)
				wg.Done()
			}(i)
		}
		wg.Wait()
	} else {
		for i := 0; i < 16; i++ {
			if child := n.Children[i]; child != nil {
				var childTnd common.TrieNodeData
				childTnd.Path = append(tnd.Path, byte(i))
				childTnd.Depth = tnd.Depth + 1
				collapsed.Children[i], cached.Children[i] = h.hash(child, false, childTnd)
			} else {
				collapsed.Children[i] = nilValueNode
			}
		}
	}
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
