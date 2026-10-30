// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library, licensed under LGPL-3.0.

package incauth

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"

	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	LeafNode      uint8 = 1
	ExtensionNode uint8 = 2
	BranchNode    uint8 = 3
	maxValueSize        = 16 << 10
	maxNodeSize         = 32 << 10
	maxProofSize        = 64 << 10
)

type Locator [32]byte
type KeyEncoder func(version uint32, path []byte, kind uint8) (Locator, error)
type Policy uint8

const (
	Incremental Policy = iota
	Full
)

// Reads counts trie-node Get calls, not physical disk I/O; schema/root-record
// reads are excluded. There is no implicit decoded-node cache. An ethdb
// implementation may have its own caches.
type Stats struct{ Reads, AuthReads, StructuralReads, ProofReads, Writes, WriteBytes uint64 }
type Root struct {
	Version    uint32
	Locator    Locator
	Commitment Commitment
}
type link struct {
	key  Locator
	node *node
}

func (r link) empty() bool { return r.node == nil && r.key == (Locator{}) }

type node struct {
	kind        uint8
	path, value []byte
	child       link
	kids        [16]link
	mask        uint16
	state       []byte
	c           Commitment
}

// Trie is a small CoW reference used to cross-check the geth authentication
// adapter in tests; EVM replay does not use it. Every persisted node is external.
// A parent stores only child locators; branch S / extension C belong to the node itself.
// One writer per DB is required. Accounts/storage are separate trie instances;
// the caller must include the storage commitment in an account's logical value.
type Trie struct {
	db     ethdb.KeyValueStore
	auth   *Scheme
	policy Policy
	layout string
	encode KeyEncoder
	root   link
	stats  Stats
}

var configKey = []byte("ia2/config")
var latestKey = []byte("ia2/latest")

func rootKey(version uint32) []byte {
	out := []byte("ia2/root/")
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], version)
	return append(out, b[:]...)
}
func (t *Trie) configuration() ([]byte, error) {
	return rlp.EncodeToBytes([]interface{}{uint(2), t.auth.ID(), t.auth.Type(), uint(t.auth.StateSize()), t.layout, "all-external"})
}
func New(db ethdb.KeyValueStore, auth *Scheme, policy Policy, layout string, encode KeyEncoder) (*Trie, error) {
	if db == nil || auth == nil || encode == nil || layout == "" || policy > Full {
		return nil, errors.New("invalid trie options")
	}
	t := &Trie{db: db, auth: auth, policy: policy, layout: layout, encode: encode}
	has, err := db.Has(configKey)
	if err != nil {
		return nil, err
	}
	if has {
		got, err := db.Get(configKey)
		if err != nil {
			return nil, err
		}
		want, _ := t.configuration()
		if !bytes.Equal(got, want) {
			return nil, errors.New("authentication/layout DB mismatch")
		}
	} else {
		it := db.NewIterator(nil, nil)
		nonempty := it.Next()
		err := it.Error()
		it.Release()
		if err != nil {
			return nil, err
		}
		if nonempty {
			return nil, errors.New("nonempty DB has no authentication schema")
		}
	}
	return t, nil
}
func Open(db ethdb.KeyValueStore, auth *Scheme, policy Policy, layout string, encode KeyEncoder, version uint32) (*Trie, error) {
	t, err := New(db, auth, policy, layout, encode)
	if err != nil {
		return nil, err
	}
	// A root without the matching schema is not a valid authentication database.
	has, err := db.Has(configKey)
	if err != nil || !has {
		return nil, errors.New("missing authentication schema")
	}
	raw, err := db.Get(rootKey(version))
	if err != nil {
		return nil, err
	}
	var root Root
	if err = rlp.DecodeBytes(raw, &root); err != nil {
		return nil, err
	}
	if root.Version != version {
		return nil, errors.New("root version")
	}
	t.root = link{key: root.Locator}
	c, err := t.commitment(t.root, 0)
	if err != nil {
		return nil, err
	}
	if c != root.Commitment {
		return nil, errors.New("root record mismatch")
	}
	t.stats = Stats{}
	return t, nil
}
func (t *Trie) Stats() Stats { return t.stats }
func (t *Trie) ResetStats()  { t.stats = Stats{} }

