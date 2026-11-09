package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

var replayConfigKey = []byte("research/replay/config/v1")

// ReplayRoot separates a physical locator from a cryptographic commitment.
// AuthenticationRoot is nil for plain VP/PV/Outward. It must never be reported
// as Ethereum's state root. Block hashes and headers always remain canonical.
type ReplayRoot struct {
	Run                common.ReplayRun
	PhysicalRoot       common.Hash
	AuthenticationRoot *common.Hash
}

type ReplayResult struct {
	Number                    uint64
	GasUsed                   uint64
	PaymentTxLen, CallTxLen   int
	PaymentTxTime, CallTxTime time.Duration // ApplyTransaction, including state reads/finalisation; excludes sender recovery and async receipt bloom
	BlockHash                 common.Hash
	CanonicalRoot             common.Hash
	ReplayRoot
	Stats    ExecuteStats
	Research common.ReplayStats
}

// initReplay refuses accidental configuration changes or opening a research DB
// with ordinary Geth. Call before genesis or any other database mutation.
func initReplay(db ethdb.Database, cfg *BlockChainConfig) error {
	has, err := db.Has(replayConfigKey)
	if err != nil {
		return err
	}
	stateDB, stateOnly := db.(*ReplayDatabase)
	if cfg.Replay != nil && cfg.Replay.StateOnly != stateOnly {
		return errors.New("state-only setting requires matching replay database wrapper")
	}
	if cfg.Replay == nil {
		if has {
			return errors.New("research database requires its original replay configuration")
		}
		return nil
	}
	if _, err := trie.AuthenticationConfig(cfg.Replay); err != nil {
		return err
	}
	if err := cfg.Replay.Validate(); err != nil {
		return err
	}
	pbss := cfg.Replay.KeyScheme == "PBSS"
	if (!pbss && cfg.StateScheme != rawdb.HashScheme) ||
		(pbss && (cfg.StateScheme != rawdb.PathScheme || cfg.ArchiveMode)) ||
		(!pbss && !cfg.ArchiveMode && cfg.TrieDirtyLimit <= 0) ||
		cfg.SnapshotLimit < 0 || cfg.StatelessSelfValidation || cfg.VmConfig.Tracer != nil {
		return errors.New("replay requires hashdb (positive dirty cache in full mode) or native PBSS full mode, no tracer or stateless self-validation")
	}
	if stateOnly {
		stateDB.store.snapshot = !pbss && cfg.SnapshotLimit > 0
	}
	descriptor := struct {
		Schema      int
		Persistence string
		Snapshot    bool `json:",omitempty"`
		NonArchive  bool `json:",omitempty"`
	}{2, cfg.Replay.PersistenceID(), !pbss && cfg.SnapshotLimit > 0, !pbss && !cfg.ArchiveMode}
	want, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}
	if has {
		have, err := db.Get(replayConfigKey)
		if err != nil {
			return err
		}
		if !bytes.Equal(have, want) {
			return errors.New("replay configuration differs from database; use a fresh database")
		}
		return nil
	}
	cfg.Replay.SetVersion(0)
	if rawdb.ReadCanonicalHash(db, 0) != (common.Hash{}) {
		return errors.New("replay requires a fresh database")
	}
	return db.Put(replayConfigKey, want)
}

func replayBlockKey(hash common.Hash) []byte { return append([]byte("research/block/"), hash[:]...) }
func replayStateKey(root common.Hash) []byte { return append([]byte("research/state/"), root[:]...) }

func writeReplayRoot(db ethdb.KeyValueWriter, block *types.Block, physical common.Hash, cfg *common.ReplayConfig, auth *common.Hash) error {
	record := ReplayRoot{PhysicalRoot: physical, AuthenticationRoot: auth, Run: cfg.RunState()}
	if cfg.CanonicalHashing() {
		record.AuthenticationRoot = &physical
	}
	return storeReplayRoot(db, block, record)
}

func storeReplayRoot(db ethdb.KeyValueWriter, block *types.Block, record ReplayRoot) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := db.Put(replayBlockKey(block.Hash()), data); err != nil {
		return err
	}
	// StateAt addresses a logical state by canonical root. If the same logical
	// state recurs, this alias may point to its latest representation. Execution
	// uses the unambiguous block-hash record, retaining historical versions.
	// Include the owning block: the physical root itself may also repeat.
	owner := block.Hash()
	alias := append(record.PhysicalRoot.Bytes(), owner[:]...)
	return db.Put(replayStateKey(block.Root()), alias)
}

