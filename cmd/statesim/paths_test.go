package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaySeparatePathsAndReset(t *testing.T) {
	root := t.TempDir()
	result, db, release, err := prepareReplayPaths(root, "", "", "first", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if result != filepath.Join(root, "results/first") || db != filepath.Join(root, "dbs/careful-run") {
		t.Fatalf("unexpected layout: %s %s", result, db)
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(result, "simblocks.jsonl"), "original results\n")
	descriptor, _ := json.Marshal(replayDescriptor{DatabasePath: db})
	write(filepath.Join(result, "replay.json"), string(descriptor))
	write(filepath.Join(db, "old-data"), "old DB")
	if _, _, unlock, err := prepareReplayPaths(root, "", "", "busy", false, true); err == nil {
		unlock()
		t.Fatal("reset a live experiment")
	}
	release()
	_, reopened, release, err := prepareReplayPaths(root, result, "", "", true, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if reopened != db {
		t.Fatal("resume did not follow saved DB path")
	}
	if _, _, unlock, err := prepareReplayPaths(root, result, "", "", false, true); err == nil {
		unlock()
		t.Fatal("reused existing results")
	}
	if _, err := os.Stat(filepath.Join(db, "old-data")); err != nil {
		t.Fatal("failed preflight removed the DB")
	}
	second, _, release, err := prepareReplayPaths(root, "", "", "second", false, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(filepath.Join(db, "old-data")); !os.IsNotExist(err) {
		t.Fatal("old DB survived reset")
	}
	if got, err := os.ReadFile(filepath.Join(result, "simblocks.jsonl")); err != nil || string(got) != "original results\n" {
		t.Fatal("reset changed old results")
	}
	if _, _, unlock, err := prepareReplayPaths(root, result, "", "", true, false); err == nil {
		unlock()
		t.Fatal("old results accepted a replaced DB")
	}
	_, separate, release, err := prepareReplayPaths(root, "", "", "normal", false, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if separate != filepath.Join(root, "dbs/normal") {
		t.Fatal("normal run shared scratch DB")
	}
	if owner, err := os.ReadFile(filepath.Join(db, replayDBOwner)); err != nil || string(owner) != second {
		t.Fatal("normal run changed scratch DB")
	}
}

func TestReplayResetProtectsPaths(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "dbs/careful-run")
	if err := os.MkdirAll(db, 0755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(db, "input.rlp")
	if err := os.WriteFile(input, []byte("preserve"), 0644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(db, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		results, database, input string
		resume                   bool
	}{
		{results: filepath.Join(db, "results")},
		{results: filepath.Join(alias, "results")},
		{input: input},
		{input: filepath.Join(alias, "input.rlp")},
		{database: filepath.Join(root, "other")},
		{resume: true},
	} {
		if _, _, unlock, err := prepareReplayPaths(root, tc.results, tc.database, "attempt", tc.resume, true, tc.input); err == nil {
			unlock()
			t.Fatalf("accepted unsafe reset: %+v", tc)
		}
		if data, err := os.ReadFile(input); err != nil || string(data) != "preserve" {
			t.Fatal("rejected reset changed input")
		}
	}
	if err := os.Symlink(input, filepath.Join(db, "LOCK")); err != nil {
		t.Fatal(err)
	}
	if _, _, unlock, err := prepareReplayPaths(root, "", "", "lock-symlink", false, true); err == nil {
		unlock()
		t.Fatal("followed a DB lock symlink")
	}
	if data, err := os.ReadFile(input); err != nil || string(data) != "preserve" {
		t.Fatal("lock attempt changed input")
	}
	if err := os.Rename(db, db+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(db+"-saved", db); err != nil {
		t.Fatal(err)
	}
	if _, _, unlock, err := prepareReplayPaths(root, "", "", "symlink", false, true); err == nil {
		unlock()
		t.Fatal("followed scratch symlink")
	}
}

func TestReplayResetRespectsNativeLocks(t *testing.T) {
	// A separate process is needed: Pebble's fcntl locks are process-scoped.
	if engine := os.Getenv("STATESIM_TEST_LOCK_ENGINE"); engine != "" {
		db, _, err := openReplayDatabase(os.Getenv("STATESIM_TEST_LOCK_PATH"), engine, "snappy", 16, 16)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		os.Stdout.WriteString("ready\n")
		io.Copy(io.Discard, os.Stdin)
		return
	}
	for _, engine := range []string{"leveldb", "pebble"} {
		t.Run(engine, func(t *testing.T) {
			root := t.TempDir()
			db := filepath.Join(root, "dbs/careful-run")
			child := exec.Command(os.Args[0], "-test.run=^TestReplayResetRespectsNativeLocks$")
			child.Env = append(os.Environ(), "STATESIM_TEST_LOCK_ENGINE="+engine, "STATESIM_TEST_LOCK_PATH="+db)
			child.Stderr = os.Stderr
			input, _ := child.StdinPipe()
			output, _ := child.StdoutPipe()
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { input.Close(); child.Wait() }()
			ready, err := bufio.NewReader(output).ReadString('\n')
			if err != nil || strings.TrimSpace(ready) != "ready" {
				t.Fatalf("DB helper failed: %q %v", ready, err)
			}
			files, err := os.ReadDir(db)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, release, err := prepareReplayPaths(root, "", "", "blocked", false, true); err == nil {
				release()
				t.Fatal("removed an open database")
			}
			for _, file := range files {
				if _, err := os.Stat(filepath.Join(db, file.Name())); err != nil {
					t.Fatal("changed an open database")
				}
			}
		})
	}
}
