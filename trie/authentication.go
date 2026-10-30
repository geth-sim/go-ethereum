package trie

// Experimental authentication adapter. Geth remains responsible for Patricia
// structure, CoW, dirty batching, embedding, locators, caches and commits.
// Authentication is deliberately separate from the 32-byte child/db locator.
import (
	"bytes"
	"errors"
	"fmt"
	"math/bits"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie/incauth"
)

// nodeAuth is immutable after publication, including changes and state. Changes
// contain only first-before-image commitments of touched branch slots, not a
// permanent array of all child hashes. CoW copies may safely share this object.
type nodeAuth struct {
	suite    byte
	kind     byte
	state    []byte // branch accumulator; extension C; account leaf storage C
	c        incauth.Commitment
	mask     uint16
	pending  bool
	deferred bool // disk metadata retained; compute commitment only when used
	changes  []incauth.Change
}
type trieAuth struct {
	scheme *incauth.Scheme
	suite  byte
	full   bool
}

// AuthParallelMinUpdates controls the existing 16-slot authentication fork.
// The ordinary key hasher keeps its independent 100-update threshold.
// Workers change execution only; they are not part of the persisted auth suite.
const AuthParallelMinUpdates = 1

// Execution policy only; persisted authentication format and roots are unchanged.
const AuthImplementation = "v2-auth-threshold-1"

// The default 16-worker policy matches geth's one goroutine per branch slot.
// Smaller worker counts are explicit scaling controls, with fixed slot groups.
const AuthParallelPolicy = "geth-slots16-v2;reader=concurrent"

// authHasher belongs to one Hash call. Workers only construct CoW results; the
// calling goroutine publishes the root after all workers join. Parallel calls
// use hashdb's concurrent node reads; custom readers must support the same.
type authHasher struct {
	trie    *Trie
	workers int
}

func (t *Trie) authenticationWorkers() int {
	if t.unhashed < AuthParallelMinUpdates || common.MeasureChildStats || common.MeasureReadStats {
		return 1
	}
	// Like geth, launch the goroutines even when GOMAXPROCS is smaller. The
	// runtime limits executing threads, not the number of branch slot tasks.
	return common.TrieAuthWorkers
}

var authSchemes sync.Map

var authComputationCaches sync.Map // startup configurations; CLI options never change during a run

func authenticationCache() (*incauth.ComputationCache, error) {
	key := struct {
		mb   int
		mode string
	}{common.TrieAuthCacheMB, common.TrieAuthCacheMode}
	if c, ok := authComputationCaches.Load(key); ok {
		return c.(*incauth.ComputationCache), nil
	}
	c, err := incauth.NewComputationCache(key.mb, key.mode)
	if err != nil {
		return nil, err
	}
	actual, _ := authComputationCaches.LoadOrStore(key, c)
	return actual.(*incauth.ComputationCache), nil
}

// Cumulative since cache creation, including both state and storage. Snapshot
// only at result checkpoints; no per-node global atomic measurement is needed.
func AuthenticationCacheStats() incauth.ComputationCacheStats {
	c, err := authenticationCache()
	if err != nil {
		return incauth.ComputationCacheStats{}
	}
	return c.Stats()
}

