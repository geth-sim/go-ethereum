package core

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/incauth"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/ethereum/go-ethereum/triedb/hashdb"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
)

// A fork-crossing signed chain, including repeated storage writes, untouched
// branches, logs and BLOCKHASH. Every candidate executes exactly these headers.
func replayFixture(t *testing.T) (*Genesis, types.Blocks, common.Address) {
	return replayFixtureCount(t, 6)
}

func TestResearchPaymentClassification(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	genesis := &Genesis{Config: params.AllEthashProtocolChanges, GasLimit: 8_000_000, Alloc: types.GenesisAlloc{
		crypto.PubkeyToAddress(key.PublicKey): {Balance: new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))},
	}}
	source, blocks, receipts := GenerateChainWithGenesis(genesis, ethash.NewFaker(), 1, func(_ int, b *BlockGen) {
		tx, err := types.SignTx(types.NewTransaction(0, common.Address{0xaa}, big.NewInt(1), 50000, big.NewInt(2_000_000_000), nil), types.LatestSigner(genesis.Config), key)
		if err != nil {
			t.Fatal(err)
		}
		b.AddTx(tx)
	})
	defer source.Close()
	if receipts[0][0].GasUsed != 21000 || blocks[0].Transactions()[0].Gas() == 21000 {
		t.Fatal("fixture must use less gas than its limit")
	}
	db := rawdb.NewMemoryDatabase()
	defer db.Close()
	cfg := replayChainConfig("H")
	seen := false
	cfg.OnReplayBlock = func(r ReplayResult) {
		seen = true
		if r.PaymentTxTime <= 0 || r.CallTxTime != 0 || r.PaymentTxTime > r.Stats.TotalTime {
			t.Fatalf("invalid payment timing: %+v", r)
		}
		if r.PaymentTxLen != 1 || r.CallTxLen != 0 {
			t.Fatal("payment classified using gas limit instead of actual gas used")
		}
	}
	chain, err := NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	if _, err := chain.InsertChain(blocks); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("missing replay result")
	}
}
func replayFixtureCount(t *testing.T, count int) (*Genesis, types.Blocks, common.Address) {
	t.Helper()
	key, err := crypto.HexToECDSA("9c647b8b7c4e7c3490668fb6c11473619db80c93704c70893d3813af4090c39c")
	if err != nil {
		t.Fatal(err)
	}
	sender := crypto.PubkeyToAddress(key.PublicKey)
	contract := common.HexToAddress("0x1234")
	config := *params.AllEthashProtocolChanges
	config.ByzantiumBlock = big.NewInt(4)
	config.ConstantinopleBlock = big.NewInt(4)
	config.PetersburgBlock = big.NewInt(4)
	config.IstanbulBlock = big.NewInt(4)
	config.MuirGlacierBlock = big.NewInt(4)
	config.BerlinBlock = big.NewInt(4)
	config.LondonBlock = big.NewInt(4)
	config.ArrowGlacierBlock = big.NewInt(4)
	config.GrayGlacierBlock = big.NewInt(4)
	storage := make(map[common.Hash]common.Hash)
	for i := 0; i < 64; i++ {
		storage[common.BigToHash(big.NewInt(int64(i)))] = common.BigToHash(big.NewInt(int64(i + 1)))
	}
	genesis := &Genesis{Config: &config, GasLimit: 8_000_000, Alloc: types.GenesisAlloc{
		sender:   {Balance: new(big.Int).Mul(big.NewInt(1000), big.NewInt(params.Ether))},
		contract: {Balance: big.NewInt(0), Code: common.Hex2Bytes("6000356000556001430340600155602a60006000a100"), Storage: storage},
	}}
	for i := 1; i <= 160; i++ {
		genesis.Alloc[common.BigToAddress(big.NewInt(int64(i)))] = types.Account{Balance: big.NewInt(int64(i))}
	}
	source, blocks, _ := GenerateChainWithGenesis(genesis, ethash.NewFaker(), count, func(i int, b *BlockGen) {
		for j := 0; j < 2; j++ {
			tx := types.NewTransaction(uint64(i*2+j), contract, big.NewInt(0), 150_000, big.NewInt(2_000_000_000), common.LeftPadBytes([]byte{byte(i*2 + j + 10)}, 32))
			signed, err := types.SignTx(tx, types.LatestSigner(&config), key)
			if err != nil {
				t.Fatal(err)
			}
			b.AddTx(signed)
		}
	})
	source.Close()
	return genesis, blocks, contract
}

func replayChainConfig(scheme string) *BlockChainConfig {
	cfg := DefaultConfig()
	cfg.StateScheme = rawdb.HashScheme
	cfg.ArchiveMode = true
	cfg.SnapshotLimit = 0
	cfg.TrieCleanLimit = 4
	if scheme != "" {
		cfg.Replay = &common.ReplayConfig{KeyScheme: scheme, Codec: "fixed32-v1", EpochSize: 2, StateDepth: 3, StorageDepth: 2, SkipIntermediateRoot: true, ShardPrefix: 1, TileBlocks: 128, TilePathPrefix: 1, RunTargetNodes: 64}
		if scheme == "DepthEpoch" {
			cfg.Replay.EpochSize = 16
		}
	}
	if scheme == "custom" {
		cfg.Replay.Codec = "test-vp-extension-v1"
		cfg.Replay.EncodeKey = (&common.ReplayConfig{KeyScheme: "VP"}).NodeKey
	}
	return cfg
}

func checkReplayState(t *testing.T, want, got *state.StateDB, genesis *Genesis, contract common.Address) {
	t.Helper()
	addresses := []common.Address{{}} // coinbase rewards
	for address := range genesis.Alloc {
		addresses = append(addresses, address)
	}
	for _, address := range addresses {
		if want.GetBalance(address).Cmp(got.GetBalance(address)) != 0 || want.GetNonce(address) != got.GetNonce(address) || !bytes.Equal(want.GetCode(address), got.GetCode(address)) {
			t.Fatalf("account differs: %s", address)
		}
	}
	for i := 0; i < 64; i++ {
		slot := common.BigToHash(big.NewInt(int64(i)))
		if want.GetState(contract, slot) != got.GetState(contract, slot) {
			t.Fatalf("storage differs at slot %d", i)
		}
	}
	if err := got.Error(); err != nil {
		t.Fatal(err)
	}
}

// Include legacy option names as well as the short CLI aliases.
var researchKeySchemes = []string{
	"H", "VP", "PV", "JMT_fixed", "PrefixTree_fixed", "Outward", "OutwardStorage", "OutwardSplit", "custom",
	"EpochPath", "TPV", "SplitPVHot", "VPRight", "DepthSplit", "DepthEpoch", "ShardVP", "DualVP",
	"RunPath", "ForestVP", "ATileVP", "JMT", "PrefixTree", "JMT_balanced", "PrefixTree_balanced", "HalfPath", "TH",
}