// FlushReplayState persists the same three recent states as native hashdb
// shutdown, and returns the physical roots needed for GC after a state-only
// restart. This is a clean-stop operation outside import timing, not periodic
// archive-style flushing. Other recent states are allowed to remain unpersisted.
func (bc *BlockChain) FlushReplayState() (map[common.Hash]ReplayRoot, error) {
	if bc.cfg.Replay == nil || bc.cfg.ArchiveMode || bc.triedb.Scheme() != rawdb.HashScheme {
		return nil, errors.New("flush replay state requires hashdb non-archive replay")
	}
	roots := make(map[common.Hash]ReplayRoot)
	head := bc.CurrentBlock().Number.Uint64()
	for offset := uint64(0); offset < state.TriesInMemory && offset < head; offset++ {
		block := bc.GetBlockByNumber(head - offset)
		if block == nil {
			return nil, fmt.Errorf("missing recent block %d", head-offset)
		}
		root, err := bc.ReplayRoot(block.Hash())
		if err != nil {
			return nil, err
		}
		roots[block.Hash()] = root
		if offset == 0 || offset == 1 || offset == state.TriesInMemory-1 {
			if err := bc.triedb.Commit(root.PhysicalRoot, false); err != nil {
				return nil, err
			}
		}
	}
	return roots, nil
}

func (bc *BlockChain) ReplayRoot(hash common.Hash) (ReplayRoot, error) {
	var root ReplayRoot
	data, err := bc.db.Get(replayBlockKey(hash))
	if err != nil {
		return root, fmt.Errorf("missing replay root for block %s: %w", hash, err)
	}
	err = json.Unmarshal(data, &root)
	return root, err
}

func (bc *BlockChain) replayStateRoot(root common.Hash) (common.Hash, error) {
	if bc.cfg.Replay == nil {
		return root, nil
	}
	data, err := bc.db.Get(replayStateKey(root))
	if err != nil {
		return common.Hash{}, err
	}
	if len(data) != common.HashLength && len(data) != 2*common.HashLength {
		return common.Hash{}, errors.New("invalid replay root record")
	}
	return common.BytesToHash(data[:common.HashLength]), nil
}

// Snapshots follow physical roots, while headers retain mainnet commitments.
func (bc *BlockChain) replaySnapshotRoot(header *types.Header) (common.Hash, error) {
	if bc.cfg.Replay == nil {
		return header.Root, nil
	}
	record, err := bc.ReplayRoot(header.Hash())
	return record.PhysicalRoot, err
}

func (bc *BlockChain) prepareReplay(chain types.Blocks) error {
	if bc.cfg.Replay == nil {
		return nil
	}
	head := bc.CurrentBlock()
	if chain[0].NumberU64() != head.Number.Uint64()+1 || chain[0].ParentHash() != head.Hash() {
		return errors.New("research replay accepts only append-only canonical blocks")
	}
	for _, b := range chain {
		if b.NumberU64() >= 1<<32 || bc.chainConfig.IsUBT(b.Number(), b.Time()) || bc.chainConfig.IsAmsterdam(b.Number(), b.Time()) {
			return errors.New("research replay supports MPT blocks before Amsterdam, versions below 2^32")
		}
	}
	return nil
}

func skipReplayIntermediateRoot(st *state.StateDB) bool {
	cfg := st.Database().TrieDB().ReplayConfig()
	return cfg != nil && cfg.SkipIntermediateRoot
}

func (v *BlockValidator) skipReplayReceiptRoot(block *types.Block) bool {
	cfg := v.bc.cfg.Replay
	return cfg != nil && (cfg.SkipBlockRootChecks || (cfg.SkipIntermediateRoot && !v.config.IsByzantium(block.Number())))
}