// Fork shares immutable nodes and DB data, with independent root/stats. It can
// be used for transaction revert. Commits still require globally fresh versions.
func (t *Trie) Fork() *Trie               { out := *t; out.stats = Stats{}; return &out }
func (t *Trie) Root() (Commitment, error) { return t.commitment(t.root, 0) }
func nibbles(key [32]byte) []byte {
	out := make([]byte, 64)
	for i, b := range key {
		out[2*i] = b >> 4
		out[2*i+1] = b & 15
	}
	return out
}
func joined(a, b []byte) []byte { out := append([]byte{}, a...); return append(out, b...) }
func prefix(a, b []byte) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}
func (t *Trie) commitment(r link, reason uint8) (Commitment, error) {
	if r.empty() {
		return t.auth.Empty(), nil
	}
	n, err := t.read(r, reason)
	if err != nil {
		return Commitment{}, err
	}
	return n.c, nil
}

// reason: 0 path read, 1 additional authentication, 2 structural, 3 proof/audit.
func (t *Trie) read(r link, reason uint8) (*node, error) {
	if r.node != nil {
		return r.node, nil
	}
	if r.empty() {
		return nil, errors.New("empty node")
	}
	t.stats.Reads++
	switch reason {
	case 1:
		t.stats.AuthReads++
	case 2:
		t.stats.StructuralReads++
	case 3:
		t.stats.ProofReads++
	}
	raw, err := t.db.Get(r.key[:])
	if err != nil {
		return nil, err
	}
	return t.decode(raw)
}
func (t *Trie) leaf(path, value []byte) (link, error) {
	c, err := t.auth.Leaf(path, value)
	if err != nil {
		return link{}, err
	}
	return link{node: &node{kind: LeafNode, path: append([]byte{}, path...), value: append([]byte{}, value...), c: c}}, nil
}
func (t *Trie) extension(path []byte, child link) (link, error) {
	if child.empty() {
		return link{}, nil
	}
	if len(path) == 0 {
		return child, nil
	}
	n, err := t.read(child, 2)
	if err != nil {
		return link{}, err
	}
	switch n.kind {
	case LeafNode:
		return t.leaf(joined(path, n.path), n.value)
	case ExtensionNode:
		return t.extension(joined(path, n.path), n.child)
	}
	c, err := t.auth.Extension(path, n.c)
	if err != nil {
		return link{}, err
	}
	return link{node: &node{kind: ExtensionNode, path: append([]byte{}, path...), child: child, c: c}}, nil
}
func (t *Trie) branch(kids [16]link, reason uint8) (link, error) {
	n := &node{kind: BranchNode, kids: kids}
	var cs [16]Child
	for i, r := range kids {
		if !r.empty() {
			n.mask |= 1 << i
			c, err := t.commitment(r, reason)
			if err != nil {
				return link{}, err
			}
			cs[i] = Child{true, c}
		}
	}
	var err error
	n.state, n.c, err = t.auth.Build(cs)
	if err != nil {
		return link{}, err
	}
	return link{node: n}, nil
}
func (t *Trie) Update(key [32]byte, value []byte) error {
	if len(value) > maxValueSize {
		return errors.New("value size")
	}
	r, _, _, err := t.update(t.root, nibbles(key), value, false)
	if err == nil {
		t.root = r
	}
	return err
}
func (t *Trie) Delete(key [32]byte) error {
	r, _, _, err := t.update(t.root, nibbles(key), nil, true)
	if err == nil {
		t.root = r
	}
	return err
}

