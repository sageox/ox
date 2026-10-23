package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/pkg/planreview"
)

// errBackfillCommitFailed marks a commitPlanBackfillToLedger failure at or
// before `git commit` — the ledger working tree is left with renames/edits
// (ApplyBackfillMeta and any git mv that got that far already ran) that were
// never captured in a commit. That is a materially worse, inconsistent state
// than a push-only failure, which leaves a complete, durable local commit for
// the next push to pick up — so the two must never be handled the same way.
// runPlanBackfillTitlesOnLedger uses errors.Is against this sentinel to route
// a pre-commit failure into a hard, non-zero-exit command failure instead of
// the best-effort warning a push failure gets.
var errBackfillCommitFailed = errors.New("backfill commit failed before landing locally")

// planCommitStatus reports how far a plan write got toward the shared Ledger,
// so a caller that answers a human (the review page) can say "saved locally,
// not yet shared" instead of a blanket success.
//
// Committed means the write is in a local Ledger commit — including the
// idempotent case where nothing changed since the last commit. Pushed means it
// is on the remote: for a plan dir, pushLedger returned nil; for a review
// record, the remote tip was checked to hold its exact bytes. Err carries the
// first failure (sanitized) when either is false.
type planCommitStatus struct {
	Committed bool
	Pushed    bool
	Err       string
}

// planCommitMu serializes plan commit+push inside one process. The review
// server handles POSTs concurrently, and two goroutines staging + committing
// the same clone race on .git/index.lock. gitutil.WithRepoLock already covers
// add+commit across processes, but it is not held across pushLedger (whose
// pull-rebase retry takes that same non-reentrant lock), so this mutex also
// keeps two in-process pushes from fighting over the branch tip.
var planCommitMu sync.Mutex

// planLedgerPathFor resolves the Ledger clone a plan under gitRoot commits to.
// A variable only so tests can point the review server at a temp Ledger
// without provisioning a full project context.
var planLedgerPathFor = func(gitRoot string) (string, error) {
	ctx, err := config.LoadProjectContext(gitRoot)
	if err != nil || ctx == nil {
		return "", fmt.Errorf("no project context for %q: cannot commit plan", gitRoot)
	}
	ledgerPath := ctx.DefaultLedgerPath()
	if ledgerPath == "" {
		return "", fmt.Errorf("no ledger configured for %q: cannot commit plan", gitRoot)
	}
	return ledgerPath, nil
}

// commitPlanToLedger commits and pushes session stop's change to a plan, its
// meta.json, as the lifecycle verbs commit theirs: planEventFiles and nothing
// else in the dir, so an author's unsaved plan.md stays out. While a whole-dir
// retry is pending for the plan, its latest save is not yet shared, so it
// commits nothing: that retry commits meta.json with the rest of the save, and
// the stop never publishes part of it. Saves and lifecycle events commit their
// own files and, in a Ledger checkout, leave a retry marker when they cannot.
//
// Commit AND push are synchronous. On any failure a pending marker owing
// planEventFiles is recorded so `ox agent prime` retries the push later
// through the full gate stack (see plan_push_pending.go); the error is still
// returned for callers to log.
func commitPlanToLedger(gitRoot, planDir string) error {
	ledgerPath, err := planLedgerPathFor(gitRoot)
	if err != nil {
		return err
	}
	// git run in a directory that is not a checkout finds any enclosing
	// repository, and a retry marker would create a .git dir the daemon reads
	// as a corrupt clone
	if !gitutil.IsGitRepo(ledgerPath) {
		return fmt.Errorf("ledger %s is not a git checkout", ledgerPath)
	}
	rel, err := ledgerRelPath(ledgerPath, planDir)
	if err != nil {
		return err
	}
	if m, err := readPlanPushMarker(planPushMarkerPath(ledgerPath, rel)); err == nil && len(m.Files) == 0 {
		return nil
	}
	ctx := context.Background()
	started := time.Now()
	err = commitAndPushPlanDir(ctx, ledgerPath, planDir, planEventFiles...)
	recordPlanPushOutcome(ctx, ledgerPath, planDir, started, err, planEventFiles...)
	return err
}

