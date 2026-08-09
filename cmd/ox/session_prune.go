package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove local-only sessions",
	Long: `Remove sessions from local stores that have not been uploaded to the ledger.

By default, prune only removes sessions in the "local" status — finalized
recordings that exist locally but were never pushed to the ledger.

With --all, prune removes every local session that is not present in the
ledger, including paused, canceled, ghost, and orphan sessions. Active
recordings are always skipped — stop them with 'ox session stop' first.

Sessions already uploaded to the ledger are never touched. Use
'ox session remove <name>' to delete those.

Prune covers two local locations:
  - the per-user session store (where new recordings start)
  - the ledger cache (<ledger>/.sageox/cache/sessions, the staging area
    before a recording is committed-as-pointer to the ledger)

Examples:
  ox session prune              # remove local-only finalized sessions
  ox session prune --all        # remove anything not uploaded
  ox session prune --dry-run    # preview what would be removed
  ox session prune --force      # skip confirmation`,
	Args: cobra.NoArgs,
	RunE: runSessionPrune,
}

func init() {
	sessionCmd.AddCommand(sessionPruneCmd)
	sessionPruneCmd.Flags().Bool("all", false, "Remove every local session not uploaded to the ledger (excludes active recordings)")
	sessionPruneCmd.Flags().Bool("dry-run", false, "Print what would be removed without deleting")
	sessionPruneCmd.Flags().Bool("force", false, "Skip confirmation prompt")
}

func runSessionPrune(cmd *cobra.Command, _ []string) error {
	pruneAll, _ := cmd.Flags().GetBool("all")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")

	localStore, _, err := newSessionStore()
	if err != nil {
		return err
	}

	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return fmt.Errorf("ledger is unavailable — refusing to prune without ledger to verify uploaded sessions: %w\nRun 'ox doctor' to fix ledger sync, or use 'ox session remove --all' to remove every local session unconditionally", err)
	}

	uploaded, err := loadUploadedKeys(ledgerPath)
	if err != nil {
		return err
	}

	// build the list of local stores to scan: per-user store + ledger cache.
	stores := buildPruneStores(localStore, ledgerPath)

	candidates, skippedRecording, err := collectPruneCandidates(stores, uploaded, pruneAll)
	if err != nil {
		return err
	}

	if len(candidates) == 0 {
		if skippedRecording > 0 {
			cli.PrintHint(fmt.Sprintf("Skipped %d active recording(s). Stop them with 'ox session stop' before pruning.", skippedRecording))
		}
		fmt.Println("Nothing to prune.")
		return nil
	}

	scope := "local-only"
	if pruneAll {
		scope = "not-uploaded"
	}

	fmt.Printf("Sessions to prune (%s):\n", scope)
	for _, c := range candidates {
		fmt.Printf("  %s  %s\n", c.name, cli.StyleDim.Render(fmt.Sprintf("(%s · %s)", c.status, c.originLabel())))
	}
	fmt.Println()

	if dryRun {
		cli.PrintHint(fmt.Sprintf("Dry run: would remove %d session(s).", len(candidates)))
		return nil
	}

	if !force {
		if !cli.ConfirmYesNo(fmt.Sprintf("Remove %d local session(s)?", len(candidates)), false) {
			fmt.Println("Canceled.")
			return nil
		}
	}

	removed := deletePruneCandidates(candidates)

	cli.PrintSuccess(fmt.Sprintf("Pruned %d session(s)", removed))
	if skippedRecording > 0 {
		cli.PrintHint(fmt.Sprintf("Skipped %d active recording(s). Stop them with 'ox session stop' to prune.", skippedRecording))
	}
	return prunePartialFailureError(removed, len(candidates))
}

// deletePruneCandidates deletes every physical location of each candidate,
// printing a warning per failed location but still attempting the rest — a
// failure on one copy (e.g. a concurrent prune, or a race with another
// process) must not stop the others from being cleaned up. Returns how many
// candidates had ALL of their locations deleted successfully.
func deletePruneCandidates(candidates []pruneCandidate) int {
	var removed int
	for _, c := range candidates {
		ok := true
		for _, loc := range c.locations {
			if err := loc.store.Delete(c.name); err != nil {
				fmt.Printf("  Warning: failed to remove %s (%s): %v\n", c.name, loc.origin, err)
				ok = false
			}
		}
		if ok {
			removed++
		}
	}
	return removed
}

// prunePartialFailureError reports a non-nil error when some sessions failed
// to delete, so scripts and CI relying on exit code don't see a false success.
func prunePartialFailureError(removed, total int) error {
	if removed >= total {
		return nil
	}
	return fmt.Errorf("pruned %d of %d session(s); check the warnings above for what's left", removed, total)
}

// pruneCandidate is a logical session selected for deletion. It may have more
// than one physical location — e.g. a not-yet-uploaded session already staged
// into the ledger cache alongside its per-user copy — all of which get deleted.
type pruneCandidate struct {
	name      string
	status    session.SessionStatus
	locations []prunableStore
}

