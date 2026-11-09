// Copyright 2019 The go-ethereum Authors
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

package leveldb

import (
	"bytes"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/dbtest"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
)

type syncStorage struct {
	storage.Storage
	journalSyncs atomic.Uint64
}

func (s *syncStorage) Create(fd storage.FileDesc) (storage.Writer, error) {
	w, err := s.Storage.Create(fd)
	if err == nil && fd.Type == storage.TypeJournal {
		w = &syncWriter{w, &s.journalSyncs}
	}
	return w, err
}

type syncWriter struct {
	storage.Writer
	syncs *atomic.Uint64
}

func (w *syncWriter) Sync() error {
	if err := w.Writer.Sync(); err != nil {
		return err
	}
	w.syncs.Add(1)
	return nil
}

func TestReplaySyncAndMetrics(t *testing.T) {
	store := &syncStorage{Storage: storage.NewMemStorage()}
	inner, err := leveldb.Open(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	db := &Database{db: inner}
	key, value := bytes.Repeat([]byte{1}, 32), []byte("immutable root")
	if err := db.Put(key, value); err != nil {
		t.Fatal(err)
	}
	before := store.journalSyncs.Load()
	if err := db.SyncImmutableKey(key); err != nil {
		t.Fatal(err)
	}
	if store.journalSyncs.Load() <= before {
		t.Fatal("checkpoint did not sync the WAL")
	}
	got, err := db.Get(key)
	if err != nil || !bytes.Equal(got, value) {
		t.Fatal("sync changed the root")
	}
	if err := db.SyncImmutableKey([]byte("missing")); err == nil {
		t.Fatal("sync claimed success for an unavailable root")
	}
	before = store.journalSyncs.Load()
	if err := db.SyncAbsentKey([]byte("missing")); err != nil {
		t.Fatal(err)
	}
	if store.journalSyncs.Load() <= before {
		t.Fatal("absent-key checkpoint did not sync the WAL")
	}
	if exists, err := db.Has([]byte("missing")); exists || err != nil {
		t.Fatal("absent-key sync created a live key", err)
	}
	if err := db.SyncAbsentKey(key); err == nil {
		t.Fatal("absent-key sync accepted an existing key")
	}
	if got, err := db.Get(key); err != nil || !bytes.Equal(got, value) {
		t.Fatal("absent-key sync deleted an existing value", err)
	}
	first, err := db.Metrics()
	if err != nil || first.IOWrite == 0 {
		t.Fatalf("no write statistics: %v", err)
	}
	second, err := db.Metrics()
	if err != nil || second.IOWrite < first.IOWrite {
		t.Fatal("snapshot reset counters")
	}
	if _, err := json.Marshal(second); err != nil {
		t.Fatal(err)
	}
}

func TestLevelDB(t *testing.T) {
	t.Run("DatabaseSuite", func(t *testing.T) {
		dbtest.TestDatabaseSuite(t, func() ethdb.KeyValueStore {
			db, err := leveldb.Open(storage.NewMemStorage(), nil)
			if err != nil {
				t.Fatal(err)
			}
			return &Database{
				db: db,
			}
		})
	})
}

func BenchmarkLevelDB(b *testing.B) {
	dbtest.BenchDatabaseSuite(b, func() ethdb.KeyValueStore {
		db, err := leveldb.Open(storage.NewMemStorage(), nil)
		if err != nil {
			b.Fatal(err)
		}
		return &Database{
			db: db,
		}
	})
}

func TestReplayReadStatsCapability(t *testing.T) {
	inner, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	db := &Database{db: inner}
	if err := db.EnableReadStats(true); err != nil {
		if _, err := db.ReadStatsJSON(); err == nil {
			t.Fatal("unsupported build returned detailed statistics")
		}
		return // Ordinary upstream build must explicitly reject the request.
	}
	readCount := func() int64 {
		t.Helper()
		data, err := db.ReadStatsJSON()
		if err != nil {
			t.Fatal(err)
		}
		var stats struct{ ReadRequestCount int64 }
		if err := json.Unmarshal(data, &stats); err != nil {
			t.Fatal(err)
		}
		return stats.ReadRequestCount
	}
	before := readCount()
	for i := 0; i < 7; i++ {
		if _, err := db.Get([]byte("absent")); err == nil {
			t.Fatal("missing key found")
		}
	}
	if got := readCount(); got != before+7 || readCount() != got {
		t.Fatalf("snapshot lost/reset Get counters: before=%d after=%d", before, got)
	}
	for _, key := range [][]byte{make([]byte, 32), append([]byte{'c'}, make([]byte, 32)...)} {
		if _, err := db.Get(key); err == nil {
			t.Fatal("unexpected key")
		}
	}
	if err := db.EnableReadStats(false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get(make([]byte, 32)); err == nil {
		t.Fatal("unexpected PBSS key")
	}
	data, err := db.ReadStatsJSON()
	if err != nil {
		t.Fatal(err)
	}
	var stats struct {
		SchemaVersion int
		ByKind        map[string]struct{ ReadRequestCount int64 }
	}
	if err := json.Unmarshal(data, &stats); err != nil {
		t.Fatal(err)
	}
	if stats.SchemaVersion != 3 || stats.ByKind["trie"].ReadRequestCount != 1 || stats.ByKind["code"].ReadRequestCount != 1 || stats.ByKind["other"].ReadRequestCount < 8 {
		t.Fatalf("wrong hashdb/code/PBSS classification: %+v", stats)
	}
}
