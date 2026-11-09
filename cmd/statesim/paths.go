package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/gofrs/flock"
)

const replayDBOwner = "statesim-results"

// Resolve existing ancestors too, so aliases cannot put results inside a DB
// that careful-run is about to remove.
func replayAbsolutePath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		parent, parentErr := replayAbsolutePath(filepath.Dir(path))
		return filepath.Join(parent, filepath.Base(path)), parentErr
	}
	return resolved, err
}

func replayPathWithin(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// prepareReplayPaths returns a lock held until after DB close. It lives outside
// the DB so removing/recreating careful-run cannot bypass another running replay.
func prepareReplayPaths(root, results, database, name string, resume, careful bool, protected ...string) (resultDir, dbPath string, release func(), err error) {
	if careful && (resume || database != "") {
		return "", "", nil, errors.New("-careful-run cannot be combined with -resume or -db-dir")
	}
	if resume && results == "" {
		return "", "", nil, errors.New("-resume requires -datadir pointing to the original results")
	}
	automatic := results == ""
	root, err = replayAbsolutePath(root)
	if err != nil {
		return "", "", nil, err
	}
	if automatic {
		results = filepath.Join(root, "results", name)
	}
	results, err = replayAbsolutePath(results)
	if err != nil {
		return "", "", nil, err
	}
	if resume && database == "" {
		data, readErr := os.ReadFile(filepath.Join(results, "replay.json"))
		if readErr != nil {
			return "", "", nil, readErr
		}
		var saved replayDescriptor
		if err := json.Unmarshal(data, &saved); err != nil {
			return "", "", nil, err
		}
		database = saved.DatabasePath
	}
	if careful {
		database = filepath.Join(root, "dbs", "careful-run")
		// Never follow a substituted scratch directory or dbs parent.
		for _, path := range []string{filepath.Dir(database), database} {
			info, err := os.Lstat(path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", "", nil, err
			}
			if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
				return "", "", nil, fmt.Errorf("careful-run requires a real directory: %s", path)
			}
		}
	} else if database == "" {
		if automatic {
			database = filepath.Join(root, "dbs", name)
		} else {
			// Existing commands and checkpoints retain their original layout.
			database = filepath.Join(results, "chaindata")
		}
	}
	database, err = replayAbsolutePath(database)
	if err != nil {
		return "", "", nil, err
	}
	if replayPathWithin(database, results) {
		return "", "", nil, errors.New("results must be outside the database directory")
	}
	if careful {
		for _, path := range protected {
			if path == "" {
				continue
			}
			path, err := replayAbsolutePath(path)
			if err != nil {
				return "", "", nil, err
			}
			if replayPathWithin(database, path) {
				return "", "", nil, fmt.Errorf("input/profile path is inside careful-run: %s", path)
			}
		}
	}
	if !resume {
		// Reserve results before creating a legacy DB parent or resetting a DB.
		if err := os.MkdirAll(filepath.Dir(results), 0755); err != nil {
			return "", "", nil, err
		}
		if err := os.Mkdir(results, 0755); err != nil {
			return "", "", nil, fmt.Errorf("results directory must be new: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(database), 0755); err != nil {
		return "", "", nil, err
	}
	lock := flock.New(database + ".statesim-lock")
	locked, err := lock.TryLock()
	if err != nil || !locked {
		lock.Close()
		return "", "", nil, fmt.Errorf("database is locked or unavailable: %s (%v)", database, err)
	}
	defer func() {
		if err != nil {
			lock.Close()
		}
	}()
	_, statErr := os.Stat(database)
	if resume {
		if statErr != nil {
			return "", "", nil, fmt.Errorf("resume database: %w", statErr)
		}
		owner, ownerErr := os.ReadFile(filepath.Join(database, replayDBOwner))
		if ownerErr != nil && !errors.Is(ownerErr, os.ErrNotExist) {
			return "", "", nil, ownerErr
		}
		if ownerErr == nil && string(owner) != results {
			return "", "", nil, errors.New("database belongs to another result directory; it may have been reset by careful-run")
		}
		if ownerErr != nil && database != filepath.Join(results, "chaindata") {
			return "", "", nil, errors.New("separate database is missing its result-directory marker")
		}
	} else {
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return "", "", nil, statErr
		}
		if statErr == nil && !careful {
			return "", "", nil, errors.New("database already exists; use -resume only for the same experiment")
		}
		if careful && statErr == nil {
			if err := removeReplayScratch(database); err != nil {
				return "", "", nil, err
			}
		}
		if err := os.MkdirAll(database, 0755); err != nil {
			return "", "", nil, err
		}
		if err := os.WriteFile(filepath.Join(database, replayDBOwner), []byte(results), 0644); err != nil {
			return "", "", nil, err
		}
	}
	return results, database, func() { lock.Close() }, nil
}

func removeReplayScratch(path string) error {
	// Also respect the native locks held by an older simulator: LevelDB uses
	// flock and Pebble uses fcntl on Linux. Neither engine is opened here.
	lockPath := filepath.Join(path, "LOCK")
	if info, err := os.Lstat(lockPath); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("careful-run requires a regular DB lock file: %s", lockPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil || !locked {
		lock.Close()
		return fmt.Errorf("careful-run database is in use: %s (%v)", path, err)
	}
	defer lock.Close()
	pebbleLock, err := vfs.Default.Lock(lockPath)
	if err != nil {
		return fmt.Errorf("careful-run database is in use: %w", err)
	}
	defer pebbleLock.Close()
	fmt.Fprintf(os.Stderr, "careful-run: resetting DB %s (previous results are retained)\n", path)
	return os.RemoveAll(path)
}