// originLabel renders where this candidate's copies live, e.g. "local" or
// "local+ledger-cache" when the session exists in both stores.
func (c pruneCandidate) originLabel() string {
	labels := make([]string, len(c.locations))
	for i, loc := range c.locations {
		labels[i] = loc.origin
	}
	return strings.Join(labels, "+")
}

// prunableStore pairs a store with a human-readable origin label.
type prunableStore struct {
	store  *session.Store
	origin string
}

// buildPruneStores returns the set of local stores prune should scan.
// Stores that fail to open are skipped with a debug log.
func buildPruneStores(localStore *session.Store, ledgerPath string) []prunableStore {
	out := []prunableStore{{store: localStore, origin: "local"}}

	// NewStore treats its argument as a repo-context root and appends "sessions"
	// itself, so passing ledgerPath/.sageox/cache here (not ledgerPath) is
	// deliberate — the result lands on ledgerPath/.sageox/cache/sessions. Do not
	// "fix" this to pass ledgerPath directly: that aliases loadUploadedKeys'
	// ledgerStore (which scans ledgerPath/sessions), making every cache-store
	// session resolve as already-uploaded and silently no-op the entire
	// ledger-cache half of prune.
	cachePath := filepath.Join(ledgerPath, ".sageox", "cache")
	cacheStore, err := session.NewStore(cachePath)
	if err != nil {
		slog.Debug("prune_open_cache_store", "path", cachePath, "err", err)
		return out
	}
	out = append(out, prunableStore{store: cacheStore, origin: "ledger-cache"})
	return out
}

// collectPruneCandidates iterates every local store, classifies each session,
// and returns the ones eligible for deletion. Sessions appearing in multiple
// stores under the same merge key are merged into a single logical candidate
// (classification from the first store wins — local is scanned first) that
// carries every physical location, so all copies get deleted together.
// Active recordings (StatusRecording) are always excluded; the count is
// returned separately so the caller can report it.
func collectPruneCandidates(stores []prunableStore, uploaded map[string]bool, pruneAll bool) ([]pruneCandidate, int, error) {
	const indexRejected = -1
	index := make(map[string]int) // merge key -> candidates[] index, or indexRejected
	var (
		candidates       []pruneCandidate
		skippedRecording int
	)

	for _, ps := range stores {
		sessions, err := ps.store.ListAllSessions()
		if err != nil {
			return nil, 0, fmt.Errorf("list sessions in %s: %w", ps.origin, err)
		}
		for _, s := range sessions {
			key := sessionMergeKey(s)

			if idx, ok := index[key]; ok {
				if idx != indexRejected {
					// same logical session, another physical copy — prune it too.
					candidates[idx].locations = append(candidates[idx].locations, prunableStore{store: ps.store, origin: ps.origin})
				}
				continue
			}

			isUploaded := uploaded[key]
			status := session.ClassifySession(s, isUploaded)

			if status == session.StatusRecording {
				skippedRecording++
				index[key] = indexRejected
				continue
			}

			if !shouldPrune(status, pruneAll) {
				index[key] = indexRejected
				continue
			}

			name := s.SessionName
			if name == "" {
				name = s.Filename
			}
			index[key] = len(candidates)
			candidates = append(candidates, pruneCandidate{
				name:      name,
				status:    status,
				locations: []prunableStore{{store: ps.store, origin: ps.origin}},
			})
		}
	}

	return candidates, skippedRecording, nil
}

// shouldPrune reports whether a session in the given status is a prune target.
func shouldPrune(status session.SessionStatus, pruneAll bool) bool {
	if status == session.StatusUploaded {
		return false
	}
	if !pruneAll {
		return status == session.StatusLocal
	}
	switch status {
	case session.StatusLocal,
		session.StatusPaused,
		session.StatusCanceled,
		session.StatusGhost,
		session.StatusOrphan:
		return true
	}
	return false
}

// loadUploadedKeys returns the set of merge keys for sessions present in the
// ledger. Caller must have a valid ledgerPath — prune refuses to run when the
// ledger is unavailable, since deleting "local-only" sessions without it could
// silently drop work that simply hasn't been pulled.
func loadUploadedKeys(ledgerPath string) (map[string]bool, error) {
	keys := make(map[string]bool)

	ledgerStore, err := session.NewStore(ledgerPath)
	if err != nil {
		return nil, fmt.Errorf("open ledger store: %w", err)
	}

	ledgerSessions, err := ledgerStore.ListAllSessions()
	if err != nil {
		return nil, fmt.Errorf("list ledger sessions: %w", err)
	}

	for _, ls := range ledgerSessions {
		// A draft placeholder is not an upload. Counting it here would make
		// shouldPrune refuse forever ("uploaded" is never a prune target), so
		// the local cache copy of every drafted session would become
		// permanently unprunable even after the session is aborted.
		if ls.Draft {
			continue
		}
		keys[sessionMergeKey(ls)] = true
	}
	return keys, nil
}