// commitAndPushPlanDir stages planDir, commits ONLY that path, and pushes.
// Given files (names in planDir), it stages and commits only those.
//
// Path-scoped on purpose: a bare `git commit` takes the whole index, so any
// unrelated change someone else staged in the Ledger (a half-finished session
// upload, a doctor repair) would ship under a "plan:" subject — and past
// pushLedger's per-writer assumptions. `git commit -- <path>` would scope it
// too (and does work after an `add --sparse`), but it re-reads the WORKTREE at
// commit time; gitutil.CommitLedgerSnapshot commits the exact index entries
// under the pathspec as an immutable, validated tree instead, and is the
// canonical Ledger commit path (AGENTS.md).
//
// Add+commit run under gitutil.WithRepoLock (cross-process) and planCommitMu
// (in-process, held through the push as well). The push runs outside the repo
// lock because PushWithRetry takes it itself when it must pull. A whole-dir
// commit uploads a plan.html an earlier upload left plain before taking
// either lock (see planUploads); while that page is still not stored, it
// commits the rest of the dir and returns an error, so the retry stays owed.
func commitAndPushPlanDir(ctx context.Context, ledgerPath, planDir string, files ...string) error {
	rel, err := ledgerRelPath(ledgerPath, planDir)
	if err != nil {
		return err
	}
	var up planUploads
	if len(files) == 0 && plan.HasLargePlainHTML(planDir) {
		page, err := os.ReadFile(filepath.Join(planDir, planHTMLFileName))
		if err != nil {
			return fmt.Errorf("read %s awaiting upload: %w", planHTMLFileName, err)
		}
		up = uploadPlanPages(findGitRoot(), page)
	}

	planCommitMu.Lock()
	defer planCommitMu.Unlock()
	err = gitutil.WithRepoLock(ctx, ledgerPath, func() error {
		if len(files) == 0 {
			return commitPlanDirLocked(ctx, ledgerPath, planDir, "", up.refs...)
		}
		return commitPlanPathsLocked(ctx, ledgerPath, "plan: "+filepath.Base(planDir), rel, files)
	})
	if err != nil {
		return err
	}
	// Push even when the snapshot was a no-op: an earlier commit of this plan
	// may be the one still waiting on a failed push.
	if err := pushLedger(ctx, ledgerPath); err != nil {
		return fmt.Errorf("push plan %s: %w", rel, err)
	}
	if len(files) == 0 && plan.HasLargePlainHTML(planDir) {
		return fmt.Errorf("%s is awaiting upload: %w", planHTMLFileName, up.failure())
	}
	return nil
}

// errLedgerNotSafe: the Ledger is mid-rebase, locked, or holds unresolved
// conflicts, so a plan write or commit was refused.
var errLedgerNotSafe = errors.New("the Ledger needs repair before a plan can be written")

// errEventNotCommitted: the plan event is recorded but its commit failed; its
// files are left unstaged for a retry.
var errEventNotCommitted = errors.New("plan event recorded but not committed")

// planEventFiles are the files plan.AppendPlanEvent writes in a plan dir.
var planEventFiles = []string{"events.jsonl", "meta.json"}

// planHTMLFileName mirrors internal/plan's planHTMLFile (unexported there).
const planHTMLFileName = "plan.html"

// commitPlanEvent records a lifecycle event (approve, work, realize, abandon,
// supersede) and commits exactly the files that writes, in one hold of the
// repo lock: a pull never finds them half written, and nothing else in the
// plan dir (an author's unsaved plan.md) rides along. While the Ledger needs
// repair it records nothing (errLedgerNotSafe). The files are committed even
// when the event changes nothing, since an earlier attempt's event may still
// be waiting on a failed commit; a commit that fails returns
// errEventNotCommitted. The caller holds planCommitMu and has checked that
// ledgerPath is a git checkout.
func commitPlanEvent(ctx context.Context, ledgerPath, planDir string, kind plan.EventKind, fields plan.PlanEventFields) (changed bool, err error) {
	rel, err := ledgerRelPath(ledgerPath, planDir)
	if err != nil {
		return false, err
	}
	err = gitutil.WithRepoLock(ctx, ledgerPath, func() error {
		if err := ledgerSafeForPlanWrite(ctx, ledgerPath); err != nil {
			return err
		}
		var err error
		if changed, err = plan.AppendPlanEvent(ctx, planDir, kind, fields); err != nil {
			return err
		}
		if err := commitPlanPathsLocked(ctx, ledgerPath, "plan: "+string(kind)+" "+filepath.Base(planDir), rel, planEventFiles); err != nil {
			return fmt.Errorf("%w: %w", errEventNotCommitted, err)
		}
		return nil
	})
	return changed, err
}

