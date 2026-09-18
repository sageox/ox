package gitutil

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// sourcePublicationFetchTimeout bounds the network fetch below. Every caller
// passes context.Background() (no deadline of its own) from inside
// WithRepoLock, so without an internal deadline here a stalled remote blocks
// the repository lock indefinitely -- freezing unrelated commands like
// `ox session abort`/`delete` for the whole Ledger. RunGit itself has no
// timeout; it only honors whatever deadline ctx already carries.
const sourcePublicationFetchTimeout = 60 * time.Second

// CheckSourcePublication requires a refreshed remote ancestor before callers
// reconcile source coverage under the repository lock. It never repairs or
// rebases a dirty Ledger; the caller retains its local journal for a fresh scan.
func CheckSourcePublication(ctx context.Context, repo string) error {
	fetchCtx, cancel := context.WithTimeout(ctx, sourcePublicationFetchTimeout)
	defer cancel()
	if _, err := RunGit(fetchCtx, repo, "fetch", "--quiet", "origin"); err != nil {
		return fmt.Errorf("refresh source publication: %w", err)
	}
	if _, err := RunGit(ctx, repo, "merge-base", "--is-ancestor", "@{upstream}", "HEAD"); err != nil {
		return fmt.Errorf("source publication pending: remote advanced; refresh and reconcile source coverage")
	}
	return nil
}

// RefuseSourcePublicationRebase protects pending sourced sessions even when
// Git could replay their files without a textual conflict. Call before every
// automatic pull/rebase, including ordinary daemon sync and doctor repair.
func RefuseSourcePublicationRebase(ctx context.Context, repo string) error {
	// No tracking branch means there is no upstream to compare against, so
	// nothing can be pending publication against it. Refusing here instead
	// would permanently block the pull for every managed clone that has a
	// remote but no configured upstream -- a legitimate state, and one no
	// amount of retrying resolves.
	if _, err := RunGit(ctx, repo, "rev-parse", "--verify", "--quiet", "@{upstream}"); err != nil {
		return nil
	}
	paths, err := RunGit(ctx, repo, "diff", "--name-only", "-z", "@{upstream}...HEAD")
	if err != nil {
		return fmt.Errorf("inspect pending source publication: %w", err)
	}
	for _, path := range strings.Split(paths, "\x00") {
		if strings.HasPrefix(path, "data/session-sources/") {
			return fmt.Errorf("source publication pending: automatic rebase is disabled for coverage and exclusions")
		}
		if !strings.HasPrefix(path, "sessions/") {
			continue
		}
		parts := strings.Split(path, "/")
		if len(parts) < 3 {
			continue
		}
		meta, err := RunGit(ctx, repo, "show", "HEAD:sessions/"+parts[1]+"/meta.json")
		if err != nil {
			// A deletion may remove metadata. Its source tombstone is caught
			// above; legacy deletions retain their existing recovery behavior.
			continue
		}
		var header struct {
			Source json.RawMessage `json:"source"`
		}
		if err := json.Unmarshal([]byte(meta), &header); err != nil {
			return fmt.Errorf("inspect pending session metadata: %w", err)
		}
		if len(header.Source) > 0 && string(header.Source) != "null" {
			return fmt.Errorf("source publication pending: automatic rebase is disabled for sourced sessions")
		}
	}
	return nil
}
