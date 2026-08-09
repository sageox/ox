package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
)

// Draft session placeholders. See docs/adr/ADR-029-session-draft-placeholder.md.
//
// A draft is a meta.json-only directory committed to <ledger>/sessions/<name>/
// partway through a recording, so https://<endpoint>/c/<session_id> resolves
// for links already circulating in PR bodies and commit trailers. It carries no
// turn data and is superseded wholesale at session stop.
//
// The "meta.json and nothing else" shape is forced, not chosen. Real transcript
// bytes on the git-tracked raw.jsonl break LFS linkage — the ledger then rejects
// every subsequent push for the whole team with "LFS objects are missing" — and
// defeat the daemon's pointer-stub skip, so anti-entropy starts re-finalizing
// live sessions. See .claude/rules/cache-only-design.md.

// draftAction is what the turn counter says to do at a given turn.
type draftAction int

const (
	draftActionNone draftAction = iota
	draftActionPublish
	draftActionRefresh
)

// draftDecision is the pure scheduling rule for draft publish/refresh.
//
// Extracted from the hook deliberately. The scheduling arithmetic is where the
// off-by-one bugs live (publish twice, refresh on the publish turn, refresh
// anchored to absolute turn instead of to the publish turn), and none of them
// need git, a ledger, or a daemon to exercise. Keeping this pure is what makes
// the property test a fast in-memory loop instead of a real-git soak nobody
// runs.
//
// turnCount is the turn that just completed (1-based). publishedTurn is
// RecordingState.DraftPublishedTurn (0 = never attempted) and published reports
// whether any attempt has actually SUCCEEDED (DraftPublishedAt != nil).
//
// The two are distinct on purpose, and conflating them was a real bug:
//
//   - Before the first success, retry every turn. A transient failure — an
//     index.lock collision, which is routine when several agents share one
//     ledger clone — otherwise backs the session off by a full refresh
//     interval, so any session shorter than publishTurn+refreshEvery silently
//     never publishes at all. That is a large fraction of sessions, and it is
//     the one publish that matters.
//   - After the first success, back off to the refresh cadence. There the
//     stamp-on-attempt behavior is exactly right: a persistently broken ledger
//     retries once per interval instead of hammering git every turn.
//
// The refresh cadence is measured from publishedTurn, not from the absolute
// turn number, so a session whose first publish was delayed still refreshes a
// full interval later rather than almost immediately.
func draftDecision(turnCount, publishedTurn int, published bool, resolved *config.ResolvedSessionDraft) draftAction {
	if resolved == nil || !resolved.Enabled {
		return draftActionNone
	}
	if turnCount < resolved.PublishTurn {
		return draftActionNone
	}
	if !published {
		return draftActionPublish
	}
	// A refresh interval of 0 disables refreshing outright; guard before the
	// modulo so a misconfigured value cannot divide by zero.
	if resolved.RefreshEvery <= 0 {
		return draftActionNone
	}
	elapsed := turnCount - publishedTurn
	if elapsed > 0 && elapsed%resolved.RefreshEvery == 0 {
		return draftActionRefresh
	}
	return draftActionNone
}

// errDraftUnsafeLedger is returned when the ledger clone is mid-rebase or
// otherwise not safe for an index mutation. Always non-fatal to the caller:
// drafts are best-effort and the next turn retries.
var errDraftUnsafeLedger = errors.New("ledger is not in a safe state for a draft write")

