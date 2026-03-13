package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// loadDistillGuidelines reads DISTILL.md from the team context root.
// Returns empty string if the file doesn't exist — the baseline prompt is used instead.
// DISTILL.md lets teams customize what gets emphasized, omitted, or structured
// differently during distillation (e.g., "always track security decisions",
// "ignore dependency update noise", "use our RFC numbering scheme").
func loadDistillGuidelines(tcPath string) string {
	data, err := os.ReadFile(filepath.Join(tcPath, "DISTILL.md"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ensureMemoryDirs creates the memory directory structure if it doesn't exist.
func ensureMemoryDirs(tcPath string) error {
	dirs := []string{
		filepath.Join(tcPath, "memory", "daily"),
		filepath.Join(tcPath, "memory", "weekly"),
		filepath.Join(tcPath, "memory", "monthly"),
		filepath.Join(tcPath, "memory", ".discussion-facts"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// seedMemoryMD creates MEMORY.md with a header template if it doesn't exist.
func seedMemoryMD(tcPath string) error {
	memPath := filepath.Join(tcPath, "MEMORY.md")
	if _, err := os.Stat(memPath); err == nil {
		return nil // already exists
	}

	header := "# Team Memory\n\nThis file contains the team's shared knowledge and memory.\nIt is maintained by the team and not overwritten by automated distillation.\n\nAutomated memory summaries are written to:\n- `memory/daily/` — daily observation summaries\n- `memory/weekly/` — weekly synthesis\n- `memory/monthly/` — monthly synthesis\n"
	if err := os.WriteFile(memPath, []byte(header), 0o644); err != nil {
		return err
	}

	// commit the seed file
	return commitMemoryFile(tcPath, "MEMORY.md", "memory: seed MEMORY.md")
}

// writeMemoryFile writes content to a memory file in the team context.
func writeMemoryFile(tcPath, relPath, content string) error {
	fullPath := filepath.Join(tcPath, relPath)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	return os.WriteFile(fullPath, []byte(content), 0o644)
}

// commitMemoryFile stages and commits a memory file in the team context git repo.
func commitMemoryFile(tcPath, relPath, commitMsg string) error {
	// git add --sparse (required for sparse checkout repos)
	addCmd := exec.Command("git", "add", "--sparse", relPath)
	addCmd.Dir = tcPath
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s: %w", string(out), err)
	}

	// git commit
	commitCmd := exec.Command("git", "commit", "-m", commitMsg, "--allow-empty-message")
	commitCmd.Dir = tcPath
	if out, err := commitCmd.CombinedOutput(); err != nil {
		// "nothing to commit" is not an error — but exit code 1 also covers
		// hook failures, so check the output to distinguish
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 &&
			strings.Contains(string(out), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit: %s: %w", string(out), err)
	}

	return nil
}