func TestResearchReplayPebble(t *testing.T) {
	genesis, blocks, contract := replayFixture(t)
	referenceDB := rawdb.NewMemoryDatabase()
	defer referenceDB.Close()
	reference, err := NewBlockChain(referenceDB, genesis, ethash.NewFaker(), replayChainConfig(""))
	if err != nil {
		t.Fatal(err)
	}
	defer reference.Stop()
	if _, err := reference.InsertChain(blocks); err != nil {
		t.Fatal(err)
	}
	for _, scheme := range researchKeySchemes {
		t.Run(scheme, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pebble")
			open := func(cfg *BlockChainConfig) (*BlockChain, ethdb.Database) {
				kv, err := pebble.New(path, 16, 16, "test", false)
				if err != nil {
					t.Fatal(err)
				}
				db := rawdb.NewDatabase(kv)
				chain, err := NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
				if err != nil {
					db.Close()
					t.Fatal(err)
				}
				return chain, db
			}
			cfg := replayChainConfig(scheme)
			count := 0
			cfg.OnReplayBlock = func(r ReplayResult) {
				count++
				if r.Stats.TotalTime <= 0 || r.BlockHash != blocks[r.Number-1].Hash() {
					t.Error("invalid execution stats/header identity")
				}
				if (r.AuthenticationRoot == nil) != (scheme != "H") {
					t.Error("locator incorrectly labeled authentication root")
				}
			}
			chain, db := open(cfg)
			if chain.Genesis().Hash() != genesis.ToBlock().Hash() {
				t.Fatal("genesis identity changed")
			}
			if _, err := chain.InsertChain(blocks[:3]); err != nil {
				t.Fatal(err)
			}
			chain.Stop()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			chain, db = open(cfg)
			defer db.Close()
			defer chain.Stop()
			if chain.CurrentBlock().Hash() != blocks[2].Hash() {
				t.Fatal("head lost on reopen")
			}
			if _, err := chain.InsertChain(blocks[3:]); err != nil {
				t.Fatal(err)
			}
			if count != len(blocks) {
				t.Fatalf("got %d block measurements", count)
			}
			for _, block := range blocks {
				if err := chain.VerifyReplayState(block); err != nil {
					t.Fatal(err)
				}
				got, err := chain.StateAt(block.Root(), block.Number(), block.Time())
				if err != nil {
					t.Fatal(err)
				}
				want, err := reference.StateAt(block.Root(), block.Number(), block.Time())
				if err != nil {
					t.Fatal(err)
				}
				checkReplayState(t, want, got, genesis, contract)
				record, err := chain.ReplayRoot(block.Hash())
				if err != nil {
					t.Fatal(err)
				}
				if scheme != "H" && record.PhysicalRoot == block.Root() {
					t.Fatal("structured scheme not applied")
				}
				if rawdb.ReadBlock(db, block.Hash(), block.NumberU64()).Hash() != block.Hash() {
					t.Fatal("canonical header changed")
				}
			}
			changed := replayChainConfig(scheme)
			changed.Replay.Codec = "other-v2"
			if err := initReplay(db, changed); err == nil {
				t.Fatal("configuration mismatch accepted")
			}
			if err := initReplay(db, replayChainConfig("")); err == nil {
				t.Fatal("ordinary Geth accepted research DB")
			}
			if _, err := chain.InsertChain(blocks[:1]); err == nil {
				t.Fatal("nonappend import accepted")
			}
		})
	}
}

