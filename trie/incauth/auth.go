// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library, licensed under LGPL-3.0.

// Package incauth is an experimental authenticated Patricia trie. Its roots and
// proofs are not Ethereum MPT roots/proofs. Storage locators are not commitments.
package incauth

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"sync"

	"github.com/cespare/xxhash/v2"
	"github.com/cloudflare/circl/group"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	Ristretto255 = "ristretto255_XMD:SHA-512_R255MAP_RO_/compressed32"
	// GLS254 fixes the jbms ECMH curve, SWChar2 mapping, BLAKE2b and checked
	// little-endian codec. It is not interchangeable with other GLS254 curves.
	GLS254        = "ECMH-GLS254-jbms3448c2a7-SWChar2-BLAKE2b-checkedLE32-v1"
	P256          = "P256_XMD:SHA-256_SSWU_RO_/compressed33-zero-identity"
	Keccak        = "keccak256-full-children"
	Account uint8 = 0
	Storage uint8 = 1
)

var ErrFullRequired = errors.New("authentication requires all child commitments")

// ComputationCache stores immutable, verified internal points, not compressed
// bytes (which would require decoding again). Both trie types and both kinds of
// computation share the same byte budget and LRU policy. Hashing only selects a
// shard; full domain-separated input strings are compared for every lookup.
// Accounting reserves 512 bytes per entry for the point/map/list/allocator, plus
// the full key length. This is a conservative entry budget, not a process RSS cap.
const ComputationCachePolicy = "v1;unified;64-shard-lru;entry-charge=512+key-bytes;immutable-points"

type ComputationCache struct {
	mode   string
	shards [64]computationShard
}
type computationShard struct {
	sync.Mutex
	lru          lru.BasicLRU[string, computationEntry]
	limit, used  uint64
	hits, misses [2]uint64
	entries      [2]uint64
	evictions    uint64
}
type computationEntry struct {
	point interface{} // immutable Go-owned internal point; never a C allocation
	kind  byte
}
type ComputationCacheStats struct {
	CapacityBytes       uint64 `json:"capacity_bytes"`
	AccountedBytes      uint64 `json:"accounted_bytes"`
	AccumulatorEntries  uint64 `json:"accumulator_entries"`
	ContributionEntries uint64 `json:"contribution_entries"`
	AccumulatorHits     uint64 `json:"accumulator_hits"`
	AccumulatorMisses   uint64 `json:"accumulator_misses"`
	ContributionHits    uint64 `json:"contribution_hits"`
	ContributionMisses  uint64 `json:"contribution_misses"`
	Evictions           uint64 `json:"evictions"`
}

