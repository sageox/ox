package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionUploadCmd = &cobra.Command{
	Use:   "upload <session-name>",
	Short: "Publish a held or unuploaded session to the ledger",
	Long: `Publish a session to the ledger.

This is how a session held on this machine (session_publishing: manual) is
published: nothing else uploads it. It also publishes a session whose content
was left in the local cache (an interrupted stop or an orphaned recording), and
retries one whose stop failed during the network phase. Publishing uploads the
content, writes meta.json, commits, and pushes; the summary is generated
afterward in the background.

The session-name can be the full directory name or just the agent ID suffix.

Example:
  ox session upload 2026-01-06T14-32-ryan-Ox7f3a
  ox session upload Ox7f3a`,
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

		// A held or cache-only session is published from its cache copy
		// (GH #1019, #1077); anything else keeps the Ledger-folder path below.
		if name, cacheDir, ok := findPublishableCacheSession(ledgerPath, args[0]); ok {
			fmt.Printf("Publishing session %s...\n", name)
			if err := publishCachedSession(projectRoot, ledgerPath, name, cacheDir); err != nil {
				if errors.Is(err, api.ErrReadOnly) {
					fmt.Println("\nUpload skipped — you have read-only access to this public repo.")
					fmt.Println("To upload sessions, request team membership from an admin.")
					return nil
				}
				return fmt.Errorf("publish %s: %w", name, err)
			}
			fmt.Printf("Session %s published to the ledger; its summary is generated in the background\n", name)
			return nil
		}

		sessionsDir := filepath.Join(ledgerPath, "sessions")

		sessionName, err := resolveSessionInDir(sessionsDir, args[0])
		if err != nil {
			return err
		}

		sessionPath := filepath.Join(sessionsDir, sessionName)

		// verify session directory exists
		if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
			return fmt.Errorf("session not found: %s", sessionName)
		}

		// verify at least one content file exists
		if !hasContentFiles(sessionPath) {
			return fmt.Errorf("no content files found in session %s\nExpected at least one of: raw.jsonl, summary.md, session.md", sessionName)
		}

		// validate raw.jsonl data quality before uploading
		rawPath := filepath.Join(sessionPath, ledgerFileRaw)
		if _, err := os.Stat(rawPath); err == nil {
			if validation := validateRawJSONLFile(rawPath); len(validation.Errors) > 0 {
				fmt.Fprintf(os.Stderr, "warning: session data has issues:\n")
				for _, e := range validation.Errors {
					fmt.Fprintf(os.Stderr, "  - %s\n", e)
				}
				fmt.Fprintln(os.Stderr, "Uploading anyway — run 'ox session lint' for details.")
			}
		}

		// build or create meta.json first (before LFS upload) to preserve metadata even if LFS fails
		meta, err := buildSessionMeta(sessionPath, sessionName, projectRoot, nil)
		if err != nil {
			return fmt.Errorf("build meta.json: %w", err)
		}

		// guard: never upload a session with zero substantive entries.
		// A pointer stub gets its own message — telling someone their
		// already-uploaded session "has no substantive entries" is actively
		// misleading, since the transcript exists and is simply not local.
		switch session.ClassifyRawFile(rawPath) {
		case session.RawPointerStub:
			return fmt.Errorf("session %s is already uploaded — its content lives in the ledger content store, "+
				"not on disk. Run `ox session download %s` if you need a local copy", sessionName, sessionName)
		case session.RawMissing:
			return fmt.Errorf("session %s has no readable %s — nothing to upload", sessionName, ledgerFileRaw)
		case session.RawHeaderOnly:
			return fmt.Errorf("session %s holds no conversation entries (only header/footer lines) — nothing to upload", sessionName)
		case session.RawSubstantive:
			// ok to upload
		}

		if err := lfs.WriteSessionMeta(sessionPath, meta); err != nil {
			return fmt.Errorf("write meta.json: %w", err)
		}

		// upload content files to LFS
		fmt.Printf("Uploading session %s...\n", sessionName)
		fileRefs, err := publishSessionLFSContext(cmd.Context(), projectRoot, sessionPath)
		if err != nil {
			if errors.Is(err, api.ErrReadOnly) {
				fmt.Println("\nUpload skipped — you have read-only access to this public repo.")
				fmt.Println("To upload sessions, request team membership from an admin.")
				return nil
			}
			return fmt.Errorf("upload: %w", err)
		}

		// update meta.json with LFS file references
		meta.Files = fileRefs
		if err := lfs.WriteSessionMeta(sessionPath, meta); err != nil {
			return fmt.Errorf("update meta.json with LFS refs: %w", err)
		}

		// ensure sessions/.gitignore exists
		if err := ensureSessionsGitignore(sessionsDir); err != nil {
			return fmt.Errorf("ensure .gitignore: %w", err)
		}

		// commit and push
		if err := commitAndPushLedger(ledgerPath, sessionName); err != nil {
			return fmt.Errorf("commit and push: %w", err)
		}

		fmt.Printf("Session %s uploaded successfully\n", sessionName)
		return nil
	},
}

