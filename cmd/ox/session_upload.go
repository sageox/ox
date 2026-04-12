// session_upload.go handles uploading session artifacts to the ledger.
//
// AUTH MODEL: Upload uses Git PAT only (no OAuth required).
//   - LFS upload: PAT via HTTP Basic auth (getLFSClient)
//   - git push: PAT embedded in remote URL (RefreshRemoteCredentials)
//   - checkUploadAccess: OAuth-based, fail-open, kept for viewer detection only
//
// See docs/ai/specs/session-auth-model.md for the full auth model.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/lfs"
)

// checkUploadAccess checks if the user has write access to upload sessions.
// Returns api.ErrReadOnly if the user is a viewer on a public repo.
// Returns nil if the user has write access or if access cannot be determined (fail-open).
func checkUploadAccess(projectRoot string) error {
	repoID := config.GetRepoID(projectRoot)
	if repoID == "" {
		return nil
	}

	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return nil // fail-open: can't determine access without auth
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)

	// try the detailed repo endpoint first
	detail, err := client.GetRepoDetail(repoID)
	if err == nil && detail != nil {
		if detail.IsReadOnly() {
			return api.ErrReadOnly
		}
		return nil
	}

	// fall back to ledger status if GetRepoDetail returned 404 (nil, nil) or errored
	if detail == nil && err == nil {
		status, statusErr := client.GetLedgerStatus(repoID)
		if statusErr == nil && status != nil && status.IsReadOnly() {
			return api.ErrReadOnly
		}
	}

	return nil // fail-open on any error
}

// uploadSessionLFS uploads session content files to LFS blob storage
// and returns the file->FileRef manifest for inclusion in meta.json.
//
// No OAuth needed — LFS upload uses the Git PAT (HTTP Basic auth).
// Access control is enforced at push time by the PAT, not by a pre-check.
func uploadSessionLFS(projectRoot, sessionPath string) (map[string]lfs.FileRef, error) {
	client, err := getLFSClient(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("create LFS client: %w", err)
	}

	return lfs.UploadSessionFiles(client, sessionPath, slog.Default())
}

// getLFSClient creates an LFS client using project credentials.
// Derives the LFS batch URL from the ledger's local git remote, avoiding any
// dependency on the OAuth API token. Only the Git PAT is needed for LFS auth.
func getLFSClient(projectRoot string) (*lfs.Client, error) {
	ep := endpoint.GetForProject(projectRoot)

	// load git credentials (PAT) for LFS HTTP Basic auth
	creds, err := gitserver.LoadCredentialsForEndpoint(ep)
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	if creds == nil {
		return nil, fmt.Errorf("no git credentials found (run 'ox login' first)")
	}
	if creds.Token == "" {
		return nil, fmt.Errorf("git credentials have empty token")
	}

	// derive LFS repo URL from the ledger's local git remote (no API call needed)
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return nil, fmt.Errorf("resolve ledger: %w", err)
	}

	repoURL, err := gitserver.GetBareRemoteURL(ledgerPath)
	if err != nil {
		return nil, fmt.Errorf("get ledger remote URL: %w", err)
	}
	if repoURL == "" {
		return nil, fmt.Errorf("ledger has no remote URL configured")
	}

	return lfs.NewClient(repoURL, creds.Username, creds.Token), nil
}

// ensureSessionsGitignore delegates to lfs.EnsureSessionsGitignore.
func ensureSessionsGitignore(sessionsDir string) error {
	return lfs.EnsureSessionsGitignore(sessionsDir)
}

// commitAndPushLedger commits meta.json and .gitignore, then pushes to remote.
// Uses pull --rebase with retry to handle concurrent pushes from other team members.
// NEVER uses --force push. Conflicts are resolved via pull --rebase.
//
// Uses exec.Command("git") rather than go-git for the same reasons as the daemon
// (see daemon/sync.go doPull): rebase support, process isolation, and lock safety.
// This is a low-volume path (once per session stop), so subprocess overhead is negligible.
func commitAndPushLedger(ledgerPath, sessionName string) error {
	// ensure .gitignore is in place before any commit to prevent cache file leakage
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	// stage meta.json and .gitignore
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	sessionDir := filepath.Join(sessionsDir, sessionName)

	metaPath := filepath.Join(sessionDir, "meta.json")
	gitignorePath := filepath.Join(sessionsDir, ".gitignore")

	// stage meta.json, .gitignore, and any LFS pointer files
	filesToAdd := []string{metaPath, gitignorePath}
	for _, pattern := range []string{"*.jsonl", "*.html", "*.md"} {
		matches, _ := filepath.Glob(filepath.Join(sessionDir, pattern))
		filesToAdd = append(filesToAdd, matches...)
	}

	// --sparse: ledger repos use sparse-checkout (cone mode); this flag
	// prevents git from blocking adds if sparse rules change or edge cases arise
	addArgs := append([]string{"-C", ledgerPath, "add", "--sparse"}, filesToAdd...)
	addCmd := exec.Command("git", addArgs...)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(output), err)
	}

	// commit
	commitMsg := fmt.Sprintf("session: %s", sessionName)
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		// check if nothing to commit
		if strings.Contains(string(output), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit failed: %s: %w", string(output), err)
	}

	// push with pull --rebase retry (up to 3 attempts)
	return pushLedger(context.Background(), ledgerPath)
}

