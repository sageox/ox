package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/proc"
	"github.com/sageox/ox/internal/selfexec"
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
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
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
	pending, err := listPlanPushPending(ledgerPath)
	if err != nil || len(pending) == 0 {
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
		slog.Warn("plan push retry could not start", "pending", len(pending), "error", err)
		return
	}
	// Reap in the background so the child never lingers as a zombie while
	// this (short-lived) prime process is still running.
	go func() { _ = cmd.Wait() }()
	slog.Info("plan push retry started", "pending", len(pending), "pid", cmd.Process.Pid)
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
			var ferr error
			ok, failed, ferr = flushPendingPlanPushes(ctx, ledgerPath)
			return ferr
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
