package memoryimport

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
)

// MergeFunc is the type-specific merge function.
// Each --type (claude, openclaw, etc.) provides its own merge implementation.
// Takes existing ledger files and new source files, returns merged output.
type MergeFunc func(ctx context.Context, existing, newFiles map[string][]byte) (map[string][]byte, error)

// ImportOptions configures a memory import operation.
//
// NOTE: All import orchestration is in this package (internal/memoryimport).
// cmd/ox/memory_import.go is ONLY cobra flag parsing and output formatting.
// internal/daemon/ is ONLY scheduling and mtime checking.
// Type-specific logic (discovery, merge) lives in the agent's package (e.g. internal/claude/).
type ImportOptions struct {
	LedgerPath  string            // resolved ledger git repo path
	UserID      string            // SageOx user identity
	AgentType   string            // agent type for ledger path (e.g. "claude", "openclaw")
	ProjectHash string            // agent's project hash for this workspace
	SourceDir   string            // path to local memory directory
	SourceFiles map[string][]byte // pre-read source files
	DryRun      bool              // show what would happen without writing
	CredRefresh func(string) error // credential refresh callback for push
	MergeFunc   MergeFunc         // type-specific merge (nil = copy only, no merge)
}

// ImportResult summarizes what happened during an import.
type ImportResult struct {
	Strategy     string // "copy", "merge"
	FilesWritten int
	BytesWritten int64
}

// ImportMemory is the single entry point for memory import.
// Both the CLI command and daemon timer call this function.
// Merge strategy, state tracking, and ledger writing logic lives here.
// Type-specific merge is provided via opts.MergeFunc.
func ImportMemory(ctx context.Context, opts ImportOptions) (*ImportResult, error) {
	if err := validateOpts(opts); err != nil {
		return nil, err
	}

	if len(opts.SourceFiles) == 0 {
		return &ImportResult{Strategy: "copy"}, nil
	}

	// load import state to decide merge strategy
	state := LoadImportState(opts.LedgerPath)
	needsMerge := state.NeedsMerge(opts.ProjectHash)

	// determine target directory in ledger: data/{agent_type}/memory/{user_id}/
	targetDir := filepath.Join(opts.LedgerPath, "data", opts.AgentType, "memory", opts.UserID)

	var filesToWrite map[string][]byte
	var strategy string

	if needsMerge && opts.MergeFunc != nil {
		strategy = "merge"

		// read existing files from ledger
		existing, err := readLedgerMemory(targetDir)
		if err != nil {
			return nil, fmt.Errorf("read existing ledger memory: %w", err)
		}

		if len(existing) == 0 {
			// nothing to merge with — straight copy
			filesToWrite = opts.SourceFiles
			strategy = "copy"
		} else {
			merged, err := opts.MergeFunc(ctx, existing, opts.SourceFiles)
			if err != nil {
				return nil, fmt.Errorf("merge memory: %w", err)
			}
			filesToWrite = merged
		}
	} else {
		strategy = "copy"
		filesToWrite = opts.SourceFiles
	}

	if opts.DryRun {
		result := &ImportResult{Strategy: strategy}
		for _, content := range filesToWrite {
			result.FilesWritten++
			result.BytesWritten += int64(len(content))
		}
		return result, nil
	}

	// remove stale files before writing (merge may drop or consolidate files)
	if strategy == "merge" {
		if err := removeStaleFiles(targetDir, filesToWrite); err != nil {
			slog.Debug("remove stale files failed", "error", err)
		}
	}

	// write files to ledger
	result, err := writeMemoryFiles(targetDir, filesToWrite)
	if err != nil {
		return nil, fmt.Errorf("write memory files: %w", err)
	}
	result.Strategy = strategy

	// commit and push
	if err := commitAndPush(ctx, opts); err != nil {
		return result, fmt.Errorf("commit/push: %w", err)
	}

	// update per-source state (after successful push)
	sourceMtime, _ := SourceMtime(opts.SourceDir)
	state.RecordImport(opts.ProjectHash, sourceMtime)
	if err := SaveImportState(opts.LedgerPath, state); err != nil {
		slog.Debug("save import state failed", "error", err)
	}

	return result, nil
}

func validateOpts(opts ImportOptions) error {
	if opts.LedgerPath == "" {
		return fmt.Errorf("ledger path is required")
	}
	if opts.UserID == "" {
		return fmt.Errorf("user ID is required")
	}
	if opts.AgentType == "" {
		return fmt.Errorf("agent type is required")
	}
	if opts.ProjectHash == "" {
		return fmt.Errorf("project hash is required")
	}
	return nil
}