func authenticationScheme(suite, kind byte) (*incauth.Scheme, error) {
	key := struct {
		suite, kind byte
		mb          int
		mode        string
	}{suite, kind, common.TrieAuthCacheMB, common.TrieAuthCacheMode}
	if s, ok := authSchemes.Load(key); ok {
		return s.(*incauth.Scheme), nil
	}
	var id string
	switch suite {
	case 1:
		id = incauth.Ristretto255
	case 2:
		id = incauth.P256
	case 3:
		id = incauth.Keccak
	case 4:
		id = incauth.GLS254
	default:
		return nil, errors.New("unknown authentication suite")
	}
	s, err := incauth.NewScheme(id, kind)
	if err != nil {
		return nil, err
	}
	cache, err := authenticationCache()
	if err != nil {
		return nil, err
	}
	s = s.WithCache(cache)
	actual, _ := authSchemes.LoadOrStore(key, s)
	return actual.(*incauth.Scheme), nil
}
func AuthenticationConfig() (string, error) {
	if common.TrieAuthCacheMB < 0 || common.TrieAuthCacheMB > 1<<20 {
		return "", errors.New("invalid authentication cache MiB")
	}
	if m := common.TrieAuthCacheMode; m != "unified" && m != "accumulator" && m != "contribution" {
		return "", errors.New("invalid authentication cache mode")
	}
	if common.TrieAuthCacheMB > 0 && common.TrieAuthentication != "ristretto255" && common.TrieAuthentication != "p256" && common.TrieAuthentication != "gls254" {
		return "", errors.New("computation cache requires a curve authentication backend")
	}
	if common.TrieAuthWorkers < 1 || common.TrieAuthWorkers > 16 {
		return "", errors.New("TrieAuthWorkers must be in [1,16]")
	}
	if common.TrieAuthentication == "" {
		return "off", nil
	}
	var suite byte
	switch common.TrieAuthentication {
	case "ristretto255":
		suite = 1
	case "p256":
		suite = 2
	case "keccak":
		suite = 3
	case "gls254":
		suite = 4
	default:
		return "", errors.New("unknown TrieAuthentication")
	}
	if common.TrieAuthMode != "incremental" && common.TrieAuthMode != "full" {
		return "", errors.New("unknown TrieAuthMode")
	}
	if suite == 3 && common.TrieAuthMode != "full" {
		return "", errors.New("keccak authentication requires full mode")
	}
	if common.ReadAllChildNodes || common.AdditionalByteLen != 0 || common.DiskSizeMultiplier != 1 {
		return "", errors.New("real authentication cannot be combined with simulated myHash overhead")
	}
	if common.EnableSnapshot || common.IsPathScheme {
		return "", errors.New("authentication adapter currently requires hashdb with snapshot disabled")
	}
	s, err := authenticationScheme(suite, incauth.Account)
	if err != nil {
		return "", err
	}
	return "geth-incauth-envelope-v1;incauth-v2;" + s.ID(), nil
}

// CheckAuthenticationDatabase prevents opening an old unauthenticated DB, or
// mixing suites. Full/incremental are evaluation policies with identical format.
func CheckAuthenticationDatabase(db ethdb.KeyValueStore) error {
	config, err := AuthenticationConfig()
	if err != nil {
		return err
	}
	key := []byte("incauth-format")
	have, err := db.Has(key)
	if err != nil {
		return err
	}
	if have {
		old, err := db.Get(key)
		if err != nil {
			return err
		}
		if string(old) != config {
			return errors.New("database authentication format mismatch")
		}
		return nil
	}
	if config == "off" {
		return nil
	}
	it := db.NewIterator(nil, nil)
	nonempty := it.Next()
	err = it.Error()
	it.Release()
	if err != nil {
		return err
	}
	if nonempty {
		return errors.New("authentication requires a new DB; existing nodes have no accumulators")
	}
	return db.Put(key, []byte(config))
}
func (t *Trie) configureAuthentication() error {
	if common.TrieAuthentication == "" {
		if nodeAuthentication(t.root) != nil {
			return errors.New("authenticated trie opened with authentication disabled")
		}
		return nil
	}
	if _, err := AuthenticationConfig(); err != nil {
		return err
	}
	suite := byte(1)
	if common.TrieAuthentication == "p256" {
		suite = 2
	}
	if common.TrieAuthentication == "keccak" {
		suite = 3
	}
	if common.TrieAuthentication == "gls254" {
		suite = 4
	}
	kind := byte(incauth.Account)
	if t.owner != (common.Hash{}) {
		kind = incauth.Storage
	}
	s, err := authenticationScheme(suite, kind)
	if err != nil {
		return err
	}
	t.auth = &trieAuth{s, suite, common.TrieAuthMode == "full"}
	if t.root != nil {
		a := nodeAuthentication(t.root)
		if a == nil || a.suite != suite || a.kind != kind {
			return errors.New("root authentication metadata missing or incompatible")
		}
	}
	return nil
}
func nodeAuthentication(n node) *nodeAuth {
	switch n := n.(type) {
	case *fullNode:
		return n.flags.auth
	case *shortNode:
		return n.flags.auth
	}
	return nil
}