// update returns the original commitment before any mutation; CoW nodes and
// their auth states remain immutable, including on errors and repeated updates.
func (t *Trie) update(r link, path, value []byte, del bool) (link, bool, Commitment, error) {
	if r.empty() {
		if del {
			return r, false, t.auth.Empty(), nil
		}
		out, err := t.leaf(path, value)
		return out, true, t.auth.Empty(), err
	}
	n, err := t.read(r, 0)
	if err != nil {
		return link{}, false, Commitment{}, err
	}
	old := n.c
	switch n.kind {
	case LeafNode:
		if len(n.path) != len(path) {
			return link{}, false, old, errors.New("leaf depth")
		}
		p := prefix(n.path, path)
		if p == len(path) {
			if del {
				return link{}, true, old, nil
			}
			if bytes.Equal(n.value, value) {
				return r, false, old, nil
			}
			out, err := t.leaf(path, value)
			return out, true, old, err
		}
		if del {
			return r, false, old, nil
		}
		var kids [16]link
		kids[n.path[p]], err = t.leaf(n.path[p+1:], n.value)
		if err != nil {
			return link{}, false, old, err
		}
		kids[path[p]], err = t.leaf(path[p+1:], value)
		if err != nil {
			return link{}, false, old, err
		}
		b, err := t.branch(kids, 2)
		if err != nil {
			return link{}, false, old, err
		}
		out, err := t.extension(path[:p], b)
		return out, true, old, err
	case ExtensionNode:
		if len(n.path) >= len(path) {
			return link{}, false, old, errors.New("extension depth")
		}
		p := prefix(n.path, path)
		if p == len(n.path) {
			child, changed, _, err := t.update(n.child, path[p:], value, del)
			if err != nil || !changed {
				return r, false, old, err
			}
			out, err := t.extension(n.path, child)
			return out, true, old, err
		}
		if del {
			return r, false, old, nil
		}
		var kids [16]link
		kids[n.path[p]], err = t.extension(n.path[p+1:], n.child)
		if err != nil {
			return link{}, false, old, err
		}
		kids[path[p]], err = t.leaf(path[p+1:], value)
		if err != nil {
			return link{}, false, old, err
		}
		b, err := t.branch(kids, 2)
		if err != nil {
			return link{}, false, old, err
		}
		out, err := t.extension(path[:p], b)
		return out, true, old, err
	case BranchNode:
		if len(path) == 0 {
			return link{}, false, old, errors.New("branch depth")
		}
		slot := path[0]
		child, changed, oldChild, err := t.update(n.kids[slot], path[1:], value, del)
		if err != nil || !changed {
			return r, false, old, err
		}
		copyNode := *n
		copyNode.kids[slot] = child
		copyNode.mask &^= 1 << slot
		if !child.empty() {
			copyNode.mask |= 1 << slot
		}
		if bits.OnesCount16(copyNode.mask) < 2 {
			for i, c := range copyNode.kids {
				if !c.empty() {
					out, err := t.extension([]byte{byte(i)}, c)
					return out, true, old, err
				}
			}
			return link{}, true, old, nil
		}
		if t.policy == Full || t.auth.backend == nil {
			out, err := t.branch(copyNode.kids, 1)
			return out, true, old, err
		}
		newC, err := t.commitment(child, 0)
		if err != nil {
			return link{}, false, old, err
		}
		copyNode.state, copyNode.c, copyNode.mask, err = t.auth.Update(n.state, n.mask, []Change{{Slot: slot, Old: Child{!n.kids[slot].empty(), oldChild}, New: Child{!child.empty(), newC}}})
		if err != nil {
			return link{}, false, old, err
		}
		return link{node: &copyNode}, true, old, nil
	default:
		return link{}, false, old, errors.New("node kind")
	}
}
func (t *Trie) Get(key [32]byte) ([]byte, bool, error) {
	r, path := t.root, nibbles(key)
	for !r.empty() {
		n, err := t.read(r, 0)
		if err != nil {
			return nil, false, err
		}
		switch n.kind {
		case LeafNode:
			if len(n.path) != len(path) {
				return nil, false, errors.New("leaf depth")
			}
			if !bytes.Equal(n.path, path) {
				return nil, false, nil
			}
			return append([]byte{}, n.value...), true, nil
		case ExtensionNode:
			if len(n.path) >= len(path) {
				return nil, false, errors.New("extension depth")
			}
			if !bytes.HasPrefix(path, n.path) {
				return nil, false, nil
			}
			path = path[len(n.path):]
			r = n.child
		case BranchNode:
			if len(path) == 0 {
				return nil, false, errors.New("branch depth")
			}
			r = n.kids[path[0]]
			path = path[1:]
		}
	}
	return nil, false, nil
}

