package skillmanager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
)

// Known repositories: the machine-local list of checkouts where ox has
// materialized a skill inventory.
//
// ox has no registry of the repositories a user has initialized. The de-facto
// list — one ledger directory per repo id under DataDir — is keyed by repo id,
// and nothing maps a repo id back to a filesystem path. The daemon registry has
// the path but tracks only RUNNING daemons and lives in the runtime dir, so it
// does not survive a reboot.
//
// That gap was the single strongest argument for symlinking a central store into
// every repo: with no way to enumerate repositories, nothing can push an update.
// Recording the paths closes it for a few dozen lines, which is why the argument
// did not survive.
//
// This list is a CACHE, not a source of truth. A stale or missing entry costs one
// deferred reconcile — the repository still self-heals at its next prime — so
// every operation here is best-effort and never fails a caller.

const knownReposFile = "known-repos.json"

type knownRepo struct {
	Path string    `json:"path"`
	Seen time.Time `json:"seen"`
}

type knownReposDoc struct {
	Repos []knownRepo `json:"repos"`
}

// knownReposPath lives under DataDir, not CacheDir: on macOS a GUI process and a
// terminal process resolve different cache roots, so a cache-based list would be
// invisible to half the processes that write it.
// knownReposLockWait bounds how long a writer waits for the registry lock.
//
// Deliberately NOT knownReposLockWait (100ms). That budget exists for the skills
// APPLY lock, where a session start must never stall behind a doctor run doing
// real filesystem work. The work under THIS lock is a small JSON read, one slice
// append, and an atomic write — microseconds. At 100ms, several processes
// starting together silently lost each other's records: eight concurrent
// RememberRepo calls retained six. A dropped record means `ox upgrade` never
// reaches that checkout, which is the entire reason the registry exists.
//
// Two seconds is still bounded, so a wedged lock can never hang a session.
const knownReposLockWait = 2 * time.Second

func knownReposPath() string {
	dir := paths.DataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "cli", knownReposFile)
}

// RememberRepo records repoRoot as a checkout with a materialized inventory.
// Called from the session hot path, so it returns immediately when the entry is
// already current.
func RememberRepo(repoRoot string) {
	path := knownReposPath()
	if path == "" || repoRoot == "" {
		return
	}
	_ = fileutil.WithFileLockTimeout(context.Background(), path, knownReposLockWait, func() error {
		doc := loadKnownRepos(path)
		now := time.Now().UTC()
		for i, r := range doc.Repos {
			if r.Path == repoRoot {
				// Only rewrite once a day; this runs at every session start.
				if now.Sub(r.Seen) < 24*time.Hour {
					return nil
				}
				doc.Repos[i].Seen = now
				saveKnownRepos(path, doc)
				return nil
			}
		}
		doc.Repos = append(doc.Repos, knownRepo{Path: repoRoot, Seen: now})
		saveKnownRepos(path, doc)
		return nil
	})
}

// KnownRepos returns the recorded checkouts that still exist on disk, pruning
// entries whose directory has been deleted.
func KnownRepos() []string {
	path := knownReposPath()
	if path == "" {
		return nil
	}
	var doc knownReposDoc
	locked := false
	_ = fileutil.WithFileLockTimeout(context.Background(), path, knownReposLockWait, func() error {
		locked = true
		doc = loadKnownRepos(path)
		return nil
	})
	if !locked {
		// Writes use atomic rename, so an unlocked fallback still observes one
		// complete snapshot. Skip pruning below because another writer owns the RMW.
		doc = loadKnownRepos(path)
	}
	var alive []string
	var kept []knownRepo
	for _, r := range doc.Repos {
		if info, err := os.Stat(r.Path); err == nil && info.IsDir() {
			alive = append(alive, r.Path)
			kept = append(kept, r)
		}
	}
	if locked && len(kept) != len(doc.Repos) {
		// Reacquire around the prune RMW: the first lock was intentionally released
		// before filesystem stats so a slow mount cannot stall every session start.
		_ = fileutil.WithFileLockTimeout(context.Background(), path, knownReposLockWait, func() error {
			current := loadKnownRepos(path)
			var currentKept []knownRepo
			for _, r := range current.Repos {
				if info, err := os.Stat(r.Path); err == nil && info.IsDir() {
					currentKept = append(currentKept, r)
				}
			}
			if len(currentKept) != len(current.Repos) {
				saveKnownRepos(path, knownReposDoc{Repos: currentKept})
			}
			return nil
		})
	}
	sort.Strings(alive)
	return alive
}

func loadKnownRepos(path string) knownReposDoc {
	var doc knownReposDoc
	data, err := os.ReadFile(path)
	if err != nil {
		return doc
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return knownReposDoc{} // a corrupt cache is rebuilt, never fatal
	}
	return doc
}

func saveKnownRepos(path string, doc knownReposDoc) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	_ = fileutil.AtomicWriteBytes(path, append(data, '\n'), 0o600)
}
