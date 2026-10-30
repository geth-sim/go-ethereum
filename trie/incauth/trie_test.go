// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library, licensed under LGPL-3.0.

package incauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"

	"github.com/cespare/xxhash/v2"
	"github.com/cloudflare/circl/group"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/rlp"
	"golang.org/x/crypto/sha3"
)

// Optional backends run the same correctness/cache suite as the original curves.
func curveTestSuites() []string {
	ids := []string{Ristretto255, P256}
	if newGLS254Backend != nil {
		ids = append(ids, GLS254)
	}
	return ids
}

// Compare byte-budget eviction against a simple independent LRU model, including
// duplicate insertion, changing key lengths and eviction of several entries.
func TestComputationCacheReusePolicy(t *testing.T) {
	c, _ := NewComputationCache(1, "unified")
	const budget = 1700
	for i := range c.shards {
		c.shards[i].limit = budget
	}
	var keys []string
	for i := 0; len(keys) < 40; i++ {
		k := fmt.Sprintf("%d-%s", i, bytes.Repeat([]byte{'x'}, i%650))
		if xxhash.Sum64String(k)%64 == 0 {
			keys = append(keys, k)
		}
	}
	p := group.Ristretto255.Generator()
	var order []string
	kinds := make(map[string]byte)
	var used, evictions uint64
	rng := rand.New(rand.NewSource(17))
	for i := 0; i < 2000; i++ {
		key := keys[rng.Intn(len(keys))]
		if i%3 == 0 {
			_, got := c.get(key, 0)
			_, want := kinds[key]
			if got != want {
				t.Fatal("hit mismatch", i)
			}
			if want {
				for j, k := range order {
					if k == key {
						order = append(order[:j], order[j+1:]...)
						break
					}
				}
				order = append(order, key)
			}
		} else {
			kind := byte(i % 2)
			c.put(key, kind, p)
			if _, exists := kinds[key]; !exists {
				cost := uint64(512 + len(key))
				for used+cost > budget {
					old := order[0]
					order = order[1:]
					used -= uint64(512 + len(old))
					delete(kinds, old)
					evictions++
				}
				order = append(order, key)
				kinds[key] = kind
				used += cost
			}
		}
		sh := &c.shards[0]
		actual := sh.lru.Keys()
		if fmt.Sprint(actual) != fmt.Sprint(order) || sh.used != used || sh.evictions != evictions {
			t.Fatal("eviction policy/accounting changed", i, actual, order)
		}
		var counts [2]uint64
		for _, kind := range kinds {
			counts[kind]++
		}
		if sh.entries != counts {
			t.Fatal("entry accounting changed")
		}
	}
	key := keys[0]
	c.put(key, 0, p)
	borrowed, _ := c.get(key, 0)
	encoded, _ := borrowed.MarshalBinaryCompress()
	for _, key := range keys {
		c.put(key, 1, group.Ristretto255.Identity())
	}
	after, _ := borrowed.MarshalBinaryCompress()
	if !bytes.Equal(encoded, after) {
		t.Fatal("eviction mutated a borrowed point")
	}
}

// Reusing scratch must not overwrite published state or the immutable inputs.
// Compare the incremental result to a full build across all-slot/mask changes.
func TestBranchScratchOwnership(t *testing.T) {
	for _, id := range curveTestSuites() {
		s := testScheme(t, id)
		var children [16]Child
		for i := range children {
			children[i] = Child{true, Commitment{byte(i + 1)}}
		}
		state, root, err := s.Build(children)
		if err != nil {
			t.Fatal(err)
		}
		original := append([]byte(nil), state...)
		for round := 0; round < 50; round++ {
			next := children
			var changes []Change
			for i := range next {
				next[i] = Child{true, Commitment{byte(round + 19), byte(i)}}
				if i == round%16 {
					next[i] = Child{}
				}
				changes = append(changes, Change{uint8(i), children[i], next[i]})
			}
			gotState, gotRoot, _, err := s.Update(state, 65535, changes)
			if err != nil {
				t.Fatal(err)
			}
			wantState, wantRoot, err := s.Build(next)
			if err != nil || gotRoot != wantRoot || !bytes.Equal(gotState, wantState) {
				t.Fatal("scratch changed root", err)
			}
			if c, err := s.Branch(state); err != nil || c != root || !bytes.Equal(state, original) {
				t.Fatal("old state overwritten", err)
			}
			if _, _, _, err := s.Update([]byte{0xff}, 65535, changes); err == nil {
				t.Fatal("invalid state accepted")
			}
		}
	}
}

