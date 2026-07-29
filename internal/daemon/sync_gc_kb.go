package daemon

// Knowledge-bubble GC. Runs on the existing GCCheckInterval (1h) tick
// alongside the team-context / ledger blue-green GC. Goal: reclaim disk
// occupied by bubbles the caller no longer has access to (revoked,
// deleted, or removed from the API list) without ever destructively
// removing data the daemon hasn't fully reconciled.
//
// Two phases run on each tick:
//
//	Phase 1 (triage): list local kb/<kb_id>/ dirs, diff against the
//	                  current API list, move orphans to kb/.trash/<kb_id>-<ts>/.
//	Phase 2 (reaper): scan kb/.trash/, remove entries older than 7 days.
//
// Critical safety properties:
//
//   - When the kb API is unavailable (ErrKBAPIUnavailable, timeout,
//     network failure, 5xx, etc.), the entire pass is skipped. The
//     source of truth is unreachable and we refuse to delete on guesses.
//   - Triage never `rm -rf`s a kb dir — orphans always go through .trash
//     first with a 7-day grace period.
//   - The reaper only deletes when it can parse a valid timestamp from
//     the trash dir name. Bad names are logged and left alone (forever
//     if necessary — fail-safe over fail-clean).
//   - Symlinks under <project>/.sageox/kb/<slug> are NOT touched here.
//     D2's symlink reconciliation handles those separately on its next
//     pass; whatever we move-aside here will surface as a broken link
//     and get cleaned up.
//
// Does not depend on git or git-lfs subprocesses (per
// .claude/rules/lfs-no-git-lfs-binary.md). Pure os.Rename / os.RemoveAll.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/paths"
)

// kbTrashDirName is the marker subdirectory under paths.KBDir("") that
// holds soft-deleted bubbles awaiting their grace period.
const kbTrashDirName = ".trash"

// kbTrashGracePeriod is how long a bubble lingers in .trash/ before the
// reaper removes it. Long enough that an accidentally-revoked bubble can
// be restored without re-cloning; short enough that disk reclaim is
// predictable.
const kbTrashGracePeriod = 7 * 24 * time.Hour

// kbAPIListFn returns the canonical list of kb_ids the caller still has
// access to according to the API. Returning api.ErrKBAPIUnavailable (or
// any error wrapping it) signals "skip GC this pass" — the source of
// truth is unreachable and we MUST NOT delete on guesses. Any other
// non-nil error is treated the same way (skip the pass, log a warning):
// "when in doubt, don't delete" is the only safe posture for GC.
type kbAPIListFn func(ctx context.Context) ([]string, error)

// runKBGC is the entry point for the knowledge-bubble GC pass. Composed
// of independent triage + reaper phases so a failure in one does not
// prevent the other from running. Returns nil even on non-fatal errors;
// failures are logged but never propagate up to the caller, mirroring
// the "GC must never impair UX" posture of the existing blue-green GC.
//
// The function is small and direct on purpose: this is a periodic
// disk-hygiene pass, not a transactional pipeline.
func (s *SyncScheduler) runKBGC(ctx context.Context, listFn kbAPIListFn) {
	// resolve the kb root once; this is the parent of every per-bubble
	// canonical dir for the active endpoint. paths.KBDir("") returns the
	// base directory by convention — see paths.go.
	kbRoot, err := s.kbGCRoot()
	if err != nil {
		s.logger.Debug("kb_gc skipped: cannot resolve kb root", "error", err)
		return
	}

	// nothing on disk yet → nothing to GC. Common on a fresh install.
	if _, err := os.Stat(kbRoot); os.IsNotExist(err) {
		return
	} else if err != nil {
		s.logger.Warn("kb_gc stat root failed", "path", kbRoot, "error", err)
		return
	}

	trashDir := filepath.Join(kbRoot, kbTrashDirName)

	// phase 1: triage — only runs if we can fetch the API list. Reaper
	// runs unconditionally below so .trash/ doesn't leak forever even
	// during prolonged API outages.
	s.kbGCTriage(ctx, kbRoot, trashDir, listFn)

	// phase 2: reaper — independent of phase 1 by design. A panic or
	// failure in triage must NOT prevent .trash/ from draining.
	s.kbGCReap(trashDir)
}

