package skillmanager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

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
	doc := loadKnownRepos(path)
	now := time.Now().UTC()
	for i, r := range doc.Repos {
		if r.Path == repoRoot {
			// Only rewrite once a day; this runs at every session start.
			if now.Sub(r.Seen) < 24*time.Hour {
				return
			}
			doc.Repos[i].Seen = now
			saveKnownRepos(path, doc)
			return
		}
	}
	doc.Repos = append(doc.Repos, knownRepo{Path: repoRoot, Seen: now})
	saveKnownRepos(path, doc)
}

// KnownRepos returns the recorded checkouts that still exist on disk, pruning
// entries whose directory has been deleted.
func KnownRepos() []string {
	path := knownReposPath()
	if path == "" {
		return nil
	}
	doc := loadKnownRepos(path)
	var alive []string
	var kept []knownRepo
	for _, r := range doc.Repos {
		if info, err := os.Stat(r.Path); err == nil && info.IsDir() {
			alive = append(alive, r.Path)
			kept = append(kept, r)
		}
	}
	if len(kept) != len(doc.Repos) {
		saveKnownRepos(path, knownReposDoc{Repos: kept})
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
	_ = os.WriteFile(path, append(data, '\n'), 0o600)
}
