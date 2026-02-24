package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
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
// and returns the file→OID manifest for inclusion in meta.json.
//
// Flow:
//  1. Read all content files from session dir
//  2. Compute SHA256 OIDs + sizes
//  3. Call LFS batch API to get upload actions
//  4. Upload all blobs in parallel
//  5. Return filename→FileRef map for meta.json
func uploadSessionLFS(projectRoot, sessionPath string) (map[string]lfs.FileRef, error) {
	if err := checkUploadAccess(projectRoot); err != nil {
		return nil, err
	}

	// content file patterns to upload (everything except meta.json)
	contentFiles := []string{
		"raw.jsonl",
		"events.jsonl",
		"summary.md",
		"session.md",
		"session.html",
		"plan.md",
	}

	// read all content files that exist
	files := make(map[string][]byte)         // filename -> content
	fileRefs := make(map[string]lfs.FileRef) // filename -> ref
	var batchObjects []lfs.BatchObject

	for _, name := range contentFiles {
		filePath := filepath.Join(sessionPath, name)
		content, err := os.ReadFile(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue // skip files that don't exist
			}
			return nil, fmt.Errorf("read %s: %w", name, err)
		}

		ref := lfs.NewFileRef(content)
		fileRefs[name] = ref
		files[ref.BareOID()] = content
		batchObjects = append(batchObjects, lfs.BatchObject{
			OID:  ref.BareOID(),
			Size: ref.Size,
		})
	}

	if len(batchObjects) == 0 {
		return fileRefs, nil // nothing to upload
	}

	slog.Info("uploading session to LFS", "path", sessionPath, "files", len(batchObjects))

	// get LFS client
	client, err := getLFSClient(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("create LFS client: %w", err)
	}

	// request upload URLs from LFS batch API
	resp, err := client.BatchUpload(batchObjects)
	if err != nil {
		slog.Info("LFS batch API failed", "error", err, "path", sessionPath, "files", len(batchObjects))
		return nil, fmt.Errorf("LFS batch upload: %w", err)
	}

	// upload blobs in parallel (up to 4 concurrent)
	results := lfs.UploadAll(resp, files, 4)

	// collect all errors so devs can see everything that failed
	var uploadErrors []string
	for _, r := range results {
		if r.Error != nil {
			slog.Info("LFS blob upload failed", "oid", r.OID, "error", r.Error)
			uploadErrors = append(uploadErrors, fmt.Sprintf("OID %s: %s", r.OID, r.Error))
		}
	}
	if len(uploadErrors) > 0 {
		return nil, fmt.Errorf("LFS upload failed (%d/%d files):\n  %s",
			len(uploadErrors), len(results), strings.Join(uploadErrors, "\n  "))
	}

	slog.Info("LFS upload complete", "path", sessionPath, "files", len(fileRefs))
	return fileRefs, nil
}

// getLFSClient creates an LFS client using project credentials.
// Derives the LFS batch URL from the ledger's git repo URL.
func getLFSClient(projectRoot string) (*lfs.Client, error) {
	// get endpoint for this project
	ep := endpoint.GetForProject(projectRoot)

	// load git credentials for this endpoint
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

	// get ledger repo URL from API
	repoID := config.GetRepoID(projectRoot)
	if repoID == "" {
		return nil, fmt.Errorf("no repo_id in project config")
	}

	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil {
		return nil, fmt.Errorf("get auth token: %w", err)
	}
	if token == nil || token.AccessToken == "" {
		return nil, fmt.Errorf("no auth token (run 'ox login' first)")
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)
	status, err := client.GetLedgerStatus(repoID)
	if err != nil {
		return nil, fmt.Errorf("get ledger status: %w", err)
	}
	if status == nil || status.RepoURL == "" {
		return nil, fmt.Errorf("ledger not ready or no repo URL")
	}

	return lfs.NewClient(status.RepoURL, creds.Username, creds.Token), nil
}

// ensureSessionsGitignore ensures the sessions/.gitignore exists in the ledger.
// Content files are stored in LFS, only meta.json is tracked by git.
func ensureSessionsGitignore(sessionsDir string) error {
	gitignorePath := filepath.Join(sessionsDir, ".gitignore")

	// check if it already exists
	if _, err := os.Stat(gitignorePath); err == nil {
		return nil // already exists
	}

	content := `# Content files are stored in LFS blob storage, not git
*.jsonl
*.html
*.md
!meta.json
`
	return os.WriteFile(gitignorePath, []byte(content), 0644)
}

