package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/lfs"
	"github.com/spf13/cobra"
)

var sessionResummaryBatchCmd = &cobra.Command{
	Use:    "resummary-batch [session-name...]",
	Short:  "Re-summarize sessions locally via daemon",
	Long:   "Hydrate raw.jsonl from LFS if needed, then signal the daemon to re-summarize and push artifacts.",
	Hidden: true,
	Args:   cobra.MinimumNArgs(1),
	RunE:   runSessionResummaryBatch,
}

// sessionHydrateLFSCmd downloads raw.jsonl from LFS for sessions that only have pointer files.
var sessionHydrateLFSCmd = &cobra.Command{
	Use:    "hydrate-lfs [session-name...]",
	Short:  "Download raw.jsonl from LFS to disk",
	Hidden: true,
	Args:   cobra.MinimumNArgs(1),
	RunE:   runSessionHydrateLFS,
}

func init() {
	sessionCmd.AddCommand(sessionResummaryBatchCmd)
	sessionCmd.AddCommand(sessionHydrateLFSCmd)
}

func runSessionHydrateLFS(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("find project root: %w", err)
	}

	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return err
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")

	for _, sessionName := range args {
		sessionPath := filepath.Join(sessionsDir, sessionName)
		rawPath := filepath.Join(sessionPath, ledgerFileRaw)

		if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "%s: session not found\n", sessionName)
			continue
		}
		if _, err := os.Stat(rawPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "%s: missing %s\n", sessionName, ledgerFileRaw)
			continue
		}

		if !lfs.IsPointerFile(rawPath) {
			fmt.Printf("%s: already hydrated (%d bytes)\n", sessionName, fileSize(rawPath))
			continue
		}

		meta, err := lfs.ReadSessionMeta(sessionPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: read meta.json: %v\n", sessionName, err)
			continue
		}

		if err := downloadFileFromLFS(projectRoot, sessionPath, meta, ledgerFileRaw); err != nil {
			fmt.Fprintf(os.Stderr, "%s: download failed: %v\n", sessionName, err)
			continue
		}

		fmt.Printf("%s: hydrated (%d bytes)\n", sessionName, fileSize(rawPath))
	}

	return nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func runSessionResummaryBatch(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("find project root: %w", err)
	}

	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return err
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	var queued int

	for _, sessionName := range args {
		sessionPath := filepath.Join(sessionsDir, sessionName)
		if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "session not found: %s\n", sessionName)
			continue
		}

		if err := resummarySession(projectRoot, ledgerPath, sessionPath, sessionName); err != nil {
			fmt.Fprintf(os.Stderr, "failed %s: %v\n", sessionName, err)
			continue
		}

		fmt.Printf("queued %s for re-summarization\n", sessionName)
		queued++
	}

	if queued > 0 {
		fmt.Printf("\n%d session(s) queued. The daemon will process them in the background.\n", queued)
	}

	return nil
}

func resummarySession(projectRoot, ledgerPath, sessionPath, sessionName string) error {
	rawPath := filepath.Join(sessionPath, ledgerFileRaw)
	if _, err := os.Stat(rawPath); os.IsNotExist(err) {
		return fmt.Errorf("missing %s", ledgerFileRaw)
	}

	// hydrate raw.jsonl from LFS if needed so daemon can read it
	if lfs.IsPointerFile(rawPath) {
		meta, err := lfs.ReadSessionMeta(sessionPath)
		if err != nil {
			return fmt.Errorf("read meta.json: %w", err)
		}
		slog.Info("downloading raw.jsonl from LFS", "session", sessionName)
		if err := downloadFileFromLFS(projectRoot, sessionPath, meta, ledgerFileRaw); err != nil {
			return fmt.Errorf("download raw.jsonl: %w", err)
		}
	}

	// signal daemon to finalize (reuses existing anti-entropy pipeline)
	if err := signalDaemonSessionFinalize(sessionName, ledgerPath, "", projectRoot); err != nil {
		return fmt.Errorf("daemon not reachable (is it running?): %w", err)
	}

	return nil
}
