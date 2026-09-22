package addons

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// LoadLock reads the team's committed add-on selection from teamPath.
//
// A missing lock file is a normal, EMPTY lock — a team that has never run
// `ox addons install` has selected nothing, which is not an error state. A
// file that exists but cannot be parsed IS an error: silently treating
// unreadable content as "nothing installed" would make Install miss a real
// namespace collision and Remove believe it owns paths it does not, which is
// the exact "absent result means broken, not clean" trap the contract warns
// about.
func LoadLock(teamPath string) (Lock, error) {
	root, err := os.OpenRoot(teamPath)
	if err != nil {
		return Lock{}, fmt.Errorf("open team context: %w", err)
	}
	defer func() { _ = root.Close() }()
	return loadLockRoot(root)
}

// loadLockRoot is LoadLock's implementation, taking an already-open root so
// the install transaction reads through the same handle it writes through
// rather than opening the checkout a second time mid-transaction.
func loadLockRoot(root *os.Root) (Lock, error) {
	data, err := root.ReadFile(filepath.FromSlash(LockRelativePath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return emptyLock(), nil
		}
		return Lock{}, fmt.Errorf("read %s: %w", LockRelativePath, err)
	}

	var lock Lock
	if err := json.Unmarshal(data, &lock); err != nil {
		return Lock{}, fmt.Errorf("parse %s: %w", LockRelativePath, err)
	}
	if lock.SchemaVersion > LockSchemaVersion {
		return Lock{}, fmt.Errorf(
			"%s is schema version %d, but this ox understands up to version %d — upgrade ox before running add-on commands in this Team Context",
			LockRelativePath, lock.SchemaVersion, LockSchemaVersion)
	}
	if lock.Addons == nil {
		lock.Addons = []LockedAddon{}
	}
	return lock, nil
}

func emptyLock() Lock {
	return Lock{SchemaVersion: LockSchemaVersion, Addons: []LockedAddon{}}
}

// WriteLock rewrites the whole lock file, sorted so that installing the same
// selection on two machines produces the same ordering — the lock changes in
// git only where content actually changed, never because a map or an install
// order happened to differ. Sorts lock.Addons and each addon's Files in
// place: the caller mutating the same Lock it is about to pass here is the
// expected shape (see runTeamTransaction), and getting the sorted order back
// is a feature, not a side effect to guard against.
//
// Written through root, a handle already rooted at the Team Context checkout
// — never a joined absolute path (see package doc / ADR-032's write
// discipline). Whole-file rewrite: WriteLock never patches the existing
// bytes, so a reader never observes a half-updated selection.
func WriteLock(root *os.Root, lock Lock) error {
	if lock.SchemaVersion == 0 {
		lock.SchemaVersion = LockSchemaVersion
	}
	if lock.Addons == nil {
		lock.Addons = []LockedAddon{}
	}
	sort.Slice(lock.Addons, func(i, j int) bool { return lock.Addons[i].Addon < lock.Addons[j].Addon })
	for i := range lock.Addons {
		if lock.Addons[i].Files == nil {
			lock.Addons[i].Files = []LockedFile{}
		}
		files := lock.Addons[i].Files
		sort.Slice(files, func(a, b int) bool { return files[a].Path < files[b].Path })
	}

	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", LockRelativePath, err)
	}
	data = append(data, '\n')

	relPath := filepath.FromSlash(LockRelativePath)
	if dir := filepath.Dir(relPath); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := root.WriteFile(relPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", LockRelativePath, err)
	}
	return nil
}