// Self-destruct must traverse physical storage nodes, not reconstruct a Keccak
// StackTrie and compare its digest with a structured database locator.
func TestResearchStorageDestruction(t *testing.T) {
	genesis, _, contract := replayFixture(t)
	account := genesis.Alloc[contract]
	account.Code = common.Hex2Bytes("6000ff") // SELFDESTRUCT to the zero address
	genesis.Alloc[contract] = account
	key, _ := crypto.HexToECDSA("9c647b8b7c4e7c3490668fb6c11473619db80c93704c70893d3813af4090c39c")
	source, blocks, _ := GenerateChainWithGenesis(genesis, ethash.NewFaker(), 2, func(i int, b *BlockGen) {
		if i == 1 {
			tx := types.NewTransaction(0, contract, big.NewInt(0), 100_000, big.NewInt(2_000_000_000), nil)
			signed, err := types.SignTx(tx, types.LatestSigner(genesis.Config), key)
			if err != nil {
				t.Fatal(err)
			}
			b.AddTx(signed)
		}
	})
	source.Close()
	for _, scheme := range []string{"H", "VP", "PV", "Outward"} {
		for _, auth := range []string{"none", "keccak", "gls254"} {
			if scheme == "H" && auth != "none" {
				continue
			}
			t.Run(scheme+"/"+auth, func(t *testing.T) {
				cfg := replayChainConfig(scheme)
				cfg.Replay.Authentication, cfg.Replay.AuthMode = auth, "incremental"
				cfg.Replay.AuthWorkers, cfg.Replay.AuthCacheMode = 16, "unified"
				if auth == "keccak" {
					cfg.Replay.AuthMode = "full"
				}
				if _, err := trie.AuthenticationConfig(cfg.Replay); err != nil {
					if auth == "gls254" && strings.Contains(err.Error(), "build") {
						t.Skip(err)
					}
					t.Fatal(err)
				}
				db := rawdb.NewMemoryDatabase()
				defer db.Close()
				bc, err := NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer bc.Stop()
				if _, err := bc.InsertChain(blocks); err != nil {
					t.Fatal(err)
				}
				for _, block := range blocks {
					if err := bc.VerifyReplayState(block); err != nil {
						t.Fatal(err)
					}
					if err := bc.VerifyReplayAuthentication(block); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestResearchValidation(t *testing.T) {
	genesis, blocks, _ := replayFixture(t)
	for _, kind := range []string{"gas", "bloom", "receipts", "transactions", "state"} {
		t.Run(kind, func(t *testing.T) {
			db := rawdb.NewMemoryDatabase()
			defer db.Close()
			scheme := "Outward"
			if kind == "state" {
				scheme = "H"
			}
			chain, err := NewBlockChain(db, genesis, ethash.NewFaker(), replayChainConfig(scheme))
			if err != nil {
				t.Fatal(err)
			}
			defer chain.Stop()
			if _, err := chain.InsertChain(blocks[:3]); err != nil {
				t.Fatal(err)
			}
			header := blocks[3].Header()
			switch kind {
			case "gas":
				header.GasUsed++
			case "bloom":
				header.Bloom[0] ^= 1
			case "receipts":
				header.ReceiptHash[0] ^= 1
			case "transactions":
				header.TxHash[0] ^= 1
			case "state":
				header.Root[0] ^= 1
			}
			broken := types.NewBlockWithHeader(header).WithBody(*blocks[3].Body())
			if _, err := chain.InsertChain(types.Blocks{broken}); err == nil {
				t.Fatal("invalid block accepted")
			}
			if chain.CurrentBlock().Number.Uint64() != 3 {
				t.Fatal("failed import advanced head")
			}
		})
	}
}

func TestResearchSkipBlockRootChecks(t *testing.T) {
	genesis, blocks, _ := replayFixture(t)
	for _, skip := range []bool{false, true} {
		for _, kind := range []string{"transactions", "uncles", "receipts", "gas", "bloom", "state"} {
			t.Run(fmt.Sprintf("skip=%v/%s", skip, kind), func(t *testing.T) {
				cfg := replayChainConfig("H")
				cfg.Replay.SkipBlockRootChecks = skip
				db := rawdb.NewMemoryDatabase()
				defer db.Close()
				bc, err := NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer bc.Stop()
				if _, err := bc.InsertChain(blocks[:3]); err != nil {
					t.Fatal(err)
				}
				header := blocks[3].Header()
				switch kind {
				case "transactions":
					header.TxHash[0] ^= 1
				case "uncles":
					header.UncleHash[0] ^= 1
				case "receipts":
					header.ReceiptHash[0] ^= 1
				case "gas":
					header.GasUsed++
				case "bloom":
					header.Bloom[0] ^= 1
				case "state":
					header.Root[0] ^= 1
				}
				block := types.NewBlockWithHeader(header).WithBody(*blocks[3].Body())
				_, err = bc.InsertChain(types.Blocks{block})
				allowed := skip && (kind == "transactions" || kind == "uncles" || kind == "receipts")
				if (err == nil) != allowed {
					t.Fatalf("allowed=%v, got %v", allowed, err)
				}
				if allowed {
					if err := bc.VerifyReplayState(block); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
	// Skipping input commitments must not skip state authentication or change
	// physical keys. Compare all block roots across the Byzantium boundary.
	var reference []ReplayRoot
	for _, skip := range []bool{false, true} {
		cfg := replayChainConfig("Outward")
		cfg.Replay.SkipBlockRootChecks = skip
		cfg.Replay.Authentication, cfg.Replay.AuthMode = "ristretto255", "incremental"
		cfg.Replay.AuthWorkers, cfg.Replay.AuthCacheMode = 16, "unified"
		var roots []ReplayRoot
		cfg.OnReplayBlock = func(r ReplayResult) { roots = append(roots, r.ReplayRoot) }
		db := rawdb.NewMemoryDatabase()
		bc, err := NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		if _, err := bc.InsertChain(blocks); err != nil {
			t.Fatal(err)
		}
		if err := bc.VerifyReplayAuthentication(blocks[len(blocks)-1]); err != nil {
			t.Fatal(err)
		}
		bc.Stop()
		db.Close()
		if !skip {
			reference = roots
			continue
		}
		for i, root := range roots {
			if root.PhysicalRoot != reference[i].PhysicalRoot || root.AuthenticationRoot == nil ||
				*root.AuthenticationRoot != *reference[i].AuthenticationRoot {
				t.Fatalf("changed state/auth root at block %d", i+1)
			}
		}
	}
}

func TestResearchReceiptDigestPolicy(t *testing.T) {
	for _, hashReceipts := range []bool{false, true} {
		p := newDigestPipeline(1, false, hashReceipts)
		receipt := &types.Receipt{Status: 1, CumulativeGasUsed: 21000, Logs: []*types.Log{{Address: common.Address{1}, Topics: []common.Hash{{2}}}}}
		p.feedReceipt(receipt)
		digest := p.joinReceipts()
		p.abandon()
		if digest.bloom != types.CreateBloom(receipt) || digest.bloom == (types.Bloom{}) {
			t.Fatal("receipt-root skipping changed bloom computation")
		}
		if hashReceipts {
			if digest.root != types.DeriveSha(types.Receipts{receipt}, trie.NewStackTrie(nil)) {
				t.Fatal("incorrect receipt root")
			}
		} else if digest.root != (common.Hash{}) {
			t.Fatal("receipt root was computed despite being disabled")
		}
	}
}

// Golden values captured from the old Outward implementation at ea4899c1.
// This checks byte-for-byte codec compatibility, not just equivalent state.
func TestResearchLegacyCodec(t *testing.T) {
	cfg := &common.ReplayConfig{KeyScheme: "Outward", EpochSize: 128, StateDepth: 3, StorageDepth: 2}
	vectors := []struct {
		storage bool
		depth   int
		branch  bool
		key     string
	}{
		{false, 0, false, "3fffffda00000000000000000000000000000000000000000000000000000040"},
		{false, 0, true, "3fffffda00000000000000000000000000000000000000000000000000000040"},
		{false, 2, false, "3fffffda24000000000000000000000000000000000000000000000000000042"},
		{false, 2, true, "3fffffda24000000000000000000000000000000000000000000000000000042"},
		{false, 3, false, "3fffffda24600000000000000000000000000000000000000000000000000043"},
		{false, 3, true, "3fffffda24600000000000000000000000000000000000000000000000000043"},
		{false, 4, false, "800000207448d000000000000000000000000000000000000000000000000004"},
		{false, 4, true, "800000207448d000000000000000000000000000000000000000000000000004"},
		{true, 0, false, "800000207c048d159e26af37bc048d159c000000000000000000000000000000"},
		{true, 0, true, "3fffffde02468acf13579bde02468ace00000000000000000000000000000040"},
		{true, 2, false, "800000207c048d159e26af37bc048d159c480000000000000000000000000002"},
		{true, 2, true, "3fffffde02468acf13579bde02468ace24000000000000000000000000000042"},
		{true, 3, false, "800000207c048d159e26af37bc048d159c48c000000000000000000000000003"},
		{true, 3, true, "800000207c048d159e26af37bc048d159c48c000000000000000000000000003"},
		{true, 4, false, "800000207c048d159e26af37bc048d159c48d000000000000000000000000004"},
		{true, 4, true, "800000207c048d159e26af37bc048d159c48d000000000000000000000000004"},
	}
	owner := common.HexToHash("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	for _, v := range vectors {
		n := common.TrieNodeContext{Version: 129, Branch: v.branch, Path: make([]byte, v.depth)}
		for i := range n.Path {
			n.Path[i] = byte(i + 1)
		}
		if v.storage {
			n.Owner = owner
		}
		key, err := cfg.NodeKey(n)
		if err != nil || key != common.HexToHash(v.key) {
			t.Fatalf("legacy codec mismatch: %+v %s %v", v, key, err)
		}
	}
	for _, storage := range []bool{false, true} {
		n := common.TrieNodeContext{Version: 0x12345678, Path: []byte{0xa, 0xb}}
		location := "d" + "ab" + strings.Repeat("0", 51)
		if storage {
			n.Owner = owner
			location = "f" + owner.Hex()[2:26] + "ab" + strings.Repeat("0", 27)
		}
		for _, scheme := range []string{"VP", "PV"} {
			cfg.KeyScheme = scheme
			want := "12345678" + location + "02"
			if scheme == "PV" {
				want = location + "12345678" + "02"
			}
			got, err := cfg.NodeKey(n)
			if err != nil || got != common.HexToHash(want) {
				t.Fatalf("%s storage=%v key mismatch", scheme, storage)
			}
		}
	}
}

func TestResearchAuthentication(t *testing.T) {
	genesis, blocks, contract := replayFixture(t)
	for _, backend := range []string{"ristretto255", "p256", "keccak", "gls254"} {
		t.Run(backend, func(t *testing.T) {
			var expected common.Hash
			for _, scheme := range researchKeySchemes {
				if scheme == "H" {
					continue // canonical Keccak control, without experimental auth
				}
				for _, mode := range []string{"full", "incremental"} {
					if backend == "keccak" && mode != "full" {
						continue
					}
					t.Run(scheme+"/"+mode, func(t *testing.T) {
						cfg := replayChainConfig(scheme)
						cfg.Replay.Authentication = backend
						cfg.Replay.AuthMode = mode
						cfg.Replay.AuthWorkers = 16
						cfg.Replay.AuthCacheMode = "unified"
						if mode == "incremental" || backend == "keccak" {
							cfg.Replay.AuthCacheMiB = 4
						}
						if _, err := trie.AuthenticationConfig(cfg.Replay); err != nil {
							if backend == "gls254" && strings.Contains(err.Error(), "build") {
								t.Skip(err)
							}
							t.Fatal(err)
						}
						path := filepath.Join(t.TempDir(), "pebble")
						open := func() (*BlockChain, ethdb.Database) {
							kv, err := pebble.New(path, 16, 16, "test", false)
							if err != nil {
								t.Fatal(err)
							}
							db := rawdb.NewDatabase(kv)
							bc, err := NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
							if err != nil {
								t.Fatal(err)
							}
							return bc, db
						}
						bc, db := open()
						if _, err := bc.InsertChain(blocks[:3]); err != nil {
							t.Fatal(err)
						}
						bc.Stop()
						db.Close()
						bc, db = open()
						defer db.Close()
						defer bc.Stop()
						if _, err := bc.InsertChain(blocks[3:]); err != nil {
							t.Fatal(err)
						}
						block := blocks[len(blocks)-1]
						record, err := bc.ReplayRoot(block.Hash())
						if err != nil || record.AuthenticationRoot == nil {
							t.Fatalf("auth root: %v", err)
						}
						if expected == (common.Hash{}) {
							expected = *record.AuthenticationRoot
						} else if expected != *record.AuthenticationRoot {
							t.Fatal("scheme/policy changed authentication root")
						}
						if err := bc.VerifyReplayState(block); err != nil {
							t.Fatal(err)
						}
						tr, err := trie.New(trie.StateTrieID(record.PhysicalRoot), bc.triedb)
						if err != nil {
							t.Fatal(err)
						}
						if audited, err := tr.AuditAuthentication(); err != nil || audited != expected {
							t.Fatalf("account audit: %v", err)
						}
						id := map[string]string{"ristretto255": incauth.Ristretto255, "p256": incauth.P256, "keccak": incauth.Keccak, "gls254": incauth.GLS254}[backend]
						accountScheme, _ := incauth.NewScheme(id, incauth.Account)
						key := crypto.Keccak256Hash(contract[:])
						proof, err := tr.AuthenticationProof(key[:])
						if err != nil {
							t.Fatal(err)
						}
						value, exists, err := incauth.Verify(accountScheme, incauth.Commitment(expected), [32]byte(key), proof)
						if err != nil || !exists {
							t.Fatalf("account proof: %v", err)
						}
						var account types.StateAccount
						if err := rlp.DecodeBytes(value, &account); err != nil {
							t.Fatal(err)
						}
						st, err := bc.StateAt(block.Root(), block.Number(), block.Time())
						if err != nil {
							t.Fatal(err)
						}
						storage, err := trie.New(trie.StorageTrieID(record.PhysicalRoot, key, st.GetStorageRoot(contract)), bc.triedb)
						if err != nil {
							t.Fatal(err)
						}
						if root, err := storage.AuditAuthentication(); err != nil || root != account.Root {
							t.Fatalf("storage binding/audit: %v", err)
						}
						missing := crypto.Keccak256Hash([]byte("not-an-account"))
						proof, err = tr.AuthenticationProof(missing[:])
						if err != nil {
							t.Fatal(err)
						}
						if _, exists, err := incauth.Verify(accountScheme, incauth.Commitment(expected), [32]byte(missing), proof); err != nil || exists {
							t.Fatalf("absence proof: %v", err)
						}
						proof[len(proof)-1] ^= 1
						if _, _, err := incauth.Verify(accountScheme, incauth.Commitment(expected), [32]byte(missing), proof); err == nil {
							t.Fatal("tampered proof accepted")
						}
					})
				}
			}
		})
	}
}

func TestResearchRepeatedRootPruning(t *testing.T) {
	db := NewReplayDatabase(rawdb.NewMemoryDatabase())
	defer db.Close()
	bc := &BlockChain{db: db, cfg: replayChainConfig("H")}
	repeated, expired := common.Hash{1}, common.Hash{2}
	for _, entry := range []struct {
		number int64
		root   common.Hash
	}{{1, repeated}, {2, expired}, {514, repeated}} {
		block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(entry.number), Root: entry.root})
		rawdb.WriteBlock(db, block)
		rawdb.WriteCanonicalHash(db, block.Hash(), uint64(entry.number))
		if err := storeReplayRoot(db, block, ReplayRoot{PhysicalRoot: entry.root}); err != nil {
			t.Fatal(err)
		}
		bc.currentBlock.Store(block.Header())
	}
	if err := bc.PruneReplayHistory(512, 512); err != nil {
		t.Fatal(err)
	}
	if got, err := bc.replayStateRoot(repeated); err != nil || got != repeated {
		t.Fatal("pruning an older block removed the current state's alias", err)
	}
	if _, err := bc.replayStateRoot(expired); err == nil {
		t.Fatal("expired state alias was retained")
	}
	if rawdb.ReadCanonicalHash(db, 1) != (common.Hash{}) || rawdb.ReadCanonicalHash(db, 514) == (common.Hash{}) {
		t.Fatal("incorrect history window")
	}
	// Databases written before alias ownership was recorded remain readable.
	if err := db.Put(replayStateKey(repeated), repeated[:]); err != nil {
		t.Fatal(err)
	}
	if got, err := bc.replayStateRoot(repeated); err != nil || got != repeated {
		t.Fatal("legacy alias rejected", err)
	}
}

func TestResearchStateOnly(t *testing.T) {
	genesis, blocks, _ := replayFixtureCount(t, 520)
	for _, auth := range []string{"none", "ristretto255"} {
		t.Run(auth, func(t *testing.T) {
			cfg := replayChainConfig("Outward")
			cfg.Replay.StateOnly = true
			cfg.Replay.Authentication = auth
			cfg.Replay.AuthMode = "incremental"
			cfg.Replay.AuthWorkers = 16
			cfg.Replay.AuthCacheMode = "unified"
			path := filepath.Join(t.TempDir(), "state")
			open := func() (*ReplayDatabase, ethdb.KeyValueStore) {
				kv, err := pebble.New(path, 16, 16, "test", false)
				if err != nil {
					t.Fatal(err)
				}
				return NewReplayDatabase(kv), kv
			}
			db, kv := open()
			bc, err := NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bc.InsertChain(blocks[:260]); err != nil {
				t.Fatal(err)
			}
			root, err := bc.ReplayRoot(blocks[259].Hash())
			if err != nil {
				t.Fatal(err)
			}
			initial, err := bc.ReplayRoot(genesis.ToBlock().Hash())
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := ReplayResult{Number: 260, BlockHash: blocks[259].Hash(), CanonicalRoot: blocks[259].Root(), ReplayRoot: root}
			if err := kv.SyncKeyValue(); err != nil {
				t.Fatal(err)
			}
			bc.Stop()
			db.Close()
			// Recreate the runtime, changing only execution/cache settings.
			cfg = replayChainConfig("Outward")
			cfg.Replay.StateOnly = true
			cfg.Replay.Authentication = auth
			cfg.Replay.AuthMode = "incremental"
			cfg.Replay.AuthWorkers = 1
			cfg.Replay.AuthCacheMode = "unified"
			cfg.Replay.AuthCacheMiB = 4
			cfg.Replay.Measure = true
			db, kv = open()
			defer db.Close()
			if h := rawdb.ReadHeadBlockHash(db); h != (common.Hash{}) {
				t.Fatal("chain head leaked to disk")
			}
			if err := RestoreReplay(db, genesis, cfg, initial, checkpoint, blocks[:260], nil); err != nil {
				t.Fatal(err)
			}
			bc, err = NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer bc.Stop()
			if _, err := bc.InsertChain(blocks[260:]); err != nil {
				t.Fatal(err)
			}
			if err := bc.PruneReplayHistory(260, 512); err != nil {
				t.Fatal(err)
			}
			if got := rawdb.ReadBlock(db, blocks[0].Hash(), 1); got != nil {
				t.Fatal("old volatile block not evicted")
			}
			if err := bc.VerifyReplayState(blocks[519]); err != nil {
				t.Fatal(err)
			}
			if err := bc.VerifyReplayAuthentication(blocks[519]); err != nil {
				t.Fatal(err)
			}
			it := kv.NewIterator(nil, nil)
			defer it.Release()
			nodes, codes := 0, 0
			for it.Next() {
				key := it.Key()
				if !replayPersistentKey(key) {
					t.Fatalf("non-state key on disk: %x", key)
				}
				if len(key) == 32 {
					nodes++
				} else {
					codes++
				}
			}
			if err := it.Error(); err != nil {
				t.Fatal(err)
			}
			if nodes == 0 || codes == 0 {
				t.Fatal("missing nodes or contract code")
			}
		})
	}
}

// Exercise dirty-node GC, physical-root flushing and restart beyond the native
// retention window. Archive tests alone never exercise these paths.
func TestResearchNonArchive(t *testing.T) {
	genesis, blocks, _ := replayFixtureCount(t, 520)
	for _, engine := range []string{"leveldb", "pebble"} {
		for _, variant := range []struct{ scheme, auth string }{
			{"H", "none"}, {"VP", "none"}, {"PV", "none"}, {"RunPath", "none"},
			{"Outward", "none"}, {"Outward", "keccak"}, {"Outward", "gls254"},
		} {
			t.Run(engine+"/"+variant.scheme+"/"+variant.auth, func(t *testing.T) {
				config := func() *BlockChainConfig {
					cfg := replayChainConfig(variant.scheme)
					cfg.ArchiveMode, cfg.TrieDirtyLimit, cfg.TrieTimeLimit = false, 16, time.Hour
					cfg.Replay.StateOnly = true
					cfg.Replay.Authentication, cfg.Replay.AuthMode = variant.auth, "incremental"
					cfg.Replay.AuthWorkers, cfg.Replay.AuthCacheMode = 16, "unified"
					if variant.auth == "keccak" {
						cfg.Replay.AuthMode = "full"
						cfg.SnapshotLimit, cfg.SnapshotWait = 4, true
					}
					return cfg
				}
				cfg := config()
				if _, err := trie.AuthenticationConfig(cfg.Replay); err != nil {
					if variant.auth == "gls254" && strings.Contains(err.Error(), "build") {
						t.Skip(err)
					}
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "state")
				open := func() (*ReplayDatabase, ethdb.KeyValueStore) {
					var kv ethdb.KeyValueStore
					var err error
					if engine == "leveldb" {
						kv, err = leveldb.New(path, 16, 16, "test", false)
					} else {
						kv, err = pebble.New(path, 16, 16, "test", false)
					}
					if err != nil {
						t.Fatal(err)
					}
					return NewReplayDatabase(kv), kv
				}
				db, kv := open()
				bc, err := NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := bc.InsertChain(blocks[:260]); err != nil {
					t.Fatal(err)
				}
				wrong := *cfg
				wrong.ArchiveMode = true
				if err := initReplay(db, &wrong); err == nil {
					t.Fatal("retention policy change accepted")
				}
				old, err := bc.ReplayRoot(blocks[0].Hash())
				if err != nil {
					t.Fatal(err)
				}
				if ok, err := kv.Has(old.PhysicalRoot[:]); err != nil || ok {
					t.Fatalf("old state was archived: %v %v", ok, err)
				}
				if bc.HasState(old.PhysicalRoot) {
					t.Fatal("obsolete unpersisted state survived GC")
				}
				if err := bc.VerifyReplayState(blocks[259]); err != nil {
					t.Fatal(err)
				}
				if err := bc.VerifyReplayAuthentication(blocks[259]); err != nil {
					t.Fatal(err)
				}
				initial, err := bc.ReplayRoot(genesis.ToBlock().Hash())
				if err != nil {
					t.Fatal(err)
				}
				head, err := bc.ReplayRoot(blocks[259].Hash())
				if err != nil {
					t.Fatal(err)
				}
				history, err := bc.FlushReplayState()
				if err != nil {
					t.Fatal(err)
				}
				if len(history) != int(state.TriesInMemory) {
					t.Fatal("incomplete GC context")
				}
				if ok, err := kv.Has(head.PhysicalRoot[:]); err != nil || !ok {
					t.Fatal("head not flushed", err)
				}
				bc.Stop()
				db.Close()
				cfg = config()
				cfg.TrieTimeLimit = time.Nanosecond // force timed flushes using old and new physical roots
				db, kv = open()
				defer db.Close()
				checkpoint := ReplayResult{Number: 260, BlockHash: blocks[259].Hash(), CanonicalRoot: blocks[259].Root(), ReplayRoot: head}
				if err := RestoreReplay(db, genesis, cfg, initial, checkpoint, blocks[:260], history); err != nil {
					t.Fatal(err)
				}
				bc, err = NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer bc.Stop()
				if _, err := bc.InsertChain(blocks[260:]); err != nil {
					t.Fatal(err)
				}
				if bc.lastWrite != 520-state.TriesInMemory {
					t.Fatal("native timed flushing did not run", bc.lastWrite)
				}
				if err := bc.PruneReplayHistory(260, 512); err != nil {
					t.Fatal(err)
				}
				if err := bc.VerifyReplayState(blocks[519]); err != nil {
					t.Fatal(err)
				}
				if err := bc.VerifyReplayAuthentication(blocks[519]); err != nil {
					t.Fatal(err)
				}
				if _, err := bc.FlushReplayState(); err != nil {
					t.Fatal(err)
				}
				it := kv.NewIterator(nil, nil)
				defer it.Release()
				for it.Next() {
					if !replayPersistentKey(it.Key()) && !(cfg.SnapshotLimit > 0 && replaySnapshotKey(it.Key())) {
						t.Fatalf("chain data leaked to disk: %x", it.Key())
					}
				}
				if err := it.Error(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// Snapshot roots must follow physical locators through flush, clean reopen and
// regeneration. Check real flat account/storage values, not only trie fallback.
func TestResearchSnapshot(t *testing.T) {
	genesis, blocks, contract := replayFixtureCount(t, 136)
	for _, engine := range []string{"leveldb", "pebble"} {
		for _, scheme := range []string{"H", "VP", "PV", "Outward"} {
			for _, prefetch := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/prefetch=%v", engine, scheme, prefetch), func(t *testing.T) {
					cfg := replayChainConfig(scheme)
					cfg.Replay.StateOnly = true
					cfg.NoPrefetch = !prefetch
					cfg.SnapshotLimit, cfg.SnapshotWait = 4, true
					if scheme == "Outward" {
						cfg.Replay.Authentication, cfg.Replay.AuthMode = "ristretto255", "incremental"
						cfg.Replay.AuthWorkers, cfg.Replay.AuthCacheMode = 16, "unified"
					}
					cfg.OnReplayBlock = func(r ReplayResult) {
						if !prefetch && r.Stats.StatePrefetchCacheStats != (state.ReaderStats{}) {
							t.Error("prefetch OFF still populated speculative reader statistics")
						}
					}
					path := filepath.Join(t.TempDir(), "state")
					open := func() (*ReplayDatabase, ethdb.KeyValueStore) {
						var kv ethdb.KeyValueStore
						var err error
						if engine == "leveldb" {
							kv, err = leveldb.New(path, 16, 16, "test", false)
						} else {
							kv, err = pebble.New(path, 16, 16, "test", false)
						}
						if err != nil {
							t.Fatal(err)
						}
						return NewReplayDatabase(kv), kv
					}
					db, kv := open()
					bc, err := NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
					if err != nil {
						db.Close()
						t.Fatal(err)
					}
					initial, err := bc.ReplayRoot(genesis.ToBlock().Hash())
					if err != nil {
						t.Fatal(err)
					}
					if _, err := bc.InsertChain(blocks[:132]); err != nil {
						t.Fatal(err)
					}
					record, err := bc.ReplayRoot(blocks[131].Hash())
					if err != nil {
						t.Fatal(err)
					}
					if err := bc.snaps.Cap(record.PhysicalRoot, 0); err != nil {
						t.Fatal(err)
					}
					checkpoint := ReplayResult{Number: 132, BlockHash: blocks[131].Hash(), CanonicalRoot: blocks[131].Root(), ReplayRoot: record}
					bc.Stop()
					if rawdb.ReadSnapshotRoot(kv) != record.PhysicalRoot {
						t.Fatal("snapshot persisted the canonical root instead of the physical root")
					}
					if rawdb.ReadHeadBlockHash(kv) != (common.Hash{}) {
						t.Fatal("chain metadata leaked to disk")
					}
					db.Close()

					db, kv = open()
					defer db.Close()
					// Also exercise generator fallback from physical-key tries when
					// flat state remains but its journal/root metadata is missing.
					if !prefetch {
						rawdb.DeleteSnapshotRoot(kv)
						rawdb.DeleteSnapshotJournal(kv)
					}
					if err := RestoreReplay(db, genesis, cfg, initial, checkpoint, blocks[:132], nil); err != nil {
						t.Fatal(err)
					}
					bc, err = NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
					if err != nil {
						t.Fatal(err)
					}
					defer bc.Stop()
					if bc.snaps == nil || bc.snaps.Snapshot(record.PhysicalRoot) == nil {
						t.Fatal("snapshot missing after reopen")
					}
					if _, err := bc.InsertChain(blocks[132:]); err != nil {
						t.Fatal(err)
					}
					last := blocks[len(blocks)-1]
					if err := bc.VerifyReplayState(last); err != nil {
						t.Fatal(err)
					}
					if err := bc.VerifyReplayAuthentication(last); err != nil {
						t.Fatal(err)
					}
					record, err = bc.ReplayRoot(last.Hash())
					if err != nil {
						t.Fatal(err)
					}
					flat := bc.snaps.Snapshot(record.PhysicalRoot)
					if flat == nil {
						t.Fatal("snapshot stopped following updates")
					}
					want, err := state.New(record.PhysicalRoot, state.NewMPTDatabase(bc.triedb, bc.codedb))
					if err != nil {
						t.Fatal(err)
					}
					account, err := flat.Account(crypto.Keccak256Hash(contract[:]))
					if err != nil || account == nil || account.Nonce != want.GetNonce(contract) || account.Balance.Cmp(want.GetBalance(contract)) != 0 {
						t.Fatalf("flat account mismatch: %v", err)
					}
					for i := 0; i < 64; i++ {
						slot := common.BigToHash(big.NewInt(int64(i)))
						value, err := flat.Storage(crypto.Keccak256Hash(contract[:]), crypto.Keccak256Hash(slot[:]))
						if err != nil {
							t.Fatal(err)
						}
						var content []byte
						if len(value) > 0 {
							content, _, err = rlp.SplitString(value)
							if err != nil {
								t.Fatal(err)
							}
						}
						if common.BytesToHash(content) != want.GetState(contract, slot) {
							t.Fatalf("flat storage mismatch at %d", i)
						}
					}
					if err := want.Error(); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

// PBSS keeps native hash commitments, path keys, pruning and state-history
// storage. Reopen must use the native journal, not an archive root lookup.
func TestResearchPBSS(t *testing.T) {
	genesis, blocks, contract := replayFixtureCount(t, 520)
	for _, stateOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(stateOnly), func(t *testing.T) {
			dir := t.TempDir()
			cfg := replayChainConfig("PBSS")
			cfg.StateScheme, cfg.ArchiveMode = rawdb.PathScheme, false
			cfg.StateHistory, cfg.TrienodeHistory = pathdb.Defaults.StateHistory, pathdb.Defaults.TrienodeHistory
			cfg.Replay.StateOnly = stateOnly
			open := func() (ethdb.Database, ethdb.KeyValueStore) {
				kv, err := pebble.New(filepath.Join(dir, "state"), 16, 16, "test", false)
				if err != nil {
					t.Fatal(err)
				}
				return NewReplayPathDatabase(kv, filepath.Join(dir, "ancient"), stateOnly), kv
			}
			db, kv := open()
			bc, err := NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			broken := blocks[0].Header()
			broken.Root[0] ^= 1
			if _, err := bc.InsertChain(types.Blocks{types.NewBlockWithHeader(broken).WithBody(*blocks[0].Body())}); err == nil {
				t.Fatal("PBSS accepted an invalid Ethereum state root")
			}
			if _, err := bc.InsertChain(blocks[:260]); err != nil {
				t.Fatal(err)
			}
			initial, err := bc.ReplayRoot(genesis.ToBlock().Hash())
			if err != nil {
				t.Fatal(err)
			}
			head, err := bc.ReplayRoot(blocks[259].Hash())
			if err != nil || head.PhysicalRoot != blocks[259].Root() || head.AuthenticationRoot == nil || *head.AuthenticationRoot != blocks[259].Root() {
				t.Fatal("PBSS changed Ethereum commitment", err)
			}
			if err := bc.TrieDB().Journal(head.PhysicalRoot); err != nil {
				t.Fatal(err)
			}
			if err := kv.SyncKeyValue(); err != nil {
				t.Fatal(err)
			}
			bc.Stop()
			db.Close()
			cfg.SnapshotLimit = 4 // PBSS flat-state read cache can change without changing persisted state.
			db, kv = open()
			defer db.Close()
			if stateOnly {
				checkpoint := ReplayResult{Number: 260, BlockHash: blocks[259].Hash(), CanonicalRoot: blocks[259].Root(), ReplayRoot: head}
				if err := RestoreReplay(db.(*ReplayDatabase), genesis, cfg, initial, checkpoint, blocks[:260], nil); err != nil {
					t.Fatal(err)
				}
			}
			bc, err = NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer bc.Stop()
			if bc.CurrentBlock().Number.Uint64() != 260 || !bc.HasState(head.PhysicalRoot) {
				t.Fatal("PBSS journal did not restore the head")
			}
			if _, err := bc.InsertChain(blocks[260:]); err != nil {
				t.Fatal(err)
			}
			if err := bc.PruneReplayHistory(260, 512); err != nil {
				t.Fatal(err)
			}
			last := blocks[519]
			if err := bc.VerifyReplayState(last); err != nil {
				t.Fatal(err)
			}
			tr, err := trie.New(trie.StateTrieID(last.Root()), bc.TrieDB())
			if err != nil {
				t.Fatal(err)
			}
			proof := rawdb.NewMemoryDatabase()
			defer proof.Close()
			key := crypto.Keccak256(contract[:])
			if err := tr.Prove(key, proof); err != nil {
				t.Fatal(err)
			}
			if value, err := trie.VerifyProof(last.Root(), key, proof); err != nil || len(value) == 0 {
				t.Fatal("native Ethereum proof failed", err)
			}
			if err := bc.TrieDB().Journal(last.Root()); err != nil {
				t.Fatal(err)
			}
			if !rawdb.HasAccountTrieNode(kv, nil) || len(rawdb.ReadTrieJournal(kv)) == 0 {
				t.Fatal("native path keys/journal missing")
			}
			if rawdb.HasLegacyTrieNode(kv, last.Root()) {
				t.Fatal("PBSS wrote hashdb node keys")
			}
			it := kv.NewIterator(nil, nil)
			defer it.Release()
			for it.Next() {
				if stateOnly && !replayPathKey(it.Key()) {
					t.Fatalf("non-state PBSS key on disk: %q", it.Key())
				}
			}
			if err := it.Error(); err != nil {
				t.Fatal(err)
			}
			if matches, _ := filepath.Glob(filepath.Join(dir, "ancient", "state", "*")); len(matches) == 0 {
				t.Fatal("native state history was not stored on disk")
			}
		})
	}
}

// Check the modern in-place/parallel batch adapter against the independent CoW
// reference, including repeated updates, deletions, copies and persisted reloads.
func TestResearchAuthenticationBatch(t *testing.T) {
	for name, suite := range map[string]string{"ristretto255": incauth.Ristretto255, "gls254": incauth.GLS254, "keccak": incauth.Keccak} {
		t.Run(name, func(t *testing.T) {
			cfg := replayChainConfig("Outward").Replay
			cfg.Authentication, cfg.AuthMode = name, "incremental"
			if name == "keccak" {
				cfg.AuthMode = "full"
			}
			cfg.AuthWorkers, cfg.AuthCacheMiB, cfg.AuthCacheMode = 16, 4, "unified"
			if _, err := trie.AuthenticationConfig(cfg); err != nil {
				if name == "gls254" && strings.Contains(err.Error(), "build") {
					t.Skip(err)
				}
				t.Fatal(err)
			}
			disk := rawdb.NewMemoryDatabase()
			defer disk.Close()
			db := triedb.NewDatabase(disk, &triedb.Config{HashDB: hashdb.Defaults, Replay: cfg})
			defer db.Close()
			owner := crypto.Keccak256Hash([]byte("contract"))
			native, err := trie.New(trie.StorageTrieID(types.EmptyRootHash, owner, types.EmptyRootHash), db)
			if err != nil {
				t.Fatal(err)
			}
			alg, _ := incauth.NewScheme(suite, incauth.Storage)
			refDB := rawdb.NewMemoryDatabase()
			defer refDB.Close()
			ref, err := incauth.New(refDB, alg, incauth.Full, "test", func(v uint32, path []byte, kind uint8) (incauth.Locator, error) {
				key, err := cfg.NodeKey(common.TrieNodeContext{Version: uint64(v), Owner: owner, Path: path, Branch: kind == incauth.BranchNode})
				return incauth.Locator(key), err
			})
			if err != nil {
				t.Fatal(err)
			}
			for round := 0; round < 9; round++ {
				cfg.SetVersion(uint64(round + 1))
				before := native.Copy()
				old := before.AuthenticationRoot()
				var keys, values [][]byte
				for i := 0; i < 256; i++ {
					if round > 0 && round < 7 && i%4 != round%4 {
						continue // retain clean siblings during the incremental rounds
					}
					key := crypto.Keccak256Hash([]byte{byte(i)})
					value := []byte{byte(round + 1), byte(i)}
					if round == 7 || (round > 0 && i%7 == 0) {
						value = nil
					}
					keys, values = append(keys, key.Bytes()), append(values, value)
					var err error
					if len(value) == 0 {
						err = ref.Delete([32]byte(key))
					} else {
						err = ref.Update([32]byte(key), value)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := native.UpdateBatch(keys, values); err != nil {
					t.Fatal(err)
				}
				// A second identical batch must not subtract old contributions twice.
				if err := native.UpdateBatch(keys, values); err != nil {
					t.Fatal(err)
				}
				want, err := ref.Root()
				if err != nil || native.AuthenticationRoot() != common.Hash(want) {
					t.Fatalf("round %d: reference authentication mismatch: %v", round, err)
				}
				if before.AuthenticationRoot() != old {
					t.Fatal("batch update mutated a copied trie")
				}
				root, nodes := native.Commit(false)
				if nodes != nil {
					for _, node := range nodes.Nodes {
						if !node.IsDeleted() {
							if err := disk.Put(node.Hash.Bytes(), node.Blob); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				native, err = trie.New(trie.StorageTrieID(root, owner, root), db)
				if err != nil {
					t.Fatal(err)
				}
				if got, err := native.AuditAuthentication(); err != nil || got != common.Hash(want) {
					t.Fatalf("round %d: persisted authentication mismatch: %v", round, err)
				}
			}
			// Deleting one of two accounts with a shared prefix collapses an
			// extension into a leaf; preserve the surviving storage commitment.
			accountTrie, _ := trie.NewStateTrie(trie.StateTrieID(types.EmptyRootHash), db)
			a := common.Address{1}
			ah := crypto.Keccak256Hash(a[:])
			var b common.Address
			for i := int64(1); ; i++ {
				b = common.BigToAddress(big.NewInt(i))
				bh := crypto.Keccak256Hash(b[:])
				if a != b && ah[0] == bh[0] {
					break
				}
			}
			acc := types.NewEmptyStateAccount()
			for _, address := range []common.Address{a, b} {
				if err := accountTrie.UpdateAccount(address, acc, 0); err != nil {
					t.Fatal(err)
				}
			}
			accountTrie.AuthenticationRoot()
			// A same-value update can leave hash references on geth's path.
			// Reattach account authentication without rewriting unchanged nodes.
			beforeAuth := accountTrie.AuthenticationRoot()
			root, nodes := accountTrie.Commit(false)
			for _, n := range nodes.Nodes {
				if !n.IsDeleted() {
					if err := disk.Put(n.Hash.Bytes(), n.Blob); err != nil {
						t.Fatal(err)
					}
				}
			}
			accountTrie, err = trie.NewStateTrie(trie.StateTrieID(root), db)
			if err != nil {
				t.Fatal(err)
			}
			cfg.SetVersion(cfg.Version() + 1)
			if err := accountTrie.UpdateAccount(a, acc, 0); err != nil {
				t.Fatalf("same-value account update after reload: %v", err)
			}
			if accountTrie.AuthenticationRoot() != beforeAuth || accountTrie.Hash() != root {
				t.Fatal("same-value account update changed authentication or physical root")
			}
			if err := accountTrie.DeleteAccount(b); err != nil {
				t.Fatal(err)
			}
			one, _ := trie.NewStateTrie(trie.StateTrieID(types.EmptyRootHash), db)
			if err := one.UpdateAccount(a, acc, 0); err != nil {
				t.Fatal(err)
			}
			if got, err := accountTrie.AuditAuthentication(); err != nil || got != one.AuthenticationRoot() {
				t.Fatalf("collapsed account leaf lost storage binding: %v", err)
			}
		})
	}
}

// Aggregate goldens generated from the unmodified legacy ea4899c1d codec:
// four versions, both trie sides, three paths, branch/leaf, per scheme.
func TestResearchAllLegacyCodecs(t *testing.T) {
	expected := map[string]string{
		"JMT_fixed":           "39dc1e311e2d235aedf236c40c0e3e47998101b0c442f93c40efffda900db3a9",
		"PrefixTree_fixed":    "df5ce2e7a84d842b5eaace3baf9b173693790dba33479ef4dfa949395a1f7037",
		"EpochPath":           "0f2b4f63d4fcd86bb384bf59638f8c38e2acc9750dcef67a183234182ac44527",
		"TPV":                 "16a828ae943f403626adbc8dafdeae67c631ae0594f17555d5dc53b3358e3431",
		"SplitPVHot":          "03833534ee51701dc45d52b4e83a594794bfcaf212615386a40908ae19bae3ee",
		"OutwardSplit":        "40796cc387c91b56987fa420627923b96ce28980be53956fe0e6ff82f58a623c",
		"OutwardStorage":      "44191948e6cf2b780833dfacb3cc82c0c55facc9821f557f6e8c7511137e241b",
		"VPRight":             "4b92f8f56044233d9f06c259805d49fc980054ec34e116c45fb08139d1e3f991",
		"DepthSplit":          "712d12f2c5643b03ae3e97da9946685fadedd58eec94daf61fe3294d70d3629d",
		"DepthEpoch":          "ed4090b6bfec4de7aa865f6bbb4d86daa28e6f795ca90a71e650cbcd2d3b16fb",
		"ShardVP":             "6ae2db1f2c07e41c59a26c2e4de9576fef45ec5d8ff4730be26a13b30db7ccfd",
		"DualVP":              "4a612385671dbe7ca3f6ff794e447d1bb151ce168f3b8e1e59d50a22fcf1ffff",
		"RunPath":             "e41bab7e9f2b81b9ed81c4bae5b25f077b48e0394a6d19d36b526353b21814ba",
		"ForestVP":            "d9b947a7d6e1d0f178060630b2ec3b45d71adb81a0788390fb9f23dd091b2a46",
		"ATileVP":             "1cc180acdc0fbd59eafb5769013701942cce0deefa0af211159056cd7962fcda",
		"JMT":                 "4e825c4219d5df84d5c5908c42ce2f96dad4d0f4de9f88a2be82e4f544911a70",
		"PrefixTree":          "368c41e4e2686ed21ea721d691636f401eb6d48e167df8654b216159007ab6b7",
		"JMT_balanced":        "4a5402d963de13f729fea50e5b767337c4885016a43d13e1c40e335006bfc82e",
		"PrefixTree_balanced": "368c41e4e2686ed21ea721d691636f401eb6d48e167df8654b216159007ab6b7",
		"HalfPath":            "81db832941b85b251844759eff028b5c117b84d7d622ebb4fb1a1cfedf05fb7c",
		"TH":                  "4a3f63ab0f9c60a664bd7748a67ef2a805f406f39791e633feec491110ba77cd",
	}
	owner := common.HexToHash("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	for scheme, want := range expected {
		t.Run(scheme, func(t *testing.T) {
			cfg := &common.ReplayConfig{KeyScheme: scheme, Codec: "fixed32-v1", EpochSize: 256, StateDepth: 3, StorageDepth: 2, ShardPrefix: 1, TileBlocks: 128, TilePathPrefix: 1, RunTargetNodes: 524288, SkipIntermediateRoot: true}
			cfg.RestoreRun(common.ReplayRun{ID: 7})
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			hash := sha256.New()
			for _, v := range []uint64{0, 129, 256, 10000000} {
				for _, storage := range []bool{false, true} {
					for _, depth := range []int{0, 2, 4} {
						for _, branch := range []bool{false, true} {
							path := make([]byte, depth)
							for i := range path {
								path[i] = byte(i + 1)
							}
							context := common.TrieNodeContext{Version: v, Path: path, Depth: depth / 2, Branch: branch, Digest: common.BytesToHash(bytes.Repeat([]byte{0xab}, 32))}
							if storage {
								context.Owner = owner
							}
							if !branch {
								context.LeafPath = append(append([]byte{}, path...), bytes.Repeat([]byte{10}, 64-depth)...)
							}
							key, err := cfg.NodeKey(context)
							if err != nil {
								t.Fatal(err)
							}
							hash.Write(key[:])
						}
					}
				}
			}
			if got := fmt.Sprintf("%x", hash.Sum(nil)); got != want {
				t.Fatalf("legacy codec differs: %s want %s", got, want)
			}
		})
	}
}

func TestResearchReadMetrics(t *testing.T) {
	genesis, blocks, _ := replayFixtureCount(t, 140)
	for _, scheme := range []string{"H", "Outward", "PBSS"} {
		for _, snapshot := range []bool{false, true} {
			if scheme == "PBSS" && snapshot {
				continue
			}
			for _, measure := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/snapshot=%t/measure=%t", scheme, snapshot, measure), func(t *testing.T) {
					dir := t.TempDir()
					kv, err := pebble.New(filepath.Join(dir, "state"), 16, 16, "test", false)
					if err != nil {
						t.Fatal(err)
					}
					cfg := replayChainConfig(scheme)
					cfg.NoPrefetch = true
					cfg.Replay.Measure = measure
					cfg.Replay.StateOnly = true
					var db ethdb.Database = NewReplayDatabase(kv)
					if snapshot {
						cfg.SnapshotLimit, cfg.SnapshotWait = 4, true
					}
					if scheme == "PBSS" {
						cfg.StateScheme, cfg.ArchiveMode = rawdb.PathScheme, false
						cfg.StateHistory, cfg.TrienodeHistory = pathdb.Defaults.StateHistory, pathdb.Defaults.TrienodeHistory
						db = NewReplayPathDatabase(kv, filepath.Join(dir, "ancient"), true)
					}
					defer db.Close()
					var lookups, snapReads uint64
					var snapCommits int64
					cfg.OnReplayBlock = func(r ReplayResult) {
						if r.CallTxLen > 0 && r.CallTxTime <= 0 {
							t.Fatal("missing call timing")
						}
						if r.CallTxTime+r.PaymentTxTime > r.Stats.TotalTime {
							t.Fatal("tx timing exceeds block scope")
						}
						rs := r.Research
						reads := rs.NodeReads
						if reads.Clean.Count != rs.CleanHits || reads.Dirty.Count != rs.DirtyHits || reads.Disk.Count != rs.DiskHits || reads.Missing.Count != rs.MissingNodes {
							t.Fatal("inconsistent lookup totals")
						}
						for _, read := range []common.ReplayReadStats{reads.Clean, reads.Dirty, reads.Disk, reads.Diff, reads.Missing} {
							if read.Count > 0 && read.TimeNS <= 0 {
								t.Fatal("lookup missing duration")
							}
							if !measure && read != (common.ReplayReadStats{}) {
								t.Fatal("detailed counters active in performance mode")
							}
							lookups += read.Count
						}
						snapReads += rs.SnapshotAccountLookups + rs.SnapshotStorageLookups
						snapCommits += rs.SnapshotCommits
						if measure && snapshot && rs.SnapshotAccountLookups > 0 && rs.SnapshotAccountReads <= 0 {
							t.Fatal("snapshot reads missing timer")
						}
					}
					bc, err := NewBlockChain(db, genesis, ethash.NewFaker(), cfg)
					if err != nil {
						t.Fatal(err)
					}
					defer bc.Stop()
					cfg.Replay.TakeStats() // discard genesis/setup
					if _, err := bc.InsertChain(blocks); err != nil {
						t.Fatal(err)
					}
					if measure && lookups == 0 {
						t.Fatal("no node lookup observations")
					}
					if measure && snapshot {
						if snapReads == 0 || snapCommits <= 0 {
							t.Fatal("snapshot metrics absent")
						}
					} else if snapReads != 0 || snapCommits != 0 {
						t.Fatal("snapshot metrics enabled unexpectedly")
					}
					if err := bc.VerifyReplayState(blocks[len(blocks)-1]); err != nil {
						t.Fatal(err)
					}

					cfg.Replay.TakeStats()
					root, err := bc.ReplayRoot(blocks[len(blocks)-1].Hash())
					if err != nil {
						t.Fatal(err)
					}
					reader, err := bc.TrieDB().NodeReader(root.PhysicalRoot)
					if err != nil {
						t.Fatal(err)
					}
					cfg.Replay.TakeStats()
					if blob, err := reader.Node(common.Hash{}, nil, common.Hash{0x99}); len(blob) != 0 {
						t.Fatalf("bad node reference returned data: %x, %v", blob, err)
					}
					missing := cfg.Replay.TakeStats().NodeReads.Missing
					if measure && (missing.Count != 1 || missing.TimeNS <= 0 || missing.Bytes != 0) {
						t.Fatalf("missing/error lookup not measured: %+v", missing)
					}
					if !measure && missing != (common.ReplayReadStats{}) {
						t.Fatal("missing lookup measured in performance mode")
					}
				})
			}
		}
	}
}