func (t *Trie) encodeNode(n *node) ([]byte, error) {
	p, err := pathBytes(n.path)
	if err != nil {
		return nil, err
	}
	switch n.kind {
	case LeafNode:
		return rlp.EncodeToBytes([]interface{}{uint(LeafNode), p, n.value})
	case ExtensionNode:
		return rlp.EncodeToBytes([]interface{}{uint(ExtensionNode), p, n.child.key, n.c})
	case BranchNode:
		var keys []Locator
		for _, r := range n.kids {
			if !r.empty() {
				keys = append(keys, r.key)
			}
		}
		return rlp.EncodeToBytes([]interface{}{uint(BranchNode), n.mask, keys, n.state})
	}
	return nil, errors.New("node kind")
}
func unpack(raw []byte) ([]byte, error) {
	if len(raw) == 0 || raw[0] > 64 || len(raw) != 1+(int(raw[0])+1)/2 {
		return nil, errors.New("packed path")
	}
	if raw[0]%2 != 0 && raw[len(raw)-1]&15 != 0 {
		return nil, errors.New("path padding")
	}
	p := make([]byte, raw[0])
	for i := range p {
		p[i] = (raw[1+i/2] >> uint(4*(1-i%2))) & 15
	}
	return p, nil
}
func (t *Trie) decode(raw []byte) (*node, error) {
	if len(raw) > maxNodeSize {
		return nil, errors.New("node size")
	}
	var fields []rlp.RawValue
	if err := rlp.DecodeBytes(raw, &fields); err != nil {
		return nil, err
	}
	if len(fields) < 3 {
		return nil, errors.New("node fields")
	}
	n := &node{}
	if err := rlp.DecodeBytes(fields[0], &n.kind); err != nil {
		return nil, err
	}
	if n.kind == LeafNode || n.kind == ExtensionNode {
		var packed []byte
		if err := rlp.DecodeBytes(fields[1], &packed); err != nil {
			return nil, err
		}
		var err error
		n.path, err = unpack(packed)
		if err != nil {
			return nil, err
		}
	}
	switch n.kind {
	case LeafNode:
		if len(fields) != 3 {
			return nil, errors.New("leaf fields")
		}
		if err := rlp.DecodeBytes(fields[2], &n.value); err != nil {
			return nil, err
		}
		var err error
		n.c, err = t.auth.Leaf(n.path, n.value)
		if err != nil {
			return nil, err
		}
	case ExtensionNode:
		if len(fields) != 4 || len(n.path) == 0 {
			return nil, errors.New("extension fields")
		}
		if err := rlp.DecodeBytes(fields[2], &n.child.key); err != nil {
			return nil, err
		}
		if n.child.empty() {
			return nil, errors.New("empty extension child")
		}
		if err := rlp.DecodeBytes(fields[3], &n.c); err != nil {
			return nil, err
		}
	case BranchNode:
		if len(fields) != 4 {
			return nil, errors.New("branch fields")
		}
		if err := rlp.DecodeBytes(fields[1], &n.mask); err != nil {
			return nil, err
		}
		var keys []Locator
		if err := rlp.DecodeBytes(fields[2], &keys); err != nil {
			return nil, err
		}
		if len(keys) != bits.OnesCount16(n.mask) || len(keys) < 2 {
			return nil, errors.New("branch mask")
		}
		j := 0
		for i := range n.kids {
			if n.mask&(1<<i) != 0 {
				if keys[j] == (Locator{}) {
					return nil, errors.New("empty branch child")
				}
				n.kids[i] = link{key: keys[j]}
				j++
			}
		}
		if err := rlp.DecodeBytes(fields[3], &n.state); err != nil {
			return nil, err
		}
		var err error
		n.c, err = t.auth.Branch(n.state)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("node kind")
	}
	return n, nil
}

