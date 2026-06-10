package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// murmurOutboxSubdir is the local-only, gitignored directory (under a ledger or
// team-context checkout's .sageox/cache/) where the CLI queues murmurs that
// could not be delivered to a running daemon. The daemon drains it on each sync
// cycle. See .claude/rules/ledger-cache.md for the cache convention.
const murmurOutboxSubdir = ".sageox/cache/murmur-outbox"

// outboxEntry is one queued murmur read back from the outbox: its on-disk path
// and the decoded payload the daemon needs to commit it.
type outboxEntry struct {
	path    string
	payload MurmurPayload
}

// MurmurOutboxDir returns the outbox directory for a given ledger/team-context
// checkout root.
func MurmurOutboxDir(targetDir string) string {
	return filepath.Join(targetDir, murmurOutboxSubdir)
}

// WriteOutboxMurmur persists a murmur payload to the outbox so it survives a
// daemon-unavailable miss. The file is named after the murmur id (the base of
// RelPath, e.g. "<id>.json"), so re-queuing the same murmur is idempotent.
func WriteOutboxMurmur(targetDir string, p MurmurPayload) error {
	dir := MurmurOutboxDir(targetDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}

// ReadOutboxMurmurs returns all queued murmurs for a target. Unreadable or
// malformed files are skipped (best-effort) rather than failing the whole drain.
func ReadOutboxMurmurs(targetDir string) ([]outboxEntry, error) {
	dir := MurmurOutboxDir(targetDir)
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read murmur outbox dir: %w", err)
	}

	var entries []outboxEntry
	for _, f := range files {
		if f.IsDir() || filepath.Ext(f.Name()) != ".json" {
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