// commitment never mutates shared CoW metadata. Ordinary reads trust the local
// DB and need only the value. Before-image capture, parent/root hashing, proofs
// and audits call here when a commitment is actually needed. Branch updates
// independently decode/validate the old accumulator through Scheme.Update.
func (a *nodeAuth) commitment(n node) (incauth.Commitment, error) {
	if a.pending {
		return incauth.Commitment{}, errors.New("unfinished authentication commitment")
	}
	if !a.deferred {
		return a.c, nil
	}
	s, err := authenticationScheme(a.suite, a.kind)
	if err != nil {
		return incauth.Commitment{}, err
	}
	switch n := n.(type) {
	case *fullNode:
		return s.Branch(a.state)
	case *shortNode:
		if hasTerm(n.Key) {
			value, err := authenticatedLeafValue(n.Val.(valueNode), a)
			if err != nil {
				return incauth.Commitment{}, err
			}
			return s.Leaf(n.Key[:len(n.Key)-1], value)
		}
	}
	return incauth.Commitment{}, errors.New("invalid deferred authentication node")
}

func branchMask(n *fullNode) uint16 {
	var m uint16
	for i := 0; i < 16; i++ {
		if n.Children[i] != nil {
			m |= 1 << i
		}
	}
	return m
}

// Resolve a touched child on the ordinary update path, so its old commitment is
// captured from the node that geth would read anyway. No clean siblings are read.
func (t *Trie) authBeforeChild(n *fullNode, slot byte, path []byte) (node, nodeFlag, error) {
	flags := t.newFlag()
	child := n.Children[slot]
	if t.auth == nil {
		return child, flags, nil
	}
	if slot >= 16 {
		return nil, flags, errors.New("authentication supports fixed 32-byte state/storage keys")
	}
	a := n.flags.auth
	if a == nil || t.auth.full {
		return child, flags, nil
	}
	copyA := *a
	copyA.pending = true
	flags.auth = &copyA
	for _, ch := range a.changes {
		if ch.Slot == slot {
			return child, flags, nil
		}
	}
	resolved, err := t.resolve(child, path)
	if err != nil {
		return nil, flags, err
	}
	var old incauth.Child
	if resolved != nil {
		c, err := t.authCommitment(resolved, path)
		if err != nil {
			return nil, flags, err
		}
		old = incauth.Child{Present: true, C: c}
	}
	copyA.changes = append(append([]incauth.Change(nil), a.changes...), incauth.Change{Slot: slot, Old: old})
	return resolved, flags, nil
}
func (t *Trie) authResolve(n node, path []byte) (node, error) {
	return (&authHasher{trie: t}).resolve(n, path)
}
func (h *authHasher) resolve(n node, path []byte) (node, error) {
	if ref, ok := n.(hashNode); ok {
		atomic.AddUint64(&common.AuthNodeReads, 1)
		blob, err := h.trie.reader.node(path, common.BytesToHash(ref))
		// Keep decoded nodes independent of the backend's immutable blob.
		blob = common.CopyBytes(blob)
		if err != nil {
			return nil, err
		}
		return decodeNodeUnsafe(ref, blob)
	}
	return n, nil
}
func (t *Trie) authCommitment(n node, path []byte) (incauth.Commitment, error) {
	return (&authHasher{trie: t}).commitment(n, path)
}
func (h *authHasher) commitment(n node, path []byte) (incauth.Commitment, error) {
	if n == nil {
		return h.trie.auth.scheme.Empty(), nil
	}
	n, err := h.resolve(n, path)
	if err != nil {
		return incauth.Commitment{}, err
	}
	if a := nodeAuthentication(n); a != nil && !a.pending {
		if a.suite != h.trie.auth.suite || a.kind != h.trie.auth.scheme.Type() {
			return incauth.Commitment{}, errors.New("child authentication suite/type mismatch")
		}
		return a.commitment(n)
	}
	// Embedded nodes carry no envelope and must be reconstructed from their small
	// inline payload. A referenced external node must always have metadata.
	if h, _ := n.cache(); h != nil {
		return incauth.Commitment{}, errors.New("external node missing authentication metadata")
	}
	_, c, err := h.authenticate(n, path, false)
	return c, err
}
func (t *Trie) authenticateRoot() error {
	start := time.Now()
	h := &authHasher{trie: t, workers: t.authenticationWorkers()}
	root, c, err := h.authenticate(t.root, nil, false)
	atomic.AddInt64((*int64)(&common.AuthHashes), int64(time.Since(start)))
	if err == nil {
		t.root = root
		t.authRoot = common.Hash(c)
	}
	return err
}
func (t *Trie) authenticate(n node, path []byte, audit bool) (node, incauth.Commitment, error) {
	return (&authHasher{trie: t}).authenticate(n, path, audit)
}
func (h *authHasher) authenticate(n node, path []byte, audit bool) (node, incauth.Commitment, error) {
	if audit && len(path) > 64 {
		return nil, incauth.Commitment{}, errors.New("authentication audit path exceeds key length")
	}
	if n == nil {
		return nil, h.trie.auth.scheme.Empty(), nil
	}
	if !audit {
		if a := nodeAuthentication(n); a != nil && !a.pending {
			if a.suite != h.trie.auth.suite || a.kind != h.trie.auth.scheme.Type() {
				return nil, incauth.Commitment{}, errors.New("node authentication suite/type mismatch")
			}
			c, err := a.commitment(n)
			return n, c, err
		}
		if _, ok := n.(hashNode); ok {
			c, err := h.commitment(n, path)
			return n, c, err
		}
	}
	var err error
	n, err = h.resolve(n, path)
	if err != nil {
		return nil, incauth.Commitment{}, err
	}
	prior := nodeAuthentication(n)
	if prior != nil && (prior.suite != h.trie.auth.suite || prior.kind != h.trie.auth.scheme.Type()) {
		return nil, incauth.Commitment{}, errors.New("node authentication suite/type mismatch")
	}
	var priorC incauth.Commitment
	if audit && prior != nil {
		priorC, err = prior.commitment(n)
		if err != nil {
			return nil, priorC, err
		}
	}
	a := &nodeAuth{suite: h.trie.auth.suite, kind: h.trie.auth.scheme.Type()}
	s := h.trie.auth.scheme
	switch nn := n.(type) {
	case *shortNode:
		cp := nn.copy()
		if hasTerm(nn.Key) {
			if audit && len(path)+len(nn.Key)-1 != 64 {
				return nil, a.c, errors.New("authentication audit leaf path length")
			}
			val, ok := nn.Val.(valueNode)
			if !ok {
				return nil, a.c, errors.New("invalid leaf")
			}
			if s.Type() == incauth.Account {
				if prior == nil || len(prior.state) != 32 {
					return nil, a.c, errors.New("account leaf missing storage authentication root")
				}
				a.state = prior.state
			}
			logical, err := authenticatedLeafValue(val, a)
			if err != nil {
				return nil, a.c, err
			}
			a.c, err = s.Leaf(nn.Key[:len(nn.Key)-1], logical)
			if err != nil {
				return nil, a.c, err
			}
		} else {
			if audit && (len(nn.Key) == 0 || len(path)+len(nn.Key) >= 64) {
				return nil, a.c, errors.New("authentication audit extension path length")
			}
			child, c, err := h.authenticate(nn.Val, concat(path, nn.Key...), audit)
			if err != nil {
				return nil, a.c, err
			}
			if audit {
				if _, ok := child.(*fullNode); !ok {
					return nil, a.c, errors.New("authentication audit extension child must be branch")
				}
			}
			cp.Val = child
			a.c, err = s.Extension(nn.Key, c)
			if err != nil {
				return nil, a.c, err
			}
			a.state = append([]byte(nil), a.c[:]...)
		}
		cp.flags.auth = a
		n = cp
	case *fullNode:
		if audit && len(path) >= 64 {
			return nil, a.c, errors.New("authentication audit branch exceeds key length")
		}
		if nn.Children[16] != nil {
			return nil, a.c, errors.New("authentication requires fixed-length keys")
		}
		cp := nn.copy()
		if err := h.children(nn, cp, path, audit); err != nil {
			return nil, a.c, err
		}
		a.mask = branchMask(cp)
		if !audit && !h.trie.auth.full && prior != nil && len(prior.state) > 0 {
			changes := append([]incauth.Change(nil), prior.changes...)
			for i := range changes {
				slot := changes[i].Slot
				if cp.Children[slot] != nil {
					c, err := h.commitment(cp.Children[slot], concat(path, slot))
					if err != nil {
						return nil, a.c, err
					}
					changes[i].New = incauth.Child{Present: true, C: c}
				}
			}
			var mask uint16
			a.state, a.c, mask, err = s.Update(prior.state, prior.mask, changes)
			if err == nil && mask != a.mask {
				err = errors.New("branch delta mask mismatch")
			}
			atomic.AddUint64(&common.AuthBranchUpdates, 1)
		} else {
			var cs [16]incauth.Child
			for i := 0; i < 16; i++ {
				if cp.Children[i] != nil {
					c, e := h.commitment(cp.Children[i], concat(path, byte(i)))
					if e != nil {
						return nil, a.c, e
					}
					cs[i] = incauth.Child{Present: true, C: c}
				}
			}
			a.state, a.c, err = s.Build(cs)
			atomic.AddUint64(&common.AuthBranchBuilds, 1)
		}
		if err != nil {
			return nil, a.c, err
		}
		cp.flags.auth = a
		n = cp
	default:
		return nil, a.c, fmt.Errorf("invalid authenticated node %T", n)
	}
	if audit && prior != nil && priorC != a.c {
		return nil, a.c, errors.New("authentication audit mismatch")
	}
	return n, a.c, nil
}