// commitPlanPathsLocked stages the named files of the plan dir at rel (those
// that exist) and commits only them. On failure it unstages them, so no later
// commit carries what this one refused. The caller holds the repo lock.
func commitPlanPathsLocked(ctx context.Context, ledgerPath, msg, rel string, files []string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lockedCommitTimeout)
	defer cancel()
	if err := ledgerSafeForPlanWrite(ctx, ledgerPath); err != nil {
		return err
	}
	var paths []string
	for _, f := range files {
		if _, err := os.Lstat(filepath.Join(ledgerPath, filepath.FromSlash(rel), f)); err == nil {
			paths = append(paths, rel+"/"+f)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	unstage := func() { _, _ = gitutil.RunGit(ctx, ledgerPath, append([]string{"reset", "-q", "--"}, paths...)...) }
	if out, err := gitutil.RunGit(ctx, ledgerPath, append([]string{"add", "--sparse", "--"}, paths...)...); err != nil {
		unstage()
		return fmt.Errorf("git add %s failed: %s: %w", strings.Join(paths, " "), gitutil.SanitizeOutput(out), err)
	}
	if _, err := commitPlanSnapshot(ctx, ledgerPath, msg, paths...); err != nil {
		unstage()
		return fmt.Errorf("commit %s: %w", strings.Join(paths, " "), err)
	}
	return nil
}

// ledgerSafeForPlanWrite refuses (errLedgerNotSafe) while the Ledger is
// mid-rebase, another git holds its locks, or its index holds unmerged
// entries. A plan file written mid-rebase is reverted by the rebase's abort,
// or makes the abort refuse, and `git add` of a conflicted path marks it
// resolved with its conflict markers inside, erasing the conflict the daemon
// and `ox doctor` look for. The caller holds the repo lock.
func ledgerSafeForPlanWrite(ctx context.Context, ledgerPath string) error {
	err := gitutil.IsSafeForGitOps(ledgerPath)
	if err == nil {
		var unmerged bool
		if unmerged, err = gitutil.HasUnmergedEntries(ctx, ledgerPath); err == nil && unmerged {
			err = errors.New("the index holds unresolved conflicts")
		}
	}
	if err != nil {
		return fmt.Errorf("%w: %w", errLedgerNotSafe, err)
	}
	return nil
}

// lockedCommitTimeout bounds a plan commit made under the repo lock, which
// ignores its caller's cancellation: git killed while it holds index.lock can
// leave the lock behind, and an ownerless lock blocks every commit and pull on
// the clone until it is an hour old (gitutil.AbandonedLockAge).
const lockedCommitTimeout = 2 * time.Minute

// commitPlanSnapshot is gitutil.CommitLedgerSnapshot, a variable so a test can
// fail a plan commit after its files are written and staged.
var commitPlanSnapshot = gitutil.CommitLedgerSnapshot

// errReviewConflict: the operation is already stored with other content or
// under another reviewer. Nothing was written, so the client keeps its draft.
var errReviewConflict = errors.New("this review operation is already stored with different content; nothing was saved")

// publishReviewRecord is the one write path for a review operation (a round
// or a resolution): saveReviewRecord makes it durable in the Ledger clone,
// then landReviewRecord commits exactly its record and publishes it. An error
// means nothing was saved. Otherwise the receipt names the record holding the
// operation, ReceiptAlreadyPublished when an earlier attempt stored it, and
// the status says how far that record got toward the remote.
func publishReviewRecord(ctx context.Context, gitRoot, planDir string, rec planreview.File) (planCommitStatus, planreview.Receipt, error) {
	ledgerPath, err := planLedgerPathFor(gitRoot)
	if err != nil {
		return planCommitStatus{}, planreview.Receipt{}, err
	}
	rel, err := ledgerRelPath(ledgerPath, planDir)
	if err != nil {
		return planCommitStatus{}, planreview.Receipt{}, err
	}
	e, duplicate, err := saveReviewRecord(ctx, ledgerPath, rel, planDir, rec)
	if err != nil {
		return planCommitStatus{}, planreview.Receipt{}, err
	}
	r := planreview.Receipt{State: planreview.ReceiptCreated, Path: e.Path}
	if duplicate {
		r.State = planreview.ReceiptAlreadyPublished
	}
	return landReviewRecord(ctx, ledgerPath, e), r, nil
}

// newResolutionRecord freezes a resolution the CLI originates (the reviewer's
// Accept, the agent's resolve) under a fresh operation id and returns its
// record, accepted at now.
func newResolutionRecord(anchor string, state planreview.State, commit, note string, now time.Time) (planreview.File, error) {
	id, err := randomToken()
	if err != nil {
		return planreview.File{}, err
	}
	frozen, err := json.Marshal(struct {
		ID     string           `json:"id"`
		Anchor string           `json:"anchor"`
		State  planreview.State `json:"state"`
		Commit string           `json:"commit,omitempty"`
		Note   string           `json:"note,omitempty"`
	}{id, anchor, state, commit, note})
	if err != nil {
		return planreview.File{}, err
	}
	q, err := planreview.ParseResolutionRequest(frozen)
	if err != nil {
		return planreview.File{}, err
	}
	return q.Record("", now)
}

// saveReviewRecord makes one review operation durable in the clone: its
// recovery entry first, then its record, written create-only. Only then may
// the reviewer be told it is saved: a crash between the two leaves an entry
// whose retry writes the record, never a record nothing will publish.
//
// A retry on this clone never adds a copy: its own entry for the operation
// answers first, else a stored copy found in the worktree. Either way the
// same payload from the same reviewer reuses that record (duplicate, unless
// the attempt that froze it stopped before writing it, and so before telling
// anyone it was saved), and anything else is errReviewConflict.
func saveReviewRecord(ctx context.Context, ledgerPath, rel, planDir string, rec planreview.File) (reviewPending, bool, error) {
	var op struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Data, &op); err != nil || op.ID == "" {
		return reviewPending{}, false, fmt.Errorf("review record %s carries no operation id", rec.Path)
	}
	kind := "round"
	if strings.HasPrefix(rec.Path, "feedback/resolutions/") {
		kind = "resolution"
	}
	planKey := planIDForDir(planDir)
	if planKey == "" {
		planKey = filepath.Base(planDir)
	}
	entryPath := reviewPendingPath(ledgerPath, planKey, kind, op.ID)
	entryFor := func(f planreview.File) reviewPending {
		return reviewPending{PlanDir: rel, Plan: planKey, Kind: kind, ID: op.ID, Path: f.Path, SHA256: sha256Hex(f.Data), Data: f.Data}
	}

	var e reviewPending
	duplicate := false
	err := fileutil.WithFileLock(ctx, entryPath, func() error {
		prev, err := readReviewPending(entryPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			// it may hold the only copy of a review: never write over it
			return fmt.Errorf("read this review operation's recovery entry: %w", err)
		}
		if err == nil {
			stored := planreview.File{Path: prev.Path, Data: prev.Data}
			if r, _ := planreview.Prior(planreview.Inventory{Files: []planreview.File{stored}}, rec); r.State != planreview.ReceiptAlreadyPublished {
				return errReviewConflict
			}
			e = prev
			if e.PlanDir != rel {
				// found by the plan's id after `ox plan backfill` renamed its
				// dir: the record lands where the plan is now
				e.PlanDir = rel
				if err := writeReviewPending(entryPath, e); err != nil {
					return err
				}
			}
			if _, err := os.Lstat(filepath.Join(planDir, filepath.FromSlash(prev.Path))); err == nil {
				duplicate = true
				return nil
			}
			// the attempt that froze it never wrote it; on failure the entry
			// stays, since it holds the only copy
			return gitutil.WithRepoLock(ctx, ledgerPath, func() error {
				if err := gitutil.IsSafeForGitOps(ledgerPath); err != nil {
					return fmt.Errorf("ledger not safe for restoring a review record (%s): %w", ledgerPath, err)
				}
				return restoreReviewRecord(ledgerPath, e)
			})
		}
		inv := plan.CaptureReview(planDir, "")
		if r, ok := planreview.Prior(inv, rec); ok {
			if r.State == planreview.ReceiptConflict {
				return errReviewConflict
			}
			stored, err := storedRecord(ctx, ledgerPath, rel, inv, r.Path)
			if err != nil {
				return err
			}
			duplicate = true
			e = entryFor(stored)
			return writeReviewPending(entryPath, e)
		}
		e = entryFor(rec)
		if err := writeReviewPending(entryPath, e); err != nil {
			return err
		}
		if err := publishRecord(planDir, rec); err != nil {
			// Publish can fail after the record exists (its directory sync);
			// then the entry stays so the retry lands it. With no record on
			// disk, nothing was saved and the client keeps its draft.
			if _, lerr := os.Lstat(filepath.Join(planDir, filepath.FromSlash(rec.Path))); lerr != nil {
				_ = os.Remove(entryPath)
			}
			return err
		}
		return nil
	})
	return e, duplicate, err
}