// The reference tests use a full-path hash locator, NOT an Outward throughput
// approximation. Auth roots must not depend on this physical layout.
func testEncoder(version uint32, path []byte, kind uint8) (Locator, error) {
	var v [4]byte
	binary.BigEndian.PutUint32(v[:], version)
	raw := append(v[:], kind)
	raw = append(raw, path...)
	return Locator(sha256.Sum256(raw)), nil
}
func testScheme(t testing.TB, id string) *Scheme {
	t.Helper()
	s, err := NewScheme(id, Storage)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func testTrie(t testing.TB, s *Scheme, p Policy, db ethdb.KeyValueStore) *Trie {
	t.Helper()
	tr, err := New(db, s, p, "test-full-path-hash-v1", testEncoder)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}
func mustRoot(t testing.TB, tr *Trie) Commitment {
	t.Helper()
	c, err := tr.Root()
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func key(i int) [32]byte { return sha256.Sum256([]byte(fmt.Sprintf("incauth-key-%d", i))) }
func value(i int) []byte { return []byte(fmt.Sprintf("value-%d", i)) }

func TestPublicHashToGroupVector(t *testing.T) {
	dst, _ := hex.DecodeString("564f50524630362d48617368546f47726f75702d000001")
	for _, v := range [][2]string{{"hello", "723c88cc59988d39889aa607b6696d423e7718a36d4825e0f940b3c3a534396a"}, {"world", "a47c0a13c42a26ab06e60d2e251ba591334a289f4fdfe3b17ed3321a9527f44c"}} {
		p := group.Ristretto255.HashToElement([]byte(v[0]), dst)
		got, err := p.MarshalBinaryCompress()
		if err != nil || hex.EncodeToString(got) != v[1] {
			t.Fatalf("public vector: %x %v", got, err)
		}
	}
}
func TestAccumulatorChanges(t *testing.T) {
	for _, id := range curveTestSuites() {
		t.Run(id, func(t *testing.T) {
			s := testScheme(t, id)
			var children [16]Child
			for i := 0; i < 12; i++ {
				children[i] = Child{true, Commitment(sha256.Sum256(value(i)))}
			}
			state, _, err := s.Build(children)
			if err != nil {
				t.Fatal(err)
			}
			saved := append([]byte{}, state...)
			changes := []Change{{2, children[2], Child{true, Commitment(sha256.Sum256(value(100)))}}, {5, children[5], Child{}}, {15, Child{}, Child{true, Commitment(sha256.Sum256(value(200)))}}}
			next, c, mask, err := s.Update(state, 0xfff, changes)
			if err != nil {
				t.Fatal(err)
			}
			for _, x := range changes {
				children[x.Slot] = x.New
			}
			want, wantC, err := s.Build(children)
			if err != nil || !bytes.Equal(next, want) || c != wantC || mask != 0x8fdf {
				t.Fatalf("incremental != full: %x %v", mask, err)
			}
			if !bytes.Equal(state, saved) {
				t.Fatal("old state mutated")
			}
			back := make([]Change, len(changes))
			for i, x := range changes {
				back[i] = Change{x.Slot, x.New, x.Old}
			}
			restored, _, _, err := s.Update(next, mask, back)
			if err != nil || !bytes.Equal(restored, state) {
				t.Fatalf("undo: %v", err)
			}
			if _, _, _, err = s.Update(state, 0xfff, append(changes, changes[0])); err == nil {
				t.Fatal("duplicate accepted")
			}
			if _, err = s.Branch(append(state, 0)); err == nil {
				t.Fatal("trailing point bytes")
			}
			if _, err = s.Branch(bytes.Repeat([]byte{255}, s.StateSize())); err == nil {
				t.Fatal("invalid point")
			}
		})
	}
}

type checkedOutputBackend struct {
	Backend
	invalid     bool
	validations int
}

func (b *checkedOutputBackend) Apply(old []byte, remove, add [][]byte) ([]byte, error) {
	if b.invalid {
		return bytes.Repeat([]byte{255}, b.Size()), nil
	}
	return b.Backend.Apply(old, remove, add)
}
func (b *checkedOutputBackend) Validate(state []byte) error {
	b.validations++
	return b.Backend.Validate(state)
}

func TestAccumulatorValidationBoundary(t *testing.T) {
	for _, id := range curveTestSuites() {
		s := testScheme(t, id)
		var children [16]Child
		children[0], children[1] = Child{true, Commitment{1}}, Child{true, Commitment{2}}
		state, c, err := s.Build(children)
		if err != nil {
			t.Fatal(err)
		}
		if want, err := s.Branch(state); err != nil || c != want {
			t.Fatal("computed and validated commitments differ")
		}
		invalid := bytes.Repeat([]byte{255}, s.StateSize())
		if _, err = s.Branch(invalid); err == nil {
			t.Fatal("public Branch accepted an invalid point")
		}
		if _, _, _, err = s.Update(invalid, 3, nil); err == nil {
			t.Fatal("Update accepted an invalid old state")
		}
		backend := &checkedOutputBackend{Backend: s.backend}
		external, err := NewWithBackend(backend, Storage)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = external.Build(children); err != nil || backend.validations != 1 {
			t.Fatal("pluggable backend output was not validated")
		}
		backend.invalid = true
		if _, _, err = external.Build(children); err == nil {
			t.Fatal("invalid backend Build output accepted")
		}
		if _, _, _, err = external.Update(state, 3, nil); err == nil {
			t.Fatal("invalid backend Update output accepted")
		}
	}
}

func TestDigestMatchesKeccak(t *testing.T) {
	for _, id := range append(curveTestSuites(), Keccak) {
		for _, kind := range []uint8{Account, Storage} {
			s, err := NewScheme(id, kind)
			if err != nil {
				t.Fatal(err)
			}
			for tag := byte(0); tag < 4; tag++ {
				for _, size := range []int{0, 1, 32, 135, 136, 137, 4096} {
					payload := bytes.Repeat([]byte{tag + 1}, size)
					h := sha3.NewLegacyKeccak256()
					h.Write(s.domain)
					h.Write([]byte{tag})
					h.Write(payload)
					got := s.digest(tag, payload)
					if !bytes.Equal(got[:], h.Sum(nil)) {
						t.Fatalf("digest changed: %s kind=%d tag=%d size=%d", id, kind, tag, size)
					}
				}
			}
		}
	}
}
func checkContents(t *testing.T, tr *Trie, want map[[32]byte][]byte) {
	t.Helper()
	root := mustRoot(t, tr)
	audit, err := tr.Audit()
	if err != nil || root != audit {
		t.Fatalf("audit %v", err)
	}
	for k, v := range want {
		got, ok, err := tr.Get(k)
		if err != nil || !ok || !bytes.Equal(got, v) {
			t.Fatalf("get %x: %v", k, err)
		}
		p, err := tr.Prove(k)
		if err != nil {
			t.Fatal(err)
		}
		got, ok, err = Verify(tr.auth, root, k, p)
		if err != nil || !ok || !bytes.Equal(got, v) {
			t.Fatalf("proof %v", err)
		}
	}
	k := key(1000000)
	p, err := tr.Prove(k)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Verify(tr.auth, root, k, p); err != nil || ok {
		t.Fatalf("absence %v", err)
	}
}
func TestTrieStructureAndHistory(t *testing.T) {
	for _, id := range append(curveTestSuites(), Keccak) {
		t.Run(id, func(t *testing.T) {
			s := testScheme(t, id)
			db := memorydb.New()
			tr := testTrie(t, s, Incremental, db)
			ref := testTrie(t, s, Full, memorydb.New())
			want := map[[32]byte][]byte{}
			var keys [7][32]byte
			keys[1][31] = 1
			keys[2][31] = 2
			keys[3][0] = 0x10
			keys[4][0] = 0x11
			keys[5][0] = 0xf0
			keys[6][15] = 0x80
			for i, k := range keys {
				if err := tr.Update(k, value(i)); err != nil {
					t.Fatal(err)
				}
				if err := ref.Update(k, value(i)); err != nil {
					t.Fatal(err)
				}
				want[k] = value(i)
				if mustRoot(t, tr) != mustRoot(t, ref) {
					t.Fatal("full mismatch")
				}
				checkContents(t, tr, want)
			}
			old := tr.Fork()
			oldC := mustRoot(t, old)
			root, err := tr.Commit(1)
			if err != nil {
				t.Fatal(err)
			}
			tr, err = Open(db, s, Incremental, tr.layout, testEncoder, 1)
			if err != nil {
				t.Fatal(err)
			}
			checkContents(t, tr, want)
			for i := range keys {
				if err := tr.Update(keys[i], value(i+100)); err != nil {
					t.Fatal(err)
				}
				if err := tr.Update(keys[i], value(i+200)); err != nil {
					t.Fatal(err)
				}
				want[keys[i]] = value(i + 200)
			}
			if mustRoot(t, old) != oldC {
				t.Fatal("CoW snapshot changed")
			}
			if _, err = tr.Commit(2); err != nil {
				t.Fatal(err)
			}
			historical, err := Open(db, s, Incremental, tr.layout, testEncoder, 1)
			if err != nil || mustRoot(t, historical) != root.Commitment {
				t.Fatalf("history %v", err)
			}
			for _, k := range keys {
				if err := tr.Delete(k); err != nil {
					t.Fatal(err)
				}
				delete(want, k)
				checkContents(t, tr, want)
			}
			if mustRoot(t, tr) != s.Empty() {
				t.Fatal("empty root")
			}
			if _, err = tr.Commit(3); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(db, s, Incremental, tr.layout, testEncoder, 3)
			if err != nil || mustRoot(t, reopened) != s.Empty() {
				t.Fatalf("empty reopen %v", err)
			}
			if _, err = tr.Commit(3); err == nil {
				t.Fatal("version reuse")
			}
		})
	}
}
func TestRandomUpdatesAndSnapshotRebuild(t *testing.T) {
	ids := []string{Ristretto255}
	if newGLS254Backend != nil {
		ids = append(ids, GLS254)
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) { testRandomUpdatesAndSnapshotRebuild(t, id) })
	}
}
func testRandomUpdatesAndSnapshotRebuild(t *testing.T, id string) {
	s := testScheme(t, id)
	db := memorydb.New()
	tr := testTrie(t, s, Incremental, db)
	ref := testTrie(t, s, Full, memorydb.New())
	want := map[[32]byte][]byte{}
	rng := rand.New(rand.NewSource(20260930))
	for i := 0; i < 180; i++ {
		k := key(rng.Intn(48))
		if rng.Intn(4) == 0 {
			if err := tr.Delete(k); err != nil {
				t.Fatal(err)
			}
			if err := ref.Delete(k); err != nil {
				t.Fatal(err)
			}
			delete(want, k)
		} else {
			v := value(rng.Intn(100))
			if err := tr.Update(k, v); err != nil {
				t.Fatal(err)
			}
			if err := ref.Update(k, v); err != nil {
				t.Fatal(err)
			}
			want[k] = v
		}
		if mustRoot(t, tr) != mustRoot(t, ref) {
			t.Fatalf("update %d differs", i)
		}
		if i%30 == 29 {
			if _, err := tr.Commit(uint32(i + 1)); err != nil {
				t.Fatal(err)
			}
			var err error
			tr, err = Open(db, s, Incremental, tr.layout, testEncoder, uint32(i+1))
			if err != nil {
				t.Fatal(err)
			}
			checkContents(t, tr, want)
		}
	}
	// Rebuild a current snapshot without genesis/history, in an unrelated order.
	rebuilt := testTrie(t, s, Full, memorydb.New())
	for k, v := range want {
		if err := rebuilt.Update(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if mustRoot(t, rebuilt) != mustRoot(t, tr) {
		t.Fatal("snapshot root")
	}
	rebuilt.layout = "alternate-locators"
	rebuilt.encode = func(v uint32, p []byte, k uint8) (Locator, error) {
		a, _ := testEncoder(v, p, k)
		a[0] ^= 0x80
		return a, nil
	}
	if _, err := rebuilt.Commit(400); err != nil {
		t.Fatal(err)
	}
	if mustRoot(t, rebuilt) != mustRoot(t, tr) {
		t.Fatal("locator affects authentication")
	}
}
func TestReadAvoidance(t *testing.T) {
	ids := []string{Ristretto255}
	if newGLS254Backend != nil {
		ids = append(ids, GLS254)
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) { testReadAvoidance(t, id) })
	}
}
func testReadAvoidance(t *testing.T, id string) {
	s := testScheme(t, id)
	db := memorydb.New()
	base := testTrie(t, s, Incremental, db)
	for i := 0; i < 16; i++ {
		k := [32]byte{byte(i << 4)}
		if err := base.Update(k, value(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := base.Commit(1); err != nil {
		t.Fatal(err)
	}
	inc, err := Open(db, s, Incremental, base.layout, testEncoder, 1)
	if err != nil {
		t.Fatal(err)
	}
	full, err := Open(db, s, Full, base.layout, testEncoder, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range []*Trie{inc, full} {
		if err := tr.Update([32]byte{}, []byte("changed")); err != nil {
			t.Fatal(err)
		}
	}
	if mustRoot(t, inc) != mustRoot(t, full) {
		t.Fatal("roots differ")
	}
	a, b := inc.Stats(), full.Stats()
	if a.Reads != 2 || a.AuthReads != 0 || b.Reads != 17 || b.AuthReads != 15 {
		t.Fatalf("read counts incremental=%+v full=%+v", a, b)
	}
	t.Logf("cold decoded-node cache: incremental %+v; full %+v", a, b)
}
func TestProofTamperingAndDomainSeparation(t *testing.T) {
	ids := []string{Ristretto255}
	if newGLS254Backend != nil {
		ids = append(ids, GLS254)
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) { testProofTamperingAndDomainSeparation(t, id) })
	}
}
func testProofTamperingAndDomainSeparation(t *testing.T, id string) {
	s := testScheme(t, id)
	legacy := memorydb.New()
	if err := legacy.Put([]byte("legacy-node"), []byte("data")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(legacy, s, Incremental, "test-full-path-hash-v1", testEncoder); err == nil {
		t.Fatal("accepted legacy DB without authentication schema")
	}
	tr := testTrie(t, s, Incremental, memorydb.New())
	for i := 0; i < 20; i++ {
		if err := tr.Update(key(i), value(i)); err != nil {
			t.Fatal(err)
		}
	}
	root := mustRoot(t, tr)
	raw, err := tr.Prove(key(2))
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*Proof){func(p *Proof) { p.Suite = P256 }, func(p *Proof) { p.Type = Account }, func(p *Proof) { p.Key = key(3) }, func(p *Proof) { p.Frames = p.Frames[:len(p.Frames)-1] }, func(p *Proof) { p.Frames = append(p.Frames, p.Frames[len(p.Frames)-1]) }, func(p *Proof) { p.Frames[0].Children[0][0] ^= 1 }, func(p *Proof) {
		p.Frames[0].Children[0], p.Frames[0].Children[1] = p.Frames[0].Children[1], p.Frames[0].Children[0]
	}, func(p *Proof) { p.Frames[len(p.Frames)-1].Value = []byte("forged") }, func(p *Proof) { p.Frames[len(p.Frames)-1].Path[0] = 16 }}
	for i, change := range mutations {
		var p Proof
		if err := rlp.DecodeBytes(raw, &p); err != nil {
			t.Fatal(err)
		}
		change(&p)
		bad, _ := rlp.EncodeToBytes(p)
		if _, _, err := Verify(s, root, key(2), bad); err == nil {
			t.Fatalf("accepted mutation %d", i)
		}
	}
	for _, bad := range [][]byte{append(append([]byte{}, raw...), 0), raw[:len(raw)-1], bytes.Repeat([]byte{0}, maxProofSize+1)} {
		if _, _, err := Verify(s, root, key(2), bad); err == nil {
			t.Fatal("malformed proof")
		}
	}
	account, _ := NewScheme(id, Account)
	if _, _, err := Verify(account, root, key(2), raw); err == nil {
		t.Fatal("trie type accepted")
	}
	other := testScheme(t, P256)
	if _, _, err := Verify(other, root, key(2), raw); err == nil {
		t.Fatal("suite accepted")
	}
	if _, err = tr.Commit(1); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(tr.db, other, Incremental, tr.layout, testEncoder, 1); err == nil {
		t.Fatal("cross-suite DB")
	}
	if _, err = Open(tr.db, s, Incremental, "wrong-layout", testEncoder, 1); err == nil {
		t.Fatal("cross-layout DB")
	}
}

// Exercise the untrusted proof boundary, including the three non-membership
// endings: absent branch slot, mismatched extension, and mismatched leaf path.
func FuzzProofVerification(f *testing.F) {
	s := testScheme(f, Ristretto255)
	tr := testTrie(f, s, Incremental, memorydb.New())
	var queries [4][32]byte
	queries[0][0] = 0xaa
	queries[1][0] = 0xac
	queries[2][0] = 0xba
	queries[3][0], queries[3][31] = 0xaa, 1
	for _, prefix := range []byte{0xaa, 0xab} {
		var k [32]byte
		k[0] = prefix
		if err := tr.Update(k, []byte{prefix}); err != nil {
			f.Fatal(err)
		}
	}
	root, err := tr.Root()
	if err != nil {
		f.Fatal(err)
	}
	for i, k := range queries {
		proof, err := tr.Prove(k)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(proof, uint8(i))
	}
	f.Add([]byte{}, uint8(0))
	f.Add([]byte{0xff, 0, 1}, uint8(1))
	f.Fuzz(func(t *testing.T, raw []byte, query uint8) {
		idx := int(query) % len(queries)
		v, exists, err := Verify(s, root, queries[idx], raw)
		if err != nil {
			return
		}
		if exists != (idx == 0) || (exists && !bytes.Equal(v, []byte{0xaa})) || (!exists && len(v) != 0) {
			t.Fatal("accepted a false membership/non-membership claim")
		}
	})
}

func TestLevelDBReopen(t *testing.T) {
	ids := []string{Ristretto255}
	if newGLS254Backend != nil {
		ids = append(ids, GLS254)
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) { testLevelDBReopen(t, id) })
	}
}
func testLevelDBReopen(t *testing.T, id string) {
	dir := t.TempDir()
	db, err := leveldb.New(dir, 16, 16, "incauth-test", false)
	if err != nil {
		t.Fatal(err)
	}
	s := testScheme(t, id)
	tr := testTrie(t, s, Incremental, db)
	for i := 0; i < 16; i++ {
		if err = tr.Update(key(i), value(i)); err != nil {
			t.Fatal(err)
		}
	}
	root, err := tr.Commit(1)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = leveldb.New(dir, 16, 16, "incauth-test", false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tr, err = Open(db, s, Incremental, tr.layout, testEncoder, 1)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := tr.Audit()
	if err != nil || audit != root.Commitment {
		t.Fatalf("reopen audit %v", err)
	}
	if err = tr.Update(key(3), []byte("after reopen")); err != nil {
		t.Fatal(err)
	}
	if _, err = tr.Commit(2); err != nil {
		t.Fatal(err)
	}
}

func TestAccountStorageProofBinding(t *testing.T) {
	ids := []string{Ristretto255}
	if newGLS254Backend != nil {
		ids = append(ids, GLS254)
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) { testAccountStorageProofBinding(t, id) })
	}
}
func testAccountStorageProofBinding(t *testing.T, id string) {
	storageScheme := testScheme(t, id)
	accountScheme, err := NewScheme(id, Account)
	if err != nil {
		t.Fatal(err)
	}
	storage := testTrie(t, storageScheme, Incremental, memorydb.New())
	accounts := testTrie(t, accountScheme, Incremental, memorydb.New())
	type accountValue struct {
		Nonce    uint64
		Balance  []byte
		Storage  Commitment
		CodeHash [32]byte
	}
	account := accountValue{Nonce: 1, Balance: []byte{10}, CodeHash: sha256.Sum256([]byte("test code"))}
	if err = storage.Update(key(1), []byte{42}); err != nil {
		t.Fatal(err)
	}
	account.Storage = mustRoot(t, storage)
	payload, _ := rlp.EncodeToBytes(account)
	if err = accounts.Update(key(100), payload); err != nil {
		t.Fatal(err)
	}
	stateRoot := mustRoot(t, accounts)
	ap, err := accounts.Prove(key(100))
	if err != nil {
		t.Fatal(err)
	}
	sp, err := storage.Prove(key(1))
	if err != nil {
		t.Fatal(err)
	}
	value, exists, err := Verify(accountScheme, stateRoot, key(100), ap)
	if err != nil || !exists {
		t.Fatal("account proof", err)
	}
	var verified accountValue
	if err = rlp.DecodeBytes(value, &verified); err != nil {
		t.Fatal(err)
	}
	value, exists, err = Verify(storageScheme, verified.Storage, key(1), sp)
	if err != nil || !exists || !bytes.Equal(value, []byte{42}) {
		t.Fatal("storage proof", err)
	}
	if err = storage.Update(key(1), []byte{43}); err != nil {
		t.Fatal(err)
	}
	newProof, err := storage.Prove(key(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = Verify(storageScheme, verified.Storage, key(1), newProof); err == nil {
		t.Fatal("new storage accepted by old account root")
	}
	account.Storage = mustRoot(t, storage)
	payload, _ = rlp.EncodeToBytes(account)
	if err = accounts.Update(key(100), payload); err != nil {
		t.Fatal(err)
	}
	if mustRoot(t, accounts) == stateRoot {
		t.Fatal("storage change did not propagate to account root")
	}
}

type failingDB struct {
	ethdb.KeyValueStore
	fail bool
}
type failingBatch struct {
	ethdb.Batch
	db *failingDB
}

func (d *failingDB) NewBatch() ethdb.Batch { return &failingBatch{d.KeyValueStore.NewBatch(), d} }
func (b *failingBatch) Write() error {
	if b.db.fail {
		return errors.New("injected batch failure")
	}
	return b.Batch.Write()
}
func TestCommitFailureAndCorruption(t *testing.T) {
	ids := []string{Ristretto255}
	if newGLS254Backend != nil {
		ids = append(ids, GLS254)
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) { testCommitFailureAndCorruption(t, id) })
	}
}
func testCommitFailureAndCorruption(t *testing.T, id string) {
	db := &failingDB{KeyValueStore: memorydb.New(), fail: true}
	s := testScheme(t, id)
	tr := testTrie(t, s, Incremental, db)
	for i := 0; i < 3; i++ {
		if err := tr.Update(key(i), value(i)); err != nil {
			t.Fatal(err)
		}
	}
	before := mustRoot(t, tr)
	if _, err := tr.Commit(1); err == nil {
		t.Fatal("expected failure")
	}
	if mustRoot(t, tr) != before {
		t.Fatal("failed commit mutated root")
	}
	if has, _ := db.Has(rootKey(1)); has {
		t.Fatal("published failed root")
	}
	db.fail = false
	r, err := tr.Commit(1)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.Get(r.Locator[:])
	if err != nil {
		t.Fatal(err)
	}
	n, err := tr.decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if n.kind != BranchNode {
		t.Fatal("fixture")
	}
	// Valid curve bytes with incorrect routing still require a full audit.
	for i, c := range n.kids {
		if !c.empty() {
			for j, d := range n.kids {
				if i != j && !d.empty() {
					n.kids[i], n.kids[j] = n.kids[j], n.kids[i]
					break
				}
			}
			break
		}
	}
	bad, _ := tr.encodeNode(n)
	if err = db.Put(r.Locator[:], bad); err != nil {
		t.Fatal(err)
	}
	if _, err = tr.Audit(); err == nil {
		t.Fatal("corrupt routing accepted by audit")
	}
}
func TestConcurrentForks(t *testing.T) {
	ids := []string{Ristretto255}
	if newGLS254Backend != nil {
		ids = append(ids, GLS254)
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) { testConcurrentForks(t, id) })
	}
}
func testConcurrentForks(t *testing.T, id string) {
	s := testScheme(t, id)
	tr := testTrie(t, s, Incremental, memorydb.New())
	for i := 0; i < 16; i++ {
		if err := tr.Update(key(i), value(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tr.Commit(1); err != nil {
		t.Fatal(err)
	}
	before := mustRoot(t, tr)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		fork := tr.Fork()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := fork.Update(key(i), value(100+i)); err != nil {
				t.Error(err)
			}
			if _, err := fork.Audit(); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if mustRoot(t, tr) != before {
		t.Fatal("fork mutated base")
	}
}
func FuzzProofDecoder(f *testing.F) {
	s := testScheme(f, Ristretto255)
	tr := testTrie(f, s, Incremental, memorydb.New())
	for i := 0; i < 3; i++ {
		if err := tr.Update(key(i), value(i)); err != nil {
			f.Fatal(err)
		}
	}
	root := mustRoot(f, tr)
	p, err := tr.Prove(key(1))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(p)
	f.Add([]byte{0xc0})
	f.Fuzz(func(t *testing.T, raw []byte) { _, _, _ = Verify(s, root, key(1), raw) })
}

func BenchmarkTrieUpdate(b *testing.B) {
	for _, test := range []struct {
		name, id string
		policy   Policy
	}{{"R255Incremental", Ristretto255, Incremental}, {"R255Full", Ristretto255, Full}, {"P256Incremental", P256, Incremental}, {"KeccakMyHash", Keccak, Full}} {
		for _, engine := range []string{"Memory", "LevelDBWarmSST"} {
			b.Run(test.name+"/"+engine, func(b *testing.B) {
				s := testScheme(b, test.id)
				var db ethdb.KeyValueStore = memorydb.New()
				if engine == "LevelDBWarmSST" {
					var err error
					db, err = leveldb.New(b.TempDir(), 16, 16, "incauth-benchmark", false)
					if err != nil {
						b.Fatal(err)
					}
				}
				defer db.Close()
				base := testTrie(b, s, Incremental, db)
				for i := 0; i < 256; i++ {
					if err := base.Update(key(i), value(i)); err != nil {
						b.Fatal(err)
					}
				}
				if _, err := base.Commit(1); err != nil {
					b.Fatal(err)
				}
				if engine == "LevelDBWarmSST" {
					if err := db.Compact(nil, nil); err != nil {
						b.Fatal(err)
					}
					// Deliberately warm this tiny SST. This is not a cold-SSD model.
					for i := 0; i < 256; i++ {
						if _, _, err := base.Get(key(i)); err != nil {
							b.Fatal(err)
						}
					}
				}
				base.policy = test.policy
				var reads, extra uint64
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					tr := base.Fork()
					if err := tr.Update(key(i%256), value(i+1000)); err != nil {
						b.Fatal(err)
					}
					if _, err := tr.Root(); err != nil {
						b.Fatal(err)
					}
					reads += tr.stats.Reads
					extra += tr.stats.AuthReads
				}
				b.StopTimer()
				b.ReportMetric(float64(reads)/float64(b.N), "gets/op")
				b.ReportMetric(float64(extra)/float64(b.N), "authgets/op")
			})
		}
	}
}

func TestComputationCache(t *testing.T) {
	for _, mode := range []string{"accumulator", "contribution", "unified"} {
		t.Run(mode, func(t *testing.T) {
			cache, err := NewComputationCache(1, mode)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range curveTestSuites() {
				for _, kind := range []uint8{Account, Storage} {
					plain, _ := NewScheme(id, kind)
					cached := plain.WithCache(cache)
					var children [16]Child
					for i := 0; i < 4; i++ {
						children[i] = Child{Present: true, C: Commitment(sha256.Sum256([]byte{byte(i)}))}
					}
					state, root, err := plain.Build(children)
					if err != nil {
						t.Fatal(err)
					}
					gotState, gotRoot, err := cached.Build(children)
					if err != nil || root != gotRoot || !bytes.Equal(state, gotState) {
						t.Fatal("cached build changed", err)
					}
					for round := 0; round < 80; round++ {
						slot := uint8(round % 4)
						next := Child{Present: true, C: Commitment(sha256.Sum256([]byte{slot, byte(round), 17}))}
						change := []Change{{Slot: slot, Old: children[slot], New: next}}
						wantState, want, _, err := plain.Update(state, 15, change)
						if err != nil {
							t.Fatal(err)
						}
						for repeat := 0; repeat < 2; repeat++ {
							gs, gr, _, e := cached.Update(state, 15, change)
							if e != nil || gr != want || !bytes.Equal(gs, wantState) {
								t.Fatal("cached point mutated or domain mixed", e)
							}
							if c, e := cached.Branch(state); e != nil || c != root {
								t.Fatal("old state mutated", e)
							}
						}
						state, root = wantState, want
						children[slot] = next
					}
					for i := 0; i < 2; i++ {
						if _, e := cached.Branch(bytes.Repeat([]byte{255}, cached.StateSize())); e == nil {
							t.Fatal("invalid encoding accepted")
						}
						if _, e := cached.Branch(state[:len(state)-1]); e == nil {
							t.Fatal("invalid length accepted")
						}
					}
				}
			}
			stats := cache.Stats()
			if stats.AccountedBytes > stats.CapacityBytes {
				t.Fatal("cache budget exceeded")
			}
			if mode != "contribution" && stats.AccumulatorHits == 0 {
				t.Fatal("accumulator cache unused")
			}
			if mode != "accumulator" && stats.ContributionHits == 0 {
				t.Fatal("contribution cache unused")
			}
		})
	}
}

func TestComputationCacheEvictionAndConcurrency(t *testing.T) {
	cache, _ := NewComputationCache(1, "unified")
	p := group.Ristretto255.NewElement()
	for i := 0; i < 5000; i++ {
		cache.put(fmt.Sprintf("key-%d", i), byte(i%2), p)
	}
	stats := cache.Stats()
	if stats.Evictions == 0 || stats.AccountedBytes > stats.CapacityBytes || stats.AccumulatorEntries == 0 || stats.ContributionEntries == 0 {
		t.Fatal(stats)
	}
	plain, _ := NewScheme(Ristretto255, Storage)
	cached := plain.WithCache(cache)
	var cs [16]Child
	for i := 0; i < 3; i++ {
		cs[i] = Child{Present: true, C: Commitment(sha256.Sum256([]byte{byte(i)}))}
	}
	state, _, _ := cached.Build(cs)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				ch := []Change{{Slot: 0, Old: cs[0], New: Child{Present: true, C: Commitment(sha256.Sum256([]byte{byte(w), byte(i), 88}))}}}
				want, c, _, e := plain.Update(state, 7, ch)
				if e != nil {
					t.Error(e)
					return
				}
				got, d, _, e := cached.Update(state, 7, ch)
				if e != nil || c != d || !bytes.Equal(want, got) {
					t.Error("concurrent cache changed root", e)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
}

// Published invalid Ristretto vectors cover field canonicality, sign and
// group-representative checks retained after removing the redundant re-encode.
func TestRistrettoCanonicalDecode(t *testing.T) {
	s, _ := NewScheme(Ristretto255, Storage)
	b := s.backend.(*curveBackend)
	bad := []string{"00ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", "f3ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", "edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", "0100000000000000000000000000000000000000000000000000000000000000", "01ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", "ed57ffd8c914fb201471d1c3d245ce3c746fcbe63a3679d51b6a516ebebe0e20", "c34c4e1826e5d403b78e246e88aa051c36ccf0aafebffe137d148a2bf9104562", "c940e5a4404157cfb1628b108db051a8d439e1a421394ec4ebccb9ec92a8ac78", "47cfc5497c53dc8e61c91d17fd626ffb1c49e2bca94eed052281b510b1117a24", "f1c6165d33367351b0da8f6e4511010c68174a03b6581212c71c0e1d026c3c72", "87260f7a2f12495118360f02c26a470f450dadf34a413d21042b43b9d93e1309", "26948d35ca62e643e26a83177332e6b6afeb9d08e4268b650f1f5bbd8d81d371", "4eac077a713c57b4f4397629a4145982c661f48044dd3f96427d40b147d9742f", "de6a7b00deadc788eb6b6c8d20c0ae96c2f2019078fa604fee5b87d6e989ad7b", "bcab477be20861e01e4a0e295284146a510150d9817763caf1a6f4b422d67042", "2a292df7e32cababbd9de088d1d1abec9fc0440f637ed2fba145094dc14bea08", "f4a9e534fc0d216c44b218fa0c42d99635a0127ee2e53c712f70609649fdff22", "8268436f8c4126196cf64b3c7ddbda90746a378625f9813dd9b8457077256731", "2810e5cbc2cc4d4eece54f61c6f69758e289aa7ab440b3cbeaa21995c2f4232b", "3eb858e78f5a7254d8c9731174a94f76755fd3941c0ac93735c07ba14579630e", "a45fdc55c76448c049a1ab33f17023edfb2be3581e9c7aade8a6125215e04220", "d483fe813c6ba647ebbfd3ec41adca1c6130c2beeee9d9bf065c8d151c5f396e", "8a2e1d30050198c65a54483123960ccc38aef6848e1ec8f5f780e8523769ba32", "32888462f8b486c68ad7dd9610be5192bbeaf3b443951ac1a8118419d9fa097b", "227142501b9d4355ccba290404bde41575b037693cef1f438c47f8fbf35d1165", "5c37cc491da847cfeb9281d407efc41e15144c876e0170b499a96a22ed31e01e", "445425117cb8c90edcbc7c1cc0e74f747f2c1efa5630a967c64f287792a48a4b", "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"}
	for _, h := range bad {
		x, _ := hex.DecodeString(h)
		if b.Validate(x) == nil {
			t.Fatal("accepted bad", h)
		}
		if _, e := b.Apply(x, nil, nil); e == nil {
			t.Fatal("Apply accepted bad", h)
		}
	}
	for i := 0; i < 1024; i++ {
		x := group.Ristretto255.HashToElement([]byte{byte(i), byte(i >> 8)}, []byte("diagnostic"))
		raw, _ := x.MarshalBinaryCompress()
		p := group.Ristretto255.NewElement()
		if e := p.UnmarshalBinary(raw); e != nil {
			t.Fatal(e)
		}
		again, _ := p.MarshalBinaryCompress()
		if !bytes.Equal(raw, again) {
			t.Fatal("roundtrip")
		}
		if _, e := b.decode(raw); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("%d invalid vectors rejected; 1024 generated points roundtrip", len(bad))
}