// hasContentFiles checks if a session directory has at least one content file.
func hasContentFiles(sessionPath string) bool {
	contentFiles := []string{
		ledgerFileRaw,
		ledgerFileSummaryMD,
		ledgerFileSessionMD,
	}
	for _, name := range contentFiles {
		if _, err := os.Stat(filepath.Join(sessionPath, name)); err == nil {
			return true
		}
	}
	return false
}

// buildSessionMeta reads existing meta.json or constructs one from the directory name.
// If fileRefs is nil, Files field is initialized as empty map.
// If fileRefs is non-nil, Files field is updated with the provided references.
func buildSessionMeta(sessionPath, sessionName, projectRoot string, fileRefs map[string]lfs.FileRef) (*lfs.SessionMeta, error) {
	// try reading existing meta.json first
	meta, err := lfs.ReadSessionMeta(sessionPath)
	if err == nil {
		// update the file manifest
		if fileRefs != nil {
			meta.Files = fileRefs
		} else if meta.Files == nil {
			meta.Files = make(map[string]lfs.FileRef)
		}
		return meta, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		// meta.json exists but couldn't be read/parsed — refuse to proceed.
		// Silently falling through to "construct from directory name" here
		// would mint a fresh SessionID and rotate away from whatever
		// identity the unreadable file held. See PreservedSessionID doc.
		return nil, fmt.Errorf("read existing meta.json (refusing to silently rotate SessionID): %w", err)
	}

	// no existing meta.json — construct from directory name
	ts, username, agentID := parseSessionDirName(sessionName)

	rawPath := filepath.Join(sessionPath, ledgerFileRaw)

	// count entries in raw.jsonl if present
	entryCount := countJSONLLines(rawPath)

	// read summary if present
	summary := readFileString(filepath.Join(sessionPath, ledgerFileSummaryMD))

	// initialize Files with empty map if nil
	if fileRefs == nil {
		fileRefs = make(map[string]lfs.FileRef)
	}

	// durable ID via the shared resolver: no preserved meta.json exists (the
	// read above returned NotExist), so the raw-header carrier is the only
	// possible source before minting fresh.
	sessionID := session.ResolveOrMintSessionID("", session.ReadHeaderSessionID(rawPath))

	ep := endpoint.GetForProject(projectRoot)
	return sessionMetaBase(sessionName, firstNonEmpty(username, identity.AttributionDisplayName(ep, config.GetDisplayName()), "unknown"), agentID, "unknown", ts, projectRoot, sessionID).
		EntryCount(entryCount).
		Summary(summary).
		WithFiles(fileRefs).
		Build(), nil
}