// kbGCRoot returns the canonical kb root directory for the active
// endpoint, i.e. the parent of paths.KBDir(<kb_id>). Returns an error
// when the endpoint cannot be resolved (e.g., not yet logged in) so
// the caller can skip rather than panic inside paths.KBDir.
func (s *SyncScheduler) kbGCRoot() (root string, err error) {
	// paths.KBDir("") panics when the endpoint is empty. Recover so
	// the GC tick survives an unconfigured environment rather than
	// taking down the scheduler goroutine.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("kb root resolution panicked: %v", r)
			root = ""
		}
	}()
	root = paths.KBDir(s.kbEndpoint(), "")
	if root == "" {
		return "", errors.New("kb root unresolved")
	}
	return root, nil
}

// kbGCTriage is phase 1: list <kbRoot>/<kb_id>/ dirs, fetch the API
// list, and move-aside any kb_id that is no longer in the API list.
// Skips entirely on any API failure — we never delete (or even
// soft-delete) without a confirmed source of truth.
func (s *SyncScheduler) kbGCTriage(ctx context.Context, kbRoot, trashDir string, listFn kbAPIListFn) {
	if listFn == nil {
		s.logger.Debug("kb_gc triage skipped: no list function")
		return
	}

	// bound the API call so a slow endpoint can't wedge the GC tick.
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	wantIDs, err := listFn(listCtx)
	if err != nil {
		// "when in doubt, don't delete" — every API failure is treated
		// the same way regardless of cause. ErrKBAPIUnavailable is the
		// expected, quiet case (flag off / endpoint missing); other
		// errors get a warn so prolonged outages are visible.
		if errors.Is(err, api.ErrKBAPIUnavailable) {
			s.logger.Debug("kb_gc skipped: kb API unavailable", "error", err)
		} else {
			s.logger.Warn("kb_gc skipped: kb API list failed", "error", err)
		}
		return
	}

	want := make(map[string]struct{}, len(wantIDs))
	for _, id := range wantIDs {
		if id != "" {
			want[id] = struct{}{}
		}
	}

	entries, err := os.ReadDir(kbRoot)
	if err != nil {
		s.logger.Warn("kb_gc triage readdir failed", "path", kbRoot, "error", err)
		return
	}

	moved := 0
	for _, ent := range entries {
		name := ent.Name()
		// skip the trash dir itself (and any dotfile sibling — paths.go
		// only writes <kb_id> dirs at this level, but be defensive).
		if !ent.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if _, ok := want[name]; ok {
			continue // still authorized — leave alone
		}
		// Clone-in-flight guard (in-process, cheap): if cloneBubble is
		// mid-flight for this kb_id in this daemon, the target dir may
		// LOOK like an orphan (not yet in the API list, freshly
		// mkdir'd, no .git yet) but renaming it to .trash/ would race
		// the active git clone and leave a silent half-cloned bubble.
		if kbCloneActive(name) {
			s.logger.Debug("kb_gc triage skip: clone in flight (same process)", "kb_id", name)
			continue
		}

		// Cross-process guard: the in-process marker above only sees
		// clones started by THIS daemon. With N per-repo daemons all
		// reconciling the shared paths.KBDir(kb_id) tree, another
		// daemon may be mid-clone or mid-pull on this kb_id right now
		// — os.Rename into .trash/ would yank the inode out from under
		// its git process. We use the same kb flock as reconcileBubble;
		// EWOULDBLOCK means "someone is working on it, leave it alone
		// this pass". See bead ox-kdt2.
		unlock, acquired, lockErr := AcquireKBLock(name)
		if lockErr != nil {
			s.logger.Warn("kb_gc triage lock acquire failed", "kb_id", name, "error", lockErr)
			continue
		}
		if !acquired {
			s.logger.Debug("kb_gc triage skip: another process holds kb lock", "kb_id", name)
			continue
		}

		// orphan — move into .trash/<kb_id>-<RFC3339>
		if err := os.MkdirAll(trashDir, 0o755); err != nil {
			unlock()
			s.logger.Warn("kb_gc triage mkdir trash failed", "path", trashDir, "error", err)
			return
		}
		ts := time.Now().UTC().Format(time.RFC3339)
		// RFC3339 contains ':' which is valid on POSIX; on Windows we'd
		// need a replacement, but the daemon's GC path is POSIX-first.
		dest := filepath.Join(trashDir, fmt.Sprintf("%s-%s", name, ts))
		src := filepath.Join(kbRoot, name)
		renameErr := os.Rename(src, dest)
		// release the kb lock immediately after the rename completes
		// (success or failure). The reaper phase does not need it —
		// once the path is under .trash/<kb_id>-<ts> there is no
		// canonical-path collision to guard against.
		unlock()
		if renameErr != nil {
			s.logger.Warn("kb_gc triage rename failed", "kb_id", name, "src", src, "dest", dest, "error", renameErr)
			continue
		}
		moved++
		s.logger.Info("kb_gc triaged orphan bubble", "kb_id", name, "trash", dest)
	}

	if moved > 0 {
		s.logger.Info("kb_gc triage complete", "moved", moved, "kept", len(entries)-moved)
	}
}

