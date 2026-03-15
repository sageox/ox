package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionResummaryBatchCmd = &cobra.Command{
	Use:    "resummary-batch [session-name...]",
	Short:  "Re-summarize sessions via the SageOx API",
	Long:   "Download raw.jsonl from LFS, call the summarize API, and push new summary.json to ledger.",
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
	ep := endpoint.GetForProject(projectRoot)

	for _, sessionName := range args {
		sessionPath := filepath.Join(sessionsDir, sessionName)
		if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "session not found: %s\n", sessionName)
			continue
		}

		if err := resummarySession(projectRoot, ledgerPath, sessionPath, sessionName, ep); err != nil {
			fmt.Fprintf(os.Stderr, "failed %s: %v\n", sessionName, err)
			continue
		}

		fmt.Printf("re-summarized %s\n", sessionName)
	}

	return nil
}

func resummarySession(projectRoot, ledgerPath, sessionPath, sessionName, ep string) error {
	// read meta.json for agent info and file OIDs
	meta, err := lfs.ReadSessionMeta(sessionPath)
	if err != nil {
		return fmt.Errorf("read meta.json: %w", err)
	}

	// ensure raw.jsonl is local (not just a pointer)
	rawPath := filepath.Join(sessionPath, ledgerFileRaw)
	if lfs.IsPointerFile(rawPath) {
		slog.Info("downloading raw.jsonl from LFS", "session", sessionName)
		if err := downloadFileFromLFS(projectRoot, sessionPath, meta, ledgerFileRaw); err != nil {
			return fmt.Errorf("download raw.jsonl: %w", err)
		}
		// restore pointer on any exit path to avoid leaving large content in ledger
		if ref, ok := meta.Files[ledgerFileRaw]; ok {
			defer func() {
				_ = lfs.WritePointerFile(rawPath, ref)
			}()
		}
	}

	// read raw.jsonl
	stored, err := session.ReadSessionFromPath(rawPath)
	if err != nil {
		return fmt.Errorf("read raw.jsonl: %w", err)
	}

	if len(stored.Entries) == 0 {
		return fmt.Errorf("empty session")
	}

	// convert map entries to session.Entry for the summarize API
	entries := convertStoredMapEntries(stored.Entries)

	// call the server-side summarize API
	slog.Info("calling summarize API", "session", sessionName, "entries", len(entries))
	result, err := session.Summarize(entries, meta.AgentID, meta.AgentType, meta.Model, ep)
	if err != nil {
		return fmt.Errorf("summarize API: %w", err)
	}

	// write summary.json
	summaryData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal summary: %w", err)
	}

	summaryPath := filepath.Join(sessionPath, "summary.json")
	if err := os.WriteFile(summaryPath, summaryData, 0644); err != nil {
		return fmt.Errorf("write summary.json: %w", err)
	}

	// update meta.json title
	if result.Title != "" {
		if err := lfs.UpdateMetaSummary(sessionPath, result.Title); err != nil {
			slog.Warn("failed to update meta.json title", "session", sessionName, "err", err)
		}
	}

	// commit and push summary.json (git-tracked, not LFS)
	pushResult := pushSummaryToLedger(summaryPath, sessionPath)
	if !pushResult.Success {
		return fmt.Errorf("push summary: %s", pushResult.Error)
	}

	return nil
}