// children follows hashFullNodeChildren: fork at the first uncached branch,
// including nil and cached slots, then process each subtree serially. With 16
// workers each goroutine owns exactly one slot. Do not move the fork deeper
// when only one child is dirty: that would optimize one authentication control.
func (h *authHasher) children(n, result *fullNode, path []byte, audit bool) error {
	if h.workers <= 1 || audit {
		for slot := 0; slot < 16; slot++ {
			if err := h.child(n, result, path, slot, audit); err != nil {
				return err
			}
		}
		return nil
	}
	workers := h.workers
	var wg sync.WaitGroup
	var errs [16]error
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			childHasher := &authHasher{trie: h.trie, workers: 1}
			for slot := i; slot < 16; slot += workers {
				errs[slot] = childHasher.child(n, result, path, slot, false)
			}
		}(i)
	}
	wg.Wait()
	atomic.AddUint64(&common.AuthParallelBatches, 1)
	atomic.AddUint64(&common.AuthParallelTasks, 16)
	// Inspect in slot order, independent of worker scheduling. No partial root
	// is published on error, and every worker has exited before returning.
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Test cached/nil children inside their assigned worker, as geth does. A clean
// hashNode is already a locator; commitment() reads its authentication payload
// only if the parent's authentication algorithm needs it.
func (h *authHasher) child(n, result *fullNode, path []byte, slot int, audit bool) error {
	child := n.Children[slot]
	if child == nil {
		return nil
	}
	if !audit {
		if a := nodeAuthentication(child); a != nil && !a.pending {
			return nil
		}
		if _, hashed := child.(hashNode); hashed {
			return nil
		}
	}
	next, _, err := h.authenticate(child, concat(path, byte(slot)), audit)
	if err == nil {
		result.Children[slot] = next
	}
	return err
}