// readLedgerMemory reads existing memory files from the ledger target directory.
// Returns empty map if directory doesn't exist.
func readLedgerMemory(targetDir string) (map[string][]byte, error) {
	files := make(map[string][]byte)

	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		return files, nil
	}

	err := filepath.WalkDir(targetDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}

		relPath, err := filepath.Rel(targetDir, path)
		if err != nil {
			return err
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", relPath, err)
		}

		files[relPath] = content
		return nil
	})
	if err != nil {
		return nil, err
	}

	return files, nil
}

// removeStaleFiles deletes files in targetDir that are not in the new file set.
// This prevents ghost files from accumulating after a merge consolidates or drops files.
func removeStaleFiles(targetDir string, newFiles map[string][]byte) error {
	existing, err := readLedgerMemory(targetDir)
	if err != nil {
		return err
	}
	for name := range existing {
		if _, keep := newFiles[name]; !keep {
			if err := os.Remove(filepath.Join(targetDir, name)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove stale file %s: %w", name, err)
			}
		}
	}
	return nil
}

// writeMemoryFiles writes the given files to the target directory.
// Creates the directory if it doesn't exist. Overwrites existing files.
// Validates filenames to prevent path traversal from untrusted sources (e.g. LLM output).
func writeMemoryFiles(targetDir string, files map[string][]byte) (*ImportResult, error) {
	result := &ImportResult{}
	cleanTargetDir := filepath.Clean(targetDir) + string(filepath.Separator)

	for relPath, content := range files {
		cleaned := filepath.Clean(relPath)
		if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
			return nil, fmt.Errorf("invalid filename (path traversal): %s", relPath)
		}
		fullPath := filepath.Join(targetDir, cleaned)
		if !strings.HasPrefix(fullPath, cleanTargetDir) {
			return nil, fmt.Errorf("path escapes target directory: %s", relPath)
		}

		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return nil, fmt.Errorf("create directory for %s: %w", relPath, err)
		}

		if err := os.WriteFile(fullPath, content, 0644); err != nil {
			return nil, fmt.Errorf("write %s: %w", relPath, err)
		}

		result.FilesWritten++
		result.BytesWritten += int64(len(content))
	}

	return result, nil
}

// commitAndPush stages memory files, commits, and pushes to remote.
func commitAndPush(ctx context.Context, opts ImportOptions) error {
	ledger := opts.LedgerPath

	// ensure .gitignore is in place before commit
	gitserver.EnsureGitignoreBeforeCommit(ledger)

	// stage the data/{agent_type}/memory/{user_id}/ directory
	dataDir := filepath.Join("data", opts.AgentType, "memory", opts.UserID)
	addCtx, addCancel := context.WithTimeout(ctx, 60*time.Second)
	_, err := gitutil.RunGit(addCtx, ledger, "add", "--sparse", dataDir)
	addCancel()
	if err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	// also stage .sageox/.gitignore if it was created/updated by EnsureGitignoreBeforeCommit
	gitignorePath := filepath.Join(sageoxDirName, ".gitignore")
	addGiCtx, addGiCancel := context.WithTimeout(ctx, 10*time.Second)
	_, _ = gitutil.RunGit(addGiCtx, ledger, "add", "--sparse", gitignorePath)
	addGiCancel()

	// commit
	commitMsg := fmt.Sprintf("memory: import %s %s", opts.AgentType, opts.UserID)
	commitCtx, commitCancel := context.WithTimeout(ctx, 30*time.Second)
	out, err := gitutil.RunGit(commitCtx, ledger, "commit", "--no-verify", "-m", commitMsg)
	commitCancel()
	if err != nil {
		if strings.Contains(out, "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit: %w", err)
	}

	// push with retry
	return gitutil.PushWithRetry(ctx, ledger, gitutil.PushOpts{
		PrePush: opts.CredRefresh,
	})
}

// SourceMtime returns the latest mtime across all files in a directory.
// Used by daemon to check if source has changed since last import.
func SourceMtime(dir string) (time.Time, error) {
	var latest time.Time

	info, err := os.Stat(dir)
	if err != nil {
		return latest, err
	}
	if !info.IsDir() {
		return latest, fmt.Errorf("not a directory: %s", dir)
	}

	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // skip files we can't stat
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})

	return latest, err
}

// DryRunSummary returns a human-readable summary for dry-run mode.
func DryRunSummary(result *ImportResult, sourceFiles map[string][]byte) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Strategy: %s\n", result.Strategy)
	fmt.Fprintf(&b, "Files: %d\n", result.FilesWritten)
	fmt.Fprintf(&b, "Bytes: %d\n", result.BytesWritten)
	fmt.Fprintf(&b, "\nFiles to import:\n")
	for name, content := range sourceFiles {
		fmt.Fprintf(&b, "  %s (%d bytes)\n", name, len(content))
	}
	return b.String()
}