func NewComputationCache(mb int, mode string) (*ComputationCache, error) {
	if mb < 0 || mb > 1<<20 {
		return nil, errors.New("authentication cache MiB must be in [0,1048576]")
	}
	if mode != "unified" && mode != "accumulator" && mode != "contribution" {
		return nil, errors.New("authentication cache mode must be unified, accumulator or contribution")
	}
	if mb == 0 {
		return nil, nil
	}
	c := &ComputationCache{mode: mode}
	for i := range c.shards {
		sh := &c.shards[i]
		sh.limit = uint64(mb) * 1024 * 1024 / uint64(len(c.shards))
		sh.lru = lru.NewBasicLRU[string, computationEntry](int(sh.limit/512) + 1)
	}
	return c, nil
}
func (c *ComputationCache) enabled(kind byte) bool {
	return c != nil && (c.mode == "unified" || kind == 0 && c.mode == "accumulator" || kind == 1 && c.mode == "contribution")
}
func (c *ComputationCache) get(key string, kind byte) (group.Element, bool) {
	p, ok := c.getValue(key, kind)
	if !ok {
		return nil, false
	}
	return p.(group.Element), true
}
func (c *ComputationCache) getValue(key string, kind byte) (interface{}, bool) {
	sh := &c.shards[xxhash.Sum64String(key)%uint64(len(c.shards))]
	sh.Lock()
	e, ok := sh.lru.Get(key)
	if ok {
		sh.hits[kind]++
	} else {
		sh.misses[kind]++
	}
	sh.Unlock()
	return e.point, ok // caller must not mutate the cache-owned point
}
func (c *ComputationCache) put(key string, kind byte, p group.Element) {
	c.putValue(key, kind, p)
}
func (c *ComputationCache) putValue(key string, kind byte, p interface{}) {
	sh := &c.shards[xxhash.Sum64String(key)%uint64(len(c.shards))]
	cost := uint64(512 + len(key))
	sh.Lock()
	defer sh.Unlock()
	if sh.lru.Contains(key) || cost > sh.limit {
		return
	}
	for sh.used+cost > sh.limit {
		old, e, ok := sh.lru.RemoveOldest()
		if !ok {
			break
		}
		sh.used -= uint64(512 + len(old))
		sh.entries[e.kind]--
		sh.evictions++
	}
	var owned interface{}
	switch p := p.(type) {
	case group.Element:
		owned = p.Copy()
	case interface{ copyForCache() interface{} }:
		owned = p.copyForCache()
	default:
		panic("unsupported authentication cache point")
	}
	sh.lru.Add(key, computationEntry{point: owned, kind: kind})
	sh.used += cost
	sh.entries[kind]++
}
func (c *ComputationCache) Stats() (out ComputationCacheStats) {
	if c == nil {
		return out
	}
	for i := range c.shards {
		sh := &c.shards[i]
		sh.Lock()
		out.CapacityBytes += sh.limit
		out.AccountedBytes += sh.used
		out.AccumulatorEntries += sh.entries[0]
		out.ContributionEntries += sh.entries[1]
		out.AccumulatorHits += sh.hits[0]
		out.AccumulatorMisses += sh.misses[0]
		out.ContributionHits += sh.hits[1]
		out.ContributionMisses += sh.misses[1]
		out.Evictions += sh.evictions
		sh.Unlock()
	}
	return out
}

type Commitment [32]byte
type Child struct {
	Present bool
	C       Commitment
}
type Change struct {
	Slot     uint8
	Old, New Child
}

// Backend is the only accumulator-specific boundary. ID must uniquely specify
// the algorithm, parameters, domain separation and canonical encoding. A new
// algorithm needs a new ID, a new DB and rebuilt roots; no live suite switching.
// Apply uses an identity accumulator when previous is nil. Implementations must
// reject noncanonical states, must not mutate arguments, and must be concurrent-safe.
// Supporting this API does not establish an implementation's cryptographic security.
type Backend interface {
	ID() string
	Size() int
	Validate(state []byte) error
	Apply(previous []byte, remove, add [][]byte) ([]byte, error)
}

// Set only by the optional CGO GLS254 implementation. Ordinary builds retain
// their existing dependencies and reject GLS254 explicitly instead of falling back.
var newGLS254Backend func() (Backend, error)

type curveBackend struct {
	g           group.Group
	id          string
	dst         []byte
	size        int
	fixed       map[string]group.Element // immutable, only the 16 empty-slot constants
	cache       *ComputationCache
	cachePrefix string // complete scheme domain (including trie type)
}

func (b *curveBackend) ID() string { return b.id }
func (b *curveBackend) Size() int  { return b.size }
func (b *curveBackend) decode(raw []byte) (group.Element, error) {
	return b.decodePoint(raw, true)
}

