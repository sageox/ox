package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/errkind"

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
		return "", errkind.Errorf(errkind.NotInitialized, "not in a SageOx project (no .sageox directory found)\nRun this command from a git project directory where SageOx has been initialized")
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

// newSessionStoreForRepoPath creates a session store for a repo given its
// filesystem path, rather than the current working directory. Backs
// 'ox session list --repo <path>' so a caller can inspect another repo's
// sessions without cd-ing there first.
//
// This deliberately does NOT mirror newSessionStore()'s tolerance for an
// unconfigured repo: newSessionStore() falls back to the shared "default"
// bucket via getRepoIDOrDefault when the CURRENT directory has no repo ID,
// which is reasonable for an ambient cwd lookup. Here the caller explicitly
// named repoPath, so silently substituting the bucket shared by every
// unconfigured repo would show unrelated sessions as if they belonged to the
// path the caller asked for — hard-error instead.
//
// Also unlike the cwd path, this does not walk up parent directories:
// config.GetRepoID requires .sageox/config.json literally at repoPath, so a
// path inside a repo (rather than at its root) will fail to resolve.
func newSessionStoreForRepoPath(repoPath string) (*session.Store, string, error) {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, "", fmt.Errorf("resolve repo path %q: %w", repoPath, err)
	}

	repoID := config.GetRepoID(abs)
	if repoID == "" {
		return nil, "", fmt.Errorf("no SageOx project found at %q (must be the repo root — .sageox/config.json is required literally there; subdirectories are not walked up)", abs)
	}

	contextPath := session.GetContextPath(repoID)
	if contextPath == "" {
		return nil, "", fmt.Errorf("failed to get context path for repo %q", abs)
	}

	store, err := session.NewStore(contextPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to access session store for repo %q: %w", abs, err)
	}

	return store, abs, nil
}

// getAuthenticatedUsername returns the authenticated user's email local part, or "".
// This is an AUTH QUERY — returns "" when not logged in. That is correct.
//
// Do NOT use for attribution — use identity.AttributionUsername() instead.
// This function is only for cases where you need to know if a user is
// authenticated (e.g., filtering, access checks).
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

// getDisplayName is superseded by identity.AttributionDisplayName().
// Kept temporarily for test compatibility — callers should migrate.
// DEPRECATED: Use identity.AttributionDisplayName(ep, config.GetDisplayName()) instead.
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