func authenticatedLeafValue(value []byte, a *nodeAuth) ([]byte, error) {
	if a == nil {
		return value, nil
	} // inline storage leaf; accounts never fit inline
	if a.kind != incauth.Account {
		return value, nil
	}
	if len(a.state) != 32 {
		return nil, errors.New("missing account storage commitment")
	}
	var account types.StateAccount
	if err := rlp.DecodeBytes(value, &account); err != nil {
		return nil, err
	}
	account.Root = common.BytesToHash(a.state)
	return rlp.EncodeToBytes(&account)
}

// On-disk envelope: RLP([original_node_RLP, auth_payload, [1,suite,type]]).
// Branch payload=S; extension payload=C; account leaf payload=storage C;
// storage leaf payload=empty. Nested inline nodes keep geth's original RLP.
func decodeAuthenticatedNode(hash, raw []byte, recoverAuth bool) (node, error) {
	// Split slices refer to the immutable input, like decodeNodeUnsafe. Decode
	// the ordinary inner node once, without reflection or temporary RLP copies.
	outer, rest, err := rlp.SplitList(raw)
	if err != nil || len(rest) != 0 {
		return nil, errors.New("authentication envelope list")
	}
	inner, rest, err := rlp.SplitList(outer)
	if err != nil {
		return nil, err
	}
	payload, rest, err := rlp.SplitString(rest)
	if err != nil {
		return nil, err
	}
	tag, rest, err := rlp.SplitString(rest)
	if err != nil || len(rest) != 0 || len(tag) != 3 || tag[0] != 1 {
		return nil, errors.New("authentication envelope version")
	}
	s, err := authenticationScheme(tag[1], tag[2])
	if err != nil {
		return nil, err
	}
	count, err := rlp.CountValues(inner)
	if err != nil {
		return nil, err
	}
	var n node
	switch count {
	case 2:
		n, err = decodeShort(hash, inner)
	case 17:
		n, err = decodeFull(hash, inner)
	default:
		return nil, errors.New("authentication inner node")
	}
	if err != nil {
		return nil, err
	}
	a := &nodeAuth{suite: tag[1], kind: tag[2], state: payload}
	switch n := n.(type) {
	case *fullNode:
		if n.Children[16] != nil {
			return nil, errors.New("authentication branch value")
		}
		a.mask = branchMask(n)
		if bits.OnesCount16(a.mask) < 2 {
			return nil, errors.New("noncanonical authenticated branch")
		}
		if len(payload) != s.StateSize() {
			return nil, errors.New("branch state length")
		}
		if !recoverAuth {
			return n, nil
		}
		a.deferred = true
		n.flags.auth = a
	case *shortNode:
		if hasTerm(n.Key) {
			if a.kind == incauth.Storage && len(payload) != 0 {
				return nil, errors.New("storage leaf authentication payload")
			}
			if a.kind == incauth.Account && len(payload) != 32 {
				return nil, errors.New("missing account storage commitment")
			}
			if !recoverAuth {
				return n, nil
			}
			a.deferred = true
		} else {
			if len(payload) != 32 {
				return nil, errors.New("extension authentication payload")
			}
			if !recoverAuth {
				return n, nil
			}
			copy(a.c[:], payload)
		}
		n.flags.auth = a
	}
	return n, err
}