// storedRecord is the stored copy of an operation as it must be published: the
// bytes HEAD holds when the record is committed (the worktree copy may carry a
// clone's line-ending conversion or a later edit), else the worktree bytes,
// which must still hash to the digest the name pins.
func storedRecord(ctx context.Context, ledgerPath, rel string, inv planreview.Inventory, path string) (planreview.File, error) {
	if gitutil.IsGitRepo(ledgerPath) {
		if b, err := gitStdout(ctx, ledgerPath, nil, "cat-file", "blob", "HEAD:"+rel+"/"+path); err == nil {
			return planreview.File{Path: path, Data: b}, nil
		}
	}
	i := slices.IndexFunc(inv.Files, func(f planreview.File) bool { return f.Path == path })
	if i < 0 || !plan.RecordIntact(inv.Files[i]) {
		return planreview.File{}, fmt.Errorf("the stored copy of this review operation, %s, no longer matches its name", path)
	}
	return inv.Files[i], nil
}

// publishReviewFile is planreview.Publish, a variable so a test can fail it
// after the record is written.
var publishReviewFile = planreview.Publish

// publishRecord writes rec into the plan's worktree create-only.
func publishRecord(planDir string, rec planreview.File) error {
	r, err := publishReviewFile(planDir, rec)
	if err == nil && r.State == planreview.ReceiptConflict {
		err = fmt.Errorf("another file holds the name %s", rec.Path)
	}
	if err != nil {
		return fmt.Errorf("write review record: %w", err)
	}
	return nil
}