// VerifyReplayState rebuilds the canonical MPT from every persisted account and
// storage leaf. It is an explicit OFFLINE correctness check, never part of timed
// import. It works without preimages and detects missing/extra accounts or slots.
func (bc *BlockChain) VerifyReplayState(block *types.Block) error {
	record, err := bc.ReplayRoot(block.Hash())
	if err != nil {
		return err
	}
	var rebuild func(*trie.ID, bool) (common.Hash, error)
	rebuild = func(id *trie.ID, accounts bool) (common.Hash, error) {
		source, err := trie.New(id, bc.triedb)
		if err != nil {
			return common.Hash{}, err
		}
		nodes, err := source.NodeIterator(nil)
		if err != nil {
			return common.Hash{}, err
		}
		it := trie.NewIterator(nodes)
		target := trie.NewStackTrie(nil)
		for it.Next() {
			value := it.Value
			if accounts {
				var account types.StateAccount
				if err := rlp.DecodeBytes(value, &account); err != nil {
					return common.Hash{}, err
				}
				account.Root, err = rebuild(trie.StorageTrieID(record.PhysicalRoot, common.BytesToHash(it.Key), account.Root), false)
				if err != nil {
					return common.Hash{}, err
				}
				value, err = rlp.EncodeToBytes(&account)
				if err != nil {
					return common.Hash{}, err
				}
			}
			if err := target.Update(it.Key, value); err != nil {
				return common.Hash{}, err
			}
		}
		return target.Hash(), it.Err
	}
	root, err := rebuild(trie.StateTrieID(record.PhysicalRoot), true)
	if err != nil {
		return err
	}
	if root != block.Root() {
		return fmt.Errorf("reconstructed state root %s differs from canonical %s", root, block.Root())
	}
	return nil
}

// ReplayDatabase keeps hashdb trie nodes/code and optional snapshots on disk. All
// ordinary Geth chain writes still execute, but target a bounded memory store.
// Iterators deliberately expose the persistent state database for inspection.
// This mode is append-only research replay, not a general-purpose Geth database.
type ReplayDatabase struct {
	ethdb.Database
	store *replayStore
}
type replayStore struct {
	ethdb.KeyValueStore
	memory   *memorydb.Database
	path     bool
	snapshot bool
}

// Native pathdb opens its own state-history freezer using AncientDatadir.
// Supplying the directory does not enable the block/receipt freezer.
type replayPathHistory struct {
	ethdb.Database
	dir string
}

func (d *replayPathHistory) AncientDatadir() (string, error) { return d.dir, nil }

func NewReplayPathDatabase(kv ethdb.KeyValueStore, ancient string, stateOnly bool) ethdb.Database {
	if !stateOnly {
		return &replayPathHistory{rawdb.NewDatabase(kv), ancient}
	}
	db := NewReplayDatabase(kv)
	db.store.path = true
	db.Database = &replayPathHistory{db.Database, ancient}
	return db
}

func NewReplayDatabase(state ethdb.KeyValueStore) *ReplayDatabase {
	store := &replayStore{KeyValueStore: state, memory: memorydb.New()}
	return &ReplayDatabase{Database: rawdb.NewDatabase(store), store: store}
}
func replayPersistentKey(key []byte) bool {
	code, _ := rawdb.IsCodeKey(key)
	return len(key) == common.HashLength || code
}

func replaySnapshotKey(key []byte) bool {
	if (len(key) == 33 && key[0] == 'a') || (len(key) == 65 && key[0] == 'o') {
		return true
	}
	switch string(key) {
	case "SnapshotRoot", "SnapshotJournal", "SnapshotGenerator", "SnapshotDisabled", "SnapshotRecovery", "SnapshotSyncStatus":
		return true
	}
	return false
}

// Preserve native pathdb state, flat values, journal and recovery metadata.
// Block/transaction/receipt/research records still target bounded memory.
func replayPathKey(key []byte) bool {
	if len(key) > 0 {
		switch key[0] {
		case 'A', 'O', 'a', 'o', 'm':
			return true
		case 'L':
			if len(key) == 33 { // state-root -> state-ID lookup
				return true
			}
		}
	}
	switch string(key) {
	case "LastStateID", "TrieJournal", "SnapshotRoot", "SnapshotGenerator", "SnapshotDisabled",
		"SnapshotJournal", "SnapshotRecovery", "SnapshotSyncStatus", "SnapSyncStatus",
		"LastStateHistoryIndex", "LastTrienodeHistoryIndex":
		return true
	}
	code, _ := rawdb.IsCodeKey(key)
	return code
}

