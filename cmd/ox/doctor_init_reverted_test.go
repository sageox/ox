//go:build !short

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
)

// simulateInitReverted reproduces the on-disk fingerprint of "ox init was
// committed on a feature branch and later reset away":
//   - .sageox/ exists
//   - .sageox/config.json absent
//   - .sageox/.repo_<uuid> marker absent (was tracked, removed by `git reset`)
//   - .sageox/config.local.toml present with a ledger.path that encodes the
//     canonical repo_id (this file is gitignored, so it survived the reset)
func simulateInitReverted(t *testing.T, gitRoot, repoID string) {
	t.Helper()
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.LocalConfig{Ledger: &config.LedgerConfig{
		Path: "/Users/x/.local/share/sageox/sageox.ai/ledgers/" + repoID,
	}}
	if err := config.SaveLocalConfig(gitRoot, cfg); err != nil {
		t.Fatal(err)
	}
}

// TestCheckInitReverted_DetectsMissingConfigJSON guards the user-facing
// signal: when init was reverted from git, the check must surface the
// situation distinctly instead of letting it look like an uninitialized repo.
//
// Failure prevented: silent "daemon unavailable" / "Status: not configured"
// when the user actually has a half-initialized workspace and a running
// daemon they cannot reach.
func TestCheckInitReverted_DetectsMissingConfigJSON(t *testing.T) {
	gitRoot := testGitRepo(t)
	simulateInitReverted(t, gitRoot, "repo_revert-test")

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatal(err)
	}

	result := checkInitReverted(false)
	if !result.warning {
		t.Errorf("expected warning to surface the half-initialized state, got: %+v", result)
	}
	if result.skipped {
		t.Errorf("expected check to surface a warning, not skip: %+v", result)
	}
	// the user-facing message must mention recovery via doctor --fix
	if result.detail == "" {
		t.Error("expected actionable detail pointing the user at `ox doctor --fix`")
	}
}

// TestCheckInitReverted_FixRecoversRepoID is the regression test for PR #568:
// running `ox doctor --fix` must restore .sageox/config.json with the
// canonical repo_id (recovered from the surviving ledger path), not mint a
// fresh ID — which would orphan the existing ledger checkout and leave the
// running daemon unreachable.
func TestCheckInitReverted_FixRecoversRepoID(t *testing.T) {
	gitRoot := testGitRepo(t)
	const wantID = "repo_recovered-via-fix"
	simulateInitReverted(t, gitRoot, wantID)

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatal(err)
	}

	result := checkInitReverted(true)
	if !result.passed {
		t.Fatalf("expected fix to succeed: %+v", result)
	}

	got := config.GetRepoID(gitRoot)
	if got != wantID {
		t.Errorf("recovered repo_id = %q, want %q (the canonical ID encoded in the ledger path; a mismatch means the daemon registry entry would be unreachable)", got, wantID)
	}
}

// TestCheckInitReverted_SkipsWhenConfigPresent ensures the check stays out of
// the way during normal operation.
//
// Why this matters: doctor runs every check on every invocation. A
// recovery check that fires on healthy repos would either (a) flood the
// output with false-positive warnings, conditioning users to ignore real
// ones, or (b) under --fix, rewrite a perfectly good config.json and
// risk dropping fields the default config doesn't set. Both are worse
// than the bug this check is for.
func TestCheckInitReverted_SkipsWhenConfigPresent(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.GetDefaultProjectConfig()
	cfg.RepoID = "repo_already-good"
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatal(err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatal(err)
	}

	result := checkInitReverted(false)
	if !result.passed {
		t.Errorf("expected pass when config.json present, got: %+v", result)
	}
}

// TestCheckInitReverted_SkipsForUninitializedRepo guards against false
// positives in genuinely uninitialized repos — the existing config-json check
// owns that case.
//
// Why this matters: a fresh `git clone` of a repo that has never been
// touched by ox should NOT see "init artifacts missing — likely
// reverted from git." That message implies prior state to recover from
// and would be misleading. The existing CheckSlugConfigJSON path emits
// the right "run ox init" guidance for genuinely uninitialized repos;
// init-reverted must defer to it (skip) rather than overlap.
func TestCheckInitReverted_SkipsForUninitializedRepo(t *testing.T) {
	gitRoot := testGitRepo(t)
	// no .sageox/ at all
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatal(err)
	}

	result := checkInitReverted(false)
	if !result.skipped {
		t.Errorf("expected skip for uninitialized repo, got: %+v", result)
	}
}