// landReviewRecord commits exactly e's record, pushes, and clears e's entry
// only once the remote tip holds the record's exact bytes; on any failure the
// entry stays for the next `ox plan flush-pending`. Pushed reports that
// verified publication, not merely a push that returned success.
func landReviewRecord(ctx context.Context, ledgerPath string, e reviewPending) planCommitStatus {
	planCommitMu.Lock()
	defer planCommitMu.Unlock()

	path := e.PlanDir + "/" + e.Path
	fail := func(st planCommitStatus, err error) planCommitStatus {
		st.Err = gitutil.SanitizeOutput(err.Error())
		slog.WarnContext(ctx, "plan review record pending: will retry on next `ox agent prime`", "record", path, "error", st.Err)
		return st
	}
	oid, err := commitReviewRecord(ctx, ledgerPath, e, "plan review: "+filepath.Base(e.PlanDir))
	if err != nil {
		return fail(planCommitStatus{}, err)
	}
	st := planCommitStatus{Committed: true}
	if err := pushLedger(ctx, ledgerPath); err != nil {
		return fail(st, fmt.Errorf("push %s: %w", path, err))
	}
	// the remote-tracking ref is the remote tip the push just updated
	if ref, err := gitutil.UpstreamTrackingRef(ctx, ledgerPath); err != nil || blobAt(ctx, ledgerPath, ref, path) != oid {
		return fail(st, fmt.Errorf("pushed, but the remote tip does not hold %s as recorded", path))
	}
	st.Pushed = true
	clearReviewPending(ctx, ledgerPath, e)
	return st
}