// commitAndPushLedger commits meta.json and .gitignore, then pushes to remote.
// Uses pull --rebase with retry to handle concurrent pushes from other team members.
func commitAndPushLedger(ledgerPath, sessionName string) error {
	// stage meta.json and .gitignore
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	sessionDir := filepath.Join(sessionsDir, sessionName)

	metaPath := filepath.Join(sessionDir, "meta.json")
	gitignorePath := filepath.Join(sessionsDir, ".gitignore")

	// git add specific files only
	addCmd := exec.Command("git", "-C", ledgerPath, "add", metaPath, gitignorePath)
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
func commitAndPushLedgerWithExtras(ledgerPath, sessionName string, includeSummary bool) error {
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	sessionDir := filepath.Join(sessionsDir, sessionName)

	filesToAdd := []string{
		filepath.Join(sessionDir, "meta.json"),
		filepath.Join(sessionsDir, ".gitignore"),
	}
	if includeSummary {
		filesToAdd = append(filesToAdd, filepath.Join(sessionDir, "summary.json"))
	}

	args := append([]string{"-C", ledgerPath, "add"}, filesToAdd...)
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

// pushLedger pushes ledger changes to remote with conflict retry.
// Retries on transient failures (network, rejection). Fails fast on permanent errors
// (auth, config) to avoid wasting time on retries that will never succeed.
// Uses context for timeout control (60s per git operation).
func pushLedger(ctx context.Context, ledgerPath string) error {
	// pre-flight: check for lock files and broken rebase state
	if err := gitutil.IsSafeForGitOps(ledgerPath); err != nil {
		return fmt.Errorf("ledger blocked: %w", err)
	}

	// ensure remote has current credentials before pushing
	ep := endpoint.GetForProject(findGitRoot())
	if ep != "" {
		if err := gitserver.RefreshRemoteCredentials(ledgerPath, ep); err != nil {
			slog.Debug("remote credential refresh skipped before push", "error", err)
		}
	}

	const maxRetries = 3
	const opTimeout = 60 * time.Second

	// Errors that indicate a permanent failure — retrying won't help.
	permanentPatterns := []string{
		"Permission denied",
		"could not read Username",
		"Authentication failed",
		"invalid credentials",
		"repository not found",
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, opTimeout)
		outStr, err := gitutil.RunGit(attemptCtx, ledgerPath, "push", "--quiet")
		cancel()
		if err == nil {
			return nil // success
		}

		// fail fast on permanent errors
		for _, pattern := range permanentPatterns {
			if strings.Contains(outStr, pattern) {
				return fmt.Errorf("git push failed (not retryable): %s", outStr)
			}
		}

		if attempt == maxRetries {
			return fmt.Errorf("git push failed after %d attempts: %s", maxRetries, outStr)
		}

		slog.Info("push failed, retrying", "attempt", attempt, "output", outStr)

		// pull --rebase to handle non-fast-forward (most common retry case)
		if strings.Contains(outStr, "non-fast-forward") || strings.Contains(outStr, "rejected") {
			// check rebase state before pulling
			if gitutil.IsRebaseInProgress(ledgerPath) {
				abortCtx, abortCancel := context.WithTimeout(ctx, opTimeout)
				_, _ = gitutil.RunGit(abortCtx, ledgerPath, "rebase", "--abort")
				abortCancel()
			}

			pullCtx, pullCancel := context.WithTimeout(ctx, opTimeout)
			pullOut, pullErr := gitutil.RunGit(pullCtx, ledgerPath, "pull", "--rebase", "--quiet")
			pullCancel()
			if pullErr != nil {
				// abort rebase to avoid leaving repo in broken state
				abortCtx, abortCancel := context.WithTimeout(ctx, opTimeout)
				_, _ = gitutil.RunGit(abortCtx, ledgerPath, "rebase", "--abort")
				abortCancel()
				return fmt.Errorf("git pull --rebase failed during retry: %s", pullOut)
			}
		}

		// backoff before retry
		time.Sleep(time.Duration(attempt) * time.Second)
	}

	return nil // unreachable
}
