package gitserver

import (
	"context"
	"fmt"
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
	if data, err := os.ReadFile(gitignorePath); err == nil {
		existing = string(data)
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

	content := string(data)
	for _, entry := range checkoutRequiredEntries {
		if !strings.Contains(content, entry) {
			return true
		}
	}
	return false
}

// commitCheckoutGitignore stages and commits .sageox/.gitignore.
// Non-fatal: if the commit fails (e.g., nothing to commit), we still have
// the file on disk which protects against the GC blocking bug.
func commitCheckoutGitignore(ctx context.Context, repoPath string) error {
	addCmd := exec.CommandContext(ctx, "git", "-C", repoPath, "add", ".sageox/.gitignore")
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add .sageox/.gitignore: %w", err)
	}

	commitCmd := exec.CommandContext(ctx, "git", "-C", repoPath,
		"commit", "-m", "chore: add .sageox/.gitignore to exclude daemon cache files")
	if output, err := commitCmd.CombinedOutput(); err != nil {
		// "nothing to commit" is fine — file was already committed
		if strings.Contains(string(output), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit .sageox/.gitignore: %w: %s", err, output)
	}

	return nil
}