// commitReviewRecord commits exactly e's record and returns its blob id. The
// entry's bytes are staged as a blob and CommitLedgerSnapshot commits HEAD plus
// that one index entry, so nothing else in the index or the worktree — an
// author's half-written plan.md, a staged session — reaches the commit, and
// the worktree file is never read. A record HEAD already holds is not
// committed again, and one that fails to commit is unstaged, so no other
// commit can carry bytes this one refused.
func commitReviewRecord(ctx context.Context, ledgerPath string, e reviewPending, msg string) (string, error) {
	path := e.PlanDir + "/" + e.Path
	var oid string
	err := gitutil.WithRepoLock(ctx, ledgerPath, func() error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lockedCommitTimeout)
		defer cancel()
		// git run in a directory that is not a checkout finds any enclosing
		// repository and would stage the record there
		if !gitutil.IsGitRepo(ledgerPath) {
			return fmt.Errorf("ledger %s is not a git checkout", ledgerPath)
		}
		if err := gitutil.IsSafeForGitOps(ledgerPath); err != nil {
			return fmt.Errorf("ledger not safe for a review commit (%s): %w", ledgerPath, err)
		}
		// a commit the worktree cannot match would leave HEAD holding a file
		// where the worktree has none
		if err := restoreReviewRecord(ledgerPath, e); err != nil {
			return fmt.Errorf("restore %s: %w", path, err)
		}
		var err error
		if oid, err = gitutil.StageLedgerBlob(ctx, ledgerPath, path, e.Data); err != nil {
			return err
		}
		if _, err = gitutil.CommitLedgerSnapshot(ctx, ledgerPath, msg, path); err == nil && blobAt(ctx, ledgerPath, "HEAD", path) != oid {
			err = errors.New("HEAD does not hold the record's bytes")
		}
		if err != nil {
			_, _ = gitutil.RunGit(ctx, ledgerPath, "reset", "-q", "--", path)
			return fmt.Errorf("commit %s: %w", path, err)
		}
		return nil
	})
	return oid, err
}

// restoreReviewRecord writes e's record back into its plan dir when the
// worktree lacks it, so local reads see it. The caller holds the repo lock and
// has run IsSafeForGitOps: a rebase replaying the record's commit has it off
// disk, and a copy written then stops the replay for good (--continue, --skip
// and --abort all refuse to overwrite it).
func restoreReviewRecord(ledgerPath string, e reviewPending) error {
	planDir := filepath.Join(ledgerPath, filepath.FromSlash(e.PlanDir))
	if _, err := os.Lstat(filepath.Join(planDir, filepath.FromSlash(e.Path))); err == nil {
		return nil
	}
	return publishRecord(planDir, planreview.File{Path: e.Path, Data: e.Data})
}

// gitStdout runs git in repo and returns its stdout alone: an answer compared
// byte for byte must not pick up a warning git prints on stderr.
func gitStdout(ctx context.Context, repo string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd.Output()
}

