package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSessionUploadRetry,
		Name:        "session upload retry",
		Category:    "Sessions",
		FixLevel:    FixLevelAuto,
		Description: "Retries failed session uploads from cache to ledger",
		Run:         func(_ bool) checkResult { return checkSessionUploadRetry() },
	})
}

// orphanedSession represents a completed session in cache that never reached the ledger.
type orphanedSession struct {
	SessionName string
	CachePath   string             // full path to cache session dir
	Meta        *session.StoreMeta // from raw.jsonl header
	EntryCount  int                // from raw.jsonl footer
}

// checkSessionUploadRetry finds sessions in cache that failed to upload and retries them.
func checkSessionUploadRetry() checkResult {
	const name = "session upload retry"

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "no git root", "")
	}

	if !config.IsInitialized(gitRoot) {
		return SkippedCheck(name, "not initialized", "")
	}

	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger", "")
	}

	orphans, err := findOrphanedSessions(gitRoot, ledgerPath)
	if err != nil {
		slog.Debug("session upload retry: scan error", "error", err)
		return SkippedCheck(name, "scan error", err.Error())
	}

	if len(orphans) == 0 {
		return PassedCheck(name, "no pending uploads")
	}

	// retry each orphaned session
	var succeeded, failed int
	for _, orphan := range orphans {
		if err := retrySessionUpload(gitRoot, ledgerPath, orphan); err != nil {
			slog.Warn("session upload retry failed", "session", orphan.SessionName, "error", err)
			failed++
		} else {
			// all files copied and committed to ledger — prune local cache
			if err := os.RemoveAll(orphan.CachePath); err != nil {
				slog.Debug("prune session cache after retry", "dir", orphan.CachePath, "error", err)
			}
			succeeded++
		}
	}

	if failed > 0 && succeeded == 0 {
		return WarningCheck(name,
			fmt.Sprintf("%d/%d retry failed", failed, len(orphans)),
			"check auth and network, then run ox doctor again")
	}

	if failed > 0 {
		return WarningCheck(name,
			fmt.Sprintf("retried %d, %d failed", succeeded, failed),
			"run ox doctor again to retry remaining")
	}

	return PassedCheck(name, fmt.Sprintf("retried %d session(s)", succeeded))
}

// findOrphanedSessions scans the session cache for completed sessions missing from the ledger.
func findOrphanedSessions(projectRoot, ledgerPath string) ([]orphanedSession, error) {
	repoID := getRepoIDOrDefault(projectRoot)
	contextPath := session.GetContextPath(repoID)
	if contextPath == "" {
		return nil, nil
	}

	cacheSessionsDir := filepath.Join(contextPath, "sessions")
	entries, err := os.ReadDir(cacheSessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cache sessions: %w", err)
	}

	var orphans []orphanedSession
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		sessionName := entry.Name()
		sessionDir := filepath.Join(cacheSessionsDir, sessionName)

		// skip legacy subdirectories
		if sessionName == "raw" || sessionName == "events" {
			continue
		}

		// check if still recording (.recording.json present)
		recordingPath := filepath.Join(sessionDir, ".recording.json")
		if _, err := os.Stat(recordingPath); err == nil {
			// read recording state to check for StopIncomplete
			recData, readErr := os.ReadFile(recordingPath)
			if readErr != nil {
				continue // can't read, skip
			}
			var recState session.RecordingState
			if json.Unmarshal(recData, &recState) != nil {
				continue // corrupt, skip
			}
			if !recState.StopIncomplete {
				continue // genuinely active recording, skip
			}
			// StopIncomplete: stop was attempted but session file was empty.
			// Clear the recording state so this session can be recovered.
			slog.Info("clearing stop-incomplete recording for retry", "session", sessionName, "agent_id", recState.AgentID)
			_ = os.Remove(recordingPath)
			// also clean up lock files
			lockFiles, _ := filepath.Glob(filepath.Join(sessionDir, "*.lock"))
			for _, lf := range lockFiles {
				_ = os.Remove(lf)
			}
		}

		// skip if no raw.jsonl (corrupt/empty)
		rawPath := filepath.Join(sessionDir, ledgerFileRaw)
		if _, err := os.Stat(rawPath); os.IsNotExist(err) {
			continue
		}

		// skip if already uploaded (meta.json exists in ledger)
		ledgerSessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
		if _, err := os.Stat(filepath.Join(ledgerSessionDir, "meta.json")); err == nil {
			continue
		}

		// parse header metadata
		meta, entryCount, err := readCacheSessionMeta(rawPath)
		if err != nil {
			slog.Debug("skip orphan: bad header", "session", sessionName, "error", err)
			continue
		}

		orphans = append(orphans, orphanedSession{
			SessionName: sessionName,
			CachePath:   sessionDir,
			Meta:        meta,
			EntryCount:  entryCount,
		})
	}

	return orphans, nil
}