// validateDraftSessionName rejects names that would widen a pathspec beyond one
// session directory.
//
// This is not theoretical hardening. draftSessionRelDir("") is "sessions", and
// filepath.Base("") is "." which produces the same thing — so an empty or
// traversal-shaped name turns purgeDraftSessionDir and deleteDraftFromLedger
// into `git rm -r --force -- sessions` plus os.RemoveAll of the entire sessions
// tree. Today that is unreachable only because those paths first require a
// draft meta.json to exist at that location, which is an incidental guard, not
// an intentional one. Make it intentional.
func validateDraftSessionName(sessionName string) error {
	if sessionName == "" || sessionName == "." || sessionName == ".." {
		return fmt.Errorf("invalid session name %q", sessionName)
	}
	if strings.ContainsAny(sessionName, `/\`) || strings.Contains(sessionName, "..") {
		return fmt.Errorf("session name %q must be a single path component", sessionName)
	}
	return nil
}

// assertLedgerSafeForDraftWrite refuses to touch the ledger index when the
// clone is mid-rebase.
//
// THIS IS THE LOAD-BEARING GUARD OF THE WHOLE FEATURE, and its absence was a
// team-wide data-destruction bug. Every other ledger index writer in the CLI is
// followed by pushLedger → gitutil.PushWithRetry → IsSafeForGitOps, so the
// rebase check rides along for free. Draft writes deliberately do NOT push
// (that is the point — no push latency on a turn boundary), so they fall
// straight through that guard.
//
// What happens without it: the daemon's `git pull --rebase` conflicts on
// sessions/<name>/meta.json — the documented steady state for this repo. The
// rebase stops mid-replay of some local commit, leaving conflict markers in the
// worktree. A Stop hook fires, WriteDraftSessionMeta atomically overwrites the
// conflicted file (markers gone), `git add` MARKS THAT CONFLICT RESOLVED, and
// `git commit -- <pathspec>` SUCCEEDS on the detached rebase HEAD — consuming
// the replay step. The next `rebase --continue` reports success, and the commit
// being replayed is silently gone. It can be any commit: a teammate's session
// finalize, a murmur, a plan, imported team data.
//
// The guard belongs at index-mutation time, not at push time. That layering
// mistake is what let this through.
func assertLedgerSafeForDraftWrite(ledgerPath string) error {
	if gitutil.IsRebaseInProgress(ledgerPath) {
		return fmt.Errorf("%w: rebase in progress", errDraftUnsafeLedger)
	}
	if err := gitutil.IsSafeForGitOps(ledgerPath); err != nil {
		return fmt.Errorf("%w: %w", errDraftUnsafeLedger, err)
	}
	return nil
}

// draftSessionRelDir returns the ledger-relative directory for a session.
func draftSessionRelDir(sessionName string) string {
	return filepath.ToSlash(filepath.Join("sessions", sessionName))
}

// draftLedgerSessionDir returns the absolute git-tracked session directory.
func draftLedgerSessionDir(ledgerPath, sessionName string) string {
	return filepath.Join(ledgerPath, "sessions", sessionName)
}

// hasStagedChangesFor reports whether anything is staged for the given
// ledger-relative pathspecs.
//
// Used to decide "is there anything to commit" by EXIT CODE rather than by
// grepping git's stdout. Matching on output text is wrong in two ways that both
// bit us: git has at least two distinct empty-commit messages ("nothing to
// commit" and "nothing added to commit but untracked files present ..."), and
// neither is stable across locales — nothing in the CLI pins LC_ALL for these
// invocations. Missing the second phrasing turned every idempotent draft
// refresh into a hard error, which in turn made the recording report its draft
// as permanently failed and retry on every interval.
//
// `git diff --cached --quiet` exits 0 when there is no staged diff and 1 when
// there is; any other exit is a real error, and we conservatively report "yes,
// there are changes" so the caller attempts the commit and surfaces git's own
// message rather than silently skipping.
func hasStagedChangesFor(ledgerPath string, paths []string) bool {
	args := append([]string{"-C", ledgerPath, "diff", "--cached", "--quiet", "--"}, paths...)
	err := exec.Command("git", args...).Run()
	if err == nil {
		return false // clean: nothing staged for these paths
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true // the documented "differences found" code
	}
	return true // unknown failure — let the commit run and report properly
}

// draftStagePaths returns the ONLY ledger-relative paths a draft commit may
// ever stage.
//
// Deliberately hard-coded, and deliberately NOT sessionArtifactsToStage. That
// helper falls back to globbing *.jsonl / *.html / *.md whenever meta.Files is
// empty — which is a draft's normal, correct state — and would sweep in any
// real raw.jsonl or server-authored summary.md sitting in the directory. That
// is precisely the 2026-04-25 LFS-linkage break, and a draft directory is the
// one place in the ledger where a stray content file is plausible.
func draftStagePaths(sessionName string) []string {
	return []string{
		filepath.ToSlash(filepath.Join("sessions", sessionName, "meta.json")),
		filepath.ToSlash(filepath.Join("sessions", ".gitignore")),
	}
}

// commitDraftLocally stages and commits the draft placeholder in the local
// ledger clone. It deliberately does NOT push.
//
// pushLedger is a pre-push secret scan plus a credential refresh plus an LFS
// reconcile plus a three-attempt pull-rebase loop — seconds of latency, which
// is unacceptable on a turn boundary. The commit is durable locally and the
// next pushLedger from any path carries it: session stop, plan commit, the
// daemon's push cycle, or `ox doctor --fix` on an ahead branch.
//
// The trailing `-- <pathspec>` on the commit is load-bearing, not style. A bare
// `git commit` writes the WHOLE index, so a file another session left staged
// after a failed finalize would ride along under this draft's message. Drafts
// fire every N turns from every agent sharing the ledger clone, so the
// co-staging window that is theoretical for session-stop is routine here.
func commitDraftLocally(ledgerPath, sessionName string) error {
	if err := prepareDraftLedgerWrite(ledgerPath, sessionName); err != nil {
		return err
	}
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	if err := lfs.EnsureSessionsGitignore(filepath.Join(ledgerPath, "sessions")); err != nil {
		slog.Debug("draft: ensure sessions gitignore", "error", err)
	}

	paths := draftStagePaths(sessionName)

	// --sparse: ledger repos use cone-mode sparse-checkout, and git 2.37+
	// refuses to stage paths outside the sparse definition without it.
	addArgs := append([]string{"-C", ledgerPath, "add", "--sparse", "--"}, paths...)
	if out, err := exec.Command("git", addArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("git add draft: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Idempotent: a refresh whose counters did not change stages nothing, and
	// must not be an error — the caller treats an error as "publish failed"
	// and would report the draft as permanently broken.
	if !hasStagedChangesFor(ledgerPath, paths) {
		return nil
	}

	// `git commit -- <pathspec>` commits the WORKING TREE at those paths, NOT
	// the index we just built. That is a genuine footgun, not pedantry: a
	// concurrent finalize rewrites this exact meta.json, so without this check
	// the finalize's bytes — LFS OIDs and all — get committed under a
	// "session-draft:" subject. The daemon's push filter matches on that
	// subject and would then push a finalize-shaped commit with neither the
	// LFS reconcile nor the pre-push secret gate that the CLI's own push path
	// applies.
	//
	// Verify the worktree still matches what we staged, and that it is still a
	// draft, immediately before committing.
	if err := assertDraftStillStaged(ledgerPath, sessionName, paths); err != nil {
		return err
	}

	commitArgs := append([]string{
		"-C", ledgerPath, "commit", "--no-verify",
		"-m", fmt.Sprintf("session-draft: %s", sessionName),
		"--",
	}, paths...)
	if out, err := exec.Command("git", commitArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("git commit draft: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// assertDraftStillStaged verifies, immediately before a partial commit, that
// the worktree content at paths is identical to what was staged and that the
// session directory is still a draft.
//
// Closes the window described on commitDraftLocally: `git commit -- <pathspec>`
// reads the worktree, so anything that rewrote these paths between `git add`
// and `git commit` gets committed under our message instead of ours.
func assertDraftStillStaged(ledgerPath, sessionName string, paths []string) error {
	args := append([]string{"-C", ledgerPath, "diff", "--quiet", "--"}, paths...)
	if err := exec.Command("git", args...).Run(); err != nil {
		return fmt.Errorf("draft %s: worktree changed between stage and commit; another writer owns this path", sessionName)
	}
	meta, err := lfs.ReadSessionMeta(draftLedgerSessionDir(ledgerPath, sessionName))
	if err != nil {
		return fmt.Errorf("draft %s: meta unreadable before commit: %w", sessionName, err)
	}
	if !meta.IsDraft() {
		return fmt.Errorf("draft %s: no longer a draft (a finalize landed); refusing to commit it under a draft subject", sessionName)
	}
	return nil
}

// prepareDraftLedgerWrite is the single gate every draft ledger-index mutation
// must pass. It validates the target, waits out a blue-green GC swap, and
// refuses when the clone is mid-rebase.
//
// One gate rather than four copies because the reviewers found each guard
// missing from a different subset of the four write paths — which is what
// happens when a safety check is a convention rather than a chokepoint.
func prepareDraftLedgerWrite(ledgerPath, sessionName string) error {
	if err := validateDraftSessionName(sessionName); err != nil {
		return err
	}
	if err := validateDraftLedgerPath(ledgerPath); err != nil {
		return err
	}
	// The daemon's blue-green GC can rename-swap the clone out from under us.
	waitForGCSwap(ledgerPath)
	return assertLedgerSafeForDraftWrite(ledgerPath)
}

// validateDraftLedgerPath refuses to run git writes against anything that is
// not a git repository.
//
// This is the structural half of the guard. The IDENTITY half — "is this
// actually THIS project's ledger?" — lives in the publisher
// (resolveDraftLedgerPath), because only the caller knows the project root and
// answering it here would make every draft git helper depend on the process
// CWD.
func validateDraftLedgerPath(ledgerPath string) error {
	if ledgerPath == "" {
		return fmt.Errorf("empty ledger path")
	}
	if _, err := os.Stat(filepath.Join(ledgerPath, ".git")); err != nil {
		return fmt.Errorf("draft target %s is not a git repository", ledgerPath)
	}
	return nil
}

// purgeDraftSessionDir removes a draft placeholder from the git index and the
// working tree, and COMMITS that removal locally.
//
// Wholesale, not selective, and that is the entire point. While meta.draft is
// true, EVERY file in that directory is provisional. The SageOx server may
// summarize the zero-turn draft, write summary.json / summary.md and push them;
// the `git pull --rebase` inside a finalize-time pushLedger folds those into our
// working tree, and preserveComputedFields would then carry their
// files_changed / chapters into the real summary as if an LLM had read the
// transcript. A whitelist of "files we know the server might write" rots the
// first time the server writes something new; deleting the directory cannot.
//
// # Why this commits instead of leaving the deletion staged
//
// Leaving it staged for the caller's own commit is one commit cheaper and
// wrong. The finalize that follows can fail at several points the pipeline is
// explicitly built to tolerate — LFS upload, meta write, read-only endpoint —
// and every one of those leaves a staged deletion in the shared ledger index
// with nothing to anchor it. The next unrelated bare `git commit` (another
// agent's session stop) then sweeps that deletion in under the wrong message.
// Committing here makes the purge durable and idempotent instead: a crash
// mid-finalize converges, because the draft is already gone and the retry sees
// a clean directory.
//
// The directory is deliberately NOT recreated. Every caller creates it (
// pipeline.CopySessionToLedger, the doctor retry, the daemon's staging), and an
// empty leftover directory is actively harmful: listSessionSessions has no
// meta.json filter, so an empty <ledger>/sessions/<name>/ renders as a
// non-draft row, lands in uploadedSessions, and displays as "✓ uploaded" for a
// session that was never uploaded — which also makes its local copy
// permanently unprunable.
//
// --ignore-unmatch so a draft that was written but never committed (crash
// between write and commit) does not fail the purge; the working-tree removal
// still runs.
func purgeDraftSessionDir(ledgerPath, sessionName string) error {
	if err := prepareDraftLedgerWrite(ledgerPath, sessionName); err != nil {
		return err
	}
	relDir := draftSessionRelDir(sessionName)

	gitRm := exec.Command("git", "-C", ledgerPath, "rm", "-r", "--force", "--ignore-unmatch", "--", relDir)
	if out, err := gitRm.CombinedOutput(); err != nil {
		return fmt.Errorf("git rm draft %s: %s: %w", sessionName, strings.TrimSpace(string(out)), err)
	}

	// git rm --ignore-unmatch leaves untracked files behind. Remove whatever
	// remains so a server-authored artifact that was never committed here
	// cannot survive into the finalized session either.
	if err := os.RemoveAll(filepath.Join(ledgerPath, "sessions", sessionName)); err != nil {
		return fmt.Errorf("remove draft dir %s: %w", sessionName, err)
	}

	return commitDraftRemoval(ledgerPath, sessionName, "supersede")
}

// commitDraftRemoval commits a staged draft deletion, scoped to that session's
// path. verb distinguishes the reason in history: "supersede" (a real recording
// is replacing it), "retract" (the session ended with no entries), "discard"
// (the user aborted).
//
// A no-op when nothing is staged, so every caller is safe to invoke blindly.
func commitDraftRemoval(ledgerPath, sessionName, verb string) error {
	if err := validateDraftSessionName(sessionName); err != nil {
		return err
	}
	if err := assertLedgerSafeForDraftWrite(ledgerPath); err != nil {
		return err
	}
	relDir := draftSessionRelDir(sessionName)
	if !hasStagedChangesFor(ledgerPath, []string{relDir}) {
		return nil
	}
	commitArgs := []string{
		"-C", ledgerPath, "commit", "--no-verify",
		"-m", fmt.Sprintf("session-draft: %s %s", verb, sessionName),
		"--", relDir,
	}
	if out, err := exec.Command("git", commitArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("git commit draft %s: %s: %w", verb, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// supersedeDraftForFinalize is the one entry point every finalize path uses to
// take over a session directory that may hold a draft placeholder.
//
// It returns the preserved ses_ id BEFORE purging, which is the whole reason it
// exists as a helper rather than two calls at each site. A draft's meta.json is
// an extra durable carrier of that id (it survives .recording.json being
// cleared), and the purge deletes it — so any path that purges first and reads
// second silently rotates an id that is already circulating in PR bodies and
// commit trailers. Four separate finalize paths need this exact ordering; a
// helper makes getting it wrong take effort.
//
// A purge failure is reported to the caller but is never fatal on its own: the
// finalize that follows writes authoritative content over everything its
// manifest names.
func supersedeDraftForFinalize(ledgerPath, sessionName string) (preservedID string, wasDraft bool, err error) {
	sessionDir := draftLedgerSessionDir(ledgerPath, sessionName)

	preservedID, wasDraft, err = lfs.PreservedSessionIDAndDraft(sessionDir)
	if err != nil {
		// Unreadable meta.json is fatal by contract — we cannot tell whether
		// it held a ses_ id we would rotate.
		return "", false, err
	}
	if !wasDraft {
		return preservedID, false, nil
	}
	if purgeErr := purgeDraftSessionDir(ledgerPath, sessionName); purgeErr != nil {
		slog.Warn("draft purge failed; finalizing over the draft", "session", sessionName, "error", purgeErr)
	}
	return preservedID, true, nil
}

// commitDraftRetraction commits and pushes the deletion of a draft for a
// session that will never be finalized — a stop that produced zero entries,
// where CopySessionToLedger short-circuits before writing anything.
//
// Without this the /c/ page keeps claiming "in progress" forever and the ledger
// worktree is left holding a staged deletion nobody commits, which the next
// unrelated `git commit` would then sweep up under the wrong message. Pushes
// synchronously via pushLedger: this is the stop path, the user is already
// waiting, and durability beats latency here.
func commitDraftRetraction(ledgerPath, sessionName string) error {
	// purgeDraftSessionDir already committed the removal; this only has to get
	// it to the remote. Still commits defensively in case a caller reaches here
	// with a staged-but-uncommitted deletion.
	waitForGCSwap(ledgerPath)
	if err := commitDraftRemoval(ledgerPath, sessionName, "retract"); err != nil {
		return err
	}
	return pushLedger(context.Background(), ledgerPath)
}

// draftViewNotice returns a human-readable explanation when sessionName
// resolves to a draft placeholder in the ledger, or "" when it does not.
//
// `ox session view --text/--json` needs real transcript content, which a draft
// has none of. Without this the command hard-errors "session not found" for a
// session `ox session list` is simultaneously displaying — the two commands
// disagreeing is exactly the "everything seems broken" symptom drafts exist to
// remove.
func draftViewNotice(sessionName string) string {
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return ""
	}
	sessionDir := draftLedgerSessionDir(ledgerPath, sessionName)
	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil || !meta.IsDraft() {
		return ""
	}

	msg := fmt.Sprintf("Session %q is still recording — a draft placeholder is published, but no turn data has been written yet.", sessionName)
	if cfg, cfgErr := config.LoadProjectConfig(""); cfgErr == nil {
		if url := buildConversationURL(cfg, meta.EffectiveSessionID()); url != "" {
			msg += "\nLive view: " + url
		}
	}
	msg += "\nRun 'ox session view " + sessionName + "' again after '/ox-session-stop' to read the transcript."
	return msg
}

// draftDeleteResult reports what removing a draft from the ledger accomplished.
// Deleted and PushWarning are independent: a local commit that failed to push is
// still progress, and reporting it as an outright failure would push the caller
// toward a retry that cannot succeed (the second `git rm` finds nothing to
// remove and reports zero staged, forever).
type draftDeleteResult struct {
	Deleted     bool
	PushWarning string
}

// deleteDraftFromLedger git-removes a session's draft placeholder from the
// ledger. Used by `ox agent session abort` so discarding a session also
// discards the placeholder that made it publicly visible.
//
// REFUSES when the ledger meta is not a draft. Abort resolves sessions by
// partial name, so a name collision with a teammate's finalized session pulled
// in from the remote must never turn into a deletion of their work — that is
// what `ox agent session delete` is for, with its own confirmation.
//
// A push failure is a WARNING, not an error. The local delete commit is already
// durable and the next pushLedger or `ox doctor --fix` on an ahead branch
// carries it, and notifySessionAbortedAsync independently flips the /c/ page to
// discarded. Failing the abort here would leave the user's session data deleted
// locally with no clean way to retry.
func deleteDraftFromLedger(ledgerPath, sessionName string) (draftDeleteResult, error) {
	var res draftDeleteResult

	// Validate the name BEFORE anything reads or removes a path built from it.
	// An empty or traversal-shaped name widens every pathspec below to the
	// whole sessions tree.
	if err := validateDraftSessionName(sessionName); err != nil {
		return res, err
	}

	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
			return res, nil // no ledger presence at all — nothing to do
		}
		// Unreadable meta.json: refuse rather than guess. We cannot tell
		// whether this is a draft or a finalized session, and deleting a
		// finalized one is unrecoverable.
		return res, fmt.Errorf("read ledger meta for %s (refusing to delete what we cannot classify): %w", sessionName, err)
	}
	if !meta.IsDraft() {
		return res, fmt.Errorf("%s is a finalized session in the ledger, not a draft; use 'ox agent session delete %s' to remove it", sessionName, sessionName)
	}

	if err := prepareDraftLedgerWrite(ledgerPath, sessionName); err != nil {
		return res, err
	}

	relDir := draftSessionRelDir(sessionName)
	gitRm := exec.Command("git", "-C", ledgerPath, "rm", "-r", "--force", "--ignore-unmatch", "--", relDir)
	if out, err := gitRm.CombinedOutput(); err != nil {
		return res, fmt.Errorf("git rm draft %s: %s: %w", sessionName, strings.TrimSpace(string(out)), err)
	}
	_ = os.RemoveAll(sessionDir)

	if err := commitDraftRemoval(ledgerPath, sessionName, "discard"); err != nil {
		return res, err
	}
	res.Deleted = true

	if err := pushLedger(context.Background(), ledgerPath); err != nil {
		res.PushWarning = fmt.Sprintf("draft removed locally but the ledger push failed (%v); it will be pushed by the next 'ox doctor --fix' or session upload", err)
		slog.Warn("draft discard push failed", "session", sessionName, "error", err)
	}
	return res, nil
}
