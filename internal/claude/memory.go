package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Claude Code Auto Memory
//
// Claude Code maintains per-project memory in ~/.claude/projects/<project>/memory/.
// Auto memory is on by default (toggle via /memory or autoMemoryEnabled setting).
// It can also be disabled via CLAUDE_CODE_DISABLE_AUTO_MEMORY=1 env var.
//
// The <project> identifier is the absolute path to the git root with all "/"
// replaced by "-". Examples:
//   /Users/ryan/Code/ox         → -Users-ryan-Code-ox
//   /private/tmp/my-project     → -private-tmp-my-project
//
// Outside a git repo, the working directory is used instead of the git root.
//
// Memory directory structure:
//   ~/.claude/projects/<project>/memory/
//   ├── MEMORY.md          # concise index, auto-loaded first 200 lines at session start
//   ├── debugging.md       # topic file (read on-demand, NOT loaded at startup)
//   ├── patterns.md        # topic file
//   └── ...                # any other topic files Claude creates
//
// Key behaviors:
//   - MEMORY.md is the entrypoint: first 200 lines (~1500 tokens) injected into
//     every session's system prompt. Lines beyond 200 are silently truncated.
//   - Topic files (debugging.md, api-conventions.md, etc.) are NOT loaded at startup.
//     Claude reads them on-demand via file tools when it needs the information.
//   - MEMORY.md acts as an index — Claude links to topic files from it and moves
//     detailed notes out to keep MEMORY.md under the 200-line budget.
//   - Claude decides what to remember based on whether information would be useful
//     in a future conversation. Not every session produces a memory write.
//   - All files are plain markdown, human-readable and editable.
//
// Worktree behavior (conflicting sources, but official docs state):
//   - "All worktrees and subdirectories within the same git repo share one
//     auto memory directory" — the project hash is derived from the main repo root.
//   - However, Conductor workspaces (which are full clones, not git worktrees)
//     get separate memory directories since they have distinct git roots.
//
// Memory is machine-local: never touches git, never synced to cloud by Claude.
// That's exactly why ox memory import exists — to collect this local knowledge
// into the ledger for team-wide access.
//
// Future import types:
//   - openclaw: similar directory structure (MEMORY.md + associated topic files),
//     different discovery path (~/.openclaw/projects/<hash>/memory/)

// ProjectMemoryDir returns the Claude Code auto memory directory for a given git root.
// The path convention matches Claude's project-hash encoding: absolute path with
// "/" replaced by "-", under ~/.claude/projects/<hash>/memory/.
//
// Returns the directory path (may not exist if Claude hasn't written memory yet).
func ProjectMemoryDir(gitRoot string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}

	projectHash, err := ProjectHash(gitRoot)
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".claude", "projects", projectHash, "memory"), nil
}

// ProjectHash returns Claude's project hash for a given git root.
// The hash is the absolute path with "/" replaced by "-".
// Used to track source identity for merge-vs-rsync decisions in memory import.
//
// Examples:
//
//	/Users/ryan/Code/ox                              → -Users-ryan-Code-ox
//	/Users/ryan/conductor/workspaces/ox/san-diego-v1 → -Users-ryan-conductor-workspaces-ox-san-diego-v1
func ProjectHash(gitRoot string) (string, error) {
	absRoot, err := filepath.Abs(gitRoot)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	return strings.ReplaceAll(absRoot, string(filepath.Separator), "-"), nil
}

// ReadMemoryFiles walks the memory directory and reads all files.
// Returns map of relative path → content, preserving any subdirectory structure.
// e.g., {"MEMORY.md": bytes, "debugging.md": bytes}
//
// Skips hidden files (dotfiles) and non-regular files.
// Returns empty map (not error) if directory doesn't exist — Claude may not have
// written memory for this project yet.
func ReadMemoryFiles(memoryDir string) (map[string][]byte, error) {
	files := make(map[string][]byte)

	info, err := os.Stat(memoryDir)
	if os.IsNotExist(err) {
		return files, nil // no memory yet — not an error
	}
	if err != nil {
		return nil, fmt.Errorf("stat memory dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("memory path is not a directory: %s", memoryDir)
	}

	err = filepath.WalkDir(memoryDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		// skip hidden files/dirs (e.g., .DS_Store)
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil // descend into subdirectories
		}

		// only regular files
		if !d.Type().IsRegular() {
			return nil
		}

		relPath, err := filepath.Rel(memoryDir, path)
		if err != nil {
			return fmt.Errorf("relative path: %w", err)
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", relPath, err)
		}

		files[relPath] = content
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk memory dir: %w", err)
	}

	return files, nil
}
