package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/lfs"
	"github.com/spf13/cobra"
)

// sessionMigrateLFSCmd migrates sessions from direct-git content to LFS pointer files.
// Pre-LFS sessions have content committed directly to git, which is invisible to the web UI.
// This command uploads content to LFS blob storage and replaces git content with pointer files.
var sessionMigrateLFSCmd = &cobra.Command{
	Use:    "migrate-lfs [session-name...]",
	Short:  "Migrate sessions from direct-git to LFS storage",
	Long:   "Upload session content to LFS blob storage and replace git content with pointer files. Required for web UI display.",
	Hidden: true,
	Args:   cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		projectRoot, err := findProjectRoot()
		if err != nil {
			return fmt.Errorf("find project root: %w", err)
		}

		ledgerPath, err := resolveLedgerPath()
		if err != nil {
			return err
		}

		sessionsDir := filepath.Join(ledgerPath, "sessions")

		var failed []string
		for _, sessionName := range args {
			sessionPath := filepath.Join(sessionsDir, sessionName)
			if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "session not found: %s\n", sessionName)
				failed = append(failed, sessionName)
				continue
			}

			if err := migrateSessionToLFS(projectRoot, ledgerPath, sessionPath, sessionName); err != nil {
				fmt.Fprintf(os.Stderr, "failed to migrate %s: %v\n", sessionName, err)
				failed = append(failed, sessionName)
				continue
			}

			fmt.Printf("migrated %s to LFS\n", sessionName)
		}

		if len(failed) > 0 {
			return fmt.Errorf("migrate-lfs failed for %d session(s): %s", len(failed), strings.Join(failed, ", "))
		}
		return nil
	},
}

func migrateSessionToLFS(projectRoot, ledgerPath, sessionPath, sessionName string) error {
	// check if already migrated (all content files are pointers)
	contentFiles := []string{ledgerFileRaw, ledgerFileSummaryMD, ledgerFileSessionMD, ledgerFilePlan}
	allPointers := true
	hasContent := false
	for _, name := range contentFiles {
		p := filepath.Join(sessionPath, name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			continue
		}
		hasContent = true
		if !lfs.IsPointerFile(p) {
			allPointers = false
		}
	}

	if !hasContent {
		return fmt.Errorf("no content files found")
	}
	if allPointers {
		slog.Info("already migrated", "session", sessionName)
		return nil
	}

	// upload content to LFS (skips files that are already pointers)
	fileRefs, err := uploadSessionLFS(projectRoot, sessionPath)
	if err != nil {
		return fmt.Errorf("LFS upload: %w", err)
	}

	if len(fileRefs) == 0 {
		return fmt.Errorf("no files uploaded")
	}

	// replace content files with LFS pointer files.
	// AssertUploaded: uploadSessionLFS uploaded these blobs at line above.
	_, err = lfs.WritePointerFiles(sessionPath, lfs.AssertUploadedManifest(fileRefs))
	if err != nil {
		return fmt.Errorf("write pointer files: %w", err)
	}

	slog.Info("wrote pointer files", "session", sessionName, "count", len(fileRefs))

	// ensure .gitignore is up to date
	if err := ensureSessionsGitignore(filepath.Join(ledgerPath, "sessions")); err != nil {
		slog.Warn("gitignore update failed", "error", err)
	}

	// commit and push
	if err := commitAndPushLedger(ledgerPath, sessionName); err != nil {
		return fmt.Errorf("commit/push: %w", err)
	}

	return nil
}

func init() {
	sessionCmd.AddCommand(sessionMigrateLFSCmd)
}
