package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/require"
)

// plan_commit_test.go covers commitPlanBackfillToLedger's failure routing:
// a failure at or before `git commit` must be distinguishable from a
// push-only failure (see errBackfillCommitFailed in plan_commit.go), since
// the caller (runPlanBackfillTitlesOnLedger) treats the two very
// differently — one is a hard command failure, the other a best-effort
// warning. Reuses initGitLedger from plan_backfill_test.go (same package).

// TestCommitPlanBackfillToLedger_PreCommitGitFailureIsErrBackfillCommitFailed
// forces `git mv` to fail (source path was never created) — a stand-in for
// any pre-commit git failure — and verifies the returned error satisfies
// errors.Is(err, errBackfillCommitFailed).
func TestCommitPlanBackfillToLedger_PreCommitGitFailureIsErrBackfillCommitFailed(t *testing.T) {
	ledger := t.TempDir()
	initGitLedger(t, ledger)

	bogusOld := filepath.Join(ledger, "data", "plans", "2026-01-01-does-not-exist")
	bogusNew := filepath.Join(ledger, "data", "plans", "2026-01-01-renamed")

	err := commitPlanBackfillToLedger(ledger, [][2]string{{bogusOld, bogusNew}}, nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error: git mv on a path that was never created")
	}
	if !errors.Is(err, errBackfillCommitFailed) {
		t.Errorf("error = %v, want errors.Is(err, errBackfillCommitFailed)", err)
	}

	// Nothing must have been committed — HEAD is still just the init commit.
	out := runGitInDir(t, ledger, "log", "--oneline")
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("expected only the init commit after a pre-commit failure, got:\n%s", out)
	}
}

// TestCommitPlanBackfillToLedger_PushOnlyFailureIsNotErrBackfillCommitFailed
// is the negative case finding 3 explicitly requires: a real, valid commit
// (mv/add/commit all succeed) followed by a push failure (no remote
// configured) must return an error that does NOT match
// errBackfillCommitFailed, and the commit must have actually landed.
func TestCommitPlanBackfillToLedger_PushOnlyFailureIsNotErrBackfillCommitFailed(t *testing.T) {
	ledger := t.TempDir()
	initGitLedger(t, ledger)

	dir := filepath.Join(ledger, "data", "plans", "2026-01-01-real")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"topic":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := commitPlanBackfillToLedger(ledger, nil, []string{dir}, nil, nil)
	if err == nil {
		t.Fatal("expected a push failure: no remote is configured for this ledger")
	}
	if errors.Is(err, errBackfillCommitFailed) {
		t.Errorf("push-only failure incorrectly matched errBackfillCommitFailed: %v", err)
	}

	out := runGitInDir(t, ledger, "log", "--oneline")
	if !strings.Contains(out, "plan: backfill 1 title") {
		t.Errorf("commit did not land locally despite this being a push-only failure:\n%s", out)
	}
	status := runGitInDir(t, ledger, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Errorf("working tree not clean after a successful commit (push-only failure): %q", status)
	}

	// Retrying unchanged content must publish the existing commit once the
	// remote is available, without creating another backfill commit.
	head := runGitInDir(t, ledger, "rev-parse", "HEAD")
	branch := runGitInDir(t, ledger, "branch", "--show-current")
	remote := t.TempDir()
	initBareRemote(t, remote)
	runGitInDir(t, ledger, "remote", "add", "origin", remote)
	runGitInDir(t, ledger, "config", "branch."+branch+".remote", "origin")
	runGitInDir(t, ledger, "config", "branch."+branch+".merge", "refs/heads/"+branch)
	require.NoError(t, commitPlanBackfillToLedger(ledger, nil, []string{dir}, nil, nil))
	require.Equal(t, head, runGitInDir(t, ledger, "rev-parse", "HEAD"))
	require.Equal(t, head, runGitInDir(t, remote, "rev-parse", "refs/heads/"+branch))
}