// parseSessionDirName extracts timestamp, username, and agent ID from a session
// directory name in the format: YYYY-MM-DDTHH-MM-<username>-<agentID>
func parseSessionDirName(name string) (createdAt time.Time, username, agentID string) {
	// try parsing the timestamp prefix (16 chars: "2006-01-02T15-04")
	const tsLayout = "2006-01-02T15-04"
	const tsLen = 16

	if len(name) > tsLen+1 && name[tsLen] == '-' {
		if t, err := time.Parse(tsLayout, name[:tsLen]); err == nil {
			createdAt = t
			rest := name[tsLen+1:] // everything after timestamp-

			// the last segment after '-' is the agent ID
			if lastDash := strings.LastIndex(rest, "-"); lastDash >= 0 {
				username = rest[:lastDash]
				agentID = rest[lastDash+1:]
			} else {
				agentID = rest
			}
			return
		}
	}

	// fallback: just use the last segment as agent ID
	if lastDash := strings.LastIndex(name, "-"); lastDash >= 0 {
		agentID = name[lastDash+1:]
	} else {
		agentID = name
	}
	return
}

// countJSONLLines counts lines in a JSONL file. Returns 0 if file doesn't exist.
func countJSONLLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		count++
	}
	return count
}

// readFileString reads a file and returns its content as a string.
// Returns empty string if the file doesn't exist or can't be read.
func readFileString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// firstNonEmpty returns the first non-empty string from the arguments.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// findPublishableCacheSession resolves arg against every local cache location
// and returns a session whose content is there and is not yet published: it is
// held, the Ledger has no entry or only a draft placeholder for it, or an
// earlier publish left a pending retry. An already-published session is never
// returned, so publishing cannot replay it.
func findPublishableCacheSession(ledgerPath, arg string) (name, cacheDir string, ok bool) {
	for _, dir := range session.HeldSessionDirs(ledgerPath) {
		resolved, err := resolveSessionInDir(dir, arg)
		if err != nil {
			continue
		}
		candidate := filepath.Join(dir, resolved)
		if session.ClassifyRawFile(filepath.Join(candidate, ledgerFileRaw)) != session.RawSubstantive {
			continue
		}
		if session.IsHeld(candidate) {
			return resolved, candidate, true
		}
		if _, err := os.Stat(filepath.Join(candidate, sessionUploadRetryPendingFile)); err == nil {
			return resolved, candidate, true
		}
		ledgerMeta, err := lfs.ReadSessionMeta(filepath.Join(ledgerPath, "sessions", resolved))
		if errors.Is(err, os.ErrNotExist) || (err == nil && ledgerMeta.IsDraft()) {
			return resolved, candidate, true
		}
	}
	return "", "", false
}

// publishCachedSession publishes a session from its cache copy through the
// same upload path doctor's retry uses, synchronously. The hold is released
// only after the commit lands, and .needs-summary is written first so the
// daemon summarizes the published session instead of mistaking the cache copy
// for a read-only download.
func publishCachedSession(projectRoot, ledgerPath, sessionName, cacheDir string) error {
	rawPath := filepath.Join(cacheDir, ledgerFileRaw)
	meta, entryCount, err := readCacheSessionMeta(rawPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", ledgerFileRaw, err)
	}
	if entryCount == 0 {
		return fmt.Errorf("session %s holds no conversation entries — nothing to publish", sessionName)
	}
	if config.GetAgentSummarizer(projectRoot) != config.AgentSummarizerOff {
		if err := session.WriteNeedsSummaryMarker(cacheDir, rawPath, filepath.Join(ledgerPath, "sessions", sessionName)); err != nil {
			return fmt.Errorf("request summary: %w", err)
		}
	}

	effects := productionSessionUploadEffects()
	effects.uploadLFS = publishSessionLFS // the explicit publish releases the hold
	orphan := orphanedSession{SessionName: sessionName, CachePath: cacheDir, Meta: meta, EntryCount: entryCount}
	if err := retrySessionUploadWithEffects(projectRoot, ledgerPath, orphan, effects); err != nil {
		return err
	}

	for _, dir := range session.HeldSessionDirs(ledgerPath) {
		if err := session.ClearHoldMarker(filepath.Join(dir, sessionName)); err != nil {
			slog.Warn("published session still marked held", "session", sessionName, "dir", dir, "error", err)
		}
	}
	_ = os.Remove(filepath.Join(cacheDir, sessionUploadRetryPendingFile))
	return nil
}
