package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitPATLiveness,
		Name:        "git PAT liveness",
		Category:    "Authentication",
		FixLevel:    FixLevelAuto,
		Description: "Validates PAT actually authenticates against the git server",
		Run: func(fix bool) checkResult {
			return checkGitPATLiveness(fix)
		},
	})
}

// checkGitPATLiveness probes the SageOx git server with the stored PAT
// to verify it is accepted, not just locally unexpired.
// When fix=true and the OAuth token is still valid, auto-repairs by fetching
// fresh git credentials from the API (same flow as `ox login` credential sync).
func checkGitPATLiveness(fix bool) checkResult {
	const name = "git PAT liveness"

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "not in git repo", "")
	}

	if !config.IsInitialized(gitRoot) {
		return SkippedCheck(name, "not initialized", "")
	}

	projectEndpoint := endpoint.GetForProject(gitRoot)
	if projectEndpoint == "" {
		projectEndpoint = endpoint.Get()
	}

	creds, err := gitserver.LoadCredentialsForEndpoint(projectEndpoint)
	if err != nil || creds == nil {
		return SkippedCheck(name, "no credentials found", "")
	}

	if creds.IsExpired() {
		return SkippedCheck(name, "credentials expired (see git-creds-freshness check)", "")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result := gitserver.ValidatePATLiveness(ctx, creds)

	if result.Skipped {
		// "no repo URL available" means credentials exist but have no repos —
		// refreshing credentials may populate the repos list and fix this
		if fix && result.Reason == "no repo URL available" {
			repaired := attemptPATAutoRepair(projectEndpoint)
			if repaired {
				return PassedCheck(name, "auto-repaired: refreshed credentials with repo URLs")
			}
			return FailedCheck(name, result.Reason,
				"auto-repair failed; run `ox login` to re-authenticate")
		}
		return SkippedCheck(name, result.Reason, "")
	}

	if !result.Valid {
		if !fix {
			return FailedCheck(name, result.Reason,
				"run `ox login` to re-authenticate and get a fresh PAT")
		}

		// auto-repair: use OAuth token to fetch fresh git credentials
		repaired := attemptPATAutoRepair(projectEndpoint)
		if repaired {
			return PassedCheck(name, "auto-repaired: synced fresh PAT from server")
		}

		return FailedCheck(name, result.Reason,
			"auto-repair failed (OAuth token may be expired); run `ox login` to re-authenticate")
	}

	return PassedCheck(name, "PAT accepted by git server")
}

// attemptPATAutoRepair tries to fetch fresh git credentials using the OAuth token.
// Returns true if credentials were successfully refreshed.
func attemptPATAutoRepair(ep string) bool {
	// try to get a valid OAuth token (refreshes if needed)
	token, err := auth.EnsureValidTokenForEndpoint(ep, 30)
	if err != nil || token == nil || token.AccessToken == "" {
		slog.Debug("PAT auto-repair: OAuth token unavailable", "error", err)
		return false
	}

	// fetch fresh git credentials from the API
	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)
	if err := fetchAndSaveGitCredentials(client); err != nil {
		slog.Debug("PAT auto-repair: credential sync failed", "error", err)
		return false
	}

	// clear any stale credential helper entry (osxkeychain, libsecret, wincred)
	// that could preempt the fresh token in future git operations
	if creds, err := gitserver.LoadCredentialsForEndpoint(ep); err == nil && creds != nil && creds.ServerURL != "" {
		gitserver.ClearCredentialHelperEntry(creds.ServerURL)
	}

	// update PAT in existing remote URLs
	refreshExistingRemotes(ep)

	return true
}
