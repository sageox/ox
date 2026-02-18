package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/signing"
	"github.com/sageox/ox/internal/ui"
)

// requireProjectRoot returns the project root or an error with user-friendly message.
// Used to ensure commands are run within a SageOx project.
func requireProjectRoot() (string, error) {
	root := config.FindProjectRoot()
	if root == "" {
		return "", fmt.Errorf("not in a SageOx project (no .sageox directory found)\nRun this command from a git project directory where SageOx has been initialized")
	}
	return root, nil
}

// getRepoIDOrDefault returns the repo ID for the project, or "default" if not set.
func getRepoIDOrDefault(projectRoot string) string {
	repoID := config.GetRepoID(projectRoot)
	if repoID == "" {
		return "default"
	}
	return repoID
}

// newSessionStore creates a session store for the current project.
// Combines the common pattern of: find project root -> get repo ID -> get context path -> create store.
func newSessionStore() (*session.Store, string, error) {
	projectRoot, err := requireProjectRoot()
	if err != nil {
		return nil, "", err
	}

	repoID := getRepoIDOrDefault(projectRoot)
	contextPath := session.GetContextPath(repoID)
	if contextPath == "" {
		return nil, "", fmt.Errorf("failed to get context path")
	}

	store, err := session.NewStore(contextPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to access session store: %w", err)
	}

	return store, projectRoot, nil
}

// findSessionByFilename looks up a session by exact filename.
// Returns user-friendly error if not found.
func findSessionByFilename(store *session.Store, filename string) (*session.StoredSession, error) {
	t, err := store.ReadSession(filename)
	if err != nil {
		return nil, fmt.Errorf("session %q not found\nRun 'ox session list' to see available sessions", filename)
	}
	return t, nil
}

// getAuthenticatedUsername returns the authenticated user's username (local part of email) or empty string.
// ep is the normalized endpoint to look up the token for.
func getAuthenticatedUsername(ep string) string {
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil {
		return ""
	}
	email := token.UserInfo.Email
	if at := strings.Index(email, "@"); at > 0 {
		return email[:at]
	}
	return email
}

// getDisplayName returns a privacy-aware display name from auth info + user config.
// Falls back to empty string if not authenticated.
// ep is the normalized endpoint to look up the token for.
func getDisplayName(ep string) string {
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil {
		return ""
	}
	configName := config.GetDisplayName()
	p := identity.NewPersonInfo(token.UserInfo.Email, token.UserInfo.Name, "", configName)
	return p.DisplayName
}

// warnIfRedactionSignatureInvalid checks the redaction policy signature and prints
// a warning to stderr if it's invalid. This is called at the start of session
// operations to alert users of potential tampering.
//
// Returns true if signature is valid or not configured (dev build), false if invalid.
func warnIfRedactionSignatureInvalid() bool {
	result := session.VerifyRedactionSignature()

	switch result.Status {
	case signing.StatusNotConfigured:
		// dev build - no signature to check
		return true

	case signing.StatusValid:
		// signature is good
		return true

	case signing.StatusInvalid:
		// CRITICAL: signature doesn't match - possible tampering
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, ui.RenderFail("WARNING: Redaction policy signature INVALID"))
		fmt.Fprintln(os.Stderr, ui.RenderWarn("  The redaction patterns may have been tampered with."))
		fmt.Fprintln(os.Stderr, ui.RenderWarn("  Your secrets may NOT be properly protected!"))
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Recommended actions:")
		fmt.Fprintln(os.Stderr, "    1. Download ox from official release: https://github.com/sageox/ox/releases")
		fmt.Fprintln(os.Stderr, "    2. Verify the checksum matches")
		fmt.Fprintln(os.Stderr, "    3. Report this issue if you downloaded from official sources")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Run 'ox redaction verify' for details.")
		fmt.Fprintln(os.Stderr, "")
		return false

	case signing.StatusMissing, signing.StatusError:
		// signature missing or error - warn but don't block
		fmt.Fprintln(os.Stderr, ui.RenderWarn("WARNING: Could not verify redaction policy signature"))
		if result.Error != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", result.Error)
		}
		fmt.Fprintln(os.Stderr, "")
		return true // don't block operations on errors, just warn
	}

	return true
}