// readCacheSessionMeta reads the header and footer from a raw.jsonl file.
// Returns the StoreMeta from the header and entry_count from the footer.
func readCacheSessionMeta(rawPath string) (*session.StoreMeta, int, error) {
	f, err := os.Open(rawPath)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("not a regular file: %s", rawPath)
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024) // 256KB line buffer

	// read first line (header)
	if !scanner.Scan() {
		return nil, 0, fmt.Errorf("empty file")
	}

	var header map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return nil, 0, fmt.Errorf("parse header: %w", err)
	}

	// extract metadata from header
	metaRaw, ok := header["metadata"]
	if !ok {
		return nil, 0, fmt.Errorf("no metadata in header")
	}

	metaMap, ok := metaRaw.(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("metadata is not a map")
	}

	meta := session.ParseStoreMeta(metaMap)

	// read to last line for footer entry_count
	var lastLine []byte
	for scanner.Scan() {
		lastLine = scanner.Bytes()
	}

	entryCount := 0
	if len(lastLine) > 0 {
		var footer map[string]any
		if json.Unmarshal(lastLine, &footer) == nil {
			if v, ok := footer["entry_count"].(float64); ok {
				entryCount = int(v)
			}
		}
	}

	return meta, entryCount, nil
}

// retrySessionUpload copies session files from cache to ledger, uploads to LFS,
// writes meta.json, and commits+pushes. This is the recovery path for sessions
// where phase 2 (ledger upload) failed during session stop. The cache always has
// the authoritative copy; raw.jsonl is the critical file from which all others
// can be regenerated.
func retrySessionUpload(projectRoot, ledgerPath string, orphan orphanedSession) error {
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	sessionDir := filepath.Join(sessionsDir, orphan.SessionName)

	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	// validate raw.jsonl integrity before uploading — skip corrupted files
	rawSrc := filepath.Join(orphan.CachePath, ledgerFileRaw)
	if err := validateRawJSONLHeader(rawSrc); err != nil {
		return fmt.Errorf("%s validation failed (skipping corrupt session): %w", ledgerFileRaw, err)
	}

	// raw.jsonl is the critical source of truth — copy it first and fail fast if missing.
	// All other artifacts can be regenerated from raw.jsonl.
	rawDst := filepath.Join(sessionDir, ledgerFileRaw)
	if err := copyFile(rawSrc, rawDst); err != nil {
		return fmt.Errorf("copy %s (critical): %w", ledgerFileRaw, err)
	}

	// copy secondary artifacts (best-effort — skip missing files, don't abort on failure)
	secondaryFiles := []string{ledgerFileEvents, ledgerFileHTML, ledgerFileSummaryMD, ledgerFileSessionMD, "summary.json"}
	for _, name := range secondaryFiles {
		src := filepath.Join(orphan.CachePath, name)
		dst := filepath.Join(sessionDir, name)
		if err := copyFile(src, dst); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			slog.Warn("skip secondary artifact in retry", "file", name, "error", err)
			continue
		}
	}

	// upload to LFS
	fileRefs, err := uploadSessionLFS(projectRoot, sessionDir)
	if err != nil {
		return fmt.Errorf("LFS upload: %w", err)
	}

	// build and write meta.json
	retryEndpoint := endpoint.GetForProject(projectRoot)
	meta := lfs.NewSessionMeta(orphan.SessionName, orphan.Meta.Username, orphan.Meta.AgentID, orphan.Meta.AgentType, orphan.Meta.CreatedAt).
		Model(orphan.Meta.Model).
		EntryCount(orphan.EntryCount).
		UserID(auth.GetUserID(retryEndpoint)).
		RepoID(orphan.Meta.RepoID).
		WithFiles(fileRefs).
		Build()
	if err := lfs.WriteSessionMeta(sessionDir, meta); err != nil {
		return fmt.Errorf("write meta.json: %w", err)
	}

	// ensure .gitignore
	if err := ensureSessionsGitignore(sessionsDir); err != nil {
		return fmt.Errorf("ensure .gitignore: %w", err)
	}

	// stage summary.json alongside meta.json if it was copied (small file, git-tracked, not LFS)
	summaryPath := filepath.Join(sessionDir, "summary.json")
	hasSummary := false
	if _, err := os.Stat(summaryPath); err == nil {
		hasSummary = true
	}

	// commit and push (meta.json + optional summary.json)
	if err := commitAndPushLedgerWithExtras(ledgerPath, orphan.SessionName, hasSummary); err != nil {
		return fmt.Errorf("commit and push: %w", err)
	}

	return nil
}

// validateRawJSONLHeader checks that raw.jsonl has a valid header line with a metadata key.
// This catches truncated or corrupted files before we waste time uploading them.
func validateRawJSONLHeader(rawPath string) error {
	f, err := os.Open(rawPath)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", rawPath)
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	if !scanner.Scan() {
		return fmt.Errorf("empty file")
	}

	var header map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return fmt.Errorf("invalid JSON on first line: %w", err)
	}

	if _, ok := header["metadata"]; !ok {
		return fmt.Errorf("first line missing 'metadata' key (not a valid session header)")
	}

	return nil
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", src)
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Sync()
}