// blobAt is the id of the blob at rev:path, or "" when there is none.
func blobAt(ctx context.Context, repo, rev, path string) string {
	out, err := gitStdout(ctx, repo, nil, "rev-parse", "--verify", "-q", rev+":"+path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// remoteHoldsBlob reports whether the remote tip holds data as a blob anywhere
// under dir (relative to the Ledger root).
func remoteHoldsBlob(ctx context.Context, ledgerPath, dir string, data []byte) bool {
	if !gitutil.IsGitRepo(ledgerPath) {
		return false
	}
	oid, err := gitStdout(ctx, ledgerPath, data, "hash-object", "--stdin")
	if err != nil {
		return false
	}
	ref, err := gitutil.UpstreamTrackingRef(ctx, ledgerPath)
	if err != nil {
		return false
	}
	tree, err := gitStdout(ctx, ledgerPath, nil, "ls-tree", "-r", ref, "--", dir)
	return err == nil && bytes.Contains(tree, []byte(" "+strings.TrimSpace(string(oid))+"\t"))
}

// ledgerRelPath returns planDir relative to the Ledger root as a slash path,
// refusing anything outside it (a pathspec must never widen past the plan).
// Symlinks are resolved first: on macOS a temp or XDG path may be reached via
// /var while git reports /private/var.
func ledgerRelPath(ledgerPath, planDir string) (string, error) {
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	rel, err := filepath.Rel(resolve(ledgerPath), resolve(planDir))
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("plan dir %q is not inside ledger %q", planDir, ledgerPath)
	}
	return filepath.ToSlash(rel), nil
}

// planUploads is what a plan write uploaded to the LFS store before taking the
// Ledger's repo lock. An upload is network I/O bounded only by the LFS client's
// timeouts, and while the lock is held the daemon's sync skips its cycles and
// other Ledger writes give up after gitutil.RepoLockTimeout, so no upload runs
// under it. Under the lock, plan.PointerizeHTML swaps in the pointer for a page
// whose bytes match one of refs.
type planUploads struct {
	refs    []lfs.UploadedRef
	err     error // the first upload that failed
	noStore bool  // no LFS client could be built, so nothing was uploaded
}

// errPlanHTMLChanged: plan.html no longer holds the bytes uploaded for it, as
// when another save of the plan landed between the upload and the lock.
var errPlanHTMLChanged = errors.New("plan.html changed after its upload")

var errNoContentStore = errors.New("no content store is reachable")

// failure is why a page these uploads were made for is not stored as a
// pointer.
func (up planUploads) failure() error {
	switch {
	case up.err != nil:
		return up.err
	case up.noStore:
		return errNoContentStore
	}
	return errPlanHTMLChanged
}

// uploadPlanPages uploads each of pages that is above the LFS threshold.
func uploadPlanPages(gitRoot string, pages ...[]byte) planUploads {
	client := planLFSClientFn(gitRoot)
	up := planUploads{noStore: client == nil}
	for _, page := range pages {
		ref, ok, err := planUploadHTML(client, page)
		if err != nil {
			slog.Warn("plan: plan.html upload failed", "error", err)
		}
		if ok {
			up.refs = append(up.refs, ref)
		}
		up.err = cmp.Or(up.err, err)
	}
	return up
}

// commitPlanDirLocked stages one plan dir and commits it through
// commitPlanSnapshot, for a caller that holds the repo lock. msgPrefix
// defaults to "plan: ". "Nothing to commit" is success (an idempotent
// re-save). A large plain plan.html becomes a pointer when uploaded holds its
// bytes, and is otherwise left out of the commit, awaiting upload.
func commitPlanDirLocked(ctx context.Context, ledgerPath, planDir, msgPrefix string, uploaded ...lfs.UploadedRef) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lockedCommitTimeout)
	defer cancel()
	rel, err := ledgerRelPath(ledgerPath, planDir)
	if err != nil {
		return err
	}
	if msgPrefix == "" {
		msgPrefix = "plan: "
	}
	if err := ledgerSafeForPlanWrite(ctx, ledgerPath); err != nil {
		return err
	}
	// ensure .gitignore is in place before any commit to prevent cache
	// leakage. It stages and untracks files, so it runs after the check.
	gitserver.EnsureGitignoreBeforeCommitCtx(ctx, ledgerPath)
	// --sparse: ledger repos use sparse-checkout (cone mode).
	addArgs := []string{"add", "--sparse", "--", rel}
	if _, err := plan.PointerizeHTML(planDir, uploaded...); err != nil {
		slog.Warn("plan: plan.html pointer not written", "error", err, "dir", planDir)
	}
	if plan.HasLargePlainHTML(planDir) {
		// a large plain plan.html (a failed upload, or no store reachable) must
		// never be committed: it would bury multi-megabyte HTML in history and
		// the pointer retry would then find nothing to dehydrate
		slog.Warn("plan: plan.html is awaiting upload, leaving it out of the commit", "dir", planDir)
		addArgs = append(addArgs, ":(exclude)"+rel+"/"+planHTMLFileName)
		// an earlier add may already have staged the plain page; the exclude
		// above does not touch the index, so unstage it explicitly
		if out, err := gitutil.RunGit(ctx, ledgerPath, "reset", "-q", "--", rel+"/"+planHTMLFileName); err != nil {
			return fmt.Errorf("unstage pending %s: %s: %w", planHTMLFileName, gitutil.SanitizeOutput(out), err)
		}
	}
	if out, err := gitutil.RunGit(ctx, ledgerPath, addArgs...); err != nil {
		return fmt.Errorf("git add %s failed: %s: %w", rel, gitutil.SanitizeOutput(out), err)
	}
	if _, err := commitPlanSnapshot(ctx, ledgerPath, msgPrefix+filepath.Base(planDir), rel); err != nil {
		return fmt.Errorf("commit plan %s: %w", rel, err)
	}
	return nil
}

