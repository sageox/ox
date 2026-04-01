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

Content is stored in the ledger cache (.sageox/cache/sessions/) rather than
overwriting pointer files in-place, keeping the git working tree clean.

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
		return hydrateFromLedger(projectRoot, sessionsDir, args[0], false)
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
// Content is streamed to the ledger cache (.sageox/cache/sessions/) using
// atomic temp+rename writes, preserving pointer files in the git working tree.
// When quiet is true, progress messages are suppressed (for JSON output contexts).
func hydrateFromLedger(projectRoot, sessionsDir, nameArg string, quiet bool) error {
	sessionName, err := resolveSessionInDir(sessionsDir, nameArg)
	if err != nil {
		return err
	}

	sessionPath := filepath.Join(sessionsDir, sessionName)
	slog.Debug("hydrate: resolved session", "session_dir", sessionPath)

	// read meta.json
	meta, err := lfs.ReadSessionMeta(sessionPath)
	if err != nil {
		return fmt.Errorf("cannot download %q: %w\nEnsure the session exists and has been synced from the ledger", sessionName, err)
	}
	slog.Debug("hydrate: read meta.json", "file_count", len(meta.Files))
	for fn, ref := range meta.Files {
		slog.Debug("hydrate: manifest entry", "file", fn, "oid", ref.OID[:20]+"…", "size", ref.Size)
	}

	// cache base: <ledger>/.sageox/cache/sessions/<sessionName>/
	ledgerRoot := filepath.Dir(sessionsDir)
	cacheBase := filepath.Join(ledgerRoot, ".sageox", "cache", "sessions", sessionName)
	slog.Debug("hydrate: cache target", "cache_dir", cacheBase)

	// check if already hydrated (in-place or cached)
	status := lfs.CheckHydrationStatusWithCache(sessionPath, cacheBase, meta)
	slog.Info("hydrate: checked status", "session", sessionName, "status", status)
	if status == lfs.HydrationStatusHydrated {
		if !quiet {
			fmt.Printf("Session %s already has local content\n", sessionName)
		}
		return nil
	}

	// collect OIDs that need downloading (not cached and not present in-place)
	var batchObjects []lfs.BatchObject
	type pendingFile struct {
		filename  string
		cachePath string
		bareOID   string
	}
	oidToFiles := make(map[string][]pendingFile)

	for filename, ref := range meta.Files {
		cachePath := filepath.Join(cacheBase, filename)

		// skip if cached (size-based validation, same as ox fetch)
		if isCacheHit(cachePath, ref.Size) {
			slog.Debug("hydrate: cache hit", "file", filename, "path", cachePath)
			continue
		}

		// skip if present in-place as real content (legacy hydration)
		inPlacePath := filepath.Join(sessionPath, filename)
		if _, err := os.Stat(inPlacePath); err == nil && !lfs.IsPointerFile(inPlacePath) {
			slog.Debug("hydrate: in-place content", "file", filename, "path", inPlacePath)
			continue
		}

		// log what's at the in-place path
		if info, statErr := os.Stat(inPlacePath); statErr == nil {
			slog.Info("hydrate: pointer stub", "file", filename, "path", inPlacePath, "bytes", info.Size())
		} else {
			slog.Info("hydrate: missing", "file", filename, "path", inPlacePath)
		}
		slog.Info("hydrate: will download", "file", filename, "dest", cachePath)

		bareOID := ref.BareOID()
		batchObjects = append(batchObjects, lfs.BatchObject{
			OID:  bareOID,
			Size: ref.Size,
		})
		oidToFiles[bareOID] = append(oidToFiles[bareOID], pendingFile{
			filename:  filename,
			cachePath: cachePath,
			bareOID:   bareOID,
		})
	}

	if len(batchObjects) == 0 {
		if !quiet {
			fmt.Printf("Session %s has no files to download\n", sessionName)
		}
		return nil
	}

	slog.Info("hydrate: batch request", "objects", len(batchObjects))

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
	slog.Info("hydrate: batch response", "objects", len(resp.Objects))

	// stream each object to cache with atomic writes
	var hydratedCount int
	var errors []string

	for _, obj := range resp.Objects {
		if obj.Error != nil {
			errors = append(errors, fmt.Sprintf("server error %d for %s: %s", obj.Error.Code, obj.OID, obj.Error.Message))
			continue
		}
		if obj.Actions == nil || obj.Actions.Download == nil {
			errors = append(errors, fmt.Sprintf("no download action for OID %s", obj.OID))
			continue
		}

		files, ok := oidToFiles[obj.OID]
		if !ok {
			continue
		}

		for _, pf := range files {
			slog.Info("hydrate: downloading", "file", pf.filename, "dest", pf.cachePath)
			if err := downloadToCache(obj.Actions.Download, pf.cachePath, pf.bareOID); err != nil {
				slog.Error("hydrate: download failed", "file", pf.filename, "error", err)
				errors = append(errors, fmt.Sprintf("%s: %v", pf.filename, err))
				continue
			}
			if info, statErr := os.Stat(pf.cachePath); statErr == nil {
				slog.Info("hydrate: downloaded", "file", pf.filename, "bytes", info.Size())
			}
			hydratedCount++
		}
	}

	if len(errors) > 0 {
		if !quiet {
			fmt.Printf("Downloaded %d files for session %s (%d failed)\n", hydratedCount, sessionName, len(errors))
		}
		return fmt.Errorf("download errors:\n  %s", strings.Join(errors, "\n  "))
	}

	if !quiet {
		fmt.Printf("Downloaded %d files for session %s\n", hydratedCount, sessionName)
	}
	return nil
}

// downloadToCache streams an LFS object to a cache path using atomic temp+rename.
func downloadToCache(action *lfs.Action, cachePath, oid string) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}

	tmpPath := cachePath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if err := lfs.DownloadToFile(action, f, true, oid); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("download failed: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, cachePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename to final path: %w", err)
	}

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