// kbGCReap is phase 2: walk <trashDir>/, parse each entry's timestamp
// suffix, and remove entries older than the grace period. Bad names
// are logged but never deleted — losing a bubble to a parser bug is
// strictly worse than leaving it on disk.
func (s *SyncScheduler) kbGCReap(trashDir string) {
	entries, err := os.ReadDir(trashDir)
	if err != nil {
		// trash dir absent is the common case — first GC pass after
		// install, no orphans yet. Anything else gets a warn.
		if !os.IsNotExist(err) {
			s.logger.Warn("kb_gc reap readdir failed", "path", trashDir, "error", err)
		}
		return
	}

	removed := 0
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		name := ent.Name()
		ts, ok := parseKBTrashTimestamp(name)
		if !ok {
			s.logger.Warn("kb_gc reap bad trash name, leaving alone", "name", name)
			continue
		}
		if time.Since(ts) <= kbTrashGracePeriod {
			continue // still in grace
		}
		victim := filepath.Join(trashDir, name)
		if err := os.RemoveAll(victim); err != nil {
			s.logger.Warn("kb_gc reap remove failed", "path", victim, "error", err)
			continue
		}
		removed++
		s.logger.Info("kb_gc reaped expired trash entry", "path", victim, "age", time.Since(ts).Round(time.Hour))
	}

	if removed > 0 {
		s.logger.Info("kb_gc reap complete", "removed", removed)
	}
}

// parseKBTrashTimestamp pulls the trailing RFC3339 timestamp out of a
// trash entry name of the form "<kb_id>-<RFC3339>". RFC3339 contains
// internal '-' characters (date separators) so we split on the LAST
// boundary that begins a valid timestamp rather than the first '-'.
//
// Strategy: try to parse progressively longer suffixes from the right.
// In practice the timestamp is always 20–25 chars; we cap the search
// length so a malicious 1MB filename doesn't burn CPU.
func parseKBTrashTimestamp(name string) (time.Time, bool) {
	const maxScan = 40
	if len(name) < len("YYYY-MM-DDTHH:MM:SSZ") {
		return time.Time{}, false
	}
	scanFrom := len(name) - maxScan
	if scanFrom < 0 {
		scanFrom = 0
	}
	// look for a '-' that is followed by a digit (start of YYYY) and
	// try to parse from there.
	for i := scanFrom; i < len(name)-1; i++ {
		if name[i] != '-' {
			continue
		}
		if name[i+1] < '0' || name[i+1] > '9' {
			continue
		}
		candidate := name[i+1:]
		if t, err := time.Parse(time.RFC3339, candidate); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// buildKBGCListFn returns a kbAPIListFn backed by the production kb
// lister wired through buildKBLister(), fanning out over the project's
// ambient scopes (same derivation as syncBubbles). On nil lister (no
// project root, not logged in, etc.) or no ambient scopes, the returned
// fn reports ErrKBAPIUnavailable so the GC pass skips cleanly.
//
// Strictness: unlike syncBubbles (which unions best-effort — it only
// clones/pulls), GC deletes on the strength of this list. A scope that
// fails to list would make its bubbles look orphaned, so ANY per-scope
// error aborts the whole list ("when in doubt, don't delete").
func (s *SyncScheduler) buildKBGCListFn() kbAPIListFn {
	return func(ctx context.Context) ([]string, error) {
		lister := s.buildKBLister()
		if lister == nil {
			return nil, api.ErrKBAPIUnavailable
		}
		scopes := kb.AmbientScopes(s.projectTeamIDForKB())
		if len(scopes) == 0 {
			return nil, api.ErrKBAPIUnavailable
		}
		var ids []string
		seen := make(map[string]bool)
		for _, scope := range scopes {
			bubbles, err := lister.ListBubbles(ctx, scope)
			if err != nil {
				return nil, err
			}
			for _, b := range bubbles {
				if b.KBID != "" && !seen[b.KBID] {
					seen[b.KBID] = true
					ids = append(ids, b.KBID)
				}
			}
		}
		return ids, nil
	}
}
