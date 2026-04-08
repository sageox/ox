package main

// NOTE: All import orchestration is in internal/memoryimport/.
// This file is ONLY cobra flag parsing and output formatting.
// Type-specific logic (discovery, merge) lives in agent packages (e.g. internal/claude/).
// Do not add import/merge/state logic here — it must be shared with the daemon.

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/claude"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/memoryimport"
	"github.com/spf13/cobra"
)

var (
	memoryImportType   string
	memoryImportSource string
	memoryImportDryRun bool
)

var memoryImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import AI coworker memory into the ledger",
	Long: `Import memory from an AI coworker's local memory system into the ledger.

Each --type has its own importer with specific discovery rules, path conventions,
and merge strategy. There is no generic importer.

Examples:
  ox memory import --type=claude              # auto-detect from git root
  ox memory import --type=claude --dry-run    # preview without writing
  ox memory import --type=claude --source ~/.claude/projects/-other-workspace/memory/`,
	RunE: runMemoryImport,
}

func init() {
	memoryImportCmd.Flags().StringVar(&memoryImportType, "type", "", "memory system to import from (required, e.g. claude)")
	memoryImportCmd.Flags().StringVar(&memoryImportSource, "source", "", "explicit path to memory directory (overrides auto-detect)")
	memoryImportCmd.Flags().BoolVar(&memoryImportDryRun, "dry-run", false, "show what would be imported without writing")
}

func runMemoryImport(cmd *cobra.Command, args []string) error {
	// validate --type
	if memoryImportType == "" {
		return fmt.Errorf("--type is required. Supported types: claude")
	}
	if memoryImportType != "claude" {
		return fmt.Errorf("unsupported memory type %q. Supported types: claude", memoryImportType)
	}

	// resolve project root
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not in a git repository")
	}

	// check memory_import config flag (default: manual, can be disabled)
	if !config.IsMemoryImportEnabled(gitRoot) {
		return fmt.Errorf("memory import is disabled for this project.\n" +
			"Enable it in .sageox/config.json:\n" +
			"  \"memory_import\": \"manual\"   (CLI only, default)\n" +
			"  \"memory_import\": \"auto\"     (daemon auto-import)")
	}

	// resolve endpoint and user ID
	ep := endpoint.GetForProject(gitRoot)
	if ep == "" {
		return fmt.Errorf("no endpoint configured (run 'ox init' first)")
	}

	userID := auth.GetUserID(ep)
	if userID == "" {
		return fmt.Errorf("not authenticated (run 'ox login' first)")
	}

	// resolve source directory (Claude-specific discovery)
	sourceDir := memoryImportSource
	if sourceDir == "" {
		var err error
		sourceDir, err = claude.ProjectMemoryDir(gitRoot)
		if err != nil {
			return fmt.Errorf("resolve memory directory: %w", err)
		}
	}

	// read source files
	sourceFiles, err := claude.ReadMemoryFiles(sourceDir)
	if err != nil {
		return fmt.Errorf("read memory files: %w", err)
	}
	if len(sourceFiles) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No memory files found. Nothing to import.")
		return nil
	}

	// resolve project hash (Claude-specific hash convention)
	projectHash, err := claude.ProjectHash(gitRoot)
	if err != nil {
		return fmt.Errorf("compute project hash: %w", err)
	}
	// if explicit --source, derive hash from source path parent
	if memoryImportSource != "" {
		if h, err := projectHashFromSource(memoryImportSource); err != nil {
			slog.Debug("could not derive project hash from source, using git root hash", "error", err)
		} else {
			projectHash = h
		}
	}

	// resolve ledger
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return fmt.Errorf("resolve ledger: %w", err)
	}

	// credential refresh callback
	credRefresh := func(repoPath string) error {
		return gitserver.RefreshRemoteCredentials(repoPath, ep)
	}

	// delegate to shared package — type-specific merge provided by claude package
	result, err := memoryimport.ImportMemory(context.Background(), memoryimport.ImportOptions{
		LedgerPath:  ledgerPath,
		UserID:      userID,
		AgentType:   "claude",
		ProjectHash: projectHash,
		SourceDir:   sourceDir,
		SourceFiles: sourceFiles,
		DryRun:      memoryImportDryRun,
		CredRefresh: credRefresh,
		MergeFunc:   claude.MergeMemory,
	})
	if err != nil {
		return err
	}

	// output
	if memoryImportDryRun {
		fmt.Fprintln(cmd.OutOrStdout(), memoryimport.DryRunSummary(result, sourceFiles))
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Imported %d files (%d bytes) via %s\n",
			result.FilesWritten, result.BytesWritten, result.Strategy)
	}

	return nil
}

// projectHashFromSource extracts a project hash from an explicit --source path.
// Claude memory paths follow: ~/.claude/projects/{hash}/memory/
// We extract the {hash} component.
func projectHashFromSource(sourcePath string) (string, error) {
	// normalize: the path should end with /memory or /memory/
	// parent of memory/ is the project dir, whose name is the hash
	dir := sourcePath
	for {
		base := filepath.Base(dir)
		if base == "memory" {
			return filepath.Base(filepath.Dir(dir)), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("cannot extract project hash from source path %q (expected .../projects/{hash}/memory/)", sourcePath)
}