// commitAndPushLedgerWithExtras commits meta.json, .gitignore, and optionally summary.json,
// then pushes to remote. Used by doctor retry path where summary.json may have been
// copied from cache alongside the LFS upload retry.
// NEVER uses --force push. Conflicts are resolved via pull --rebase.
func commitAndPushLedgerWithExtras(ledgerPath, sessionName string, includeSummary bool) error {
	// ensure .gitignore is in place before any commit to prevent cache file leakage
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	sessionDir := filepath.Join(sessionsDir, sessionName)

	filesToAdd := []string{
		filepath.Join(sessionDir, "meta.json"),
		filepath.Join(sessionsDir, ".gitignore"),
	}
	if includeSummary {
		filesToAdd = append(filesToAdd, filepath.Join(sessionDir, "summary.json"))
	}
	// stage LFS pointer files
	for _, pattern := range []string{"*.jsonl", "*.html", "*.md"} {
		matches, _ := filepath.Glob(filepath.Join(sessionDir, pattern))
		filesToAdd = append(filesToAdd, matches...)
	}

	// --sparse: see commitAndPushLedger for rationale
	args := append([]string{"-C", ledgerPath, "add", "--sparse"}, filesToAdd...)
	addCmd := exec.Command("git", args...)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(output), err)
	}

	commitMsg := fmt.Sprintf("session: %s (retry)", sessionName)
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit failed: %s: %w", string(output), err)
	}

	return pushLedger(context.Background(), ledgerPath)
}

// resolveLedgerPath returns the ledger git repo path for the project.
// Uses the existing getLedgerPath() helper, wrapping its result for error handling.
func resolveLedgerPath() (string, error) {
	path := getLedgerPath()
	if path == "" {
		return "", fmt.Errorf("no ledger path found (run 'ox doctor --fix' or wait for daemon to clone)")
	}

	// verify ledger exists on disk
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("ledger not found at %s (run 'ox doctor --fix')", path)
	}

	return path, nil
}

// ledgerAutoResolvePrefixes aliases the canonical list from internal/ledger
// for use by the CLI push path.
var ledgerAutoResolvePrefixes = ledger.AutoResolvePrefixes

// pushLedger pushes ledger changes to remote with conflict retry.
// Delegates to gitutil.PushWithRetry with ledger-appropriate options:
// LFS reconciliation, rebase on conflict, auto-resolve for data/github/.
func pushLedger(ctx context.Context, ledgerPath string) error {
	// resolve endpoint once, before entering the push loop.
	// only refresh credentials when we have a real project root —
	// GetForProject("") falls back to Default, which would inject
	// production credentials into a local file:// remote URL.
	// findGitRoot() is CWD-dependent — if the caller isn't in a git repo
	// (e.g., doctor retry from a different dir), this silently returns "".
	var ep string
	if root := findGitRoot(); root != "" {
		ep = endpoint.GetForProject(root)
	} else {
		slog.Warn("pushLedger: no git root found, credential refresh will be skipped")
	}
	return gitutil.PushWithRetry(ctx, ledgerPath, gitutil.PushOpts{
		AutoResolvePrefixes: ledgerAutoResolvePrefixes,
		PrePush: func(repoPath string) error {
			if ep != "" {
				if err := gitserver.RefreshRemoteCredentials(repoPath, ep); err != nil {
					return fmt.Errorf("credential refresh: %w", err)
				}
			}
			return nil
		},
		ReconcileLFS: makeLFSReconciler(ep),
	})
}

// makeLFSReconciler returns a ReconcileLFS callback that strips orphaned LFS
// pointer stubs and squashes unpushed history so the push can succeed.
// Returns nil (no reconciliation) if no endpoint is available.
func makeLFSReconciler(ep string) func(string) (bool, error) {
	if ep == "" {
		return nil
	}
	return func(repoPath string) (bool, error) {
		result, err := lfs.ReconcileUnpushedPointers(
			context.Background(), repoPath, ep, slog.Default())
		if err != nil {
			return false, err
		}
		return result.Replaced > 0, nil
	}
}
