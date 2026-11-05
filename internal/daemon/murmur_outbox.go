package daemon

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/paths"
)

// outboxEntry is one queued murmur read back from the outbox: its on-disk path
// and the decoded payload the daemon needs to commit it.
type outboxEntry struct {
	path    string
	payload MurmurPayload
}

// MurmurOutboxDir returns the outbox directory for a given ledger/team-context
// checkout root. It lives in the checkout's own .git directory: git never
// materializes tracked content there, so remote repository content cannot plant
// queue entries, and the CLI and daemon agree on it whatever their cache
// environment. Checkouts without a real .git directory fall back to daemon-owned
// cache state keyed by the canonical path. Deliberately do not read or migrate
// the old .sageox/cache/murmur-outbox queue.
func MurmurOutboxDir(targetDir string) string {
	if abs, err := filepath.Abs(targetDir); err == nil {
		targetDir = abs
	}
	// A CLI may address the same registered workspace through a local alias.
	if real, err := filepath.EvalSymlinks(targetDir); err == nil {
		targetDir = real
	}
	targetDir = filepath.Clean(targetDir)
	if info, err := os.Lstat(filepath.Join(targetDir, ".git")); err == nil && info.IsDir() {
		return filepath.Join(targetDir, murmurOutboxGitDir)
	}
	return murmurOutboxCacheDir(targetDir)
}

// murmurOutboxGitDir is checkout-relative so GC reclones can carry it over.
const murmurOutboxGitDir = ".git/sageox-murmur-outbox"

func murmurOutboxCacheDir(cleanTarget string) string {
	sum := sha256.Sum256([]byte(cleanTarget))
	return filepath.Join(paths.DaemonCacheDir(), "murmur-outbox", fmt.Sprintf("%x", sum))
}

// murmurOutboxDirs lists every queue a target may hold: murmurs queued before
// the checkout was cloned sit in the cache fallback, so it is drained too.
func murmurOutboxDirs(targetDir string) []string {
	dir := MurmurOutboxDir(targetDir)
	dirs := []string{dir}
	if abs, err := filepath.Abs(targetDir); err == nil {
		targetDir = abs
	}
	if real, err := filepath.EvalSymlinks(targetDir); err == nil {
		targetDir = real
	}
	if cache := murmurOutboxCacheDir(filepath.Clean(targetDir)); cache != dir {
		dirs = append(dirs, cache)
	}
	return dirs
}

// WriteOutboxMurmur persists a murmur payload to the outbox so it survives a
// daemon-unavailable miss. The file is named after the murmur id (the base of
// RelPath, e.g. "<id>.json"), so re-queuing the same murmur is idempotent.
func WriteOutboxMurmur(targetDir string, p MurmurPayload) error {
	dir := MurmurOutboxDir(targetDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create murmur outbox dir: %w", err)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal outbox murmur: %w", err)
	}
	name := filepath.Base(p.RelPath) // "<id>.json"
	if name == "" || name == "." || name == string(filepath.Separator) {
		return fmt.Errorf("invalid murmur rel_path %q", p.RelPath)
	}
	return os.WriteFile(filepath.Join(dir, name), data, 0o600)
}

// ReadOutboxMurmurs returns all queued murmurs for a target. Unreadable or
// malformed files are skipped (best-effort) rather than failing the whole drain.
func ReadOutboxMurmurs(targetDir string) ([]outboxEntry, error) {
	dirs := murmurOutboxDirs(targetDir)
	entries, err := readOutboxDir(dirs[0])
	if err != nil {
		return nil, err
	}
	// The pre-clone fallback is secondary: a damaged one must not hold back
	// the checkout's own queue.
	for _, dir := range dirs[1:] {
		found, err := readOutboxDir(dir)
		if err != nil {
			slog.Warn("skipping unreadable murmur outbox fallback", "dir", dir, "error", err)
			continue
		}
		entries = append(entries, found...)
	}
	return entries, nil
}

func readOutboxDir(dir string) ([]outboxEntry, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read murmur outbox dir: %w", err)
	}

	var entries []outboxEntry
	for _, f := range files {
		if !f.Type().IsRegular() || filepath.Ext(f.Name()) != ".json" {
			continue
		}
		full := filepath.Join(dir, f.Name())
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		var p MurmurPayload
		if err := json.Unmarshal(data, &p); err != nil {
			continue // skip malformed
		}
		entries = append(entries, outboxEntry{path: full, payload: p})
	}
	return entries, nil
}

// RemoveOutboxMurmur deletes a drained outbox file. A missing file is not an error.
func RemoveOutboxMurmur(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
