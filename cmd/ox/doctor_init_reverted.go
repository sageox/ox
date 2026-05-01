package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/config"
)

// checkInitReverted detects the "ox init was committed on a feature branch and
// later reset away" failure mode.
//
// Symptom on disk:
//   - .sageox/ exists
//   - .sageox/config.json is MISSING (was tracked, removed by `git reset`)
//   - .sageox/config.local.toml exists with a ledger.path that encodes a
//     valid repo_id (this file is gitignored, so it survived the reset)
//
// Without this check, ox status reports "not configured" — which reads like a
// fresh repo and hides the real problem: the user has a running daemon
// registered under repo-id-derived workspace_id `f14832e3 = sha256(repo_id)[:8]`
// but the CLI now computes a path-hash workspace_id and can never reach the
// daemon. Symptoms include `ox murmur` printing "daemon unavailable" and
// `ox daemon status` showing "Not running" while `ox daemon list` shows the
// daemon alive.
//
// FixLevelAuto: when --fix is on, ensureSageoxConfig recovers repo_id from
// local state and writes a backfilled config.json.
func checkInitReverted(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Init artifacts", "not in git repo", "")
	}

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if _, err := os.Stat(sageoxDir); err != nil {
		return SkippedCheck("Init artifacts", ".sageox/ not present", "")
	}

	configPath := filepath.Join(sageoxDir, "config.json")
	if _, err := os.Stat(configPath); err == nil {
		return PassedCheck("Init artifacts", "config.json present")
	}

	// config.json missing — see if local state lets us recover.
	repoID, _ := config.RecoverRepoIDFromLocalState(gitRoot)
	if repoID == "" {
		// genuinely uninitialized — checkConfigFile / repo-marker checks own this case.
		return SkippedCheck("Init artifacts", "no recoverable repo_id", "")
	}

	if fix {
		// ensureSageoxConfig now recovers repo_id from local state when creating fresh.
		switch ensureSageoxConfig(gitRoot) {
		case configError:
			return FailedCheck("Init artifacts", "backfill failed", "")
		case configCreated:
			return PassedCheck("Init artifacts", fmt.Sprintf("recovered config.json (repo_id=%s)", repoID))
		default:
			// configPreserved/configUpgraded shouldn't happen here (we already verified
			// config.json was missing), but treat them as success regardless.
			return PassedCheck("Init artifacts", "recovered")
		}
	}

	return WarningCheck(
		"Init artifacts",
		"config.json missing but ledger state survives — likely reverted from git",
		fmt.Sprintf("Run `ox doctor --fix` to recover (repo_id=%s)", repoID),
	)
}
