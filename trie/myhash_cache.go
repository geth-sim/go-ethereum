package trie

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/ethereum/go-ethereum/common"
)

// Two separate caches:
// - State trie:   key = childPath
// - Storage trie: key = addrHash(32) || childPath
//
// value = random 32 bytes (placeholder)
// semantics: key presence means "already additionally read+decoded once"

var (
	childReadStateCache   *fastcache.Cache
	childReadStorageCache *fastcache.Cache
	childReadUnifiedCache *fastcache.Cache
	onceInit              sync.Once
)

func initChildReadCachesOnce() {
	onceInit.Do(func() {
		if common.UseUnifiedCache {
			unifiedChildReadCacheSize := common.StateChildReadCacheSize + common.StorageChildReadCacheSize
			if unifiedChildReadCacheSize > 0 {
				childReadUnifiedCache = fastcache.New(unifiedChildReadCacheSize * 1024 * 1024)
			}
			return
		}
		if common.StateChildReadCacheSize > 0 {
			childReadStateCache = fastcache.New(common.StateChildReadCacheSize * 1024 * 1024)
		}
		if common.StorageChildReadCacheSize > 0 {
			childReadStorageCache = fastcache.New(common.StorageChildReadCacheSize * 1024 * 1024)
		}
	})
}

func currentChildReadCache() *fastcache.Cache {
	initChildReadCachesOnce()

	if common.UseUnifiedCache {
		return childReadUnifiedCache
	}

	if common.HashingStateTrie && !common.HashingStorageTrie {
		return childReadStateCache
	}
	if !common.HashingStateTrie && common.HashingStorageTrie {
		return childReadStorageCache
	}
	return nil
}

// key 생성: state는 childPath 그대로(복사)
// storage는 addrHash||childPath (복사)
func buildChildReadKey(childPath []byte) []byte {
	if common.HashingStateTrie && !common.HashingStorageTrie {
		k := make([]byte, len(childPath))
		copy(k, childPath)
		return k
	}
	// storage
	addr := common.AddrHashOfCurrentStorageTrie
	k := make([]byte, len(addr)+len(childPath))
	copy(k[:len(addr)], addr[:])
	copy(k[len(addr):], childPath)
	return k
}

// shouldAdditionalReadChild returns true if we SHOULD do extra read+decode.
// Rule (temporary spec):
// - cache disabled => do read
// - key exists => skip read
// - key not exists => do read (and caller should mark cache on success)
func shouldAdditionalReadChild(childPath []byte) bool {
	cache := currentChildReadCache()
	if cache == nil {
		// cache disabled => treat as miss (or ignore)
		// 여기서 카운트할지 말지는 취향인데, 보통은 제외하는 게 깔끔함
		return true
	}
	key := buildChildReadKey(childPath)

	if cache.Get(nil, key) != nil {
		// hit
		if common.HashingStateTrie && !common.HashingStorageTrie {
			addChildReadStat(&ChildReadStateHit)
		} else if !common.HashingStateTrie && common.HashingStorageTrie {
			addChildReadStat(&ChildReadStorageHit)
		}
		return false // already read => skip
	}

	// miss
	if common.HashingStateTrie && !common.HashingStorageTrie {
		addChildReadStat(&ChildReadStateMiss)
	} else if !common.HashingStateTrie && common.HashingStorageTrie {
		addChildReadStat(&ChildReadStorageMiss)
	}
	return true
}

var marker32 = make([]byte, 32) // all zeros

// markAdditionalReadDone inserts a random 32B value for the childPath key.
// Call this ONLY when extra read+decode succeeded.
func markAdditionalReadDone(childPath []byte) {
	cache := currentChildReadCache()
	if cache == nil {
		return
	}
	key := buildChildReadKey(childPath)

	val := make([]byte, 32)
	// _, _ = crand.Read(val) // ignore error; val may be partially random but irrelevant
	// common.FillRandomBytes(val)
	copy(val, marker32)

	cache.Set(key, val)
}

func refreshAdditionalReadMarkerIfPresent(childPath []byte) {
	cache := currentChildReadCache()
	if cache == nil {
		return
	}
	key := buildChildReadKey(childPath)

	// 존재 여부 확인 (Get이 nil이면 없음)
	if cache.Get(nil, key) == nil {
		return
	}
	// 있으면 overwrite
	val := make([]byte, 32)
	// common.FillRandomBytes(val)
	copy(val, marker32)

	cache.Set(key, val)
}

var (
	ChildReadStateHit    uint64
	ChildReadStateMiss   uint64
	ChildReadStorageHit  uint64
	ChildReadStorageMiss uint64
)

func addChildReadStat(counter *uint64) {
	if common.MeasureReadStats {
		atomic.AddUint64(counter, 1)
		return
	}
	*counter++
}

func loadChildReadStat(counter *uint64) uint64 {
	if common.MeasureReadStats {
		return atomic.LoadUint64(counter)
	}
	return *counter
}

func ResetChildReadCacheStats() {
	if common.MeasureReadStats {
		atomic.StoreUint64(&ChildReadStateHit, 0)
		atomic.StoreUint64(&ChildReadStateMiss, 0)
		atomic.StoreUint64(&ChildReadStorageHit, 0)
		atomic.StoreUint64(&ChildReadStorageMiss, 0)
		return
	}
	ChildReadStateHit, ChildReadStateMiss = 0, 0
	ChildReadStorageHit, ChildReadStorageMiss = 0, 0
}

func PrintChildReadCacheStats() {
	fmt.Println("==== ChildReadCache stats ====")

	printOne := func(name string, hit, miss uint64) {
		total := hit + miss
		if total == 0 {
			fmt.Printf("  %s: no lookups\n", name)
			return
		}
		rate := float64(hit) * 100.0 / float64(total)
		fmt.Printf("  %s: hit=%d miss=%d hitRate=%.2f%% total=%d\n", name, hit, miss, rate, total)
	}

	stateHit := loadChildReadStat(&ChildReadStateHit)
	stateMiss := loadChildReadStat(&ChildReadStateMiss)
	storageHit := loadChildReadStat(&ChildReadStorageHit)
	storageMiss := loadChildReadStat(&ChildReadStorageMiss)

	if common.UseUnifiedCache {
		uHit := stateHit + storageHit
		uMiss := stateMiss + storageMiss
		printOne("unified", uHit, uMiss)
	}

	printOne("state", stateHit, stateMiss)
	printOne("storage", storageHit, storageMiss)
}
