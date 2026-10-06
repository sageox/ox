package main

import (
	"fmt"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitCredsFreshness,
		Name:        "git credentials freshness",
		Category:    "Authentication",
		FixLevel:    FixLevelSuggested,
		Description: "Checks if git credentials are expired or expiring soon",
		Run: func(fix bool) checkResult {
			return checkGitCredentialsFreshness(fix)
		},
	})
}

// checkGitCredentialsFreshness verifies git credentials are not expired or near expiry.
func checkGitCredentialsFreshness(fix bool) checkResult {
	const name = "git credentials freshness"

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

	now := time.Now()

	if now.After(creds.ExpiresAt) {
		msg := fmt.Sprintf("expired %s ago", formatDurationRough(now.Sub(creds.ExpiresAt)))
		return WarningCheck(name, msg, auth.ReauthenticationRemedy(projectEndpoint))
	}

	token, tokenErr := auth.GetTokenForEndpoint(projectEndpoint)
	if tokenErr != nil || (token != nil && token.AccessToken != "" &&
		creds.BearerTokenHash != gitserver.BearerTokenFingerprint(token.AccessToken)) {
		if fix {
			return refreshGitCredentials("credentials are unverified for the current token")
		}
		remedy := "run `ox doctor --fix` to refresh credentials for the current token"
		if tokenErr != nil {
			remedy = auth.ReauthenticationRemedy(projectEndpoint)
		}
		return WarningCheck(name, "credentials are unverified for the current token", remedy)
	}

	remaining := time.Until(creds.ExpiresAt)
	if remaining < time.Hour {
		msg := fmt.Sprintf("expiring in %s", formatDurationRough(remaining))
		return WarningCheck(name, msg, auth.ReauthenticationRemedy(projectEndpoint))
	}

	return PassedCheck(name, fmt.Sprintf("valid, expires in %s", formatCredentialExpiry(creds.ExpiresAt)))
}

// formatDurationRough returns a rough human-readable duration string.
func formatDurationRough(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
