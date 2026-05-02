package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// loadGuidance reads a single guidance file from memory/guidance/.
// Returns empty string if the file doesn't exist.
func loadGuidance(tcPath, filename string) string {
	data, err := os.ReadFile(filepath.Join(tcPath, "memory", "guidance", filename))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// seedGuidanceFiles creates default EXTRACT.md and DISTILL.md in memory/guidance/
// if they don't already exist. Does NOT create the optional per-stage files (those
// are opt-in customization). Commits each created file to git.
func seedGuidanceFiles(tcPath string) error {
	guidanceDir := filepath.Join(tcPath, "memory", "guidance")
	if err := os.MkdirAll(guidanceDir, 0o755); err != nil {
		return fmt.Errorf("create guidance dir: %w", err)
	}

	type guidanceDefault struct {
		name    string
		content string
	}
	defaults := []guidanceDefault{
		{"EXTRACT.md", `# Extraction Guidance

These guidelines apply to all fact extraction stages.
Customize to control what gets extracted and how facts are structured.

For source-specific guidance, create an optional file named EXTRACT-{source}.md
in this directory (e.g., EXTRACT-discussions.md, EXTRACT-github.md).
If the file exists, read it for additional guidance specific to that source.
`},
		{"DISTILL.md", `# Distillation Guidance

These guidelines apply to all synthesis stages.
Customize to control what gets emphasized, omitted, or structured differently.

For cadence-specific guidance, create an optional file named DISTILL-{cadence}.md
in this directory (e.g., DISTILL-daily.md, DISTILL-weekly.md, DISTILL-monthly.md).
If the file exists, read it for additional guidance specific to that cadence.
`},
	}

	for _, d := range defaults {
		path := filepath.Join(guidanceDir, d.name)
		if _, err := os.Stat(path); err == nil {
			continue // already exists
		}
		if err := os.WriteFile(path, []byte(d.content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", d.name, err)
		}
		relPath := filepath.Join("memory", "guidance", d.name)
		if err := commitMemoryFile(tcPath, relPath, fmt.Sprintf("memory: seed %s guidance", strings.TrimSuffix(d.name, ".md"))); err != nil {
			return fmt.Errorf("commit %s: %w", d.name, err)
		}
	}

	return nil
}

// migrateDistillGuidelines moves tc.Path/DISTILL.md to memory/guidance/DISTILL.md
// if the old file exists and the new one doesn't. Uses git mv for atomic
// move+stage so partial failures don't leave an inconsistent state.
// Returns true if migration occurred.
func migrateDistillGuidelines(tcPath string) (bool, error) {
	oldPath := filepath.Join(tcPath, "DISTILL.md")
	newDir := filepath.Join(tcPath, "memory", "guidance")
	newPath := filepath.Join(newDir, "DISTILL.md")

	// skip if old file doesn't exist
	if _, err := os.Stat(oldPath); err != nil {
		return false, nil
	}

	// skip if new file already exists
	if _, err := os.Stat(newPath); err == nil {
		return false, nil
	}

	// ensure target directory exists
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return false, fmt.Errorf("create guidance dir: %w", err)
	}

	// git mv atomically moves the file and stages both the deletion and addition
	mvCmd := exec.Command("git", "mv", "DISTILL.md", filepath.Join("memory", "guidance", "DISTILL.md"))
	mvCmd.Dir = tcPath
	if out, err := mvCmd.CombinedOutput(); err != nil {
		// git mv fails if file is untracked — fall back to manual move+stage
		data, readErr := os.ReadFile(oldPath)
		if readErr != nil {
			return false, fmt.Errorf("read old DISTILL.md: %w", readErr)
		}
		if writeErr := os.WriteFile(newPath, data, 0o644); writeErr != nil {
			return false, fmt.Errorf("write new DISTILL.md: %w", writeErr)
		}
		if removeErr := os.Remove(oldPath); removeErr != nil {
			return false, fmt.Errorf("remove old DISTILL.md: %w", removeErr)
		}
		addCmd := exec.Command("git", "add", "--sparse", filepath.Join("memory", "guidance", "DISTILL.md"))
		addCmd.Dir = tcPath
		if addOut, addErr := addCmd.CombinedOutput(); addErr != nil {
			// rollback: restore old file so next run can retry cleanly
			_ = os.WriteFile(oldPath, data, 0o644)
			_ = os.Remove(newPath)
			return false, fmt.Errorf("git add: %s: %w", string(addOut), addErr)
		}
		_ = out // suppress unused warning
	}

	commitCmd := exec.Command("git", "commit", "-m", "memory: migrate DISTILL.md to guidance/")
	commitCmd.Dir = tcPath
	if out, err := commitCmd.CombinedOutput(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 &&
			strings.Contains(string(out), "nothing to commit") {
			return true, nil
		}

		// unstage both paths (old location + new location) so a failed commit
		// doesn't leave dirty index state or affect unrelated staged files
		resetCmd := exec.Command("git", "reset", "HEAD", "--", "DISTILL.md", filepath.Join("memory", "guidance", "DISTILL.md"))
		resetCmd.Dir = tcPath
		if resetOut, resetErr := resetCmd.CombinedOutput(); resetErr != nil {
			slog.Warn("failed to unstage after migration commit failure", "error", fmt.Sprintf("%s: %v", string(resetOut), resetErr))
		}

		return false, fmt.Errorf("git commit: %s: %w", string(out), err)
	}

	return true, nil
}

// ensureMemoryDirs creates the memory directory structure if it doesn't exist.
func ensureMemoryDirs(tcPath string) error {
	dirs := []string{
		filepath.Join(tcPath, "memory", "daily"),
		filepath.Join(tcPath, "memory", "weekly"),
		filepath.Join(tcPath, "memory", "monthly"),
		filepath.Join(tcPath, "memory", "guidance"),
		filepath.Join(tcPath, "memory", ".discussion-facts"),
		filepath.Join(tcPath, "memory", ".github-facts"),
		filepath.Join(tcPath, "memory", ".session-facts"),
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

// removeMemoryFile deletes a file from the team context and commits the removal.
func removeMemoryFile(tcPath, relPath, commitMsg string) error {
	fullPath := filepath.Join(tcPath, relPath)
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove file: %w", err)
	}

	// git add --sparse stages the deletion
	addCmd := exec.Command("git", "add", "--sparse", relPath)
	addCmd.Dir = tcPath
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add (removal): %s: %w", string(out), err)
	}

	commitCmd := exec.Command("git", "commit", "-m", commitMsg, "--allow-empty-message")
	commitCmd.Dir = tcPath
	if out, err := commitCmd.CombinedOutput(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 &&
			strings.Contains(string(out), "nothing to commit") {
			return nil
		}

		// unstage so a failed commit doesn't leave dirty index state
		resetCmd := exec.Command("git", "reset", "HEAD", "--", relPath)
		resetCmd.Dir = tcPath
		if resetOut, resetErr := resetCmd.CombinedOutput(); resetErr != nil {
			slog.Warn("failed to unstage after commit failure", "path", relPath, "error", fmt.Sprintf("%s: %v", string(resetOut), resetErr))
		}

		return fmt.Errorf("git commit: %s: %w", string(out), err)
	}
	return nil
}

// commitMemoryFile stages and commits a memory file in the team context git repo.
// On commit failure, the file is unstaged to prevent dirty index accumulation.
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

		// unstage the file so a failed commit doesn't leave dirty index state
		// that persists across daemon --autostash sync cycles
		resetCmd := exec.Command("git", "reset", "HEAD", "--", relPath)
		resetCmd.Dir = tcPath
		if resetOut, resetErr := resetCmd.CombinedOutput(); resetErr != nil {
			slog.Warn("failed to unstage after commit failure", "path", relPath, "error", fmt.Sprintf("%s: %v", string(resetOut), resetErr))
		}

		return fmt.Errorf("git commit: %s: %w", string(out), err)
	}

	return nil
}