func (s *replayStore) persistent(key []byte) bool {
	if s.path {
		return replayPathKey(key)
	}
	return replayPersistentKey(key) || (s.snapshot && replaySnapshotKey(key))
}
func (s *replayStore) target(key []byte) ethdb.KeyValueStore {
	if s.persistent(key) {
		return s.KeyValueStore
	}
	return s.memory
}
func (s *replayStore) Get(key []byte) ([]byte, error) { return s.target(key).Get(key) }
func (s *replayStore) Has(key []byte) (bool, error)   { return s.target(key).Has(key) }
func (s *replayStore) Put(key, value []byte) error    { return s.target(key).Put(key, value) }
func (s *replayStore) Delete(key []byte) error        { return s.target(key).Delete(key) }
func (s *replayStore) DeleteRange(start, end []byte) error {
	if s.path && len(start) > 0 && len(end) > 0 && replayPathKey(start) &&
		(bytes.HasPrefix(end, start[:1]) || bytes.Equal(end, []byte{start[0] + 1})) {
		return s.KeyValueStore.DeleteRange(start, end)
	}
	return errors.New("range deletion is unsupported in state-only replay")
}
func (s *replayStore) Close() error          { return errors.Join(s.memory.Close(), s.KeyValueStore.Close()) }
func (s *replayStore) NewBatch() ethdb.Batch { return s.NewBatchWithSize(0) }
func (s *replayStore) NewBatchWithSize(size int) ethdb.Batch {
	return &replayBatch{disk: s.KeyValueStore.NewBatchWithSize(size), memory: s.memory.NewBatchWithSize(size), persistent: s.persistent}
}

type replayBatch struct {
	disk, memory ethdb.Batch
	persistent   func([]byte) bool
}

func (b *replayBatch) target(key []byte) ethdb.Batch {
	if b.persistent(key) {
		return b.disk
	}
	return b.memory
}
func (b *replayBatch) Put(k, v []byte) error { return b.target(k).Put(k, v) }
func (b *replayBatch) Delete(k []byte) error { return b.target(k).Delete(k) }
func (b *replayBatch) DeleteRange(start, end []byte) error {
	return errors.New("range deletion is unsupported in state-only replay")
}
func (b *replayBatch) ValueSize() int { return b.disk.ValueSize() + b.memory.ValueSize() }
func (b *replayBatch) Write() error {
	if b.disk.ValueSize() > 0 {
		if err := b.disk.Write(); err != nil {
			return err
		}
	}
	return b.memory.Write()
}
func (b *replayBatch) Reset() { b.disk.Reset(); b.memory.Reset() }
func (b *replayBatch) Close() { b.disk.Close(); b.memory.Close() }
func (b *replayBatch) Replay(w ethdb.KeyValueWriter) error {
	if err := b.disk.Replay(w); err != nil {
		return err
	}
	return b.memory.Replay(w)
}

// PruneReplayHistory retains enough recent chain context for BLOCKHASH and
// uncle validation. State nodes/code are never deleted. Call after InsertChain,
// with keep >= 512 and the previous head to bound work to newly expired blocks.
func (bc *BlockChain) PruneReplayHistory(previous, keep uint64) error {
	if _, ok := bc.db.(*ReplayDatabase); !ok {
		return nil
	}
	if keep < 512 {
		return errors.New("replay chain window must be at least 512 blocks")
	}
	head := bc.CurrentBlock().Number.Uint64()
	if head <= keep {
		return nil
	}
	first := uint64(1)
	if previous > keep {
		first = previous - keep + 1
	}
	batch := bc.db.NewBatch()
	defer batch.Close()
	for n := first; n <= head-keep; n++ {
		hash := rawdb.ReadCanonicalHash(bc.db, n)
		if hash == (common.Hash{}) {
			continue
		}
		block := rawdb.ReadBlock(bc.db, hash, n)
		if block != nil {
			for _, tx := range block.Transactions() {
				rawdb.DeleteTxLookupEntry(batch, tx.Hash())
			}
			// Do not delete a newer alias if the logical state repeated.
			// Legacy aliases without an owner cannot safely be pruned.
			if current, err := bc.db.Get(replayStateKey(block.Root())); err == nil && len(current) == 2*common.HashLength && bytes.Equal(current[common.HashLength:], hash[:]) {
				if err := batch.Delete(replayStateKey(block.Root())); err != nil {
					return err
				}
			}
		}
		rawdb.DeleteBlock(batch, hash, n)
		rawdb.DeleteCanonicalHash(batch, n)
		if err := batch.Delete(replayBlockKey(hash)); err != nil {
			return err
		}
	}
	return batch.Write()
}