// AuthenticationRoot is separate from Hash(), which continues to return the DB
// root locator. The experimental root is not Ethereum's canonical state root.
func (t *Trie) AuthenticationRoot() common.Hash {
	if t.auth == nil {
		panic("authentication disabled")
	}
	if t.committed && t.root != nil {
		return t.authRoot
	}
	if a := nodeAuthentication(t.root); a != nil && !a.pending {
		c, err := a.commitment(t.root)
		if err != nil {
			panic(err)
		}
		return common.Hash(c)
	}
	if err := t.authenticateRoot(); err != nil {
		panic(err)
	}
	if t.root == nil {
		return common.Hash(t.auth.scheme.Empty())
	}
	c, err := t.authCommitment(t.root, nil)
	if err != nil {
		panic(err)
	}
	return common.Hash(c)
}
func (t *StateTrie) AuthenticationRoot() common.Hash { return t.trie.AuthenticationRoot() }

// AuditAuthentication recomputes this trie's commitment and checks its canonical
// fixed-key structure. The caller must compare the result with an independently
// trusted root. Account audits do not traverse the separate storage tries; audit
// those too when validating imported state. Ordinary reads trust the local DB.
func (t *Trie) AuditAuthentication() (common.Hash, error) {
	if t.auth == nil {
		return common.Hash{}, errors.New("authentication disabled")
	}
	if err := t.authenticateRoot(); err != nil {
		return common.Hash{}, err
	}
	_, c, err := t.authenticate(t.root, nil, true)
	return common.Hash(c), err
}
func (t *StateTrie) AuditAuthentication() (common.Hash, error) { return t.trie.AuditAuthentication() }

