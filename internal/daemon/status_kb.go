package daemon

// status_kb.go — enumerate locally-synced knowledge bubbles for `ox daemon
// status`. Purely local: it scans the canonical XDG kb root
// (paths.KBDir("")) and reads each bubble's .sageox/meta.json. This works in
// ANY daemon, owner or follower — followers report the on-disk truth the
// global-sync owner keeps fresh (see global_lease.go / sync.go:920-928), so
// `ox daemon status` always shows what's on disk regardless of which daemon
// holds the lease.
//
// The daemon owns git reads (clone/pull); this is a read-only status view, so
// it never touches git state beyond a .git existence stat.

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/paths"
)

// kbWorkspaceStatus returns one WorkspaceSyncStatus per locally-cloned
// knowledge bubble, sourced entirely from disk. Returns nil (not an error)
// when the kb store doesn't exist yet or the endpoint is unresolved — a
// daemon with no bubbles on disk simply has no kb rows to report.
func (s *SyncScheduler) kbWorkspaceStatus() []WorkspaceSyncStatus {
	root := kbRootSafe()
	if root == "" {
		return nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		// no kb store yet (fresh install / not logged in) is the common case;
		// anything else is logged at debug since status must never fail hard.
		if !os.IsNotExist(err) {
			s.logger.Debug("kb_status readdir failed", "root", root, "error", err)
		}
		return nil
	}

	rows := make([]WorkspaceSyncStatus, 0, len(entries))
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		name := ent.Name()
		if len(name) > 0 && name[0] == '.' {
			// skip .trash/ and any other dotfile siblings
			continue
		}

		kbPath := filepath.Join(root, name)
		row := WorkspaceSyncStatus{
			ID:     name, // kb_id is the directory name
			Type:   "kb",
			Path:   kbPath,
			Exists: kbHasGitDir(kbPath),
		}

		// meta.json is the daemon's own per-bubble record (written every
		// reconcile in writeKBMeta). Reuse the in-package kbMeta shape.
		if meta, ok := readKBMetaForStatus(kbPath); ok {
			row.KBType = string(meta.Type)
			row.Slug = meta.Slug
			row.LastSync = meta.LastSync
		}

		rows = append(rows, row)
	}
	return rows
}

// kbRootSafe returns paths.KBDir(""), recovering the panic KBDir raises when
// no endpoint is configured (tests with no SAGEOX_ENDPOINT, fresh installs)
// so a status query degrades to "no bubbles" instead of crashing the daemon.
func kbRootSafe() (root string) {
	defer func() {
		if recover() != nil {
			root = ""
		}
	}()
	return paths.KBDir("")
}

// kbHasGitDir reports whether <kbPath>/.git exists — the same "is this a real
// clone" signal reconcileBubble uses to decide clone-vs-pull.
func kbHasGitDir(kbPath string) bool {
	_, err := os.Stat(filepath.Join(kbPath, ".git"))
	return err == nil
}

// readKBMetaForStatus loads <kbPath>/.sageox/meta.json into the in-package
// kbMeta shape. Returns ok=false when the file is absent (initial-clone
// pending) or unparseable, so the caller leaves those fields zero.
func readKBMetaForStatus(kbPath string) (kbMeta, bool) {
	data, err := os.ReadFile(filepath.Join(kbPath, ".sageox", "meta.json"))
	if err != nil {
		return kbMeta{}, false
	}
	var meta kbMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return kbMeta{}, false
	}
	return meta, true
}
