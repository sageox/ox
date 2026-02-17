package main

import (
	"fmt"
	"os"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
)

// checkAuthentication verifies user is logged in - this is CRITICAL for SageOx to work
func checkAuthentication() checkResult {
	// use project-specific endpoint if available, otherwise default
	gitRoot := findGitRoot()
	projectEndpoint := endpoint.GetForProject(gitRoot)

	var authenticated bool
	var err error
	if projectEndpoint != "" {
		authenticated, err = auth.IsAuthenticatedForEndpoint(projectEndpoint)
	} else {
		authenticated, err = auth.IsAuthenticated()
	}

	if err != nil {
		return CriticalCheck("Logged in", "check failed",
			"Could not verify authentication status: "+err.Error())
	}

	if !authenticated {
		return CriticalCheck("Logged in", "NOT LOGGED IN",
			"Run `ox login` to authenticate. SageOx requires authentication to function.")
	}

	// get user info for display
	var token *auth.StoredToken
	if projectEndpoint != "" {
		token, err = auth.GetTokenForEndpoint(projectEndpoint)
	} else {
		token, err = auth.GetToken()
	}
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
			"Not authenticated. Run `ox login` first.")
	}

	// fetch credentials from API
	client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)
	if err := fetchAndSaveGitCredentials(client); err != nil {
		return WarningCheck("Git credentials", fmt.Sprintf("%s (refresh failed)", reason),
			fmt.Sprintf("API error: %v. Run `ox login` to retry.", err))
	}

	// check new status
	newStatus := gitserver.CheckCredentialStatusForEndpoint(projectEndpoint)
	return PassedCheck("Git credentials",
		fmt.Sprintf("refreshed, %d repos (expires in %s)", newStatus.RepoCount, newStatus.FormatExpiry()))
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