func (t *Trie) accountLeaf(key []byte) (*shortNode, error) {
	// Populate the normal geth update-path cache, without a second DB read later.
	if _, err := t.Get(key); err != nil {
		return nil, err
	}
	n, path := t.root, keybytesToHex(key)
	for n != nil {
		switch nn := n.(type) {
		case *shortNode:
			if !bytes.HasPrefix(path, nn.Key) {
				return nil, nil
			}
			path = path[len(nn.Key):]
			if hasTerm(nn.Key) {
				return nn, nil
			}
			n = nn.Val
		case *fullNode:
			n = nn.Children[path[0]]
			path = path[1:]
		default:
			return nil, errors.New("unresolved account path")
		}
	}
	return nil, nil
}
func (t *StateTrie) UpdateAccountWithStorageAuth(address common.Address, acc *types.StateAccount, storageRoot *common.Hash) error {
	if t.trie.auth == nil {
		return errors.New("authentication disabled")
	}
	hk := common.CopyBytes(t.hashKey(address.Bytes()))
	var c common.Hash
	if acc.Root == types.EmptyRootHash || acc.Root == (common.Hash{}) {
		s, _ := authenticationScheme(t.trie.auth.suite, incauth.Storage)
		c = common.Hash(s.Empty())
	} else if storageRoot != nil {
		c = *storageRoot
	} else {
		leaf, err := t.trie.accountLeaf(hk)
		if err != nil {
			return err
		}
		if leaf == nil || leaf.flags.auth == nil {
			return errors.New("nonempty storage root requires its authentication commitment")
		}
		var old types.StateAccount
		if err := rlp.DecodeBytes(leaf.Val.(valueNode), &old); err != nil {
			return err
		}
		if old.Root != acc.Root {
			return errors.New("storage root changed without authentication commitment")
		}
		c = common.BytesToHash(leaf.flags.auth.state)
	}
	data, err := rlp.EncodeToBytes(acc)
	if err != nil {
		return err
	}
	// Normal insert records old branch commitments before replacing values.
	if err = t.trie.Update(hk, data); err != nil {
		return err
	}
	root, err := t.trie.attachStorageAuth(t.trie.root, keybytesToHex(hk), nil, c)
	if err != nil {
		return err
	}
	t.trie.root = root
	t.getSecKeyCache()[string(hk)] = address.Bytes()
	return nil
}
func (t *Trie) attachStorageAuth(n node, key, path []byte, c common.Hash) (node, error) {
	switch n := n.(type) {
	case *shortNode:
		cp := n.copy()
		if hasTerm(n.Key) {
			if a := n.flags.auth; a != nil && !a.pending && bytes.Equal(a.state, c[:]) {
				return n, nil
			}
			cp.flags = t.newFlag()
			cp.flags.auth = &nodeAuth{suite: t.auth.suite, kind: incauth.Account, state: common.CopyBytes(c[:]), pending: true}
			return cp, nil
		}
		child, err := t.attachStorageAuth(n.Val, key[len(n.Key):], concat(path, n.Key...), c)
		if err != nil {
			return nil, err
		}
		if child == n.Val {
			return n, nil
		}
		cp.Val = child
		cp.flags = t.newFlag()
		return cp, nil
	case *fullNode:
		slot := key[0]
		child, flags, err := t.authBeforeChild(n, slot, concat(path, slot))
		if err != nil {
			return nil, err
		}
		next, err := t.attachStorageAuth(child, key[1:], concat(path, slot), c)
		if err != nil {
			return nil, err
		}
		if next == child {
			return n, nil
		}
		cp := n.copy()
		cp.flags = flags
		cp.Children[slot] = next
		return cp, nil
	default:
		return nil, errors.New("account leaf missing after update")
	}
}

