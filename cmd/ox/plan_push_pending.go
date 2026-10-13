package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/proc"
	"github.com/sageox/ox/internal/selfexec"
	"github.com/sageox/ox/pkg/planreview"
	"github.com/spf13/cobra"
)

// plan_push_pending.go makes a failed plan commit/push durable instead of a
// log line. Nothing else retries it: the daemon deliberately refuses to push
// while any unpushed commit is not one of its own (daemon-git.md — its pushes
// skip pushLedger's secret gate and LFS reconcile), so a stranded `plan:`
// commit would otherwise wait until some unrelated CLI command happened to
// call pushLedger.
//
// A failure writes a marker under the Ledger clone's .git/ dir: per-clone,
// local-only, and untrackable by construction — unlike .sageox/cache/, whose
// protection depends on .sageox/.gitignore already existing in that clone
// (the same reason the repo lock keys on .git/ox-sync). It is exactly as
// durable as the unpushed commit it describes. `ox agent prime` notices markers and
// spawns a detached `ox plan flush-pending`, which re-runs the commit and
// pushLedger — the full CLI gate stack — and clears each marker on success.

// planPushPendingDir is the marker directory, relative to the Ledger root.
const planPushPendingDir = ".git/ox-plan-push-pending"

// planPushFlushTimeout bounds one detached flush. pushLedger retries with
// backoff and may pull-rebase; a few minutes covers a slow remote while
// making sure a wedged network never leaves a stray process behind for long.
const planPushFlushTimeout = 5 * time.Minute

// planReviewPendingDir holds one entry per review operation (a round or a
// resolution) whose record is not yet known to be on the remote, under .git/
// for the same reasons. The plan-dir markers retry an author's whole plan dir;
// these retry exactly one record each and are cleared only by verifying that
// record (see landReviewRecord), never by another operation's success.
const planReviewPendingDir = ".git/ox-plan-review-pending"

// reviewPending is one review operation's recovery entry, removed once its
// record is verified. It holds the exact record bytes, which never change, so
// a retry can publish the record again even when the worktree copy is gone,
// and publishes nothing else. Only PlanDir is rewritten: when a resend finds
// the plan's dir renamed.
type reviewPending struct {
	PlanDir string `json:"plan_dir"` // slash path relative to the Ledger root
	Plan    string `json:"plan"`     // the plan's pln_ id, else its dir name
	Kind    string `json:"kind"`     // round | resolution
	ID      string `json:"id"`       // the operation id
	Path    string `json:"path"`     // the record, relative to the plan dir
	SHA256  string `json:"sha256"`   // of Data
	Data    []byte `json:"data"`
}

// reviewPendingPath is the entry for one operation, keyed by (plan, kind, id).
func reviewPendingPath(ledgerPath, plan, kind, id string) string {
	sum := sha256.Sum256([]byte(plan + "\x00" + kind + "\x00" + id))
	return filepath.Join(ledgerPath, filepath.FromSlash(planReviewPendingDir), hex.EncodeToString(sum[:8])+".json")
}

func (e reviewPending) path(ledgerPath string) string {
	return reviewPendingPath(ledgerPath, e.Plan, e.Kind, e.ID)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// readReviewPending reads one entry. An entry whose bytes no longer hash to
// its recorded SHA-256 is refused: a retry never publishes changed bytes under
// the original operation.
func readReviewPending(path string) (reviewPending, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return reviewPending{}, err
	}
	var e reviewPending
	if err := json.Unmarshal(b, &e); err != nil {
		return reviewPending{}, fmt.Errorf("parse review pending entry %s: %w", path, err)
	}
	switch {
	case e.PlanDir == "" || !filepath.IsLocal(filepath.FromSlash(e.PlanDir)) || e.Path == "" || !filepath.IsLocal(filepath.FromSlash(e.Path)):
		return reviewPending{}, fmt.Errorf("review pending entry %s names no record inside the Ledger", path)
	case sha256Hex(e.Data) != e.SHA256:
		return reviewPending{}, fmt.Errorf("review pending entry %s: its bytes do not hash to its recorded sha256", path)
	}
	return e, nil
}