// A later metadata failure must not leave earlier references rewritten or staged.
func TestCommitPlanBackfillRestoresReferencesOnPreparationFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"plan staging locked", "index.lock"},
		{"session removed after discovery", "meta.json"},
		{"malformed session metadata", "invalid character"},
		{"rejected legacy summary", "session meta failed invariant"},
		{"ignored session metadata", "ignored by one of your .gitignore files"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "plan staging locked" && runtime.GOOS == "windows" {
				t.Skip("post-index-change fixture requires a POSIX shell")
			}
			ledger, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			oldDir := writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
			first := writeBackfillSessionFixture(t, ledger, "2026-05-03-first-Ox0001", []string{"1-context-why-now"})
			second := writeBackfillSessionFixture(t, ledger, "2026-05-03-second-Ox0002", []string{"1-context-why-now"})
			initGitLedger(t, ledger)
			head := runGitInDir(t, ledger, "rev-parse", "HEAD")
			original, err := os.ReadFile(filepath.Join(first, "meta.json"))
			require.NoError(t, err)
			newDir := filepath.Join(filepath.Dir(oldDir), "2026-05-01-corrected")
			renames := [][2]string{{oldDir, newDir}}
			secondMeta := filepath.Join(second, "meta.json")
			switch tc.name {
			case "plan staging locked":
				// Hold the index after git mv succeeds, so the following add fails.
				hook := "#!/bin/sh\nif git ls-files --error-unmatch data/plans/2026-05-01-corrected/meta.json >/dev/null 2>&1; then\n  touch .git/index.lock\nfi\n"
				require.NoError(t, os.WriteFile(filepath.Join(ledger, ".git", "hooks", "post-index-change"), []byte(hook), 0o755))
			case "session removed after discovery":
				require.NoError(t, os.Remove(secondMeta))
			case "malformed session metadata":
				require.NoError(t, os.WriteFile(secondMeta, []byte("{broken"), 0o644))
			case "rejected legacy summary":
				require.NoError(t, os.WriteFile(secondMeta, []byte(`{"summary":"Summary failed content validation: title too short","produced_plans":["1-context-why-now"]}`), 0o644))
			case "ignored session metadata":
				rel := "sessions/" + filepath.Base(second) + "/meta.json"
				runGitInDir(t, ledger, "rm", "--cached", "--", rel)
				require.NoError(t, os.WriteFile(filepath.Join(ledger, ".git", "info", "exclude"), []byte(rel+"\n"), 0o644))
			}
			staged := runGitInDir(t, ledger, "ls-files", "--stage", "--", "sessions")
			secondOriginal, secondReadErr := os.ReadFile(secondMeta)
			if tc.name == "session removed after discovery" {
				require.ErrorIs(t, secondReadErr, os.ErrNotExist)
			} else {
				require.NoError(t, secondReadErr)
			}
			err = commitPlanBackfillToLedger(ledger, renames, nil, []string{first, second}, map[string]string{"1-context-why-now": "corrected"})
			require.ErrorIs(t, err, errBackfillCommitFailed)
			require.ErrorContains(t, err, tc.want)
			if tc.name == "plan staging locked" {
				require.ErrorContains(t, err, "git add", "the failure must occur after the move")
				require.FileExists(t, filepath.Join(ledger, ".git", "index.lock"))
			}
			after, err := os.ReadFile(filepath.Join(first, "meta.json"))
			require.NoError(t, err)
			require.Equal(t, string(original), string(after))
			secondAfter, secondAfterErr := os.ReadFile(secondMeta)
			if tc.name == "session removed after discovery" {
				require.ErrorIs(t, secondAfterErr, os.ErrNotExist)
			} else {
				require.NoError(t, secondAfterErr)
				require.Equal(t, string(secondOriginal), string(secondAfter))
			}
			require.Equal(t, staged, runGitInDir(t, ledger, "ls-files", "--stage", "--", "sessions"))
			require.Equal(t, head, runGitInDir(t, ledger, "rev-parse", "HEAD"))
			require.NoError(t, lfs.MutateSessionMeta(context.Background(), first, func(*lfs.SessionMeta) (*lfs.SessionMeta, error) {
				return nil, nil // the failed preparation must release the metadata lock
			}))
			if tc.name == "ignored session metadata" {
				require.NoError(t, lfs.MutateSessionMeta(context.Background(), second, func(*lfs.SessionMeta) (*lfs.SessionMeta, error) {
					return nil, nil // this session was rewritten before staging failed
				}))
			}
		})
	}
}