// AuthenticationProof uses the reference implementation's proof format and
// independent verifier. Account values contain storage C, not the DB locator.
func (t *Trie) AuthenticationProof(key []byte) ([]byte, error) {
	if t.auth == nil || len(key) != 32 {
		return nil, errors.New("authentication proof requires a 32-byte hashed key")
	}
	if err := t.authenticateRoot(); err != nil {
		return nil, err
	}
	p := incauth.Proof{Schema: 2, Suite: t.auth.scheme.ID(), Type: t.auth.scheme.Type()}
	copy(p.Key[:], key)
	n, path, prefix := t.root, keybytesToHex(key)[:64], []byte(nil)
	for n != nil {
		var err error
		n, err = t.authResolve(n, prefix)
		if err != nil {
			return nil, err
		}
		f := incauth.ProofFrame{}
		switch nn := n.(type) {
		case *shortNode:
			if hasTerm(nn.Key) {
				f.Kind = incauth.LeafNode
				f.Path = nn.Key[:len(nn.Key)-1]
				f.Value, err = authenticatedLeafValue(nn.Val.(valueNode), nodeAuthentication(nn))
				if err != nil {
					return nil, err
				}
				n = nil
			} else {
				f.Kind = incauth.ExtensionNode
				f.Path = nn.Key
				c, e := t.authCommitment(nn.Val, concat(prefix, nn.Key...))
				if e != nil {
					return nil, e
				}
				f.Children = []incauth.Commitment{c}
				if !bytes.HasPrefix(path, nn.Key) {
					n = nil
				} else {
					n = nn.Val
					path = path[len(nn.Key):]
					prefix = concat(prefix, nn.Key...)
				}
			}
		case *fullNode:
			f.Kind = incauth.BranchNode
			f.Mask = branchMask(nn)
			for i := 0; i < 16; i++ {
				if nn.Children[i] != nil {
					c, e := t.authCommitment(nn.Children[i], concat(prefix, byte(i)))
					if e != nil {
						return nil, e
					}
					f.Children = append(f.Children, c)
				}
			}
			if len(path) == 0 {
				return nil, errors.New("proof branch depth")
			}
			n = nn.Children[path[0]]
			prefix = concat(prefix, path[0])
			path = path[1:]
		default:
			return nil, errors.New("proof node type")
		}
		p.Frames = append(p.Frames, f)
		if len(p.Frames) > 65 {
			return nil, errors.New("proof depth")
		}
	}
	return rlp.EncodeToBytes(p)
}

// AuthenticationProof takes the already-hashed 32-byte key, like StateTrie.Prove.
func (t *StateTrie) AuthenticationProof(key []byte) ([]byte, error) {
	return t.trie.AuthenticationProof(key)
}

// Only account leaf storage C survives a path split/merge. The node's own C
// depends on its changed compact path and must be recomputed.
func (t *Trie) authShortFlag(old *shortNode) nodeFlag {
	f := t.newFlag()
	if t.auth != nil && hasTerm(old.Key) && old.flags.auth != nil && old.flags.auth.kind == incauth.Account {
		f.auth = &nodeAuth{suite: t.auth.suite, kind: incauth.Account, state: old.flags.auth.state, pending: true}
	}
	return f
}