// commitPlanBackfillToLedger stages every backfilled plan rename, in-place
// plan edit, and session produced_plans fix into ONE commit and pushes it —
// the batch counterpart to commitAndPushPlanDir (which commits a single plan
// dir). A repair run can touch hundreds of plans; landing that as one
// reviewable commit beats one commit per directory. Mirrors
// commitAndPushPlanDir's pattern (--sparse, --no-verify, pushLedger with
// pull-rebase retry) rather than hand-rolling new git plumbing.
//
// Only the final push is best-effort. Every git op at or before `git
// commit` (mv, add, commit itself) returns an error wrapped in
// errBackfillCommitFailed on failure — unlike a push failure, those leave
// the working tree in a half-mutated state with nothing to anchor it, so
// the caller must treat that as a hard failure, not a warning.
//
// renames are (oldAbsPath, newAbsPath) pairs applied via `git mv --sparse`.
// touchedPlanDirs/touchedSessionDirs are absolute paths edited in-place
// (topic-only correction with no slug change; a session's produced_plans
// fix) that need a plain `git add`.
//
// Each rename is `git mv` followed by an explicit `git add` of the NEW path.
// `git mv` alone is NOT enough here: it only restages the move of whatever
// content is already in the index (HEAD) — a file mutated on disk but never
// `git add`'d before the mv (exactly ApplyBackfillMeta's write, which happens
// before this function is ever called) rides along as a rename of the OLD
// content, leaving the just-mutated bytes as an uncommitted diff on the NEW
// path immediately after the commit. Confirmed against git 2.55 directly —
// `git status --porcelain` shows "RM" (rename + pending modify), not a clean
// rename, when the source was dirty. The follow-up `git add` stages the
// current working-tree content at the new path in the same pass.
func commitPlanBackfillToLedger(ledgerPath string, renames [][2]string, touchedPlanDirs, touchedSessionDirs []string) error {
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	for _, pair := range renames {
		oldRel, err := filepath.Rel(ledgerPath, pair[0])
		if err != nil {
			return fmt.Errorf("relativize %q: %w", pair[0], err)
		}
		newRel, err := filepath.Rel(ledgerPath, pair[1])
		if err != nil {
			return fmt.Errorf("relativize %q: %w", pair[1], err)
		}
		mvArgs := []string{"-C", ledgerPath, "mv", "--sparse", oldRel, newRel}
		if out, err := exec.Command("git", mvArgs...).CombinedOutput(); err != nil {
			return fmt.Errorf("%w: git mv %s -> %s failed: %s: %w", errBackfillCommitFailed, oldRel, newRel, string(out), err)
		}
		addArgs := []string{"-C", ledgerPath, "add", "--sparse", newRel}
		if out, err := exec.Command("git", addArgs...).CombinedOutput(); err != nil {
			return fmt.Errorf("%w: git add %s (post-mv content) failed: %s: %w", errBackfillCommitFailed, newRel, string(out), err)
		}
	}

	addPaths := slices.Concat(touchedPlanDirs, touchedSessionDirs)
	if len(addPaths) > 0 {
		addArgs := append([]string{"-C", ledgerPath, "add", "--sparse"}, addPaths...)
		if out, err := exec.Command("git", addArgs...).CombinedOutput(); err != nil {
			return fmt.Errorf("%w: git add failed: %s: %w", errBackfillCommitFailed, string(out), err)
		}
	}

	commitMsg := fmt.Sprintf("plan: backfill %d title(s)", len(renames)+len(touchedPlanDirs))
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "nothing to commit") {
			return nil // idempotent: re-run with nothing left to change
		}
		return fmt.Errorf("%w: %s: %w", errBackfillCommitFailed, wrapCommitError(string(out), err), err)
	}

	// Everything from here down is push-only: the commit above already
	// landed locally, so a failure past this point must NOT match
	// errBackfillCommitFailed — see the caller's best-effort push contract.
	return pushLedger(context.Background(), ledgerPath)
}