// writeReviewPending writes an entry atomically and durably: fsync'd temp,
// rename, and an fsync'd directory, whose failure is returned (AtomicWriteBytes
// tolerates it), and .git/ is fsync'd when the directory is new.
func writeReviewPending(path string, e reviewPending) error {
	dir := filepath.Dir(path)
	_, statErr := os.Stat(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create review pending dir: %w", err)
	}
	if errors.Is(statErr, os.ErrNotExist) {
		if err := syncDir(filepath.Dir(dir)); err != nil {
			return fmt.Errorf("sync review pending dir's parent: %w", err)
		}
	}
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("encode review pending entry: %w", err)
	}
	if err := fileutil.AtomicWriteBytes(path, b, 0o644); err != nil {
		return fmt.Errorf("write review pending entry: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync review pending dir: %w", err)
	}
	return nil
}

// syncDir makes a directory's entries durable. Windows cannot fsync a
// directory; there NTFS commits a rename with its journal.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// listReviewPending returns the readable review entries in a Ledger. An
// unreadable entry is logged and skipped.
func listReviewPending(ledgerPath string) ([]reviewPending, error) {
	dir := filepath.Join(ledgerPath, filepath.FromSlash(planReviewPendingDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read plan review pending entries: %w", err)
	}
	var out []reviewPending
	for _, de := range entries {
		if !isPendingEntry(de) {
			continue
		}
		e, err := readReviewPending(filepath.Join(dir, de.Name()))
		if err != nil {
			slog.Warn("plan review pending entry unreadable, skipped", "entry", de.Name(), "error", err)
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// flushPendingReviewRecords lands every pending review record: each is
// restored create-only when the worktree lost it, committed alone, pushed, and
// cleared only once the remote tip holds its exact bytes.
func flushPendingReviewRecords(ctx context.Context, ledgerPath string) (succeeded, failed int, err error) {
	pending, err := listReviewPending(ledgerPath)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range pending {
		planDir := filepath.Join(ledgerPath, filepath.FromSlash(e.PlanDir))
		if _, statErr := os.Stat(planDir); statErr != nil {
			// `ox plan backfill` renames plan dirs, records included, so the
			// entry is done once the remote tip holds its bytes anywhere among
			// the plans. Until then it is kept: it still holds a review the
			// reviewer was told is saved.
			if !remoteHoldsBlob(ctx, ledgerPath, path.Dir(e.PlanDir), e.Data) {
				failed++
				slog.WarnContext(ctx, "plan review record kept pending: its plan dir is gone", "plan_dir", e.PlanDir, "record", e.Path)
				continue
			}
			clearReviewPending(ctx, ledgerPath, e)
			succeeded++
			continue
		}
		// The commit carries the entry's bytes, not the worktree's. A record an
		// earlier attempt froze but never wrote is restored, create-only, so
		// local reads see it too.
		if _, lerr := os.Lstat(filepath.Join(planDir, filepath.FromSlash(e.Path))); errors.Is(lerr, os.ErrNotExist) {
			if perr := publishRecord(planDir, planreview.File{Path: e.Path, Data: e.Data}); perr != nil {
				slog.WarnContext(ctx, "plan review record not restored to the worktree", "plan_dir", e.PlanDir, "record", e.Path, "error", perr)
			}
		}
		if st := landReviewRecord(ctx, ledgerPath, e); !st.Pushed {
			failed++
			continue
		}
		succeeded++
	}
	return succeeded, failed, nil
}

// isPendingEntry reports whether de is a plan push marker or a review entry:
// a .json file, never a dotfile such as the ".tmp-*" a crash can leave behind
// fileutil.AtomicWriteBytes.
func isPendingEntry(de os.DirEntry) bool {
	return !de.IsDir() && strings.HasSuffix(de.Name(), ".json") && !strings.HasPrefix(de.Name(), ".")
}

func clearReviewPending(ctx context.Context, ledgerPath string, e reviewPending) {
	if err := os.Remove(e.path(ledgerPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.WarnContext(ctx, "plan review pending entry could not be cleared", "plan_dir", e.PlanDir, "record", e.Path, "error", err)
	}
}

// planPushPending is one marker: a plan dir whose latest state is not known to
// be on the remote.
type planPushPending struct {
	PlanDir        string    `json:"plan_dir"` // slash path relative to the Ledger root
	FirstFailedAt  time.Time `json:"first_failed_at"`
	LastFailedAt   time.Time `json:"last_failed_at"`
	LastError      string    `json:"last_error"`
	FailedAttempts int       `json:"failed_attempts"`
}

func planPushMarkerPath(ledgerPath, rel string) string {
	name := strings.ReplaceAll(rel, "/", "__") + ".json"
	return filepath.Join(ledgerPath, filepath.FromSlash(planPushPendingDir), name)
}

// recordPlanPushOutcome writes (or clears) the pending marker for planDir
// after a commit+push attempt. Marker I/O failures are logged, never returned:
// the caller already has the real outcome to report.
func recordPlanPushOutcome(ctx context.Context, ledgerPath, planDir string, pushErr error) {
	rel, err := ledgerRelPath(ledgerPath, planDir)
	if err != nil {
		return // not a Ledger plan dir — nothing a retry could push
	}
	path := planPushMarkerPath(ledgerPath, rel)

	if pushErr == nil {
		if rmErr := os.Remove(path); rmErr == nil {
			slog.InfoContext(ctx, "plan push pending cleared", "plan_dir", rel)
		} else if !errors.Is(rmErr, os.ErrNotExist) {
			slog.WarnContext(ctx, "plan push marker could not be cleared", "plan_dir", rel, "error", rmErr)
		}
		return
	}

	now := time.Now().UTC()
	m := planPushPending{PlanDir: rel, FirstFailedAt: now}
	if b, rerr := os.ReadFile(path); rerr == nil {
		var prev planPushPending
		if json.Unmarshal(b, &prev) == nil && !prev.FirstFailedAt.IsZero() {
			m.FirstFailedAt = prev.FirstFailedAt
			m.FailedAttempts = prev.FailedAttempts
		}
	}
	m.LastFailedAt = now
	m.LastError = gitutil.SanitizeOutput(pushErr.Error())
	m.FailedAttempts++
	if werr := writePlanPushMarker(path, m); werr != nil {
		slog.WarnContext(ctx, "plan push marker could not be written; retry will rely on the next push",
			"plan_dir", rel, "error", werr)
		return
	}
	slog.WarnContext(ctx, "plan push pending: will retry on next `ox agent prime`",
		"plan_dir", rel, "failed_attempts", m.FailedAttempts, "error", m.LastError)
}

// writePlanPushMarker writes atomically (fsync'd temp + rename) so a
// concurrent flush never reads a torn marker. The temp file is dot-prefixed,
// which listPlanPushPending skips.
func writePlanPushMarker(path string, m planPushPending) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create marker dir: %w", err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode marker: %w", err)
	}
	if err := fileutil.AtomicWriteBytes(path, b, 0o644); err != nil {
		return fmt.Errorf("write marker: %w", err)
	}
	return nil
}

// listPlanPushPending returns the pending markers in a Ledger. A missing
// marker dir means nothing is pending.
func listPlanPushPending(ledgerPath string) ([]planPushPending, error) {
	entries, err := os.ReadDir(filepath.Join(ledgerPath, filepath.FromSlash(planPushPendingDir)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read plan push markers: %w", err)
	}
	var out []planPushPending
	for _, e := range entries {
		if !isPendingEntry(e) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(ledgerPath, filepath.FromSlash(planPushPendingDir), e.Name()))
		if err != nil {
			continue
		}
		var m planPushPending
		if json.Unmarshal(b, &m) != nil || m.PlanDir == "" || !filepath.IsLocal(filepath.FromSlash(m.PlanDir)) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// flushPendingPlanPushes retries every pending plan through commitAndPushPlanDir
// (stage + snapshot commit + pushLedger) and clears each marker that lands.
// Returns how many succeeded and failed.
func flushPendingPlanPushes(ctx context.Context, ledgerPath string) (succeeded, failed int, err error) {
	pending, err := listPlanPushPending(ledgerPath)
	if err != nil {
		return 0, 0, err
	}
	start := time.Now()
	for _, m := range pending {
		planDir := filepath.Join(ledgerPath, filepath.FromSlash(m.PlanDir))
		if _, statErr := os.Stat(planDir); errors.Is(statErr, os.ErrNotExist) {
			// The plan dir is gone (renamed by a backfill, or a re-clone that
			// already carries it): there is nothing left to push for it.
			_ = os.Remove(planPushMarkerPath(ledgerPath, m.PlanDir))
			slog.WarnContext(ctx, "plan push retry dropped: plan dir no longer exists", "plan_dir", m.PlanDir)
			continue
		}
		_, pushErr := commitAndPushPlanDir(ctx, ledgerPath, planDir)
		recordPlanPushOutcome(ctx, ledgerPath, planDir, pushErr)
		if pushErr != nil {
			failed++
			slog.WarnContext(ctx, "plan push retry failed", "plan_dir", m.PlanDir, "error", pushErr)
			continue
		}
		succeeded++
		slog.InfoContext(ctx, "plan push retry succeeded", "plan_dir", m.PlanDir,
			"pending_since", m.FirstFailedAt)
	}
	slog.InfoContext(ctx, "plan push retry pass",
		"operation", "plan_push_retry", "total", len(pending),
		"succeeded", succeeded, "failed", failed,
		"elapsed_ms", time.Since(start).Milliseconds())
	return succeeded, failed, nil
}

// kickPendingPlanPushes is the retry trigger, called from `ox agent prime`.
//
// Why prime: it runs automatically at every AI coworker session start and
// after every compaction, so a stranded plan push is retried within the next
// session with no human action — unlike `ox doctor --fix`, which someone must
// remember to run. The daemon cannot do it (its pushes bypass the gate stack
// by design). The retry itself runs in a DETACHED child so prime's latency and
// its stdout contract (the coworker's context) are untouched; the check here is
// one ReadDir, so prime pays nothing when nothing is pending.
func kickPendingPlanPushes(projectRoot string) {
	ledgerPath, err := planLedgerPathFor(projectRoot)
	if err != nil {
		return
	}
	markers, err := listPlanPushPending(ledgerPath)
	if err != nil {
		return
	}
	// counted, not parsed: an entry carries a whole record
	entries, _ := os.ReadDir(filepath.Join(ledgerPath, filepath.FromSlash(planReviewPendingDir)))
	pending := len(markers)
	for _, de := range entries {
		if isPendingEntry(de) {
			pending++
		}
	}
	if pending == 0 {
		return
	}
	exe, err := selfexec.Path()
	if err != nil {
		slog.Debug("plan push retry not started: no ox executable", "error", err)
		return
	}
	cmd := exec.Command(exe, "plan", "flush-pending")
	cmd.Dir = projectRoot
	cmd.Env = os.Environ()
	proc.Detach(cmd)
	if err := cmd.Start(); err != nil {
		slog.Warn("plan push retry could not start", "pending", pending, "error", err)
		return
	}
	// Reap in the background so the child never lingers as a zombie while
	// this (short-lived) prime process is still running.
	go func() { _ = cmd.Wait() }()
	slog.Info("plan push retry started", "pending", pending, "pid", cmd.Process.Pid)
}

// planFlushPendingCmd is the hidden worker `ox agent prime` spawns.
var planFlushPendingCmd = &cobra.Command{
	Use:    "flush-pending",
	Short:  "Retry plan commits/pushes that failed earlier (run automatically by ox agent prime)",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		gitRoot := findGitRoot()
		if gitRoot == "" {
			return fmt.Errorf("flush-pending: not inside a git repository")
		}
		ledgerPath, err := planLedgerPathFor(gitRoot)
		if err != nil {
			return fmt.Errorf("flush-pending: %w", err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), planPushFlushTimeout)
		defer cancel()

		// Single-flight across processes: two primes in quick succession must
		// not run two flushes against one clone. A busy lock means another
		// flush is already on it — not an error.
		lockTarget := filepath.Join(ledgerPath, filepath.FromSlash(planPushPendingDir), ".flush")
		var ok, failed int
		lockErr := fileutil.WithFileLockTimeout(ctx, lockTarget, time.Second, func() error {
			rok, rfailed, rerr := flushPendingReviewRecords(ctx, ledgerPath)
			pok, pfailed, perr := flushPendingPlanPushes(ctx, ledgerPath)
			ok, failed = rok+pok, rfailed+pfailed
			return errors.Join(rerr, perr)
		})
		var busy *fileutil.ErrLockTimeout
		if errors.As(lockErr, &busy) {
			slog.InfoContext(ctx, "plan push retry skipped: another flush is running")
			return nil
		}
		if lockErr != nil {
			return fmt.Errorf("flush-pending: %w", lockErr)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "plan push retry: %d pushed, %d still pending\n", ok, failed)
		return nil
	},
}

func init() {
	planCmd.AddCommand(planFlushPendingCmd)
}
