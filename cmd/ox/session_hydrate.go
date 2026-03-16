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

var sessionHydrateCmd = &cobra.Command{
	Use:   "download <session-name>",
	Short: "Download session content from the ledger",
	Long: `Download session content from the ledger.

Sessions authored by other team members arrive as stubs (metadata only)
via ledger sync. This command downloads the actual content files (raw.jsonl,
summary.md, session.md, session.html) from the ledger.

Example:
  ox session download 2026-01-06T14-32-ryan-Ox7f3a`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		projectRoot, err := requireProjectRoot()
		if err != nil {
			return err
		}

		ledgerPath, err := resolveLedgerPath()
		if err != nil {
			return err
		}

		sessionsDir := filepath.Join(ledgerPath, "sessions")
		return hydrateFromLedger(projectRoot, sessionsDir, args[0])
	},
}

// resolveSessionInDir resolves a partial session name (e.g. agent ID suffix)
// to the full session directory name by scanning the given directory.
func resolveSessionInDir(dir, name string) (string, error) {
	// exact match
	if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
		return name, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return name, nil // let caller handle the "not found"
	}

	var matches []string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), "-"+name) {
			matches = append(matches, e.Name())
		}
	}

	switch len(matches) {
	case 0:
		return name, nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous session name %q matches %d sessions: %s",
			name, len(matches), strings.Join(matches, ", "))
	}
}

// hydrateFromLedger downloads content files for a session from the ledger.
func hydrateFromLedger(projectRoot, sessionsDir, nameArg string) error {
	sessionName, err := resolveSessionInDir(sessionsDir, nameArg)
	if err != nil {
		return err
	}

	sessionPath := filepath.Join(sessionsDir, sessionName)

	// read meta.json
	meta, err := lfs.ReadSessionMeta(sessionPath)
	if err != nil {
		return fmt.Errorf("cannot download %q: %w\nEnsure the session exists and has been synced from the ledger", sessionName, err)
	}

	// check if already local
	status := lfs.CheckHydrationStatus(sessionPath, meta)
	if status == lfs.HydrationStatusHydrated {
		fmt.Printf("Session %s already has local content\n", sessionName)
		return nil
	}

	// collect OIDs that need downloading
	var batchObjects []lfs.BatchObject
	oidToFiles := make(map[string][]string)

	for filename, ref := range meta.Files {
		filePath := filepath.Join(sessionPath, filename)
		if _, err := os.Stat(filePath); err == nil {
			continue
		}
		bareOID := ref.BareOID()
		batchObjects = append(batchObjects, lfs.BatchObject{
			OID:  bareOID,
			Size: ref.Size,
		})
		oidToFiles[bareOID] = append(oidToFiles[bareOID], filename)
	}

	if len(batchObjects) == 0 {
		fmt.Printf("Session %s has no files to download\n", sessionName)
		return nil
	}

	// get LFS client
	client, err := getLFSClient(projectRoot)
	if err != nil {
		return hydrateHint(err)
	}

	// request download URLs
	resp, err := client.BatchDownload(batchObjects)
	if err != nil {
		return hydrateHint(err)
	}

	// download all blobs in parallel
	results := lfs.DownloadAll(resp, 4)

	// write downloads to session dir and collect errors
	var hydratedCount int
	var errors []string
	for _, r := range results {
		if r.Error != nil {
			errors = append(errors, r.Error.Error())
			continue
		}

		computedOID := lfs.ComputeOID(r.Content)
		if computedOID != r.OID {
			errors = append(errors, fmt.Sprintf("SHA256 mismatch for OID %s: got %s", r.OID, computedOID))
			continue
		}

		for _, filename := range oidToFiles[r.OID] {
			filePath := filepath.Join(sessionPath, filename)
			if err := os.WriteFile(filePath, r.Content, 0644); err != nil {
				return fmt.Errorf("write %s: %w", filename, err)
			}
			hydratedCount++
			slog.Debug("downloaded file", "session", sessionName, "file", filename)
		}
	}

	if len(errors) > 0 {
		fmt.Printf("Downloaded %d files for session %s (%d failed)\n", hydratedCount, sessionName, len(errors))
		return fmt.Errorf("download errors:\n  %s", strings.Join(errors, "\n  "))
	}

	fmt.Printf("Downloaded %d files for session %s\n", hydratedCount, sessionName)
	return nil
}

// hydrateHint wraps an error with an actionable suggestion based on the failure type.
func hydrateHint(err error) error {
	msg := err.Error()

	switch {
	case strings.Contains(msg, "no git credentials found") ||
		strings.Contains(msg, "no auth token") ||
		strings.Contains(msg, "empty token"):
		return fmt.Errorf("%w\n\nFix: run 'ox login' to refresh credentials", err)

	case strings.Contains(msg, "ledger not ready") ||
		strings.Contains(msg, "no repo_id"):
		return fmt.Errorf("%w\n\nFix: run 'ox sync' to set up the ledger, or 'ox doctor --fix' if that fails", err)

	case strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded"):
		return fmt.Errorf("%w\n\nFix: check your network connection and try again", err)

	case strings.Contains(msg, "HTTP 401") || strings.Contains(msg, "HTTP 403"):
		return fmt.Errorf("%w\n\nFix: run 'ox login' — your credentials may have expired", err)

	case strings.Contains(msg, "HTTP 404"):
		return fmt.Errorf("%w\n\nFix: the ledger may not exist yet — run 'ox sync' or 'ox doctor --fix'", err)

	default:
		return fmt.Errorf("%w\n\nRun 'ox doctor' to diagnose the issue", err)
	}
}
