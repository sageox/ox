package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
)

// malformedEnvTokenRemedy is the family-aware guidance for a SAGEOX_TOKEN that
// is set for ep but fails the local format check.
//
// `ox login` is the wrong remedy for every shape of this failure, and actively
// harmful for a team token: it only ever mints a personal oxp_ credential, so
// following the advice silently changes which principal the CLI acts as.
func malformedEnvTokenRemedy(ep string) string {
	if auth.EnvTokenIsTeamFamily(ep) {
		return auth.EnvVarToken + " holds a team token whose checksum does not match — a truncated paste is the usual cause. Re-copy it from your CI secret store. " + auth.ReauthenticationRemedy(ep)
	}
	return auth.EnvVarToken + " is set for this endpoint but its value failed a local format check — a truncated paste is the usual cause. Re-copy the value, or unset " + auth.EnvVarToken + " to fall back to `ox login`."
}

// checkAuthentication verifies user is logged in - this is CRITICAL for SageOx to work
func checkAuthentication() checkResult {
	// use project-specific endpoint if available, otherwise default
	gitRoot := findGitRoot()
	projectEndpoint := endpoint.GetForProject(gitRoot)
	// Resolve the endpoint once. auth.IsAuthenticated() is exactly
	// IsAuthenticatedForEndpoint(endpoint.Get()), and the env-token questions
	// below need the same endpoint the auth check used — SAGEOX_TOKEN is bound
	// to one endpoint, so asking about "" would report every token as absent.
	if projectEndpoint == "" {
		projectEndpoint = endpoint.Get()
	}

	authenticated, err := auth.IsAuthenticatedForEndpoint(projectEndpoint)

	// A silently-substituted principal is exactly the failure mode doctor
	// exists to catch: the operator named one credential, ox declined to use
	// it, and every remaining path reports the state as an ordinary absence.
	if errors.Is(err, auth.ErrEnvTokenMalformed) {
		return CriticalCheck("Logged in", auth.EnvVarToken+" malformed",
			malformedEnvTokenRemedy(projectEndpoint))
	}

	if err != nil {
		detail := "Could not verify authentication status: " + err.Error()
		if auth.EnvTokenIsTeamFamily(projectEndpoint) && !errors.Is(err, auth.ErrEndpointUnreachable) {
			detail += " " + auth.ReauthenticationRemedy(projectEndpoint)
		}
		return CriticalCheck("Logged in", "check failed",
			detail)
	}

	if !authenticated {
		return CriticalCheck("Logged in", "NOT LOGGED IN",
			auth.ReauthenticationRemedy(projectEndpoint)+" SageOx requires authentication to function.")
	}

	// get user info for display
	token, err := auth.GetTokenForEndpoint(projectEndpoint)
	if err != nil || token == nil {
		return PassedCheck("Logged in", "yes")
	}

	email := token.UserInfo.Email
	if email == "" {
		return PassedCheck("Logged in", "yes")
	}

	return PassedCheck("Logged in", email)
}

// checkGitCredentials verifies git credentials are present and not expired.
// This check auto-refreshes credentials if they are missing or expired - no --fix required.
// Credentials are now per-endpoint to support multi-endpoint setups.
func checkGitCredentials() checkResult {
	// get endpoint for this project
	gitRoot := findGitRoot()
	projectEndpoint := endpoint.GetForProject(gitRoot)
	if projectEndpoint == "" {
		projectEndpoint = endpoint.Get()
	}

	// check credentials for this specific endpoint
	creds, err := gitserver.LoadCredentialsForEndpoint(projectEndpoint)
	if err != nil {
		return refreshGitCredentials("load error: " + err.Error())
	}

	if creds == nil {
		return refreshGitCredentials("no credentials for endpoint")
	}

	if creds.IsExpired() {
		return refreshGitCredentials("expired")
	}

	token, tokenErr := auth.GetTokenForEndpoint(projectEndpoint)
	if tokenErr != nil || (token != nil && token.AccessToken != "" &&
		creds.BearerTokenHash != gitserver.BearerTokenFingerprint(token.AccessToken)) {
		return refreshGitCredentials("credentials are unverified for the current token")
	}

	// credentials are valid - show expiry info
	return PassedCheck("Git credentials",
		fmt.Sprintf("valid, %d repos (expires in %s)", len(creds.Repos), formatCredentialExpiry(creds.ExpiresAt)))
}

// formatCredentialExpiry formats time until credential expiry for display
func formatCredentialExpiry(expiresAt time.Time) string {
	d := time.Until(expiresAt)
	if d < 0 {
		return "expired"
	}
	if d > 24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if d > time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// refreshGitCredentials attempts to refresh git credentials from the API.
// Returns a check result indicating success or failure.
func refreshGitCredentials(reason string) checkResult {
	// get endpoint - use project endpoint if in a project, otherwise default
	gitRoot := findGitRoot()
	projectEndpoint := endpoint.GetForProject(gitRoot)
	if projectEndpoint == "" {
		projectEndpoint = endpoint.Get()
	}

	// get auth token
	token, err := auth.GetTokenForEndpoint(projectEndpoint)
	if err != nil || token == nil || token.AccessToken == "" {
		return WarningCheck("Git credentials", reason,
			"Not authenticated. "+auth.ReauthenticationRemedy(projectEndpoint))
	}

	// fetch credentials from API
	creds, err := auth.RefreshGitCredentialsForEndpoint(context.Background(), projectEndpoint, true)
	if err != nil {
		return WarningCheck("Git credentials", fmt.Sprintf("%s (refresh failed)", reason),
			fmt.Sprintf("API error: %v. %s", err, auth.ReauthenticationRemedy(projectEndpoint)))
	}

	refreshExistingRemotes(projectEndpoint)

	return PassedCheck("Git credentials",
		fmt.Sprintf("refreshed, %d repos (expires in %s)", len(creds.Repos), formatCredentialExpiry(creds.ExpiresAt)))
}

func checkAuthFilePermissions(fix bool) checkResult {
	authPath, err := auth.GetAuthFilePath()
	if err != nil {
		return checkResult{
			name:    "Auth file",
			skipped: true,
			message: "path unknown",
		}
	}

	info, err := os.Stat(authPath)
	if os.IsNotExist(err) {
		return checkResult{
			name:    "Auth file",
			skipped: true,
			message: "not logged in",
		}
	}
	if err != nil {
		return checkResult{
			name:    "Auth file",
			passed:  false,
			message: "stat failed",
			detail:  err.Error(),
		}
	}

	mode := info.Mode().Perm()
	expectedMode := os.FileMode(0600)

	if mode != expectedMode {
		if fix {
			if err := os.Chmod(authPath, expectedMode); err != nil {
				return checkResult{
					name:    "Permissions",
					passed:  false,
					message: "fix failed",
					detail:  err.Error(),
				}
			}
			return checkResult{
				name:    "Permissions",
				passed:  true,
				message: fmt.Sprintf("fixed to %04o", expectedMode),
			}
		}
		return checkResult{
			name:    "Permissions",
			passed:  false,
			message: fmt.Sprintf("insecure %04o", mode),
			detail:  "Run `ox doctor --fix`",
		}
	}

	return checkResult{
		name:    "Permissions",
		passed:  true,
		message: fmt.Sprintf("%04o (secure)", mode),
	}
}
