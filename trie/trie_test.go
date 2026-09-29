// Copyright 2014 The go-ethereum Authors
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
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"math/bits"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/quick"

	"github.com/davecgh/go-spew/spew"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"
)

func init() {
	spew.Config.Indent = "    "
	spew.Config.DisableMethods = false
}

func TestEmptyTrie(t *testing.T) {
	trie := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
	res := trie.Hash()
	exp := types.EmptyRootHash
	if res != exp {
		t.Errorf("expected %x got %x", exp, res)
	}
}

func TestNull(t *testing.T) {
	trie := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
	key := make([]byte, 32)
	value := []byte("test")
	trie.MustUpdate(key, value)
	if !bytes.Equal(trie.MustGet(key), value) {
		t.Fatal("wrong value")
	}
}

func TestMissingRoot(t *testing.T) {
	testMissingRoot(t, rawdb.HashScheme)
	testMissingRoot(t, rawdb.PathScheme)
}

func testMissingRoot(t *testing.T, scheme string) {
	root := common.HexToHash("0beec7b5ea3f0fdbc95d0dd47f3c5bc275da8a33")
	trie, err := New(TrieID(root), newTestDatabase(rawdb.NewMemoryDatabase(), scheme))
	if trie != nil {
		t.Error("New returned non-nil trie for invalid root")
	}
	if _, ok := err.(*MissingNodeError); !ok {
		t.Errorf("New returned wrong error: %v", err)
	}
}

func TestMissingNode(t *testing.T) {
	testMissingNode(t, false, rawdb.HashScheme)
	testMissingNode(t, false, rawdb.PathScheme)
	testMissingNode(t, true, rawdb.HashScheme)
	testMissingNode(t, true, rawdb.PathScheme)
}

func testMissingNode(t *testing.T, memonly bool, scheme string) {
	diskdb := rawdb.NewMemoryDatabase()
	triedb := newTestDatabase(diskdb, scheme)

	trie := NewEmpty(triedb)
	updateString(trie, "120000", "qwerqwerqwerqwerqwerqwerqwerqwer")
	updateString(trie, "123456", "asdfasdfasdfasdfasdfasdfasdfasdf")
	root, nodes, _ := trie.Commit(false)
	triedb.Update(root, types.EmptyRootHash, trienode.NewWithNodeSet(nodes))

	if !memonly {
		triedb.Commit(root)
	}

	trie, _ = New(TrieID(root), triedb)
	_, err := trie.Get([]byte("120000"))
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	trie, _ = New(TrieID(root), triedb)
	_, err = trie.Get([]byte("120099"))
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	trie, _ = New(TrieID(root), triedb)
	_, err = trie.Get([]byte("123456"))
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	trie, _ = New(TrieID(root), triedb)
	err = trie.Update([]byte("120099"), []byte("zxcvzxcvzxcvzxcvzxcvzxcvzxcvzxcv"))
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	trie, _ = New(TrieID(root), triedb)
	err = trie.Delete([]byte("123456"))
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	var (
		path []byte
		hash = common.HexToHash("0xe1d943cc8f061a0c0b98162830b970395ac9315654824bf21b73b891365262f9")
	)
	for p, n := range nodes.Nodes {
		if n.Hash == hash {
			path = common.CopyBytes([]byte(p))
			break
		}
	}
	trie, _ = New(TrieID(root), triedb)
	if memonly {
		trie.reader.banned = map[string]struct{}{string(path): {}}
	} else {
		rawdb.DeleteTrieNode(diskdb, common.Hash{}, path, hash, scheme)
	}

	_, err = trie.Get([]byte("120000"))
	if _, ok := err.(*MissingNodeError); !ok {
		t.Errorf("Wrong error: %v", err)
	}
	_, err = trie.Get([]byte("120099"))
	if _, ok := err.(*MissingNodeError); !ok {
		t.Errorf("Wrong error: %v", err)
	}
	_, err = trie.Get([]byte("123456"))
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	err = trie.Update([]byte("120099"), []byte("zxcv"))
	if _, ok := err.(*MissingNodeError); !ok {
		t.Errorf("Wrong error: %v", err)
	}
	err = trie.Delete([]byte("123456"))
	if _, ok := err.(*MissingNodeError); !ok {
		t.Errorf("Wrong error: %v", err)
	}
}

func TestInsert(t *testing.T) {
	trie := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))

	updateString(trie, "doe", "reindeer")
	updateString(trie, "dog", "puppy")
	updateString(trie, "dogglesworth", "cat")

	exp := common.HexToHash("8aad789dff2f538bca5d8ea56e8abe10f4c7ba3a5dea95fea4cd6e7c3a1168d3")
	root := trie.Hash()
	if root != exp {
		t.Errorf("case 1: exp %x got %x", exp, root)
	}

	trie = NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
	updateString(trie, "A", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	exp = common.HexToHash("d23786fb4a010da3ce639d66d5e904a11dbc02746d1ce25029e53290cabf28ab")
	root, _, _ = trie.Commit(false)
	if root != exp {
		t.Errorf("case 2: exp %x got %x", exp, root)
	}
}

func TestGet(t *testing.T) {
	db := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	trie := NewEmpty(db)
	updateString(trie, "doe", "reindeer")
	updateString(trie, "dog", "puppy")
	updateString(trie, "dogglesworth", "cat")

	for i := 0; i < 2; i++ {
		res := getString(trie, "dog")
		if !bytes.Equal(res, []byte("puppy")) {
			t.Errorf("expected puppy got %x", res)
		}
		unknown := getString(trie, "unknown")
		if unknown != nil {
			t.Errorf("expected nil got %x", unknown)
		}
		if i == 1 {
			return
		}
		root, nodes, _ := trie.Commit(false)
		db.Update(root, types.EmptyRootHash, trienode.NewWithNodeSet(nodes))
		trie, _ = New(TrieID(root), db)
	}
}

func TestDelete(t *testing.T) {
	db := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	trie := NewEmpty(db)
	vals := []struct{ k, v string }{
		{"do", "verb"},
		{"ether", "wookiedoo"},
		{"horse", "stallion"},
		{"shaman", "horse"},
		{"doge", "coin"},
		{"ether", ""},
		{"dog", "puppy"},
		{"shaman", ""},
	}
	for _, val := range vals {
		if val.v != "" {
			updateString(trie, val.k, val.v)
		} else {
			deleteString(trie, val.k)
		}
	}

	hash := trie.Hash()
	exp := common.HexToHash("5991bb8c6514148a29db676a14ac506cd2cd5775ace63c30a4fe457715e9ac84")
	if hash != exp {
		t.Errorf("expected %x got %x", exp, hash)
	}
}

func TestEmptyValues(t *testing.T) {
	trie := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))

	vals := []struct{ k, v string }{
		{"do", "verb"},
		{"ether", "wookiedoo"},
		{"horse", "stallion"},
		{"shaman", "horse"},
		{"doge", "coin"},
		{"ether", ""},
		{"dog", "puppy"},
		{"shaman", ""},
	}
	for _, val := range vals {
		updateString(trie, val.k, val.v)
	}

	hash := trie.Hash()
	exp := common.HexToHash("5991bb8c6514148a29db676a14ac506cd2cd5775ace63c30a4fe457715e9ac84")
	if hash != exp {
		t.Errorf("expected %x got %x", exp, hash)
	}
}

func TestReplication(t *testing.T) {
	db := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	trie := NewEmpty(db)
	vals := []struct{ k, v string }{
		{"do", "verb"},
		{"ether", "wookiedoo"},
		{"horse", "stallion"},
		{"shaman", "horse"},
		{"doge", "coin"},
		{"dog", "puppy"},
		{"somethingveryoddindeedthis is", "myothernodedata"},
	}
	for _, val := range vals {
		updateString(trie, val.k, val.v)
	}
	root, nodes, _ := trie.Commit(false)
	db.Update(root, types.EmptyRootHash, trienode.NewWithNodeSet(nodes))

	// create a new trie on top of the database and check that lookups work.
	trie2, err := New(TrieID(root), db)
	if err != nil {
		t.Fatalf("can't recreate trie at %x: %v", root, err)
	}
	for _, kv := range vals {
		if string(getString(trie2, kv.k)) != kv.v {
			t.Errorf("trie2 doesn't have %q => %q", kv.k, kv.v)
		}
	}
	hash, nodes, _ := trie2.Commit(false)
	if hash != root {
		t.Errorf("root failure. expected %x got %x", root, hash)
	}

	// recreate the trie after commit
	if nodes != nil {
		db.Update(hash, types.EmptyRootHash, trienode.NewWithNodeSet(nodes))
	}
	trie2, err = New(TrieID(hash), db)
	if err != nil {
		t.Fatalf("can't recreate trie at %x: %v", hash, err)
	}
	// perform some insertions on the new trie.
	vals2 := []struct{ k, v string }{
		{"do", "verb"},
		{"ether", "wookiedoo"},
		{"horse", "stallion"},
		// {"shaman", "horse"},
		// {"doge", "coin"},
		// {"ether", ""},
		// {"dog", "puppy"},
		// {"somethingveryoddindeedthis is", "myothernodedata"},
		// {"shaman", ""},
	}
	for _, val := range vals2 {
		updateString(trie2, val.k, val.v)
	}
	if trie2.Hash() != hash {
		t.Errorf("root failure. expected %x got %x", hash, hash)
	}
}

func TestLargeValue(t *testing.T) {
	trie := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
	trie.MustUpdate([]byte("key1"), []byte{99, 99, 99, 99})
	trie.MustUpdate([]byte("key2"), bytes.Repeat([]byte{1}, 32))
	trie.Hash()
}