// Cached points are immutable. Apply needs a private copy, while Validate only
// checks membership and may borrow the point without allocating a copy.
func (b *curveBackend) decodePoint(raw []byte, mutable bool) (group.Element, error) {
	if len(raw) != b.size {
		return nil, errors.New("accumulator length")
	}
	var key string
	if b.cache.enabled(0) {
		key = b.cachePrefix + "\x00" + string(raw)
		if p, ok := b.cache.get(key, 0); ok {
			if mutable {
				return p.Copy(), nil
			}
			return p, nil
		}
	}
	p := b.g.NewElement()
	if b.g == group.P256 && bytes.Equal(raw, make([]byte, 33)) {
		if key != "" {
			b.cache.put(key, 0, p)
		}
		return p, nil
	}
	if err := p.UnmarshalBinary(raw); err != nil {
		return nil, err
	}
	// go-ristretto's decoder already rejects noncanonical encodings and
	// invalid representatives (RFC 9496 section 4.3.1). Keep the roundtrip
	// check for other curves, whose decoders may accept alternate encodings.
	if b.g != group.Ristretto255 {
		encoded, err := b.encode(p)
		if err != nil || !bytes.Equal(encoded, raw) {
			return nil, errors.New("noncanonical accumulator")
		}
	}
	if key != "" {
		b.cache.put(key, 0, p)
	}
	return p, nil
}
func (b *curveBackend) encode(p group.Element) ([]byte, error) {
	if b.g == group.P256 && p.IsIdentity() {
		return make([]byte, 33), nil
	}
	return p.MarshalBinaryCompress()
}
func (b *curveBackend) Validate(raw []byte) error { _, err := b.decodePoint(raw, false); return err }
func (b *curveBackend) Apply(previous []byte, remove, add [][]byte) ([]byte, error) {
	s := b.g.NewElement()
	if previous != nil {
		var err error
		s, err = b.decode(previous)
		if err != nil {
			return nil, err
		}
	}
	contribution := func(m []byte) group.Element {
		if p := b.fixed[string(m)]; p != nil {
			return p
		}
		if !b.cache.enabled(1) {
			return b.g.HashToElement(m, b.dst)
		}
		key := b.cachePrefix + "\x01" + string(m)
		if p, ok := b.cache.get(key, 1); ok {
			return p
		}
		p := b.g.HashToElement(m, b.dst)
		b.cache.put(key, 1, p)
		return p
	}
	neg := b.g.NewElement()
	for _, m := range remove {
		neg.Neg(contribution(m))
		s.Add(s, neg)
	}
	for _, m := range add {
		s.Add(s, contribution(m))
	}
	raw, err := b.encode(s)
	if err == nil && b.cache.enabled(0) {
		// Seed at creation: the next block can reuse this point on its first read.
		b.cache.put(b.cachePrefix+"\x00"+string(raw), 0, s)
	}
	return raw, err
}

// Scheme binds a backend to an immutable protocol and trie type. A nil backend
// is the full-child Keccak myHash control, not an incremental accumulator.
type Scheme struct {
	backend Backend
	id      string
	kind    uint8
	domain  []byte
	empty   Commitment
}

func NewScheme(id string, kind uint8) (*Scheme, error) {
	var b Backend
	switch id {
	case Ristretto255:
		b = &curveBackend{g: group.Ristretto255, id: id, size: 32, dst: []byte("incauth-v2-ristretto255_XMD:SHA-512_R255MAP_RO_")}
	case P256:
		b = &curveBackend{g: group.P256, id: id, size: 33, dst: []byte("incauth-v2-P256_XMD:SHA-256_SSWU_RO_")}
	case GLS254:
		if newGLS254Backend == nil {
			return nil, errors.New("GLS254 requires CGO on amd64 and a build with -tags gls254; run python3 build/gls254.py first")
		}
		var err error
		b, err = newGLS254Backend()
		if err != nil {
			return nil, err
		}
	case Keccak:
		if kind > Storage {
			return nil, errors.New("trie type")
		}
		s := newScheme(nil, id, kind)
		return s, nil
	default:
		return nil, fmt.Errorf("unknown authentication suite %q", id)
	}
	return NewWithBackend(b, kind)
}
func NewWithBackend(b Backend, kind uint8) (*Scheme, error) {
	if b == nil || len(b.ID()) == 0 || len(b.ID()) > 128 || b.Size() < 1 || b.Size() > 4096 || kind > Storage {
		return nil, errors.New("invalid backend descriptor")
	}
	return newScheme(b, b.ID(), kind), nil
}
func newScheme(b Backend, id string, kind uint8) *Scheme {
	d := append([]byte("incauth-v2\x00"), byte(len(id)>>8), byte(len(id)))
	d = append(d, []byte(id)...)
	d = append(d, kind)
	s := &Scheme{backend: b, id: id, kind: kind, domain: d}
	s.empty = s.digest(0, nil)
	if cb, ok := b.(*curveBackend); ok { // Do not mutate a caller's existing backend.
		clone := *cb
		clone.fixed = make(map[string]group.Element, 16)
		for i := uint8(0); i < 16; i++ {
			m := s.edge(i, Child{})
			clone.fixed[string(m)] = cb.g.HashToElement(m, cb.dst)
		}
		s.backend = &clone
	}
	if cb, ok := b.(interface{ bindScheme(*Scheme) Backend }); ok {
		s.backend = cb.bindScheme(s)
	}
	return s
}
func (s *Scheme) ID() string { return s.id }

