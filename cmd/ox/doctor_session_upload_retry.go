package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

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
			// read recording state to check for StopIncomplete or staleness
			recData, readErr := os.ReadFile(recordingPath)
			if readErr != nil {
				continue // can't read, skip
			}
			var recState session.RecordingState
			if json.Unmarshal(recData, &recState) != nil {
				continue // corrupt, skip
			}

			// determine if this recording is stale (older than threshold)
			isStale := !recState.StartedAt.IsZero() && time.Since(recState.StartedAt) > session.StaleRecordingThreshold

			if recState.StopIncomplete {
				// StopIncomplete: stop was attempted but session file was empty.
				slog.Info("clearing stop-incomplete recording for retry", "session", sessionName, "agent_id", recState.AgentID)
			} else if isStale {
				// stale recording with content — agent exited without calling stop.
				// clear recording state so session can be recovered.
				slog.Info("clearing stale recording for retry", "session", sessionName, "agent_id", recState.AgentID, "age", time.Since(recState.StartedAt))
			} else {
				continue // genuinely active recording, skip
			}

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

		// skip if already uploaded (meta.json exists in ledger).
		//
		// A DRAFT placeholder does not count as uploaded. It is a
		// meta.json-only marker published mid-recording (ADR-029) and carries
		// no turn data, so treating it as "already uploaded" would make this
		// orphan sweep skip the session permanently — and this check runs at
		// FixLevelAuto, so a session whose upload failed after a draft was
		// published would silently never be retried and its only copy would
		// rot in the cache until pruned.
		//
		// Fail-safe direction: an UNREADABLE ledger meta.json is treated as
		// "uploaded" (skip), matching the pre-draft behavior. Retrying an
		// upload we cannot classify risks clobbering a finalized session;
		// skipping it only defers recovery to a human running `ox doctor`.
		ledgerSessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
		if _, err := os.Stat(filepath.Join(ledgerSessionDir, "meta.json")); err == nil {
			ledgerMeta, metaErr := lfs.ReadSessionMeta(ledgerSessionDir)
			if metaErr != nil || !ledgerMeta.IsDraft() {
				continue
			}
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

// resolveOrphanSessionID picks the durable ses_ ID for a retried orphan
// upload. Precedence, via the shared session.ResolveOrMintSessionID
// resolver: a preserved meta.json ID (a prior retry attempt already wrote
// meta.json to sessionDir before crashing on push) beats the ID already
// carried in the orphan's raw.jsonl header (orphan.Meta.SessionID, parsed
// by findOrphanedSessions); a fresh ID is minted only when neither exists
// (legacy recordings with no session_id anywhere).
//
// This must never be bypassed in favor of an inline mint: doing so is
// exactly the bug this function fixes (ox-5n8e) — every retry of a still-
// failing orphan silently rotated to a brand new SessionID because the
// header-carried one was never consulted, so a later successful push could
// commit a different ID than a locally stranded prior attempt, producing
// two meta.json states for the same session.
//
// Non-NotExist meta.json read errors are fatal — see PreservedSessionID doc.
func resolveOrphanSessionID(sessionDir string, orphan orphanedSession, draftPreservedID string) (string, error) {
	preservedID, err := lfs.PreservedSessionID(sessionDir)
	if err != nil {
		return "", fmt.Errorf("preserve existing SessionID: %w", err)
	}
	// A draft placeholder that was just purged held the id; the file it lived in
	// is gone by now, so the caller had to read it beforehand and hand it in.
	//
	// It slots in at PRESERVED precedence, not below the raw-header carrier, and
	// that ordering is deliberate: ResolveSessionID's contract is
	// preserved-beats-start-minted because preserved means "already written to
	// meta.json", and a draft's meta.json has additionally been PUSHED — it is
	// the id teammates' /c/ links and any commit trailers already reference.
	// The header id is start-minted. On the rare occasion they disagree, the
	// published one is the one that must survive.
	if preservedID == "" {
		preservedID = draftPreservedID
	}
	headerID := ""
	if orphan.Meta != nil {
		headerID = orphan.Meta.SessionID
	}
	return session.ResolveOrMintSessionID(preservedID, headerID), nil
}

// retrySessionUpload copies session files from cache to ledger, uploads to LFS,
// writes meta.json, and commits+pushes. This is the recovery path for sessions
// where phase 2 (ledger upload) failed during session stop. The cache always has
// the authoritative copy; raw.jsonl is the critical file from which all others
// can be regenerated.
func retrySessionUpload(projectRoot, ledgerPath string, orphan orphanedSession) error {
	// guard: never upload a session with zero substantive entries
	if orphan.EntryCount == 0 {
		slog.Info("skipping retry upload: zero entries", "session", orphan.SessionName)
		return nil
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	sessionDir := filepath.Join(sessionsDir, orphan.SessionName)

	// Validate BEFORE mutating the ledger. Purging first and then bailing on a
	// corrupt raw.jsonl would leave a staged deletion in the shared index that
	// nothing commits — and the next unrelated bare `git commit` sweeps it up
	// under the wrong message.
	rawSrc := filepath.Join(orphan.CachePath, ledgerFileRaw)
	if err := validateRawJSONLHeader(rawSrc); err != nil {
		return fmt.Errorf("%s validation failed (skipping corrupt session): %w", ledgerFileRaw, err)
	}

	// Supersede a draft placeholder if one was published before the agent died,
	// capturing its ses_ id first — the draft meta.json is a durable carrier of
	// that id, and for a recording whose raw.jsonl header predates the SessionID
	// field it is the ONLY carrier. Losing it here mints a fresh id and 404s a
	// /c/ link already published in a PR body.
	draftPreservedID, _, err := supersedeDraftForFinalize(ledgerPath, orphan.SessionName)
	if err != nil {
		return fmt.Errorf("classify existing ledger meta for %s: %w", orphan.SessionName, err)
	}

	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	// raw.jsonl is the critical source of truth — copy it first and fail fast if missing.
	// All other artifacts can be regenerated from raw.jsonl.
	rawDst := filepath.Join(sessionDir, ledgerFileRaw)
	if err := copyFile(rawSrc, rawDst); err != nil {
		return fmt.Errorf("copy %s (critical): %w", ledgerFileRaw, err)
	}

	// copy secondary artifacts (best-effort — skip missing files, don't abort on failure)
	secondaryFiles := []string{ledgerFileSummaryMD, ledgerFileSessionMD, ledgerFilePlan, "summary.json"}
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

	// durable ID via the shared resolver, resolved before the network round
	// trip so a corrupt pre-existing meta.json aborts fast rather than after
	// a wasted LFS upload. See resolveOrphanSessionID doc.
	sessionID, err := resolveOrphanSessionID(sessionDir, orphan, draftPreservedID)
	if err != nil {
		return err
	}

	// upload to LFS
	fileRefs, err := uploadSessionLFS(projectRoot, sessionDir)
	if err != nil {
		return fmt.Errorf("LFS upload: %w", err)
	}

	meta, err := writeRetryUploadMeta(sessionDir, projectRoot, orphan, sessionID, fileRefs)
	if err != nil {
		return err
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

	// push succeeded — now safe to replace content files with LFS pointer stubs
	if len(meta.Files) > 0 {
		if _, err := lfs.WritePointerFiles(sessionDir, meta.Files); err != nil {
			slog.Warn("LFS pointer file write failed after push", "error", err, "session", orphan.SessionName)
		}
	}

	return nil
}

// writeRetryUploadMeta records a retry-upload's results into meta.json,
// updating only the fields this path owns and preserving everything else.
//
// Uses WriteSessionMetaOnly semantics (via MutateSessionMeta) so content
// files stay intact until after the push — pointer stubs with no remote
// would be unrecoverable.
//
// # Why read-modify-write and not a fresh builder (GH #710)
//
// This used to rebuild meta.json from sessionMetaBase(...).Build(), which
// never sets summary_status, validation_error or summary_attempts. All
// three are `omitempty`, so they did not merely go stale — they vanished
// from the file. `ox doctor: auto-commit ledger changes` then committed
// the stripped version while origin still carried the fields, and both
// sides had edited the same lines. That is the exact conflict hunk the
// #710 reporter pasted, reproducing on the same 6 files on every single
// rebase until their ledger could no longer push at all.
//
// The same applies to redactions (an audit record), produced_commits,
// produced_plans, linked_prs, linked_issues and linkage_status.
func writeRetryUploadMeta(
	sessionDir, projectRoot string,
	orphan orphanedSession,
	sessionID string,
	fileRefs map[string]lfs.FileRef,
) (*lfs.SessionMeta, error) {
	// Meta carries the raw.jsonl header fields this function reads.
	// findOrphanedSessions never produces a nil one, but the guard belongs
	// here rather than at the top of retrySessionUpload: an earlier check
	// would preempt the more specific "raw.jsonl validation failed"
	// diagnostic for a corrupt session, which is the case that actually
	// produces a nil Meta.
	if orphan.Meta == nil {
		return nil, fmt.Errorf("session %s has no header metadata; cannot rebuild meta.json", orphan.SessionName)
	}

	var meta *lfs.SessionMeta
	if err := lfs.MutateSessionMeta(context.Background(), sessionDir, func(current *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		next := current
		if next == nil {
			// first write for this session — nothing on disk to preserve.
			next = sessionMetaBase(orphan.SessionName, orphan.Meta.Username,
				orphan.Meta.AgentID, orphan.Meta.AgentType, orphan.Meta.CreatedAt,
				projectRoot, sessionID).Build()
		} else {
			// identity fields are backfilled only when absent: a value
			// already on disk was resolved when more context was available
			// than a doctor sweep has.
			if next.UserID == "" {
				next.UserID = auth.GetUserID(endpoint.GetForProject(projectRoot))
			}
			if next.RepoID == "" {
				next.RepoID = getRepoIDOrDefault(projectRoot)
			}
		}
		// sessionID is already resolved by resolveOrphanSessionID, which
		// prefers a preserved meta.json id over minting a new one — so it
		// is authoritative and never rotates across retries.
		next.SessionID = sessionID

		// fields this retry actually owns
		next.Model = orphan.Meta.Model
		next.EntryCount = orphan.EntryCount
		next.Files = fileRefs
		// preserve-if-set: don't stomp a real terminal stop reason with the
		// generic "recovered" just because doctor re-uploaded the content.
		if next.StopReason == "" {
			next.StopReason = session.StopReasonRecovered
		}

		meta = next
		return next, nil
	}); err != nil {
		return nil, fmt.Errorf("write meta.json: %w", err)
	}
	return meta, nil
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