// Commit writes complete immutable nodes and the root record in one ethdb batch.
// It does not promise fsync durability beyond the supplied backend's Write.
// A version may be committed once; keys must be fresh and exactly 32 bytes.
func (t *Trie) Commit(version uint32) (Root, error) {
	has, err := t.db.Has(latestKey)
	if err != nil {
		return Root{}, err
	}
	if has {
		raw, err := t.db.Get(latestKey)
		if err != nil {
			return Root{}, err
		}
		var latest uint32
		if err = rlp.DecodeBytes(raw, &latest); err != nil {
			return Root{}, err
		}
		if version <= latest {
			return Root{}, errors.New("version must increase")
		}
	}
	batch := t.db.NewBatch()
	seen := map[Locator]bool{}
	var writes, size uint64
	var save func(link, []byte) (link, error)
	save = func(r link, path []byte) (link, error) {
		if r.node == nil {
			return r, nil
		}
		n := *r.node
		switch n.kind {
		case ExtensionNode:
			var err error
			n.child, err = save(n.child, joined(path, n.path))
			if err != nil {
				return link{}, err
			}
		case BranchNode:
			for i, c := range n.kids {
				var err error
				n.kids[i], err = save(c, joined(path, []byte{byte(i)}))
				if err != nil {
					return link{}, err
				}
			}
		}
		key, err := t.encode(version, append([]byte{}, path...), n.kind)
		if err != nil {
			return link{}, err
		}
		if key == (Locator{}) || seen[key] {
			return link{}, errors.New("locator collision")
		}
		seen[key] = true
		has, err := t.db.Has(key[:])
		if err != nil {
			return link{}, err
		}
		if has {
			return link{}, errors.New("immutable locator already exists")
		}
		raw, err := t.encodeNode(&n)
		if err != nil {
			return link{}, err
		}
		if err = batch.Put(key[:], raw); err != nil {
			return link{}, err
		}
		writes++
		size += uint64(32 + len(raw))
		return link{key: key}, nil
	}
	r, err := save(t.root, nil)
	if err != nil {
		return Root{}, err
	}
	c, err := t.Root()
	if err != nil {
		return Root{}, err
	}
	root := Root{version, r.key, c}
	conf, _ := t.configuration()
	raw, err := rlp.EncodeToBytes(root)
	if err != nil {
		return Root{}, err
	}
	latest, _ := rlp.EncodeToBytes(version)
	for _, kv := range [][2][]byte{{configKey, conf}, {rootKey(version), raw}, {latestKey, latest}} {
		if err = batch.Put(kv[0], kv[1]); err != nil {
			return Root{}, err
		}
	}
	if err = batch.Write(); err != nil {
		return Root{}, err
	}
	t.root = r
	t.stats.Writes += writes
	t.stats.WriteBytes += size
	return root, nil
}

// Audit fully recomputes the root, validating stored branch states/extensions.
// This is required for an untrusted snapshot; ordinary reads trust local state.
func (t *Trie) Audit() (Commitment, error) {
	var walk func(link, int) (Commitment, uint8, error)
	walk = func(r link, depth int) (Commitment, uint8, error) {
		if r.empty() {
			return t.auth.Empty(), 0, nil
		}
		if depth > 64 {
			return Commitment{}, 0, errors.New("depth")
		}
		n, err := t.read(r, 3)
		if err != nil {
			return Commitment{}, 0, err
		}
		var c Commitment
		switch n.kind {
		case LeafNode:
			if depth+len(n.path) != 64 {
				return c, 0, errors.New("leaf depth")
			}
			c, err = t.auth.Leaf(n.path, n.value)
		case ExtensionNode:
			var child Commitment
			var kind uint8
			child, kind, err = walk(n.child, depth+len(n.path))
			if err == nil && kind != BranchNode {
				err = errors.New("noncanonical extension child")
			}
			if err == nil {
				c, err = t.auth.Extension(n.path, child)
			}
		case BranchNode:
			if depth >= 64 {
				return c, 0, errors.New("branch depth")
			}
			var cs [16]Child
			for i, r := range n.kids {
				if !r.empty() {
					child, _, e := walk(r, depth+1)
					if e != nil {
						return c, 0, e
					}
					cs[i] = Child{true, child}
				}
			}
			var state []byte
			state, c, err = t.auth.Build(cs)
			if err == nil && !bytes.Equal(state, n.state) {
				err = errors.New("stored accumulator mismatch")
			}
		}
		if err == nil && c != n.c {
			err = errors.New("stored commitment mismatch")
		}
		return c, n.kind, err
	}
	c, _, err := walk(t.root, 0)
	return c, err
}

type ProofFrame struct {
	Kind        uint8
	Path, Value []byte
	Mask        uint16
	Children    []Commitment
}
type Proof struct {
	Schema uint
	Suite  string
	Type   uint8
	Key    [32]byte
	Frames []ProofFrame
}

