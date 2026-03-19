package main

import (
	"context"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitPATLiveness,
		Name:        "git PAT liveness",
		Category:    "Authentication",
		FixLevel:    FixLevelCheckOnly,
		Description: "Validates PAT actually authenticates against the git server",
		Run: func(fix bool) checkResult {
			return checkGitPATLiveness()
		},
	})
}

// checkGitPATLiveness probes the SageOx git server with the stored PAT
// to verify it is accepted, not just locally unexpired.
func checkGitPATLiveness() checkResult {
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

	result := gitserver.ValidatePATLiveness(ctx, creds.ServerURL, creds.Token)

	if result.Skipped {
		return SkippedCheck(name, result.Reason, "")
	}

	if !result.Valid {
		return FailedCheck(name, result.Reason,
			"run `ox login` to re-authenticate and get a fresh PAT")
	}

	return PassedCheck(name, "PAT accepted by git server")
}