// TestRandomCases tests some cases that were found via random fuzzing
func TestRandomCases(t *testing.T) {
	var rt = []randTestStep{
		{op: 6, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 0
		{op: 6, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 1
		{op: 0, key: common.Hex2Bytes("d51b182b95d677e5f1c82508c0228de96b73092d78ce78b2230cd948674f66fd1483bd"), value: common.Hex2Bytes("0000000000000002")},           // step 2
		{op: 2, key: common.Hex2Bytes("c2a38512b83107d665c65235b0250002882ac2022eb00711552354832c5f1d030d0e408e"), value: common.Hex2Bytes("")},                         // step 3
		{op: 3, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 4
		{op: 3, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 5
		{op: 6, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 6
		{op: 3, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 7
		{op: 0, key: common.Hex2Bytes("c2a38512b83107d665c65235b0250002882ac2022eb00711552354832c5f1d030d0e408e"), value: common.Hex2Bytes("0000000000000008")},         // step 8
		{op: 0, key: common.Hex2Bytes("d51b182b95d677e5f1c82508c0228de96b73092d78ce78b2230cd948674f66fd1483bd"), value: common.Hex2Bytes("0000000000000009")},           // step 9
		{op: 2, key: common.Hex2Bytes("fd"), value: common.Hex2Bytes("")},                                                                                               // step 10
		{op: 6, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 11
		{op: 6, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 12
		{op: 0, key: common.Hex2Bytes("fd"), value: common.Hex2Bytes("000000000000000d")},                                                                               // step 13
		{op: 6, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 14
		{op: 1, key: common.Hex2Bytes("c2a38512b83107d665c65235b0250002882ac2022eb00711552354832c5f1d030d0e408e"), value: common.Hex2Bytes("")},                         // step 15
		{op: 3, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 16
		{op: 0, key: common.Hex2Bytes("c2a38512b83107d665c65235b0250002882ac2022eb00711552354832c5f1d030d0e408e"), value: common.Hex2Bytes("0000000000000011")},         // step 17
		{op: 5, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 18
		{op: 3, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 19
		{op: 0, key: common.Hex2Bytes("d51b182b95d677e5f1c82508c0228de96b73092d78ce78b2230cd948674f66fd1483bd"), value: common.Hex2Bytes("0000000000000014")},           // step 20
		{op: 0, key: common.Hex2Bytes("d51b182b95d677e5f1c82508c0228de96b73092d78ce78b2230cd948674f66fd1483bd"), value: common.Hex2Bytes("0000000000000015")},           // step 21
		{op: 0, key: common.Hex2Bytes("c2a38512b83107d665c65235b0250002882ac2022eb00711552354832c5f1d030d0e408e"), value: common.Hex2Bytes("0000000000000016")},         // step 22
		{op: 5, key: common.Hex2Bytes(""), value: common.Hex2Bytes("")},                                                                                                 // step 23
		{op: 1, key: common.Hex2Bytes("980c393656413a15c8da01978ed9f89feb80b502f58f2d640e3a2f5f7a99a7018f1b573befd92053ac6f78fca4a87268"), value: common.Hex2Bytes("")}, // step 24
		{op: 1, key: common.Hex2Bytes("fd"), value: common.Hex2Bytes("")},                                                                                               // step 25
	}
	if err := runRandTest(rt); err != nil {
		t.Fatal(err)
	}
}

// randTest performs random trie operations.
// Instances of this test are created by Generate.
type randTest []randTestStep

// compile-time interface check
var _ quick.Generator = (randTest)(nil)

type randTestStep struct {
	op    int
	key   []byte // for opUpdate, opDelete, opGet
	value []byte // for opUpdate
	err   error  // for debugging
}

const (
	opUpdate = iota
	opDelete
	opGet
	opHash
	opCommit
	opItercheckhash
	opNodeDiff
	opProve
	opMax // boundary value, not an actual op
)

func (randTest) Generate(r *rand.Rand, size int) reflect.Value {
	var finishedFn = func() bool {
		size--
		return size == 0
	}
	return reflect.ValueOf(generateSteps(finishedFn, r))
}

func generateSteps(finished func() bool, r io.Reader) randTest {
	var allKeys [][]byte
	var one = []byte{0}
	genKey := func() []byte {
		r.Read(one)
		if len(allKeys) < 2 || one[0]%100 > 90 {
			// new key
			size := one[0] % 50
			key := make([]byte, size)
			r.Read(key)
			allKeys = append(allKeys, key)
			return key
		}
		// use existing key
		idx := int(one[0]) % len(allKeys)
		return allKeys[idx]
	}
	var steps randTest
	for !finished() {
		r.Read(one)
		step := randTestStep{op: int(one[0]) % opMax}
		switch step.op {
		case opUpdate:
			step.key = genKey()
			step.value = make([]byte, 8)
			binary.BigEndian.PutUint64(step.value, uint64(len(steps)))
		case opGet, opDelete, opProve:
			step.key = genKey()
		}
		steps = append(steps, step)
	}
	return steps
}

func verifyAccessList(old *Trie, new *Trie, set *trienode.NodeSet) error {
	deletes, inserts, updates := diffTries(old, new)

	// Check insertion set
	for path := range inserts {
		n, ok := set.Nodes[path]
		if !ok || n.IsDeleted() {
			return errors.New("expect new node")
		}
		//if len(n.Prev) > 0 {
		//	return errors.New("unexpected origin value")
		//}
	}
	// Check deletion set
	for path := range deletes {
		n, ok := set.Nodes[path]
		if !ok || !n.IsDeleted() {
			return errors.New("expect deleted node")
		}
		//if len(n.Prev) == 0 {
		//	return errors.New("expect origin value")
		//}
		//if !bytes.Equal(n.Prev, blob) {
		//	return errors.New("invalid origin value")
		//}
	}
	// Check update set
	for path := range updates {
		n, ok := set.Nodes[path]
		if !ok || n.IsDeleted() {
			return errors.New("expect updated node")
		}
		//if len(n.Prev) == 0 {
		//	return errors.New("expect origin value")
		//}
		//if !bytes.Equal(n.Prev, blob) {
		//	return errors.New("invalid origin value")
		//}
	}
	return nil
}

// runRandTestBool coerces error to boolean, for use in quick.Check
func runRandTestBool(rt randTest) bool {
	return runRandTest(rt) == nil
}

func runRandTest(rt randTest) error {
	var scheme = rawdb.HashScheme
	if rand.Intn(2) == 0 {
		scheme = rawdb.PathScheme
	}
	var (
		origin   = types.EmptyRootHash
		triedb   = newTestDatabase(rawdb.NewMemoryDatabase(), scheme)
		tr       = NewEmpty(triedb)
		values   = make(map[string]string) // tracks content of the trie
		origTrie = NewEmpty(triedb)
	)
	for i, step := range rt {
		// fmt.Printf("{op: %d, key: common.Hex2Bytes(\"%x\"), value: common.Hex2Bytes(\"%x\")}, // step %d\n",
		// 	step.op, step.key, step.value, i)

		switch step.op {
		case opUpdate:
			tr.MustUpdate(step.key, step.value)
			values[string(step.key)] = string(step.value)
		case opDelete:
			tr.MustDelete(step.key)
			delete(values, string(step.key))
		case opGet:
			v := tr.MustGet(step.key)
			want := values[string(step.key)]
			if string(v) != want {
				rt[i].err = fmt.Errorf("mismatch for key %#x, got %#x want %#x", step.key, v, want)
			}
		case opProve:
			hash := tr.Hash()
			if hash == types.EmptyRootHash {
				continue
			}
			proofDb := rawdb.NewMemoryDatabase()
			err := tr.Prove(step.key, proofDb)
			if err != nil {
				rt[i].err = fmt.Errorf("failed for proving key %#x, %v", step.key, err)
			}
			_, err = VerifyProof(hash, step.key, proofDb)
			if err != nil {
				rt[i].err = fmt.Errorf("failed for verifying key %#x, %v", step.key, err)
			}
		case opHash:
			tr.Hash()
		case opCommit:
			root, nodes, _ := tr.Commit(true)
			if nodes != nil {
				triedb.Update(root, origin, trienode.NewWithNodeSet(nodes))
			}
			newtr, err := New(TrieID(root), triedb)
			if err != nil {
				rt[i].err = err
				return err
			}
			if nodes != nil {
				if err := verifyAccessList(origTrie, newtr, nodes); err != nil {
					rt[i].err = err
					return err
				}
			}
			tr = newtr
			origTrie = tr.Copy()
			origin = root
		case opItercheckhash:
			checktr := NewEmpty(triedb)
			it := NewIterator(tr.MustNodeIterator(nil))
			for it.Next() {
				checktr.MustUpdate(it.Key, it.Value)
			}
			if tr.Hash() != checktr.Hash() {
				rt[i].err = fmt.Errorf("hash mismatch in opItercheckhash")
			}
		case opNodeDiff:
			var (
				origIter = origTrie.MustNodeIterator(nil)
				curIter  = tr.MustNodeIterator(nil)
				origSeen = make(map[string]struct{})
				curSeen  = make(map[string]struct{})
			)
			for origIter.Next(true) {
				if origIter.Leaf() {
					continue
				}
				origSeen[string(origIter.Path())] = struct{}{}
			}
			for curIter.Next(true) {
				if curIter.Leaf() {
					continue
				}
				curSeen[string(curIter.Path())] = struct{}{}
			}
			var (
				insertExp = make(map[string]struct{})
				deleteExp = make(map[string]struct{})
			)
			for path := range curSeen {
				_, present := origSeen[path]
				if !present {
					insertExp[path] = struct{}{}
				}
			}
			for path := range origSeen {
				_, present := curSeen[path]
				if !present {
					deleteExp[path] = struct{}{}
				}
			}
			if len(insertExp) != len(tr.tracer.inserts) {
				rt[i].err = fmt.Errorf("insert set mismatch")
			}
			if len(deleteExp) != len(tr.tracer.deletes) {
				rt[i].err = fmt.Errorf("delete set mismatch")
			}
			for insert := range tr.tracer.inserts {
				if _, present := insertExp[insert]; !present {
					rt[i].err = fmt.Errorf("missing inserted node")
				}
			}
			for del := range tr.tracer.deletes {
				if _, present := deleteExp[del]; !present {
					rt[i].err = fmt.Errorf("missing deleted node")
				}
			}
		}
		// Abort the test on error.
		if rt[i].err != nil {
			return rt[i].err
		}
	}
	return nil
}

func TestRandom(t *testing.T) {
	if err := quick.Check(runRandTestBool, nil); err != nil {
		if cerr, ok := err.(*quick.CheckError); ok {
			t.Fatalf("random test iteration %d failed: %s", cerr.Count, spew.Sdump(cerr.In))
		}
		t.Fatal(err)
	}
}

func BenchmarkGet(b *testing.B)      { benchGet(b) }
func BenchmarkUpdateBE(b *testing.B) { benchUpdate(b, binary.BigEndian) }
func BenchmarkUpdateLE(b *testing.B) { benchUpdate(b, binary.LittleEndian) }

const benchElemCount = 20000

func benchGet(b *testing.B) {
	triedb := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	trie := NewEmpty(triedb)
	k := make([]byte, 32)
	for i := 0; i < benchElemCount; i++ {
		binary.LittleEndian.PutUint64(k, uint64(i))
		v := make([]byte, 32)
		binary.LittleEndian.PutUint64(v, uint64(i))
		trie.MustUpdate(k, v)
	}
	binary.LittleEndian.PutUint64(k, benchElemCount/2)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trie.MustGet(k)
	}
	b.StopTimer()
}

func benchUpdate(b *testing.B, e binary.ByteOrder) *Trie {
	trie := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
	k := make([]byte, 32)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		v := make([]byte, 32)
		e.PutUint64(k, uint64(i))
		e.PutUint64(v, uint64(i))
		trie.MustUpdate(k, v)
	}
	return trie
}

// Benchmarks the trie hashing. Since the trie caches the result of any operation,
// we cannot use b.N as the number of hashing rounds, since all rounds apart from
// the first one will be NOOP. As such, we'll use b.N as the number of account to
// insert into the trie before measuring the hashing.
// BenchmarkHash-6   	  288680	      4561 ns/op	     682 B/op	       9 allocs/op
// BenchmarkHash-6   	  275095	      4800 ns/op	     685 B/op	       9 allocs/op
// pure hasher:
// BenchmarkHash-6   	  319362	      4230 ns/op	     675 B/op	       9 allocs/op
// BenchmarkHash-6   	  257460	      4674 ns/op	     689 B/op	       9 allocs/op
// With hashing in-between and pure hasher:
// BenchmarkHash-6   	  225417	      7150 ns/op	     982 B/op	      12 allocs/op
// BenchmarkHash-6   	  220378	      6197 ns/op	     983 B/op	      12 allocs/op
// same with old hasher
// BenchmarkHash-6   	  229758	      6437 ns/op	     981 B/op	      12 allocs/op
// BenchmarkHash-6   	  212610	      7137 ns/op	     986 B/op	      12 allocs/op
func BenchmarkHash(b *testing.B) {
	// Create a realistic account trie to hash. We're first adding and hashing N
	// entries, then adding N more.
	addresses, accounts := makeAccounts(2 * b.N)
	// Insert the accounts into the trie and hash it
	trie := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
	i := 0
	for ; i < len(addresses)/2; i++ {
		trie.MustUpdate(crypto.Keccak256(addresses[i][:]), accounts[i])
	}
	trie.Hash()
	for ; i < len(addresses); i++ {
		trie.MustUpdate(crypto.Keccak256(addresses[i][:]), accounts[i])
	}
	b.ResetTimer()
	b.ReportAllocs()
	//trie.hashRoot(nil, nil)
	trie.Hash()
}

// Benchmarks the trie Commit following a Hash. Since the trie caches the result of any operation,
// we cannot use b.N as the number of hashing rounds, since all rounds apart from
// the first one will be NOOP. As such, we'll use b.N as the number of account to
// insert into the trie before measuring the hashing.
func BenchmarkCommitAfterHash(b *testing.B) {
	b.Run("no-onleaf", func(b *testing.B) {
		benchmarkCommitAfterHash(b, false)
	})
	b.Run("with-onleaf", func(b *testing.B) {
		benchmarkCommitAfterHash(b, true)
	})
}

func benchmarkCommitAfterHash(b *testing.B, collectLeaf bool) {
	// Make the random benchmark deterministic
	addresses, accounts := makeAccounts(b.N)
	trie := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
	for i := 0; i < len(addresses); i++ {
		trie.MustUpdate(crypto.Keccak256(addresses[i][:]), accounts[i])
	}
	// Insert the accounts into the trie and hash it
	trie.Hash()
	b.ResetTimer()
	b.ReportAllocs()
	trie.Commit(collectLeaf)
}

func TestTinyTrie(t *testing.T) {
	// Create a realistic account trie to hash
	_, accounts := makeAccounts(5)
	trie := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
	trie.MustUpdate(common.Hex2Bytes("0000000000000000000000000000000000000000000000000000000000001337"), accounts[3])
	if exp, root := common.HexToHash("8c6a85a4d9fda98feff88450299e574e5378e32391f75a055d470ac0653f1005"), trie.Hash(); exp != root {
		t.Errorf("1: got %x, exp %x", root, exp)
	}
	trie.MustUpdate(common.Hex2Bytes("0000000000000000000000000000000000000000000000000000000000001338"), accounts[4])
	if exp, root := common.HexToHash("ec63b967e98a5720e7f720482151963982890d82c9093c0d486b7eb8883a66b1"), trie.Hash(); exp != root {
		t.Errorf("2: got %x, exp %x", root, exp)
	}
	trie.MustUpdate(common.Hex2Bytes("0000000000000000000000000000000000000000000000000000000000001339"), accounts[4])
	if exp, root := common.HexToHash("0608c1d1dc3905fa22204c7a0e43644831c3b6d3def0f274be623a948197e64a"), trie.Hash(); exp != root {
		t.Errorf("3: got %x, exp %x", root, exp)
	}
	checktr := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
	it := NewIterator(trie.MustNodeIterator(nil))
	for it.Next() {
		checktr.MustUpdate(it.Key, it.Value)
	}
	if troot, itroot := trie.Hash(), checktr.Hash(); troot != itroot {
		t.Fatalf("hash mismatch in opItercheckhash, trie: %x, check: %x", troot, itroot)
	}
}

func TestCommitAfterHash(t *testing.T) {
	// Create a realistic account trie to hash
	addresses, accounts := makeAccounts(1000)
	trie := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
	for i := 0; i < len(addresses); i++ {
		trie.MustUpdate(crypto.Keccak256(addresses[i][:]), accounts[i])
	}
	// Insert the accounts into the trie and hash it
	trie.Hash()
	trie.Commit(false)
	root := trie.Hash()
	exp := common.HexToHash("72f9d3f3fe1e1dd7b8936442e7642aef76371472d94319900790053c493f3fe6")
	if exp != root {
		t.Errorf("got %x, exp %x", root, exp)
	}
	root, _, _ = trie.Commit(false)
	if exp != root {
		t.Errorf("got %x, exp %x", root, exp)
	}
}

func makeAccounts(size int) (addresses [][20]byte, accounts [][]byte) {
	// Make the random benchmark deterministic
	random := rand.New(rand.NewSource(0))
	// Create a realistic account trie to hash
	addresses = make([][20]byte, size)
	for i := 0; i < len(addresses); i++ {
		data := make([]byte, 20)
		random.Read(data)
		copy(addresses[i][:], data)
	}
	accounts = make([][]byte, len(addresses))
	for i := 0; i < len(accounts); i++ {
		var (
			nonce = uint64(random.Int63())
			root  = types.EmptyRootHash
			code  = crypto.Keccak256(nil)
		)
		// The big.Rand function is not deterministic with regards to 64 vs 32 bit systems,
		// and will consume different amount of data from the rand source.
		//balance = new(big.Int).Rand(random, new(big.Int).Exp(common.Big2, common.Big256, nil))
		// Therefore, we instead just read via byte buffer
		numBytes := random.Uint32() % 33 // [0, 32] bytes
		balanceBytes := make([]byte, numBytes)
		random.Read(balanceBytes)
		balance := new(uint256.Int).SetBytes(balanceBytes)
		data, _ := rlp.EncodeToBytes(&types.StateAccount{Nonce: nonce, Balance: balance, Root: root, CodeHash: code})
		accounts[i] = data
	}
	return addresses, accounts
}

// spongeDb is a dummy db backend which accumulates writes in a sponge
type spongeDb struct {
	sponge  hash.Hash
	id      string
	journal []string
	keys    []string
	values  map[string]string
}

func (s *spongeDb) Has(key []byte) (bool, error)             { panic("implement me") }
func (s *spongeDb) Get(key []byte) ([]byte, error)           { return nil, errors.New("no such elem") }
func (s *spongeDb) Delete(key []byte) error                  { panic("implement me") }
func (s *spongeDb) NewBatch() ethdb.Batch                    { return &spongeBatch{s} }
func (s *spongeDb) NewBatchWithSize(size int) ethdb.Batch    { return &spongeBatch{s} }
func (s *spongeDb) NewSnapshot() (ethdb.Snapshot, error)     { panic("implement me") }
func (s *spongeDb) Stat(property string) (string, error)     { panic("implement me") }
func (s *spongeDb) Compact(start []byte, limit []byte) error { panic("implement me") }
func (s *spongeDb) Close() error                             { return nil }
func (s *spongeDb) Put(key []byte, value []byte) error {
	var (
		keybrief = key
		valbrief = value
	)
	if len(keybrief) > 8 {
		keybrief = keybrief[:8]
	}
	if len(valbrief) > 8 {
		valbrief = valbrief[:8]
	}
	s.journal = append(s.journal, fmt.Sprintf("%v: PUT([%x...], [%d bytes] %x...)\n", s.id, keybrief, len(value), valbrief))

	if s.values == nil {
		s.sponge.Write(key)
		s.sponge.Write(value)
	} else {
		s.keys = append(s.keys, string(key))
		s.values[string(key)] = string(value)
	}
	return nil
}
func (s *spongeDb) NewIterator(prefix []byte, start []byte) ethdb.Iterator { panic("implement me") }

func (s *spongeDb) Flush() {
	// Bottom-up, the longest path first
	sort.Sort(sort.Reverse(sort.StringSlice(s.keys)))
	for _, key := range s.keys {
		s.sponge.Write([]byte(key))
		s.sponge.Write([]byte(s.values[key]))
	}
}

// spongeBatch is a dummy batch which immediately writes to the underlying spongedb
type spongeBatch struct {
	db *spongeDb
}

func (b *spongeBatch) Put(key, value []byte) error {
	b.db.Put(key, value)
	return nil
}
func (b *spongeBatch) Delete(key []byte) error             { panic("implement me") }
func (b *spongeBatch) ValueSize() int                      { return 100 }
func (b *spongeBatch) Write() error                        { return nil }
func (b *spongeBatch) Reset()                              {}
func (b *spongeBatch) Replay(w ethdb.KeyValueWriter) error { return nil }

// TestCommitSequence tests that the trie.Commit operation writes the elements of the trie
// in the expected order.
// The test data was based on the 'master' code, and is basically random. It can be used
// to check whether changes to the trie modifies the write order or data in any way.
func TestCommitSequence(t *testing.T) {
	for i, tc := range []struct {
		count           int
		expWriteSeqHash []byte
	}{
		{20, common.FromHex("330b0afae2853d96b9f015791fbe0fb7f239bf65f335f16dfc04b76c7536276d")},
		{200, common.FromHex("5162b3735c06b5d606b043a3ee8adbdbbb408543f4966bca9dcc63da82684eeb")},
		{2000, common.FromHex("4574cd8e6b17f3fe8ad89140d1d0bf4f1bd7a87a8ac3fb623b33550544c77635")},
	} {
		addresses, accounts := makeAccounts(tc.count)
		// This spongeDb is used to check the sequence of disk-db-writes
		s := &spongeDb{sponge: sha3.NewLegacyKeccak256()}
		db := newTestDatabase(rawdb.NewDatabase(s), rawdb.HashScheme)
		trie := NewEmpty(db)
		// Fill the trie with elements
		for i := 0; i < tc.count; i++ {
			trie.MustUpdate(crypto.Keccak256(addresses[i][:]), accounts[i])
		}
		// Flush trie -> database
		root, nodes, _ := trie.Commit(false)
		db.Update(root, types.EmptyRootHash, trienode.NewWithNodeSet(nodes))
		// Flush memdb -> disk (sponge)
		db.Commit(root)
		if got, exp := s.sponge.Sum(nil), tc.expWriteSeqHash; !bytes.Equal(got, exp) {
			t.Errorf("test %d, disk write sequence wrong:\ngot %x exp %x\n", i, got, exp)
		}
	}
}

// TestCommitSequenceRandomBlobs is identical to TestCommitSequence
// but uses random blobs instead of 'accounts'
func TestCommitSequenceRandomBlobs(t *testing.T) {
	for i, tc := range []struct {
		count           int
		expWriteSeqHash []byte
	}{
		{20, common.FromHex("8016650c7a50cf88485fd06cde52d634a89711051107f00d21fae98234f2f13d")},
		{200, common.FromHex("dde92ca9812e068e6982d04b40846dc65a61a9fd4996fc0f55f2fde172a8e13c")},
		{2000, common.FromHex("ab553a7f9aff82e3929c382908e30ef7dd17a332933e92ba3fe873fc661ef382")},
	} {
		prng := rand.New(rand.NewSource(int64(i)))
		// This spongeDb is used to check the sequence of disk-db-writes
		s := &spongeDb{sponge: sha3.NewLegacyKeccak256()}
		db := newTestDatabase(rawdb.NewDatabase(s), rawdb.HashScheme)
		trie := NewEmpty(db)
		// Fill the trie with elements
		for i := 0; i < tc.count; i++ {
			key := make([]byte, 32)
			var val []byte
			// 50% short elements, 50% large elements
			if prng.Intn(2) == 0 {
				val = make([]byte, 1+prng.Intn(32))
			} else {
				val = make([]byte, 1+prng.Intn(4096))
			}
			prng.Read(key)
			prng.Read(val)
			trie.MustUpdate(key, val)
		}
		// Flush trie -> database
		root, nodes, _ := trie.Commit(false)
		db.Update(root, types.EmptyRootHash, trienode.NewWithNodeSet(nodes))
		// Flush memdb -> disk (sponge)
		db.Commit(root)
		if got, exp := s.sponge.Sum(nil), tc.expWriteSeqHash; !bytes.Equal(got, exp) {
			t.Fatalf("test %d, disk write sequence wrong:\ngot %x exp %x\n", i, got, exp)
		}
	}
}

func TestCommitSequenceStackTrie(t *testing.T) {
	for count := 1; count < 200; count++ {
		prng := rand.New(rand.NewSource(int64(count)))
		// This spongeDb is used to check the sequence of disk-db-writes
		s := &spongeDb{
			sponge: sha3.NewLegacyKeccak256(),
			id:     "a",
			values: make(map[string]string),
		}
		db := newTestDatabase(rawdb.NewDatabase(s), rawdb.HashScheme)
		trie := NewEmpty(db)

		// Another sponge is used for the stacktrie commits
		stackTrieSponge := &spongeDb{
			sponge: sha3.NewLegacyKeccak256(),
			id:     "b",
			values: make(map[string]string),
		}
		options := NewStackTrieOptions()
		options = options.WithWriter(func(path []byte, hash common.Hash, blob []byte) {
			rawdb.WriteTrieNode(stackTrieSponge, common.Hash{}, path, hash, blob, db.Scheme())
		})
		stTrie := NewStackTrie(options)

		// Fill the trie with elements
		for i := 0; i < count; i++ {
			// For the stack trie, we need to do inserts in proper order
			key := make([]byte, 32)
			binary.BigEndian.PutUint64(key, uint64(i))
			var val []byte
			// 50% short elements, 50% large elements
			if prng.Intn(2) == 0 {
				val = make([]byte, 1+prng.Intn(32))
			} else {
				val = make([]byte, 1+prng.Intn(1024))
			}
			prng.Read(val)
			trie.Update(key, val)
			stTrie.Update(key, val)
		}
		// Flush trie -> database
		root, nodes, _ := trie.Commit(false)
		// Flush memdb -> disk (sponge)
		db.Update(root, types.EmptyRootHash, trienode.NewWithNodeSet(nodes))
		db.Commit(root)
		s.Flush()

		// And flush stacktrie -> disk
		stRoot := stTrie.Commit()
		if stRoot != root {
			t.Fatalf("root wrong, got %x exp %x", stRoot, root)
		}
		stackTrieSponge.Flush()
		if got, exp := stackTrieSponge.sponge.Sum(nil), s.sponge.Sum(nil); !bytes.Equal(got, exp) {
			// Show the journal
			t.Logf("Expected:")
			for i, v := range s.journal {
				t.Logf("op %d: %v", i, v)
			}
			t.Logf("Stacktrie:")
			for i, v := range stackTrieSponge.journal {
				t.Logf("op %d: %v", i, v)
			}
			t.Fatalf("test %d, disk write sequence wrong:\ngot %x exp %x\n", count, got, exp)
		}
	}
}

// TestCommitSequenceSmallRoot tests that a trie which is essentially only a
// small (<32 byte) shortnode with an included value is properly committed to a
// database.
// This case might not matter, since in practice, all keys are 32 bytes, which means
// that even a small trie which contains a leaf will have an extension making it
// not fit into 32 bytes, rlp-encoded. However, it's still the correct thing to do.
func TestCommitSequenceSmallRoot(t *testing.T) {
	s := &spongeDb{
		sponge: sha3.NewLegacyKeccak256(),
		id:     "a",
		values: make(map[string]string),
	}
	db := newTestDatabase(rawdb.NewDatabase(s), rawdb.HashScheme)
	trie := NewEmpty(db)

	// Another sponge is used for the stacktrie commits
	stackTrieSponge := &spongeDb{
		sponge: sha3.NewLegacyKeccak256(),
		id:     "b",
		values: make(map[string]string),
	}
	options := NewStackTrieOptions()
	options = options.WithWriter(func(path []byte, hash common.Hash, blob []byte) {
		rawdb.WriteTrieNode(stackTrieSponge, common.Hash{}, path, hash, blob, db.Scheme())
	})
	stTrie := NewStackTrie(options)

	// Add a single small-element to the trie(s)
	key := make([]byte, 5)
	key[0] = 1
	trie.Update(key, []byte{0x1})
	stTrie.Update(key, []byte{0x1})

	// Flush trie -> database
	root, nodes, _ := trie.Commit(false)
	// Flush memdb -> disk (sponge)
	db.Update(root, types.EmptyRootHash, trienode.NewWithNodeSet(nodes))
	db.Commit(root)

	// And flush stacktrie -> disk
	stRoot := stTrie.Commit()
	if stRoot != root {
		t.Fatalf("root wrong, got %x exp %x", stRoot, root)
	}
	t.Logf("root: %x\n", stRoot)

	s.Flush()
	stackTrieSponge.Flush()
	if got, exp := stackTrieSponge.sponge.Sum(nil), s.sponge.Sum(nil); !bytes.Equal(got, exp) {
		t.Fatalf("test, disk write sequence wrong:\ngot %x exp %x\n", got, exp)
	}
}

// BenchmarkCommitAfterHashFixedSize benchmarks the Commit (after Hash) of a fixed number of updates to a trie.
// This benchmark is meant to capture the difference on efficiency of small versus large changes. Typically,
// storage tries are small (a couple of entries), whereas the full post-block account trie update is large (a couple
// of thousand entries)
func BenchmarkHashFixedSize(b *testing.B) {
	b.Run("10", func(b *testing.B) {
		b.StopTimer()
		acc, add := makeAccounts(20)
		for i := 0; i < b.N; i++ {
			benchmarkHashFixedSize(b, acc, add)
		}
	})
	b.Run("100", func(b *testing.B) {
		b.StopTimer()
		acc, add := makeAccounts(100)
		for i := 0; i < b.N; i++ {
			benchmarkHashFixedSize(b, acc, add)
		}
	})

	b.Run("1K", func(b *testing.B) {
		b.StopTimer()
		acc, add := makeAccounts(1000)
		for i := 0; i < b.N; i++ {
			benchmarkHashFixedSize(b, acc, add)
		}
	})
	b.Run("10K", func(b *testing.B) {
		b.StopTimer()
		acc, add := makeAccounts(10000)
		for i := 0; i < b.N; i++ {
			benchmarkHashFixedSize(b, acc, add)
		}
	})
	b.Run("100K", func(b *testing.B) {
		b.StopTimer()
		acc, add := makeAccounts(100000)
		for i := 0; i < b.N; i++ {
			benchmarkHashFixedSize(b, acc, add)
		}
	})
}

func benchmarkHashFixedSize(b *testing.B, addresses [][20]byte, accounts [][]byte) {
	b.ReportAllocs()
	trie := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
	for i := 0; i < len(addresses); i++ {
		trie.MustUpdate(crypto.Keccak256(addresses[i][:]), accounts[i])
	}
	// Insert the accounts into the trie and hash it
	b.StartTimer()
	trie.Hash()
	b.StopTimer()
}

func BenchmarkCommitAfterHashFixedSize(b *testing.B) {
	b.Run("10", func(b *testing.B) {
		b.StopTimer()
		acc, add := makeAccounts(20)
		for i := 0; i < b.N; i++ {
			benchmarkCommitAfterHashFixedSize(b, acc, add)
		}
	})
	b.Run("100", func(b *testing.B) {
		b.StopTimer()
		acc, add := makeAccounts(100)
		for i := 0; i < b.N; i++ {
			benchmarkCommitAfterHashFixedSize(b, acc, add)
		}
	})

	b.Run("1K", func(b *testing.B) {
		b.StopTimer()
		acc, add := makeAccounts(1000)
		for i := 0; i < b.N; i++ {
			benchmarkCommitAfterHashFixedSize(b, acc, add)
		}
	})
	b.Run("10K", func(b *testing.B) {
		b.StopTimer()
		acc, add := makeAccounts(10000)
		for i := 0; i < b.N; i++ {
			benchmarkCommitAfterHashFixedSize(b, acc, add)
		}
	})
	b.Run("100K", func(b *testing.B) {
		b.StopTimer()
		acc, add := makeAccounts(100000)
		for i := 0; i < b.N; i++ {
			benchmarkCommitAfterHashFixedSize(b, acc, add)
		}
	})
}

func benchmarkCommitAfterHashFixedSize(b *testing.B, addresses [][20]byte, accounts [][]byte) {
	b.ReportAllocs()
	trie := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
	for i := 0; i < len(addresses); i++ {
		trie.MustUpdate(crypto.Keccak256(addresses[i][:]), accounts[i])
	}
	// Insert the accounts into the trie and hash it
	trie.Hash()
	b.StartTimer()
	trie.Commit(false)
	b.StopTimer()
}

func getString(trie *Trie, k string) []byte {
	return trie.MustGet([]byte(k))
}

func updateString(trie *Trie, k, v string) {
	trie.MustUpdate([]byte(k), []byte(v))
}

func deleteString(trie *Trie, k string) {
	trie.MustDelete([]byte(k))
}

func TestDecodeNode(t *testing.T) {
	t.Parallel()

	var (
		hash  = make([]byte, 20)
		elems = make([]byte, 20)
	)
	for i := 0; i < 5000000; i++ {
		prng.Read(hash)
		prng.Read(elems)
		decodeNode(hash, elems)
	}
}

func FuzzTrie(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		var steps = 500
		var input = bytes.NewReader(data)
		var finishedFn = func() bool {
			steps--
			return steps < 0 || input.Len() == 0
		}
		if err := runRandTest(generateSteps(finishedFn, input)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStructuredKeyLayouts(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()

	path := []byte{1, 2, 3}
	version := uint64(0x00123456)
	tests := []struct {
		name   string
		method string
		depth  int64
		want   string
	}{
		{
			name:   "EpochPath",
			method: "EpochPath",
			depth:  9,
			want:   "00123" + "d" + "123" + strings.Repeat("0", 50) + "456" + "03",
		},
		{
			name:   "DepthSplit shallow",
			method: "DepthSplit",
			depth:  4,
			want:   "0" + "00123456" + "d" + "123" + strings.Repeat("0", 49) + "03",
		},
		{
			name:   "DepthSplit deep",
			method: "DepthSplit",
			depth:  5,
			want:   "1" + "d" + "123" + strings.Repeat("0", 49) + "00123456" + "03",
		},
		{
			name:   "DepthEpoch shallow",
			method: "DepthEpoch",
			depth:  4,
			want:   "00123" + "0" + "456" + "d" + "123" + strings.Repeat("0", 49) + "03",
		},
		{
			name:   "DepthEpoch deep",
			method: "DepthEpoch",
			depth:  5,
			want:   "00123" + "1" + "d" + "123" + strings.Repeat("0", 49) + "456" + "03",
		},
		{
			name:   "ShardVP state matches VP star",
			method: "ShardVP",
			depth:  9,
			want:   "00123456" + "d" + "123" + strings.Repeat("0", 50) + "03",
		},
		{
			name:   "DualVP state",
			method: "DualVP",
			depth:  9,
			want:   "d" + "00123456" + "123" + strings.Repeat("0", 50) + "03",
		},
		{
			name:   "RunPath state",
			method: "RunPath",
			depth:  9,
			want:   "00000000" + "d" + "123" + strings.Repeat("0", 42) + "00123456" + "03",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			common.ModifyHashMethod = tt.method
			got, err := modifyStructuredKey(version, common.TrieNodeData{Path: path, Depth: tt.depth})
			if err != nil {
				t.Fatal(err)
			}
			if gotHex := hex.EncodeToString(got); gotHex != tt.want {
				t.Fatalf("key mismatch\n got: %s\nwant: %s", gotHex, tt.want)
			}
		})
	}
}

func TestRunPathAdvancesAtBlockBoundary(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()

	common.ModifyHashMethod = "RunPath"
	common.RunPathTargetNodes = 5
	ResetRunPath()
	AdvanceRunPath(4)
	got, err := modifyStructuredKey(0x42, common.TrieNodeData{})
	if err != nil {
		t.Fatal(err)
	}
	if gotHex := hex.EncodeToString(got); !strings.HasPrefix(gotHex, "00000000") {
		t.Fatalf("run advanced too early: %s", gotHex)
	}
	AdvanceRunPath(1)
	got, err = modifyStructuredKey(0x43, common.TrieNodeData{})
	if err != nil {
		t.Fatal(err)
	}
	if gotHex := hex.EncodeToString(got); !strings.HasPrefix(gotHex, "00000001") {
		t.Fatalf("run did not advance at target: %s", gotHex)
	}
}

func TestForestVPLayouts(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()

	versionStr := "00123456"
	common.ModifyHashMethod = "ForestVP"
	common.HashingStateTrie = true
	common.HashingStorageTrie = false

	internal, err := modifyForestVPKey(versionStr, &fullNode{}, common.TrieNodeData{Path: []byte{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	wantInternal := versionStr + "123" + strings.Repeat("0", 50) + "d" + "03"
	if got := hex.EncodeToString(internal); got != wantInternal {
		t.Fatalf("state internal mismatch\n got: %s\nwant: %s", got, wantInternal)
	}

	leafKey := append(bytes.Repeat([]byte{0xa}, 64), byte(16))
	leaf := &shortNode{Key: leafKey, Val: valueNode{1}}
	stateLeaf, err := modifyForestVPKey(versionStr, leaf, common.TrieNodeData{})
	if err != nil {
		t.Fatal(err)
	}
	wantLeaf := versionStr + strings.Repeat("a", 24) + strings.Repeat("0", 29) + "e" + "18"
	if got := hex.EncodeToString(stateLeaf); got != wantLeaf {
		t.Fatalf("state leaf mismatch\n got: %s\nwant: %s", got, wantLeaf)
	}

	common.HashingStateTrie = false
	common.HashingStorageTrie = true
	common.AddrHashOfCurrentStorageTrie = common.HexToHash("0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	storage, err := modifyForestVPKey(versionStr, &fullNode{}, common.TrieNodeData{Path: []byte{0xe}})
	if err != nil {
		t.Fatal(err)
	}
	wantStorage := versionStr + "abcdef0123456789abcdef01" + "e" + strings.Repeat("0", 28) + "f" + "01"
	if got := hex.EncodeToString(storage); got != wantStorage {
		t.Fatalf("storage mismatch\n got: %s\nwant: %s", got, wantStorage)
	}
}

func TestDualVPStorageLayout(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()

	common.HashingStateTrie = false
	common.HashingStorageTrie = true
	common.AddrHashOfCurrentStorageTrie = common.HexToHash("0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	common.ModifyHashMethod = "DualVP"

	got, err := modifyStructuredKey(0x00123456, common.TrieNodeData{Path: []byte{0xe}})
	if err != nil {
		t.Fatal(err)
	}
	want := "f" + "00123456" + "abcdef0123456789abcdef01" + "e" + strings.Repeat("0", 28) + "01"
	if gotHex := hex.EncodeToString(got); gotHex != want {
		t.Fatalf("storage key mismatch\n got: %s\nwant: %s", gotHex, want)
	}
}

func TestShardVPStorageLayouts(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()

	common.HashingStateTrie = false
	common.HashingStorageTrie = true
	common.AddrHashOfCurrentStorageTrie = common.HexToHash("0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	common.ModifyHashMethod = "ShardVP"

	version := uint64(0x00123456)
	path := []byte{0xe}
	owner := "abcdef0123456789abcdef01"
	for _, prefixLen := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("prefix-%d", prefixLen), func(t *testing.T) {
			common.ShardOwnerPrefixLen = prefixLen
			got, err := modifyStructuredKey(version, common.TrieNodeData{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			versionStr := "00123456"
			versionSplit := len(versionStr) - prefixLen
			want := owner[:prefixLen] + versionStr[:versionSplit] + "f" + versionStr[versionSplit:] + owner[prefixLen:] + "e" + strings.Repeat("0", 28) + "01"
			gotHex := hex.EncodeToString(got)
			if gotHex != want {
				t.Fatalf("storage key mismatch\n got: %s\nwant: %s", gotHex, want)
			}
			if gotHex[8] != 'f' {
				t.Fatalf("trie type moved from collision-safe offset 8: %s", gotHex)
			}
		})
	}

	common.ShardOwnerPrefixLen = 3
	if _, err := modifyStructuredKey(version, common.TrieNodeData{Path: path}); err == nil {
		t.Fatal("unsupported owner prefix length was accepted")
	}
}

func TestATileVP64Layouts(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()

	common.ModifyHashMethod = "ATileVP"
	common.ATileBlocks = 64
	common.ATileStoragePathPrefixLen = 1
	version := uint64(0x00123456)

	common.HashingStateTrie = true
	common.HashingStorageTrie = false
	state, err := modifyStructuredKey(version, common.TrieNodeData{Path: []byte{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	wantState := "001234756123" + strings.Repeat("0", 50) + "03"
	if got := hex.EncodeToString(state); got != wantState {
		t.Fatalf("state key mismatch\n got: %s\nwant: %s", got, wantState)
	}

	common.HashingStateTrie = false
	common.HashingStorageTrie = true
	common.AddrHashOfCurrentStorageTrie = common.HexToHash("0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	storage, err := modifyStructuredKey(version, common.TrieNodeData{Path: []byte{0xe}})
	if err != nil {
		t.Fatal(err)
	}
	wantStorage := "0012347eaf37bc048d159e26af37bc0796" + strings.Repeat("0", 28) + "01"
	if got := hex.EncodeToString(storage); got != wantStorage {
		t.Fatalf("storage key mismatch\n got: %s\nwant: %s", got, wantStorage)
	}
}

func TestATileVPEndpointAndVersionOrdering(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()

	common.ModifyHashMethod = "ATileVP"
	common.ATileBlocks = 1
	common.HashingStateTrie = true
	common.HashingStorageTrie = false
	path := []byte{0xa, 0xb}
	version := uint64(0x12345678)
	got, err := modifyStructuredKey(version, common.TrieNodeData{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	wantVP := "12345678" + "d" + "ab" + strings.Repeat("0", 51) + "02"
	if gotHex := hex.EncodeToString(got); gotHex != wantVP {
		t.Fatalf("ATileBlocks=1 should match VP* state layout\n got: %s\nwant: %s", gotHex, wantVP)
	}

	common.ATileBlocks = 64
	previous, err := modifyStructuredKey(63, common.TrieNodeData{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	next, err := modifyStructuredKey(64, common.TrieNodeData{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Compare(previous, next) >= 0 {
		t.Fatalf("version tile order is not monotonic: %x >= %x", previous, next)
	}
}

func TestATileVPRejectsInvalidParameters(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()

	common.ModifyHashMethod = "ATileVP"
	common.ATileBlocks = 48
	if _, err := modifyStructuredKey(1, common.TrieNodeData{}); err == nil {
		t.Fatal("non-power-of-two ATileBlocks was accepted")
	}
	common.ATileBlocks = 64
	common.ATileStoragePathPrefixLen = 30
	common.HashingStateTrie = false
	common.HashingStorageTrie = true
	if _, err := modifyStructuredKey(1, common.TrieNodeData{}); err == nil {
		t.Fatal("invalid ATileStoragePathPrefixLen was accepted")
	}
}

func TestEpochPathEndpoints(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()

	common.ModifyHashMethod = "EpochPath"
	path := []byte{0xa, 0xb}
	version := uint64(0x12345678)

	common.EpochSize = 1
	got, err := modifyStructuredKey(version, common.TrieNodeData{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	wantVP := "12345678" + "d" + "ab" + strings.Repeat("0", 51) + "02"
	if gotHex := hex.EncodeToString(got); gotHex != wantVP {
		t.Fatalf("EpochSize=1 should match VP* layout\n got: %s\nwant: %s", gotHex, wantVP)
	}

	common.EpochSize = uint64(1) << 32
	got, err = modifyStructuredKey(version, common.TrieNodeData{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	wantPV := "d" + "ab" + strings.Repeat("0", 51) + "12345678" + "02"
	if gotHex := hex.EncodeToString(got); gotHex != wantPV {
		t.Fatalf("EpochSize=2^32 should match PV* layout\n got: %s\nwant: %s", gotHex, wantPV)
	}
}

func TestStructuredStorageKeyAndInvalidEpoch(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()

	common.HashingStateTrie = false
	common.HashingStorageTrie = true
	common.AddrHashOfCurrentStorageTrie = common.HexToHash("0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	common.ModifyHashMethod = "DepthSplit"

	got, err := modifyStructuredKey(0x42, common.TrieNodeData{Path: []byte{0xe}, Depth: 5})
	if err != nil {
		t.Fatal(err)
	}
	want := "1" + "fabcdef0123456789abcdef01" + "e" + strings.Repeat("0", 27) + "00000042" + "01"
	if gotHex := hex.EncodeToString(got); gotHex != want {
		t.Fatalf("storage key mismatch\n got: %s\nwant: %s", gotHex, want)
	}

	common.ModifyHashMethod = "EpochPath"
	common.EpochSize = 1000
	if _, err := modifyStructuredKey(0x42, common.TrieNodeData{}); err == nil {
		t.Fatal("expected non-power-of-16 EpochSize to fail")
	}
}

func TestStructuredKeyCommitAndReload(t *testing.T) {
	for _, method := range []string{"EpochPath", "TPV", "SplitPVHot", "OutwardSplit", "VPRight", "DepthSplit", "DepthEpoch", "ShardVP", "DualVP", "RunPath", "ForestVP", "ATileVP"} {
		t.Run(method, func(t *testing.T) {
			restore := setStructuredKeyTestGlobals()
			defer restore()

			common.ModifyHashMethod = method
			db := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
			parent := types.EmptyRootHash
			expected := make(map[string][]byte)

			for version := uint64(1); version <= 4; version++ {
				var tr *Trie
				if version == 1 {
					tr = NewEmpty(db)
				} else {
					var err error
					tr, err = New(TrieID(parent), db)
					if err != nil {
						t.Fatalf("open version %d: %v", version, err)
					}
				}

				key := bytes.Repeat([]byte{byte(version)}, 32)
				value := bytes.Repeat([]byte{byte(version + 16)}, 64)
				tr.MustUpdate(key, value)
				expected[string(key)] = value
				SetCurrentBlockNum(version)

				root, nodes, err := tr.Commit(false)
				if err != nil {
					t.Fatalf("commit version %d: %v", version, err)
				}
				if err := db.Update(root, parent, trienode.NewWithNodeSet(nodes)); err != nil {
					t.Fatalf("update database at version %d: %v", version, err)
				}
				if err := db.Commit(root); err != nil {
					t.Fatalf("flush database at version %d: %v", version, err)
				}

				reloaded, err := New(TrieID(root), db)
				if err != nil {
					t.Fatalf("reload version %d: %v", version, err)
				}
				for storedKey, want := range expected {
					got, err := reloaded.Get([]byte(storedKey))
					if err != nil {
						t.Fatalf("read version %d: %v", version, err)
					}
					if !bytes.Equal(got, want) {
						t.Fatalf("value mismatch at version %d", version)
					}
				}
				parent = root
			}
		})
	}
}

func TestStructuredKeyTimingReachesBlockCounter(t *testing.T) {
	for _, updates := range []int{1, 128} { // serial and parallel hashing thresholds
		t.Run(fmt.Sprint(updates), func(t *testing.T) {
			restore := setStructuredKeyTestGlobals()
			defer restore()
			oldTime, oldTrie := common.ModifyHashes, CurrentTrie
			oldReadStats, oldChildStats := common.MeasureReadStats, common.MeasureChildStats
			defer func() {
				common.ModifyHashes, CurrentTrie = oldTime, oldTrie
				common.MeasureReadStats, common.MeasureChildStats = oldReadStats, oldChildStats
			}()
			common.ModifyHashMethod = "OutwardSplit"
			common.MeasureReadStats, common.MeasureChildStats = false, false
			SetCurrentBlockNum(1)
			tr := NewEmpty(newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme))
			for i := 0; i < updates; i++ {
				tr.MustUpdate([]byte{byte(i)}, bytes.Repeat([]byte{1}, 64))
			}
			common.ModifyHashes = 0
			root := tr.Hash()
			if common.ModifyHashes <= 0 {
				t.Fatal("key-construction time did not reach the block counter")
			}
			common.ModifyHashes = 0
			if tr.Hash() != root || common.ModifyHashes != 0 {
				t.Fatal("cached hashing changed the root or counted key construction twice")
			}
		})
	}
}

func setStructuredKeyTestGlobals() func() {
	oldMethod := common.ModifyHashMethod
	oldVersionLength := common.VersionLength
	oldLenOfPathLen := common.LenOfPathLen
	oldAddrHashPrefixLen := common.AddrHashPrefixLen
	oldEpochSize := common.EpochSize
	oldDepthThreshold := common.DepthThreshold
	oldShardOwnerPrefixLen := common.ShardOwnerPrefixLen
	oldRunPathTargetNodes := common.RunPathTargetNodes
	oldATileBlocks := common.ATileBlocks
	oldATileStoragePathPrefixLen := common.ATileStoragePathPrefixLen
	oldHashingStateTrie := common.HashingStateTrie
	oldHashingStorageTrie := common.HashingStorageTrie
	oldAddrHash := common.AddrHashOfCurrentStorageTrie
	oldBlockNum := CurrentBlockNum
	oldRunPathID := currentRunPathID
	oldRunPathPendingNodes := runPathPendingNodes

	common.VersionLength = 8
	common.LenOfPathLen = 2
	common.AddrHashPrefixLen = 24
	common.EpochSize = 4096
	common.DepthThreshold = 4
	common.ShardOwnerPrefixLen = 1
	common.RunPathTargetNodes = 524288
	common.ATileBlocks = 64
	common.ATileStoragePathPrefixLen = 1
	ResetRunPath()
	common.HashingStateTrie = true
	common.HashingStorageTrie = false

	return func() {
		common.ModifyHashMethod = oldMethod
		common.VersionLength = oldVersionLength
		common.LenOfPathLen = oldLenOfPathLen
		common.AddrHashPrefixLen = oldAddrHashPrefixLen
		common.EpochSize = oldEpochSize
		common.DepthThreshold = oldDepthThreshold
		common.ShardOwnerPrefixLen = oldShardOwnerPrefixLen
		common.RunPathTargetNodes = oldRunPathTargetNodes
		common.ATileBlocks = oldATileBlocks
		common.ATileStoragePathPrefixLen = oldATileStoragePathPrefixLen
		common.HashingStateTrie = oldHashingStateTrie
		common.HashingStorageTrie = oldHashingStorageTrie
		common.AddrHashOfCurrentStorageTrie = oldAddrHash
		CurrentBlockNum = oldBlockNum
		currentRunPathID = oldRunPathID
		runPathPendingNodes = oldRunPathPendingNodes
	}
}

func TestEpochPathBitLayoutsAndLegacyCompatibility(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod = "EpochPath"
	common.AddrHashOfCurrentStorageTrie = common.HexToHash("abcdef0123456789abcdef010000000000000000000000000000000000000000")
	path := []byte{1, 2, 3}
	version := uint64(0x12345678)
	for _, storage := range []bool{false, true} {
		common.HashingStateTrie, common.HashingStorageTrie = !storage, storage
		common.EpochSize = 128
		got, err := modifyStructuredKey(version, common.TrieNodeData{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		want := "1234566891800000000000000000000000000000000000000000000000007803"
		if storage {
			want = "1234567d5e6f78091a2b3c4d5e6f780891800000000000000000000000007803"
		}
		if hex.EncodeToString(got) != want {
			t.Fatalf("EP-128 storage=%v: got %x, want %s", storage, got, want)
		}
		// The new bit encoder must preserve every old nibble-based EP layout.
		for digits := 0; digits <= 8; digits++ {
			common.EpochSize = uint64(1) << (4 * digits)
			got, err := modifyStructuredKey(version, common.TrieNodeData{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			v := fmt.Sprintf("%08x", version)
			location := "d" + "123" + strings.Repeat("0", 50)
			if storage {
				location = "f" + "abcdef0123456789abcdef01" + "123" + strings.Repeat("0", 26)
			}
			want := v[:8-digits] + location + v[8-digits:] + "03"
			if hex.EncodeToString(got) != want {
				t.Fatalf("legacy mismatch storage=%v epoch=%d: %x != %s", storage, common.EpochSize, got, want)
			}
		}
	}
}

func TestEpochPath128OrderingAndValidation(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod, common.EpochSize = "EpochPath", 128
	key := func(v uint64, path []byte) []byte {
		t.Helper()
		k, err := modifyStructuredKey(v, common.TrieNodeData{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	for _, storage := range []bool{false, true} {
		common.HashingStateTrie, common.HashingStorageTrie = !storage, storage
		a0, a1, b0 := key(128, []byte{1}), key(129, []byte{1}), key(128, []byte{2})
		if bytes.Compare(a0, a1) >= 0 || bytes.Compare(a1, b0) >= 0 {
			t.Fatal("revisions were not grouped by path inside epoch")
		}
		if bytes.Compare(key(127, []byte{15}), key(128, nil)) >= 0 {
			t.Fatal("successive epoch ranges overlap")
		}
		if bytes.Equal(a0, key(128, []byte{1, 0})) {
			t.Fatal("path padding lost the actual path length")
		}
		capacity := 53
		if storage {
			capacity = 29
		}
		key((uint64(1)<<32)-1, bytes.Repeat([]byte{15}, capacity))
		for _, path := range [][]byte{bytes.Repeat([]byte{0}, capacity+1), {16}} {
			if _, err := modifyStructuredKey(128, common.TrieNodeData{Path: path}); err == nil {
				t.Fatal("invalid path accepted")
			}
		}
	}
	if _, err := modifyStructuredKey(uint64(1)<<32, common.TrieNodeData{}); err == nil {
		t.Fatal("overflowing version accepted")
	}
	for _, epoch := range []uint64{0, 3, 1000, (uint64(1) << 32) + 1} {
		common.EpochSize = epoch
		if _, err := modifyStructuredKey(1, common.TrieNodeData{}); err == nil {
			t.Fatalf("invalid epoch %d accepted", epoch)
		}
	}
}

func TestEpochPath128PreservesHistoricalReferences(t *testing.T) {
	for _, storage := range []bool{false, true} {
		t.Run(fmt.Sprintf("storage=%v", storage), func(t *testing.T) {
			restore := setStructuredKeyTestGlobals()
			defer restore()
			common.ModifyHashMethod, common.EpochSize = "EpochPath", 128
			common.HashingStateTrie, common.HashingStorageTrie = !storage, storage
			common.AddrHashOfCurrentStorageTrie = common.HexToHash("123456789abcdef01234567890000000000000000000000000000000000000000")
			disk := rawdb.NewMemoryDatabase()
			db := newTestDatabase(disk, rawdb.HashScheme)
			parent := types.EmptyRootHash
			var roots []common.Hash
			var snapshots []map[string][]byte
			expected := make(map[string][]byte)
			versions := []uint64{126, 127, 128, 129, 255, 256}
			for i, version := range versions {
				tr, err := New(TrieID(parent), db)
				if err != nil {
					t.Fatal(err)
				}
				for j := 1; j <= 4; j++ {
					key := bytes.Repeat([]byte{byte(j)}, 32)
					// Keep some old-born children while rewriting one logical path
					// repeatedly on both sides of the epoch boundary.
					if i == 0 || j == 1 {
						value := bytes.Repeat([]byte{byte(version)}, 64)
						tr.MustUpdate(key, value)
						expected[string(key)] = value
					}
				}
				if i == 3 {
					key := bytes.Repeat([]byte{4}, 32)
					tr.MustDelete(key)
					delete(expected, string(key))
				}
				SetCurrentBlockNum(version)
				root, nodes, err := tr.Commit(false)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Update(root, parent, trienode.NewWithNodeSet(nodes)); err != nil {
					t.Fatal(err)
				}
				if err := db.Commit(root); err != nil {
					t.Fatal(err)
				}
				copy := make(map[string][]byte)
				for k, v := range expected {
					copy[k] = common.CopyBytes(v)
				}
				roots, snapshots = append(roots, root), append(snapshots, copy)
				parent = root
			}
			// No cached node sets: every historical root must resolve references
			// directly as physical DB keys, including children from old epochs.
			for i, root := range roots {
				tr, err := New(TrieID(root), newTestDatabase(disk, rawdb.HashScheme))
				if err != nil {
					t.Fatal(err)
				}
				for j := 1; j <= 4; j++ {
					key := bytes.Repeat([]byte{byte(j)}, 32)
					got, err := tr.Get(key)
					if err != nil || !bytes.Equal(got, snapshots[i][string(key)]) {
						t.Fatalf("version %d key %x: got %x, error %v", versions[i], key, got, err)
					}
				}
			}
		})
	}
}

func TestTPV128D3Layouts(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod, common.EpochSize, common.DepthThreshold = "TPV", 128, 3
	common.AddrHashOfCurrentStorageTrie = common.HexToHash("abcdef0123456789abcdef010000000000000000000000000000000000000000")
	// Independently generated integer-packing vectors, including both sides of
	// the cutoff. Deliberately incorrect traversal depth must not affect G.
	for _, test := range []struct {
		storage bool
		path    []byte
		want    string
	}{
		{false, nil, "1234563400000000000000000000000000000000000000000000000000003c00"},
		{false, []byte{1, 2, 3}, "1234563448c00000000000000000000000000000000000000000000000003c03"},
		{false, []byte{1, 2, 3, 4}, "1234567c6891a000000000000000000000000000000000000000000000000004"},
		{true, nil, "1234567c7d5e6f78091a2b3c4d5e6f7808000000000000000000000000000000"},
		{true, []byte{1, 2, 3, 4}, "1234567c7d5e6f78091a2b3c4d5e6f780891a000000000000000000000000004"},
	} {
		common.HashingStateTrie, common.HashingStorageTrie = !test.storage, test.storage
		got, err := modifyStructuredKey(0x12345678, common.TrieNodeData{Path: test.path, Depth: 0})
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(got) != test.want {
			t.Fatalf("storage=%v path=%x: %x != %s", test.storage, test.path, got, test.want)
		}
	}
}

func TestTPV128D3OrderingAndIdentity(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod, common.EpochSize, common.DepthThreshold = "TPV", 128, 3
	paths := [][]byte{nil, {1}, {1, 2}, {1, 2, 3}, {1, 2, 3, 4}, {9}, {9, 10}, {9, 10, 11}, {9, 10, 11, 12}}
	names := []string{"r", "a", "b", "c", "d", "x", "y", "z", "w"}
	type entry struct {
		name string
		key  []byte
	}
	var entries []entry
	for version := uint64(128); version <= 130; version++ {
		for i, path := range paths {
			key, err := modifyStructuredKey(version, common.TrieNodeData{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			entries = append(entries, entry{fmt.Sprintf("%s%d", names[i], version-127), key})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return bytes.Compare(entries[i].key, entries[j].key) < 0 })
	var got []string
	for _, entry := range entries {
		got = append(got, entry.name)
	}
	want := "r1 r2 r3 a1 a2 a3 b1 b2 b3 c1 c2 c3 x1 x2 x3 y1 y2 y3 z1 z2 z3 d1 w1 d2 w2 d3 w3"
	if strings.Join(got, " ") != want {
		t.Fatalf("wrong TPV order: %v", got)
	}
	// Across both trie sides, all body keys retain VP's relative ordering.
	var priorMax []byte
	for _, version := range []uint64{127, 128, 129, 255, 256, (uint64(1) << 32) - 1} {
		var body [][]byte
		for _, storage := range []bool{false, true} {
			common.HashingStateTrie, common.HashingStorageTrie = !storage, storage
			for _, path := range [][]byte{{1, 2, 3, 4}, {15, 15, 15, 15}} {
				key, err := modifyStructuredKey(version, common.TrieNodeData{Path: path})
				if err != nil {
					t.Fatal(err)
				}
				body = append(body, key)
			}
		}
		sort.Slice(body, func(i, j int) bool { return bytes.Compare(body[i], body[j]) < 0 })
		if priorMax != nil && bytes.Compare(priorMax, body[0]) >= 0 {
			t.Fatal("body version order lost")
		}
		priorMax = body[len(body)-1]
	}
	// Full identity remains distinct at padding ties and epoch endpoints.
	for _, epoch := range []uint64{1, 128, 256, uint64(1) << 32} {
		common.EpochSize = epoch
		seen := make(map[string]bool)
		for _, storage := range []bool{false, true} {
			common.HashingStateTrie, common.HashingStorageTrie = !storage, storage
			capacity := 53
			if storage {
				capacity = 29
			}
			for _, version := range []uint64{0, 127, 128, 129, (uint64(1) << 32) - 1} {
				for length := 0; length <= capacity; length++ {
					key, err := modifyStructuredKey(version, common.TrieNodeData{Path: make([]byte, length)})
					if err != nil {
						t.Fatal(err)
					}
					if len(key) != 32 || seen[string(key)] {
						t.Fatal("identity collision or wrong length")
					}
					seen[string(key)] = true
				}
			}
		}
	}
}

func TestTPVRejectsInvalidParameters(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod, common.EpochSize, common.DepthThreshold = "TPV", 128, 3
	for _, path := range [][]byte{{16}, make([]byte, 54)} {
		if _, err := modifyStructuredKey(1, common.TrieNodeData{Path: path}); err == nil {
			t.Fatal("invalid state path accepted")
		}
	}
	common.HashingStateTrie, common.HashingStorageTrie = false, true
	if _, err := modifyStructuredKey(1, common.TrieNodeData{Path: make([]byte, 30)}); err == nil {
		t.Fatal("invalid storage path accepted")
	}
	if _, err := modifyStructuredKey(uint64(1)<<32, common.TrieNodeData{}); err == nil {
		t.Fatal("version overflow accepted")
	}
	for _, cutoff := range []int64{-1, 54} {
		common.DepthThreshold = cutoff
		if _, err := modifyStructuredKey(1, common.TrieNodeData{}); err == nil {
			t.Fatal("invalid cutoff accepted")
		}
	}
	common.DepthThreshold = 3
	for _, epoch := range []uint64{0, 1000, (uint64(1) << 32) + 1} {
		common.EpochSize = epoch
		if _, err := modifyStructuredKey(1, common.TrieNodeData{}); err == nil {
			t.Fatal("invalid epoch accepted")
		}
	}
}

func TestTPVPreservesHistoricalReferences(t *testing.T) {
	testPartitionedHistoricalReferences(t, "TPV")
}

func TestSplitPVHotPreservesHistoricalReferences(t *testing.T) {
	testPartitionedHistoricalReferences(t, "SplitPVHot")
}

func testPartitionedHistoricalReferences(t *testing.T, method string) {
	for _, storage := range []bool{false, true} {
		t.Run(fmt.Sprintf("storage=%v", storage), func(t *testing.T) {
			restore := setStructuredKeyTestGlobals()
			defer restore()
			common.ModifyHashMethod, common.EpochSize, common.DepthThreshold = method, 128, 3
			common.HashingStateTrie, common.HashingStorageTrie = !storage, storage
			common.AddrHashOfCurrentStorageTrie = common.HexToHash("123456789abcdef01234567890000000000000000000000000000000000000000")
			disk := rawdb.NewMemoryDatabase()
			db := newTestDatabase(disk, rawdb.HashScheme)
			keys := [][]byte{}
			for _, prefix := range [][]byte{{0x12, 0x34, 0x50}, {0x12, 0x34, 0x60}, {0x12, 0x44}, {0x98}} {
				key := make([]byte, 32)
				copy(key, prefix)
				keys = append(keys, key)
			}
			versions := []uint64{126, 127, 128, 129, 255, 256}
			parent := types.EmptyRootHash
			var roots []common.Hash
			var snapshots []map[string][]byte
			expected := make(map[string][]byte)
			seenUpper, seenBody := false, false
			for i, version := range versions {
				tr, err := New(TrieID(parent), db)
				if err != nil {
					t.Fatal(err)
				}
				for j, key := range keys {
					if i == 0 || j == 0 || (i == 4 && j == 1) {
						value := bytes.Repeat([]byte{byte(version)}, 64)
						tr.MustUpdate(key, value)
						expected[string(key)] = value
					}
				}
				if i == 3 {
					tr.MustDelete(keys[1])
					delete(expected, string(keys[1]))
				}
				SetCurrentBlockNum(version)
				root, nodes, err := tr.Commit(false)
				if err != nil {
					t.Fatal(err)
				}
				for path, node := range nodes.Nodes {
					if node.IsDeleted() {
						continue
					}
					want, err := modifyStructuredKey(version, common.TrieNodeData{Path: []byte(path)})
					if err != nil {
						t.Fatal(err)
					}
					if node.Hash != common.BytesToHash(want) {
						t.Fatal("persisted node ID differs from constructed key")
					}
					if storage || len(path) > 3 {
						seenBody = true
					} else {
						seenUpper = true
					}
				}
				if err := db.Update(root, parent, trienode.NewWithNodeSet(nodes)); err != nil {
					t.Fatal(err)
				}
				if err := db.Commit(root); err != nil {
					t.Fatal(err)
				}
				for _, node := range nodes.Nodes {
					if !node.IsDeleted() && !bytes.Equal(rawdb.ReadLegacyTrieNode(disk, node.Hash), node.Blob) {
						t.Fatal("node blob not stored under its identical 32B ID")
					}
				}
				snapshot := make(map[string][]byte)
				for key, value := range expected {
					snapshot[key] = common.CopyBytes(value)
				}
				roots = append(roots, root)
				snapshots = append(snapshots, snapshot)
				parent = root
			}
			if !seenBody || (!storage && !seenUpper) {
				t.Fatal("fixture did not cover required classes")
			}
			for i, root := range roots {
				tr, err := New(TrieID(root), newTestDatabase(disk, rawdb.HashScheme))
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range keys {
					got, err := tr.Get(key)
					if err != nil || !bytes.Equal(got, snapshots[i][string(key)]) {
						t.Fatalf("version %d key %x: got %x, error %v", versions[i], key, got, err)
					}
				}
			}
		})
	}
}

func TestOutwardSplitPreservesHistoricalReferences(t *testing.T) {
	testPartitionedHistoricalReferences(t, "OutwardSplit")
}

func TestVPRightPreservesHistoricalReferences(t *testing.T) {
	testPartitionedHistoricalReferences(t, "VPRight")
}

// Independent binary-string oracle covers length padding ties, both owners,
// maximum paths and full version boundaries without using bitKeyBuilder.
func TestSplitPVHotIdentityAndLayout(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod, common.DepthThreshold = "SplitPVHot", 3
	seen := make(map[string]bool)
	for _, storage := range []bool{false, true} {
		common.HashingStateTrie, common.HashingStorageTrie = !storage, storage
		width, section := 53, 13
		owners := []byte{0}
		if storage {
			width, section, owners = 29, 15, []byte{0, 255}
		}
		for _, owner := range owners {
			common.AddrHashOfCurrentStorageTrie = common.BytesToHash(bytes.Repeat([]byte{owner}, 32))
			for _, version := range []uint64{0, 1, 127, 128, 129, 0x12345678, 0xfffffffe, 0xffffffff} {
				for depth := 0; depth <= width; depth++ {
					for pattern := byte(0); pattern <= 1; pattern++ {
						if depth == 0 && pattern == 1 {
							continue
						}
						path := bytes.Repeat([]byte{pattern * 15}, depth)
						cold := !storage && depth <= 3
						padded := strings.Repeat(fmt.Sprintf("%04b", pattern*15), depth) + strings.Repeat("0", (width-depth)*4)
						prefix := "0" + fmt.Sprintf("%032b%04b", version, section)
						if storage {
							prefix += strings.Repeat(fmt.Sprintf("%08b", owner), 12)
						}
						bits := prefix + padded + fmt.Sprintf("%07b", depth)
						if cold {
							bits = "1" + fmt.Sprintf("%04b", section) + padded + fmt.Sprintf("%032b%07b", version, depth)
						}
						x, ok := new(big.Int).SetString(bits, 2)
						if !ok || len(bits) != 256 {
							t.Fatal("invalid oracle")
						}
						want := x.FillBytes(make([]byte, 32))
						got, err := modifyStructuredKey(version, common.TrieNodeData{Path: path, Depth: 99})
						if err != nil || !bytes.Equal(got, want) || seen[string(got)] {
							t.Fatalf("storage=%v version=%d depth=%d: got %x want %x err=%v", storage, version, depth, got, want, err)
						}
						seen[string(got)] = true
						// Epoch is inactive, including invalid values for epoch schemes.
						common.EpochSize = 0
						again, err := modifyStructuredKey(version, common.TrieNodeData{Path: path})
						if err != nil || !bytes.Equal(got, again) {
							t.Fatal("key depends on epoch")
						}
					}
				}
			}
		}
	}
}

func TestSplitPVHotThreeBlockOrdering(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod, common.DepthThreshold = "SplitPVHot", 3
	type entry struct {
		name string
		key  []byte
	}
	var entries []entry
	for v := uint64(128); v <= 130; v++ {
		for d := 0; d <= 4; d++ {
			key, err := modifyStructuredKey(v, common.TrieNodeData{Path: bytes.Repeat([]byte{1}, d)})
			if err != nil {
				t.Fatal(err)
			}
			entries = append(entries, entry{fmt.Sprintf("d%dv%d", d, v), key})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return bytes.Compare(entries[i].key, entries[j].key) < 0 })
	var names []string
	for _, e := range entries {
		names = append(names, e.name)
	}
	want := "d4v128 d4v129 d4v130 d0v128 d0v129 d0v130 d1v128 d1v129 d1v130 d2v128 d2v129 d2v130 d3v128 d3v129 d3v130"
	if strings.Join(names, " ") != want {
		t.Fatal(names)
	}
}

func TestSplitPVHotInvalidInput(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod, common.DepthThreshold = "SplitPVHot", 3
	for _, path := range [][]byte{{16}, make([]byte, 54)} {
		if _, err := modifyStructuredKey(1, common.TrieNodeData{Path: path}); err == nil {
			t.Fatal("invalid path accepted")
		}
	}
	common.HashingStateTrie, common.HashingStorageTrie = false, true
	if _, err := modifyStructuredKey(1, common.TrieNodeData{Path: make([]byte, 30)}); err == nil {
		t.Fatal("storage overflow accepted")
	}
	if _, err := modifyStructuredKey(1<<32, common.TrieNodeData{}); err == nil {
		t.Fatal("version overflow accepted")
	}
	for _, depth := range []int64{-1, 54} {
		common.DepthThreshold = depth
		if _, err := modifyStructuredKey(1, common.TrieNodeData{}); err == nil {
			t.Fatal("invalid cutoff accepted")
		}
	}
	common.DepthThreshold = 3
	common.HashingStateTrie = true
	if _, err := modifyStructuredKey(1, common.TrieNodeData{}); err == nil {
		t.Fatal("ambiguous side accepted")
	}
}

func TestVPRightIdentityOrderAndOutwardBody(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod = "VPRight"
	type pair struct{ vp, right []byte }
	var pairs []pair
	seen := make(map[string]bool)
	for _, storage := range []bool{false, true} {
		common.HashingStateTrie, common.HashingStorageTrie = !storage, storage
		width, section := 53, "d"
		owners := []byte{0}
		if storage {
			width, section, owners = 29, "f", []byte{0, 255}
		}
		for _, owner := range owners {
			common.AddrHashOfCurrentStorageTrie = common.BytesToHash(bytes.Repeat([]byte{owner}, 32))
			for _, v := range []uint64{0, 1, 127, 128, 129, 0x12345678, 0xfffffffe, 0xffffffff} {
				for depth := 0; depth <= width; depth++ {
					for _, nibble := range []byte{0, 15} {
						if depth == 0 && nibble != 0 {
							continue
						}
						path := bytes.Repeat([]byte{nibble}, depth)
						x := section
						if storage {
							x += hex.EncodeToString(common.AddrHashOfCurrentStorageTrie[:12])
						}
						x += strings.Repeat(fmt.Sprintf("%x", nibble), depth) + strings.Repeat("0", width-depth)
						vp, err := hex.DecodeString(fmt.Sprintf("%08x%s%02x", v, x, depth))
						if err != nil {
							t.Fatal(err)
						}
						// Independent integer transform of the original nibble-aligned VP key.
						want := new(big.Int).SetBytes(vp)
						want.Rsh(want, 8).Lsh(want, 6)
						want.Or(want, big.NewInt(int64(depth))).SetBit(want, 255, 1)
						common.EpochSize, common.DepthThreshold = 0, -100 // Inactive for VPRight.
						got, err := modifyStructuredKey(v, common.TrieNodeData{Path: path})
						if err != nil || len(got) != 32 || !bytes.Equal(got, want.FillBytes(make([]byte, 32))) {
							t.Fatalf("identity/layout mismatch: storage=%v version=%d depth=%d err=%v", storage, v, depth, err)
						}
						if got[0] < 0x80 || got[0] > 0xbf || seen[string(got)] {
							t.Fatal("namespace violation or identity collision")
						}
						seen[string(got)] = true
						common.EpochSize, common.DepthThreshold = 128, 3
						if storage || depth > 3 {
							outward, err := modifyOutwardSplitKey(v, common.TrieNodeData{Path: path})
							if err != nil || !bytes.Equal(got, outward) {
								t.Fatal("VPRight differs from Outward body")
							}
						}
						pairs = append(pairs, pair{vp: vp, right: got})
					}
				}
			}
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return bytes.Compare(pairs[i].vp, pairs[j].vp) < 0 })
	for i := 1; i < len(pairs); i++ {
		if bytes.Compare(pairs[i-1].right, pairs[i].right) >= 0 {
			t.Fatal("original VP order was not preserved")
		}
	}
}

func TestVPRightRejectsInvalidInput(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod = "VPRight"
	common.HashingStateTrie, common.HashingStorageTrie = true, false
	if _, err := modifyStructuredKey(1<<32, common.TrieNodeData{}); err == nil {
		t.Fatal("accepted overflowing version")
	}
	if _, err := modifyStructuredKey(1, common.TrieNodeData{Path: []byte{16}}); err == nil {
		t.Fatal("accepted invalid path nibble")
	}
	common.HashingStorageTrie = true
	if _, err := modifyStructuredKey(1, common.TrieNodeData{}); err == nil {
		t.Fatal("accepted ambiguous trie side")
	}
	common.HashingStorageTrie, common.LenOfPathLen = false, 1
	if _, err := modifyStructuredKey(1, common.TrieNodeData{}); err == nil {
		t.Fatal("accepted incompatible field configuration")
	}
}

func TestOutwardSplitIdentityAndLayout(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod, common.DepthThreshold = "OutwardSplit", 3
	// Independent binary-string oracle, including zero-width epoch/offset,
	// full version range, padding ties, owner changes and maximum path lengths.
	field := func(v uint64, w int) string {
		if w == 0 {
			return ""
		}
		return fmt.Sprintf("%0*b", w, v)
	}
	for _, epoch := range []uint64{1, 2, 128, 1 << 32} {
		common.EpochSize = epoch
		w := bits.Len64(epoch - 1)
		seen := make(map[string]bool)
		for _, storage := range []bool{false, true} {
			common.HashingStateTrie, common.HashingStorageTrie = !storage, storage
			width, section := 53, uint64(13)
			owners := []byte{0}
			if storage {
				width, section, owners = 29, 15, []byte{0, 255}
			}
			for _, owner := range owners {
				common.AddrHashOfCurrentStorageTrie = common.BytesToHash(bytes.Repeat([]byte{owner}, 32))
				for _, v := range []uint64{0, 1, 127, 128, 129, 0x12345678, 0xfffffffe, 0xffffffff} {
					for depth := 0; depth <= width; depth++ {
						for pattern := byte(0); pattern <= 1; pattern++ {
							if depth == 0 && pattern == 1 {
								continue
							}
							x := field(section, 4)
							if storage {
								x += strings.Repeat(field(uint64(owner), 8), 12)
							}
							x += strings.Repeat(field(uint64(pattern*15), 4), depth) + strings.Repeat("0", (width-depth)*4)
							oracle := "10" + field(v, 32) + x + field(uint64(depth), 6)
							cold := !storage && depth <= 3
							if cold {
								r := (uint64(1)<<(32-w) - 1) - (v >> w)
								oracle = "00" + field(r, 32-w) + x + field(v&(epoch-1), w) + field(uint64(depth), 6)
							}
							n, ok := new(big.Int).SetString(oracle, 2)
							if !ok || len(oracle) != 256 {
								t.Fatal("oracle width")
							}
							got, err := modifyStructuredKey(v, common.TrieNodeData{Path: bytes.Repeat([]byte{pattern * 15}, depth), Depth: 99})
							if err != nil || !bytes.Equal(got, n.FillBytes(make([]byte, 32))) || seen[string(got)] {
								t.Fatalf("epoch=%d storage=%v v=%d depth=%d got=%x err=%v", epoch, storage, v, depth, got, err)
							}
							seen[string(got)] = true
							if (cold && got[0] >= 0x40) || (!cold && (got[0] < 0x80 || got[0] > 0xbf)) {
								t.Fatal("namespace ordering lost")
							}
						}
					}
				}
			}
		}
	}
}

func TestOutwardSplitCodeBoundaryAndL0Range(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod, common.EpochSize, common.DepthThreshold = "OutwardSplit", 128, 3
	key := func(v uint64, d int, nibble byte) []byte {
		k, err := modifyStructuredKey(v, common.TrieNodeData{Path: bytes.Repeat([]byte{nibble}, d)})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	code := append([]byte{'c'}, bytes.Repeat([]byte{0xff}, 32)...)
	newCold, oldCold := key(256, 3, 15), key(255, 3, 0)
	oldBody, newBody := key(255, 4, 15), key(256, 4, 0)
	ordered := [][]byte{newCold, oldCold, code, oldBody, newBody}
	for i := 1; i < len(ordered); i++ {
		if bytes.Compare(ordered[i-1], ordered[i]) >= 0 {
			t.Fatal("outward growth condition")
		}
	}
	// The whole new mixed L0 range still covers BOTH old classes.
	if !(bytes.Compare(newCold, oldBody) < 0 && bytes.Compare(oldCold, newBody) < 0) {
		t.Fatal("L0 overlap counterexample")
	}
	// Within an epoch the same path's revisions remain contiguous by u.
	if !(bytes.Compare(key(128, 3, 0), key(129, 3, 0)) < 0 && bytes.Compare(key(129, 3, 0), key(128, 3, 15)) < 0) {
		t.Fatal("path/revision order lost")
	}
}

func TestOutwardSplitRejectsInvalidInput(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	common.ModifyHashMethod, common.EpochSize, common.DepthThreshold = "OutwardSplit", 128, 3
	for _, path := range [][]byte{{16}, make([]byte, 54)} {
		if _, err := modifyStructuredKey(1, common.TrieNodeData{Path: path}); err == nil {
			t.Fatal("invalid state path accepted")
		}
	}
	common.HashingStateTrie, common.HashingStorageTrie = false, true
	if _, err := modifyStructuredKey(1, common.TrieNodeData{Path: make([]byte, 30)}); err == nil {
		t.Fatal("storage path overflow accepted")
	}
	if _, err := modifyStructuredKey(1<<32, common.TrieNodeData{}); err == nil {
		t.Fatal("version overflow accepted")
	}
	for _, epoch := range []uint64{0, 3, (1 << 32) + 1} {
		common.EpochSize = epoch
		if _, err := modifyStructuredKey(1, common.TrieNodeData{}); err == nil {
			t.Fatal("invalid epoch accepted")
		}
	}
	common.EpochSize = 128
	for _, depth := range []int64{-1, 54} {
		common.DepthThreshold = depth
		if _, err := modifyStructuredKey(1, common.TrieNodeData{}); err == nil {
			t.Fatal("invalid cutoff accepted")
		}
	}
	common.DepthThreshold, common.HashingStateTrie = 3, true
	if _, err := modifyStructuredKey(1, common.TrieNodeData{}); err == nil {
		t.Fatal("ambiguous side accepted")
	}
}

func TestOutwardStorageCodecAndDispatch(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	oldDepth := common.StorageDepthThreshold
	defer func() { common.StorageDepthThreshold = oldDepth }()
	common.ModifyHashMethod, common.DepthThreshold, common.StorageDepthThreshold = "OutwardStorage", 3, 1
	field := func(v uint64, w int) string {
		if w == 0 {
			return ""
		}
		return fmt.Sprintf("%0*b", w, v)
	}
	for _, epoch := range []uint64{1, 128, 1 << 32} {
		common.EpochSize = epoch
		w := bits.Len64(epoch - 1)
		for _, storage := range []bool{false, true} {
			common.HashingStateTrie, common.HashingStorageTrie = !storage, storage
			maxPath := 53
			if storage {
				maxPath = 29
			}
			for _, owner := range []byte{0, 255} {
				common.AddrHashOfCurrentStorageTrie = common.BytesToHash(bytes.Repeat([]byte{owner}, 32))
				for _, branch := range []bool{false, true} {
					var n node = &shortNode{Key: []byte{16}}
					if branch {
						n = &fullNode{}
					}
					for _, v := range []uint64{0, 1, 127, 128, 255, 256, 0xffffffff} {
						for d := 0; d <= maxPath; d++ {
							tnd := common.TrieNodeData{Path: bytes.Repeat([]byte{15}, d), Depth: 99}
							got, err := modifyOutwardStorageKey(v, tnd, n)
							if err != nil || len(got) != 32 {
								t.Fatalf("codec: %x %v", got, err)
							}
							if !bytes.Equal(got, modifyHashV5(n, make(hashNode, 32), v, tnd)) {
								t.Fatal("hash dispatch bypassed selector")
							}
							selected := storage && branch && d <= 1
							if !selected {
								want, err := modifyOutwardSplitKey(v, tnd)
								if err != nil || !bytes.Equal(got, want) {
									t.Fatal("unselected/account key changed")
								}
								continue
							}
							oracle := "00" + field((uint64(1)<<(32-w)-1)-(v>>w), 32-w) + "1111" + strings.Repeat(field(uint64(owner), 8), 12) + strings.Repeat("1111", d) + strings.Repeat("0", (29-d)*4) + field(v&(epoch-1), w) + field(uint64(d), 6)
							value, ok := new(big.Int).SetString(oracle, 2)
							if !ok || len(oracle) != 256 || !bytes.Equal(got, value.FillBytes(make([]byte, 32))) {
								t.Fatal("cold storage field order/identity")
							}
						}
					}
				}
			}
		}
	}
	common.EpochSize, common.HashingStateTrie, common.HashingStorageTrie = 128, false, true
	a, _ := modifyOutwardStorageKey(127, common.TrieNodeData{}, &fullNode{})
	b, _ := modifyOutwardStorageKey(128, common.TrieNodeData{Path: []byte{15}}, &fullNode{})
	if bytes.Compare(b, a) >= 0 || a[0] >= 0x40 {
		t.Fatal("cold storage must grow left across epochs")
	}
	for _, cutoff := range []int64{-1, 30} {
		common.StorageDepthThreshold = cutoff
		if _, err := modifyOutwardStorageKey(1, common.TrieNodeData{}, &fullNode{}); err == nil {
			t.Fatal("invalid storage cutoff")
		}
	}
	common.StorageDepthThreshold = 1
	if _, err := modifyOutwardStorageKey(1<<32, common.TrieNodeData{}, &fullNode{}); err == nil {
		t.Fatal("version overflow")
	}
}

// Force leaf -> extension/branch -> root branch -> leaf transitions across
// epoch boundaries, then reopen every historical root from a fresh node cache.
func TestOutwardStorageHistoricalShapeTransitions(t *testing.T) {
	restore := setStructuredKeyTestGlobals()
	defer restore()
	oldDepth := common.StorageDepthThreshold
	defer func() { common.StorageDepthThreshold = oldDepth }()
	common.ModifyHashMethod, common.EpochSize, common.DepthThreshold, common.StorageDepthThreshold = "OutwardStorage", 128, 3, 1
	common.HashingStateTrie, common.HashingStorageTrie = false, true
	disk := rawdb.NewMemoryDatabase()
	db := newTestDatabase(disk, rawdb.HashScheme)
	keys := [][]byte{make([]byte, 32), make([]byte, 32), make([]byte, 32), make([]byte, 32)}
	keys[0][0], keys[1][0], keys[2][0], keys[3][0], keys[3][1] = 0x10, 0x11, 0x20, 0x10, 0x10
	versions := []uint64{126, 127, 128, 129, 255, 256}
	sets := [][]int{{0}, {0, 1}, {0, 1, 2}, {0, 1, 2, 3}, {0}, {0, 1, 2, 3}}
	parent := types.EmptyRootHash
	var roots []common.Hash
	var snapshots []map[string][]byte
	seen := map[string]bool{}
	for i, v := range versions {
		tr, err := New(TrieID(parent), db)
		if err != nil {
			t.Fatal(err)
		}
		wanted := map[string][]byte{}
		for j, key := range keys {
			present := false
			for _, index := range sets[i] {
				if index == j {
					present = true
				}
			}
			if present {
				value := bytes.Repeat([]byte{byte(v), byte(j + 1)}, 32)
				tr.MustUpdate(key, value)
				wanted[string(key)] = value
			} else {
				tr.MustDelete(key)
			}
		}
		SetCurrentBlockNum(v)
		root, nodes, err := tr.Commit(false)
		if err != nil {
			t.Fatal(err)
		}
		for path, entry := range nodes.Nodes {
			if entry.IsDeleted() {
				continue
			}
			n, err := decodeNode(entry.Hash[:], entry.Blob)
			if err != nil {
				t.Fatal(err)
			}
			_, branch := n.(*fullNode)
			cold := branch && len(path) <= 1
			if (entry.Hash[0] < 0x40) != cold {
				t.Fatalf("wrong class path=%x branch=%v key=%x", path, branch, entry.Hash)
			}
			seen[fmt.Sprintf("%T:%d", n, len(path))] = true
		}
		if err := db.Update(root, parent, trienode.NewWithNodeSet(nodes)); err != nil {
			t.Fatal(err)
		}
		if err := db.Commit(root); err != nil {
			t.Fatal(err)
		}
		for _, entry := range nodes.Nodes {
			if !entry.IsDeleted() && !bytes.Equal(rawdb.ReadLegacyTrieNode(disk, entry.Hash), entry.Blob) {
				t.Fatal("ID differs from DB key")
			}
		}
		roots = append(roots, root)
		snapshots = append(snapshots, wanted)
		parent = root
	}
	for _, kind := range []string{"*trie.shortNode:0", "*trie.fullNode:0", "*trie.fullNode:1", "*trie.fullNode:2"} {
		if !seen[kind] {
			t.Fatalf("fixture missed %s: %v", kind, seen)
		}
	}
	for i, root := range roots {
		tr, err := New(TrieID(root), newTestDatabase(disk, rawdb.HashScheme))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range keys {
			got, err := tr.Get(key)
			if err != nil || !bytes.Equal(got, snapshots[i][string(key)]) {
				t.Fatalf("historical version %d path %x: %x %v", versions[i], key, got, err)
			}
		}
	}
}

// Independent run histories cannot be matched solely by (birth,path): a
// delete/insert order can recreate an unchanged leaf after branch collapse.
func TestStructuredBirthDependsOnEditOrder(t *testing.T) {
	for _, scheme := range []string{"JMT_fixed", "TPV"} {
		t.Run(scheme, func(t *testing.T) {
			restore := setStructuredKeyTestGlobals()
			defer restore()
			common.ModifyHashMethod, common.EpochSize, common.DepthThreshold = scheme, 128, 3
			var recreated [2]bool
			for order := 0; order < 2; order++ {
				disk := rawdb.NewMemoryDatabase()
				db := newTestDatabase(disk, rawdb.HashScheme)
				a, b, c := make([]byte, 32), make([]byte, 32), make([]byte, 32)
				a[0], b[0], c[0] = 0x10, 0x11, 0x12
				value := bytes.Repeat([]byte{7}, 40)
				tr, err := New(TrieID(types.EmptyRootHash), db)
				if err != nil {
					t.Fatal(err)
				}
				tr.MustUpdate(a, value)
				tr.MustUpdate(b, value)
				SetCurrentBlockNum(1)
				root, nodes, err := tr.Commit(false)
				if err != nil {
					t.Fatal(err)
				}
				if err = db.Update(root, types.EmptyRootHash, trienode.NewWithNodeSet(nodes)); err != nil {
					t.Fatal(err)
				}
				if err = db.Commit(root); err != nil {
					t.Fatal(err)
				}
				tr, err = New(TrieID(root), db)
				if err != nil {
					t.Fatal(err)
				}
				if order == 0 {
					tr.MustDelete(b)
					tr.MustUpdate(c, value)
				} else {
					tr.MustUpdate(c, value)
					tr.MustDelete(b)
				}
				if !bytes.Equal(tr.MustGet(a), value) || !bytes.Equal(tr.MustGet(c), value) || len(tr.MustGet(b)) != 0 {
					t.Fatal("different logical state")
				}
				SetCurrentBlockNum(2)
				_, nodes, err = tr.Commit(false)
				if err != nil {
					t.Fatal(err)
				}
				node, ok := nodes.Nodes[string([]byte{1, 0})]
				recreated[order] = ok && !node.IsDeleted()
				t.Logf("order=%d unchanged leaf A rewritten=%v", order, recreated[order])
			}
			if !recreated[0] || recreated[1] {
				t.Fatalf("expected collapse/reexpand to recreate A only for delete-first, got %v", recreated)
			}
		})
	}
}