// WithCache leaves the scheme and persisted authentication protocol unchanged.
// Built-in curves share one policy and budget; arbitrary backends retain their own checks.
func (s *Scheme) WithCache(cache *ComputationCache) *Scheme {
	clone := *s
	if b, ok := s.backend.(*curveBackend); ok {
		backend := *b
		backend.cache, backend.cachePrefix = cache, string(s.domain)
		clone.backend = &backend
	}
	if b, ok := s.backend.(interface {
		withCache(*ComputationCache, string) Backend
	}); ok {
		clone.backend = b.withCache(cache, string(s.domain))
	}
	return &clone
}
func (s *Scheme) Type() uint8 { return s.kind }
func (s *Scheme) StateSize() int {
	if s.backend == nil {
		return 32
	}
	return s.backend.Size()
}
func (s *Scheme) Empty() Commitment { return s.empty }

// Like geth's hasher pool, this holds scratch state, never node commitments.
// Each call owns its state exclusively; Scheme remains immutable and shareable.
var digestPool = sync.Pool{New: func() interface{} { return crypto.NewKeccakState() }}

func (s *Scheme) digest(tag byte, payload []byte) Commitment {
	h := digestPool.Get().(crypto.KeccakState)
	h.Reset()
	h.Write(s.domain)
	h.Write([]byte{tag})
	h.Write(payload)
	var c Commitment
	h.Read(c[:]) // No need to clone the state as Sum does: it is reset before reuse.
	digestPool.Put(h)
	return c
}
func pathBytes(path []byte) ([]byte, error) {
	if len(path) > 64 {
		return nil, errors.New("path length")
	}
	out := make([]byte, 1+(len(path)+1)/2)
	out[0] = byte(len(path))
	for i, n := range path {
		if n > 15 {
			return nil, errors.New("path nibble")
		}
		out[1+i/2] |= n << uint(4*(1-i%2))
	}
	return out, nil
}
func (s *Scheme) Leaf(path, value []byte) (Commitment, error) {
	p, err := pathBytes(path)
	if err != nil {
		return Commitment{}, err
	}
	if len(value) > maxValueSize {
		return Commitment{}, errors.New("value size")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	p = append(p, size[:]...)
	p = append(p, value...)
	return s.digest(1, p), nil
}
func (s *Scheme) Extension(path []byte, child Commitment) (Commitment, error) {
	if len(path) == 0 || child == s.empty {
		return Commitment{}, errors.New("empty extension")
	}
	p, err := pathBytes(path)
	if err != nil {
		return Commitment{}, err
	}
	return s.digest(2, append(p, child[:]...)), nil
}
func (s *Scheme) meta(mask uint16) []byte {
	p := append([]byte{}, s.domain...)
	return append(p, 0x10, byte(mask>>8), byte(mask))
}
func (s *Scheme) edge(slot uint8, c Child) []byte {
	p := append([]byte{}, s.domain...)
	p = append(p, 0x11, slot, 0)
	if c.Present {
		p[len(p)-1] = 1
		p = append(p, c.C[:]...)
	}
	return p
}
func (s *Scheme) validChild(c Child) bool { return !c.Present || c.C != s.empty }
func (s *Scheme) Branch(state []byte) (Commitment, error) {
	if len(state) != s.StateSize() {
		return Commitment{}, errors.New("branch state length")
	}
	if s.backend == nil {
		var c Commitment
		copy(c[:], state)
		return c, nil
	}
	if err := s.backend.Validate(state); err != nil {
		return Commitment{}, err
	}
	return s.digest(3, state), nil
}

// A built-in curve backend returns the canonical encoding of a freshly computed
// group element. Re-decoding that output adds no validation. Public Branch and
// pluggable backends retain their full checks, as do old states passed to Apply.
func (s *Scheme) computedBranch(state []byte) (Commitment, error) {
	if _, ok := s.backend.(*curveBackend); ok {
		return s.digest(3, state), nil
	}
	if _, ok := s.backend.(interface{ canonicalOutput() }); ok {
		return s.digest(3, state), nil
	}
	return s.Branch(state)
}
func (s *Scheme) Build(children [16]Child) ([]byte, Commitment, error) {
	var mask uint16
	for i, c := range children {
		if !s.validChild(c) {
			return nil, Commitment{}, errors.New("empty commitment in present slot")
		}
		if c.Present {
			mask |= 1 << i
		}
	}
	if bits.OnesCount16(mask) < 2 {
		return nil, Commitment{}, errors.New("noncanonical branch")
	}
	if s.backend == nil {
		p := []byte{byte(mask >> 8), byte(mask)}
		for _, c := range children {
			if c.Present {
				p = append(p, c.C[:]...)
			}
		}
		c := s.digest(3, p)
		return append([]byte{}, c[:]...), c, nil
	}
	add := [][]byte{s.meta(mask)}
	for i, c := range children {
		add = append(add, s.edge(uint8(i), c))
	}
	state, err := s.backend.Apply(nil, nil, add)
	if err != nil {
		return nil, Commitment{}, err
	}
	c, err := s.computedBranch(state)
	return state, c, err
}

// Update trusts old child commitments obtained from a previously verified local
// state. It is not a verifier for adversarial update witnesses.
func (s *Scheme) Update(state []byte, mask uint16, changes []Change) ([]byte, Commitment, uint16, error) {
	if s.backend == nil {
		return nil, Commitment{}, 0, ErrFullRequired
	}
	if bits.OnesCount16(mask) < 2 {
		return nil, Commitment{}, 0, errors.New("old branch mask")
	}
	newMask, seen := mask, uint16(0)
	var remove, add [][]byte
	for _, x := range changes {
		if x.Slot >= 16 || seen&(1<<x.Slot) != 0 || x.Old.Present != (mask&(1<<x.Slot) != 0) || !s.validChild(x.Old) || !s.validChild(x.New) {
			return nil, Commitment{}, 0, errors.New("invalid change")
		}
		seen |= 1 << x.Slot
		if x.New.Present {
			newMask |= 1 << x.Slot
		} else {
			newMask &^= 1 << x.Slot
		}
		if x.Old.Present == x.New.Present && (!x.Old.Present || x.Old.C == x.New.C) {
			continue
		}
		remove = append(remove, s.edge(x.Slot, x.Old))
		add = append(add, s.edge(x.Slot, x.New))
	}
	if bits.OnesCount16(newMask) < 2 {
		return nil, Commitment{}, 0, errors.New("branch must collapse")
	}
	if mask != newMask {
		remove = append(remove, s.meta(mask))
		add = append(add, s.meta(newMask))
	}
	next, err := s.backend.Apply(state, remove, add)
	if err != nil {
		return nil, Commitment{}, 0, err
	}
	c, err := s.computedBranch(next)
	return next, c, newMask, err
}
