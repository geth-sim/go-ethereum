package common

import (
	"time"
)

var (
	//
	// for evm simulation
	//

	// option: simulation mode (0: original ethereum, 1: Ethane, 2: Ethanos)
	SimulationMode      = 0
	EthereumMode        = 0
	EthaneMode          = 1
	EthanosMode         = 2
	SimulationModeNames = []string{"Ethereum", "Ethane", "Ethanos"}

	// simulation results, SimBlocks[blockNumStr] = SimBlock
	SimBlocks = make(map[string]*SimBlock)

	//
	// Ethanos
	//
	// to deal with SELF_DESTRUCTed contract accounts
	DeletedContractRoot = HexToHash("0xdeadffffffffffffffffffffffffffffffffffffffffffffffffffffffffdead")
	// cached trie's root
	CachedTrieRoot Hash

	//
	// Ethane
	//
	AddrToKeyActive   = make(map[Address]Hash)   // K_A
	AddrToKeyInactive = make(map[Address][]Hash) // K_I
	KeysToDelete      = make([]Hash, 0)          // D_A
	RestoredKeys      = make([]Hash, 0)          // D_I

	InactiveTrieRoot Hash

	// next key to insert new account in active trie
	NextKey  = uint64(1)
	ZeroHash = Hash{}
	MaxHash  = HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
)

// store simulation results as a block with performance metrics
type SimBlock struct {
	Number          uint64 // block number
	StateRoot       Hash   // state trie root
	SubStateRoot    Hash   // cached trie root (Ethanos) or inactive trie root (Ethane)
	LastActiveKey   uint64 // last used key in active trie in this block (similar to CheckpointKey)
	LastInactiveKey uint64 // last used key in inactive trie in this block (similar to InactiveBoundaryKey)

	// payment tx num & execution time
	PaymentTxLen      uint64
	PaymentTxExecutes time.Duration
	// contract call tx num & execution time
	CallTxLen      uint64
	CallTxExecutes time.Duration
	GasUsed        uint64

	// from StateDB struct
	// Measurements gathered during execution for debugging purposes
	AccountReads           time.Duration
	AccountReadNum         int
	NonExistAccountReadNum int
	AccountHashes          time.Duration
	AccountUpdates         time.Duration
	AccountCommits         time.Duration
	StorageReads           time.Duration
	StorageHashes          time.Duration
	StorageUpdates         time.Duration
	StorageCommits         time.Duration
	SnapshotAccountReads   time.Duration
	SnapshotStorageReads   time.Duration
	SnapshotCommits        time.Duration
	TrieDBCommits          time.Duration
	AccountUpdated         int
	StorageUpdated         int
	AccountDeleted         int
	StorageDeleted         int

	DiskSize         int64         // disk usage (i.e., result of du -b)
	DiskCommits      time.Duration // flush time to disk
	BlockExecuteTime time.Duration // elapsed time to execute this block

	// restoration for Ethanos & Ethane
	AccountRestoreNum    int
	AccountRestores      time.Duration
	RestoreReads         time.Duration
	RestoreSubReads      time.Duration // void read for Ethane, cached trie read for Ethanos
	RestoreHashes        time.Duration
	RestoreUpdates       time.Duration
	RestoreCommits       time.Duration
	RestoreTrieDBCommits time.Duration
	RestoreDiskCommits   time.Duration

	// for Ethanos
	CachedAccountReads   time.Duration
	CachedAccountReadNum int

	// for Ethane
	ActiveIndexReads   time.Duration
	VoidAccountReadNum int
	VoidAccountReads   time.Duration // for fair comparison, Ethane reads random account when desired account does not exist
	// do not measure commit times independently for deletion and inactivation
	// just add to other metrics (AccountCommits, TrieDBCommits, DiskCommits)
	DeleteNum         int
	DeleteUpdates     time.Duration
	DeleteHashes      time.Duration
	InactivateNum     int
	InactivateUpdates time.Duration
	InactivateHashes  time.Duration
	UsedProofNum      int
	UsedProofUpdtaes  time.Duration
}

// buffer to save StateDB's metrics
var (
	AccountUpdated int
	StorageUpdated int
	AccountDeleted int
	StorageDeleted int
)
