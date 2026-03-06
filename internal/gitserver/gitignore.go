package gitserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// checkoutRequiredEntries are the entries that must be present in .sageox/.gitignore
// inside ledger and team context checkout directories. These prevent daemon-written
// files from appearing as untracked in git status --porcelain, which would permanently
// block blue-green GC reclone (isCheckoutClean() treats any porcelain output as dirty).
//
// Strategy: ignore everything (*), then re-include files that must be committed.
// This is safer than enumerating every daemon-written file — any new local files
// are automatically ignored without needing a gitignore update.
//
// Re-included committed files:
//   - !.gitignore:      the gitignore itself (must be committed to propagate)
//   - !sync.manifest:   sparse checkout manifest (read during clone phase 1)
var checkoutRequiredEntries = []string{
	"*",
	"!.gitignore",
	"!sync.manifest",
}

// EnsureCheckoutGitignore ensures .sageox/.gitignore exists in the given repo
// with required entries to prevent daemon-written files from appearing as untracked.
// Without this, isCheckoutClean() in the GC path sees these files as dirty and
// permanently blocks blue-green reclone.
//
// Writes the file and commits it so the gitignore itself doesn't appear as untracked.
// The commit propagates upstream on the next daemon push cycle.
//
// Idempotent: reads existing content, only writes/commits if entries are missing.
// Preserves any existing custom entries in the file.
func EnsureCheckoutGitignore(repoPath string) error {
	return EnsureCheckoutGitignoreCtx(context.Background(), repoPath)
}

// EnsureCheckoutGitignoreCtx is like EnsureCheckoutGitignore but accepts a context.
func EnsureCheckoutGitignoreCtx(ctx context.Context, repoPath string) error {
	sageoxDir := filepath.Join(repoPath, ".sageox")
	if _, err := os.Stat(sageoxDir); os.IsNotExist(err) {
		return nil // no .sageox dir, nothing to protect
	}

	gitignorePath := filepath.Join(sageoxDir, ".gitignore")

	var existing string
	data, err := os.ReadFile(gitignorePath)
	if err == nil {
		existing = string(data)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read .sageox/.gitignore: %w", err)
	}

	// find which required entries are missing
	lines := make(map[string]bool)
	for _, line := range strings.Split(existing, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			lines[trimmed] = true
		}
	}

	var missing []string
	for _, entry := range checkoutRequiredEntries {
		if !lines[entry] {
			missing = append(missing, entry)
		}
	}

	if len(missing) == 0 {
		return nil // all entries present
	}

	// build new content: keep existing + append missing
	content := existing
	if content == "" {
		content = "# Ignore all daemon-written files; re-include committed files\n"
	} else if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	for _, entry := range missing {
		content += entry + "\n"
	}

	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		return err
	}

	// commit the gitignore so it doesn't appear as untracked itself.
	// follows the same pattern as commitAgentsMD in agents_md.go.
	return commitCheckoutGitignore(ctx, repoPath)
}

// CheckoutGitignoreNeedsFix returns true if .sageox/.gitignore is missing or
// doesn't contain all required entries. Used by doctor checks to detect
// whether EnsureCheckoutGitignore needs to run.
func CheckoutGitignoreNeedsFix(repoPath string) bool {
	sageoxDir := filepath.Join(repoPath, ".sageox")
	if _, err := os.Stat(sageoxDir); os.IsNotExist(err) {
		return false // no .sageox dir, nothing to fix
	}

	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		return true // missing or unreadable
	}

	lines := make(map[string]struct{})
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			lines[trimmed] = struct{}{}
		}
	}
	for _, entry := range checkoutRequiredEntries {
		if _, ok := lines[entry]; !ok {
			return true
		}
	}
	return false
}

// EnsureGitignoreBeforeCommit is a guard that MUST be called before any git commit
// to a ledger or team context. It ensures .sageox/.gitignore is in place so that
// local-only cache files (e.g., sync-state.json) are never committed.
//
// Without this guard, broad operations like `git add -A` will stage cache files.
// Even with explicit file lists, this guard prevents future regressions.
//
// Also untracks any cache files that were committed before .gitignore existed
// (git rm --cached does not delete the local file).
func EnsureGitignoreBeforeCommit(repoPath string) {
	EnsureGitignoreBeforeCommitCtx(context.Background(), repoPath)
}

// EnsureGitignoreBeforeCommitCtx is like EnsureGitignoreBeforeCommit but accepts a context.
func EnsureGitignoreBeforeCommitCtx(ctx context.Context, repoPath string) {
	if repoPath == "" {
		return
	}

	// ensure .gitignore exists with required entries
	if err := EnsureCheckoutGitignoreCtx(ctx, repoPath); err != nil {
		slog.Debug("pre-commit gitignore guard failed", "path", repoPath, "error", err)
	}

	// untrack cache files that were committed before .gitignore existed.
	// --cached removes from git index only, local files are preserved.
	// --ignore-unmatch avoids errors when nothing is tracked.
	rmCmd := exec.CommandContext(ctx, "git", "-C", repoPath,
		"rm", "--cached", "-r", "--ignore-unmatch", ".sageox/cache/")
	if out, err := rmCmd.CombinedOutput(); err != nil {
		slog.Debug("pre-commit cache untrack failed", "path", repoPath, "error", err, "output", strings.TrimSpace(string(out)))
	}
}

// CacheFilesTracked returns true if any .sageox/cache/ files are tracked by git.
// Used by doctor checks to detect cache files that were committed before
// .gitignore was in place.
func CacheFilesTracked(repoPath string) bool {
	cmd := exec.Command("git", "-C", repoPath, "ls-files", ".sageox/cache/")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// commitCheckoutGitignore stages and commits .sageox/.gitignore.
// Non-fatal: if the commit fails (e.g., nothing to commit), we still have
// the file on disk which protects against the GC blocking bug.
func commitCheckoutGitignore(ctx context.Context, repoPath string) error {
	// --sparse: repos may have sparse-checkout enabled (team contexts use --no-cone);
	// without this flag git blocks staging new files even inside included paths.
	// -f: the root .gitignore may exclude .sageox/ to hide daemon-created local state;
	// force-add overrides this so the committed .gitignore inside .sageox/ is tracked.
	addCmd := exec.CommandContext(ctx, "git", "-C", repoPath, "add", "--sparse", "-f", ".sageox/.gitignore")
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add .sageox/.gitignore: %s: %w", strings.TrimSpace(string(output)), err)
	}

	commitCmd := exec.CommandContext(ctx, "git", "-C", repoPath,
		"commit", "-m", "chore: add .sageox/.gitignore to exclude daemon cache files")
	if output, err := commitCmd.CombinedOutput(); err != nil {
		// exit code 1 = nothing to commit (file already committed)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("git commit .sageox/.gitignore: %w: %s", err, output)
	}

	return nil
}