// Prove sends individual sibling commitments, never an arbitrary residual point.
// Proof generation may read clean siblings that updates do not need to read.
func (t *Trie) Prove(key [32]byte) ([]byte, error) {
	p := Proof{Schema: 2, Suite: t.auth.ID(), Type: t.auth.Type(), Key: key}
	r, path := t.root, nibbles(key)
	for !r.empty() {
		if len(p.Frames) >= 65 {
			return nil, errors.New("proof depth")
		}
		n, err := t.read(r, 3)
		if err != nil {
			return nil, err
		}
		f := ProofFrame{Kind: n.kind, Path: n.path, Value: n.value, Mask: n.mask}
		switch n.kind {
		case LeafNode:
			r = link{}
		case ExtensionNode:
			c, err := t.commitment(n.child, 3)
			if err != nil {
				return nil, err
			}
			f.Children = []Commitment{c}
			if !bytes.HasPrefix(path, n.path) {
				r = link{}
			} else {
				path = path[len(n.path):]
				r = n.child
			}
		case BranchNode:
			if len(path) == 0 {
				return nil, errors.New("branch depth")
			}
			for _, child := range n.kids {
				if !child.empty() {
					c, err := t.commitment(child, 3)
					if err != nil {
						return nil, err
					}
					f.Children = append(f.Children, c)
				}
			}
			r = n.kids[path[0]]
			path = path[1:]
		}
		p.Frames = append(p.Frames, f)
	}
	raw, err := rlp.EncodeToBytes(p)
	if len(raw) > maxProofSize {
		return nil, errors.New("proof size")
	}
	return raw, err
}

// Verify distinguishes valid absence from malformed proofs. The caller supplies
// the trusted root and suite. A proof's own root or arbitrary points are not trusted.
func Verify(auth *Scheme, root Commitment, key [32]byte, raw []byte) ([]byte, bool, error) {
	if auth == nil || len(raw) > maxProofSize {
		return nil, false, errors.New("proof size/scheme")
	}
	var p Proof
	if err := rlp.DecodeBytes(raw, &p); err != nil {
		return nil, false, err
	}
	if p.Schema != 2 || p.Suite != auth.ID() || p.Type != auth.Type() || p.Key != key || len(p.Frames) > 65 {
		return nil, false, errors.New("proof metadata")
	}
	if len(p.Frames) == 0 {
		if root != auth.Empty() {
			return nil, false, errors.New("empty proof root")
		}
		return nil, false, nil
	}
	path, expected := nibbles(key), root
	needBranch := false
	for i, f := range p.Frames {
		last := i == len(p.Frames)-1
		var c Commitment
		var err error
		terminal, present := false, false
		if needBranch && f.Kind != BranchNode {
			return nil, false, errors.New("noncanonical extension child")
		}
		needBranch = false
		switch f.Kind {
		case LeafNode:
			if f.Mask != 0 || len(f.Children) != 0 || len(f.Path) != len(path) {
				return nil, false, errors.New("leaf proof shape")
			}
			c, err = auth.Leaf(f.Path, f.Value)
			terminal = true
			present = bytes.Equal(f.Path, path)
		case ExtensionNode:
			if f.Mask != 0 || len(f.Value) != 0 || len(f.Children) != 1 || len(f.Path) == 0 || len(f.Path) >= len(path) {
				return nil, false, errors.New("extension proof shape")
			}
			c, err = auth.Extension(f.Path, f.Children[0])
			terminal = !bytes.HasPrefix(path, f.Path)
		case BranchNode:
			if len(f.Value) != 0 || len(f.Path) != 0 || len(path) == 0 || len(f.Children) != bits.OnesCount16(f.Mask) {
				return nil, false, errors.New("branch proof shape")
			}
			var cs [16]Child
			j := 0
			for slot := range cs {
				if f.Mask&(1<<slot) != 0 {
					cs[slot] = Child{true, f.Children[j]}
					j++
				}
			}
			_, c, err = auth.Build(cs)
			terminal = f.Mask&(1<<path[0]) == 0
		default:
			return nil, false, errors.New("proof node kind")
		}
		if err != nil {
			return nil, false, err
		}
		if c != expected {
			return nil, false, fmt.Errorf("proof commitment at frame %d", i)
		}
		if terminal {
			if !last {
				return nil, false, errors.New("trailing proof frames")
			}
			if present {
				return append([]byte{}, f.Value...), true, nil
			}
			return nil, false, nil
		}
		if last {
			return nil, false, errors.New("truncated proof")
		}
		if f.Kind == ExtensionNode {
			expected = f.Children[0]
			path = path[len(f.Path):]
			needBranch = true
		} else {
			index := bits.OnesCount16(f.Mask & ((1 << path[0]) - 1))
			expected = f.Children[index]
			path = path[1:]
		}
	}
	return nil, false, errors.New("unterminated proof")
}