// RestoreReplay rebuilds only volatile chain context from trusted local result
// metadata and the original external block stream. It never re-executes blocks
// or copies block/transaction/receipt records into the persistent state DB.
func RestoreReplay(db *ReplayDatabase, genesis *Genesis, cfg *BlockChainConfig, genesisRoot ReplayRoot, head ReplayResult, recent types.Blocks, history map[common.Hash]ReplayRoot) error {
	if !cfg.Replay.StateOnly {
		return errors.New("restore requires state-only mode")
	}
	if err := initReplay(db, cfg); err != nil {
		return err
	}
	cfg.Replay.RestoreRun(head.Run)
	if len(recent) == 0 || recent[len(recent)-1].Hash() != head.BlockHash || recent[len(recent)-1].NumberU64() != head.Number {
		return errors.New("checkpoint/input head mismatch")
	}
	for i := 1; i < len(recent); i++ {
		if recent[i].ParentHash() != recent[i-1].Hash() {
			return errors.New("checkpoint input is not contiguous")
		}
	}
	roots := []common.Hash{genesisRoot.PhysicalRoot, head.PhysicalRoot}
	if cfg.Replay.KeyScheme == "PBSS" {
		// Native pathdb prunes old roots and restores the head from its journal.
		// The caller checks head availability after opening the chain.
		roots = nil
	}
	for _, root := range roots {
		if root != types.EmptyRootHash {
			ok, err := db.Has(root[:])
			if err != nil || !ok {
				return fmt.Errorf("checkpoint state root unavailable: %s (%v)", root, err)
			}
		}
	}
	batch := db.NewBatch()
	defer batch.Close()
	g := genesis.ToBlock()
	rawdb.WriteBlock(batch, g)
	rawdb.WriteCanonicalHash(batch, g.Hash(), 0)
	rawdb.WriteChainConfig(batch, g.Hash(), genesis.Config)
	alloc, err := json.Marshal(genesis.Alloc)
	if err != nil {
		return err
	}
	rawdb.WriteGenesisStateSpec(batch, g.Hash(), alloc)
	if err := writeReplayRoot(batch, g, genesisRoot.PhysicalRoot, cfg.Replay, genesisRoot.AuthenticationRoot); err != nil {
		return err
	}
	for _, block := range recent {
		rawdb.WriteBlock(batch, block)
		rawdb.WriteCanonicalHash(batch, block.Hash(), block.NumberU64())
		if root, ok := history[block.Hash()]; ok {
			if err := storeReplayRoot(batch, block, root); err != nil {
				return err
			}
		} else if !cfg.ArchiveMode && cfg.StateScheme == rawdb.HashScheme && head.Number-block.NumberU64() < state.TriesInMemory {
			return fmt.Errorf("missing non-archive checkpoint root at block %d", block.NumberU64())
		}
	}
	last := recent[len(recent)-1]
	if last.Root() != head.CanonicalRoot {
		return errors.New("checkpoint canonical root mismatch")
	}
	if err := writeReplayRoot(batch, last, head.PhysicalRoot, cfg.Replay, head.AuthenticationRoot); err != nil {
		return err
	}
	rawdb.WriteHeadBlockHash(batch, head.BlockHash)
	rawdb.WriteHeadHeaderHash(batch, head.BlockHash)
	rawdb.WriteHeadFastBlockHash(batch, head.BlockHash)
	return batch.Write()
}

// VerifyReplayAuthentication validates all account and storage commitments,
// including their linkage. It is an offline audit, outside import timings.
func (bc *BlockChain) VerifyReplayAuthentication(block *types.Block) error {
	record, err := bc.ReplayRoot(block.Hash())
	if err != nil {
		return err
	}
	if !bc.cfg.Replay.AuthEnabled() {
		return nil
	}
	account, err := trie.New(trie.StateTrieID(record.PhysicalRoot), bc.triedb)
	if err != nil {
		return err
	}
	root, err := account.AuditAuthentication()
	if err != nil {
		return err
	}
	if record.AuthenticationRoot == nil || root != *record.AuthenticationRoot {
		return errors.New("account authentication root mismatch")
	}
	nodes, err := account.NodeIterator(nil)
	if err != nil {
		return err
	}
	it := trie.NewIterator(nodes)
	for it.Next() {
		var value types.StateAccount
		if err := rlp.DecodeBytes(it.Value, &value); err != nil {
			return err
		}
		expected, err := account.AccountStorageAuthentication(it.Key)
		if err != nil {
			return err
		}
		storage, err := trie.New(trie.StorageTrieID(record.PhysicalRoot, common.BytesToHash(it.Key), value.Root), bc.triedb)
		if err != nil {
			return err
		}
		actual, err := storage.AuditAuthentication()
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("storage authentication mismatch for %x", it.Key)
		}
	}
	return it.Err
}
