package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/spf13/cobra"
)

var sessionRegenerateCmd = &cobra.Command{
	Use:   "regenerate [session-name]",
	Short: "Regenerate session artifacts or re-redact session data",
	Long: `Regenerate session artifacts from raw data.

By default, regenerates markdown artifacts (summary.md, session.md)
from raw session data.

With --redact, re-applies all current REDACT.md rules (team + repo + user
layers), regenerates all downstream artifacts (session.md, summary.md),
and uploads updated content to LFS.
Old LFS blobs become orphaned after regeneration. Server-side blob purge
will be handled by the /api/v1/git/lfs/purge cloud API endpoint.

The session name supports partial matching (e.g. agent ID suffix).

With --summary, re-generates summary.json using the current prompt template
by invoking Claude. Requires the claude CLI to be installed. Single-session
only (ledgers can contain 10,000+ sessions; each requires an LLM invocation).

Examples:
  ox session regenerate OxK3ZN                          # regenerate artifacts
  ox session regenerate --all                           # regenerate all artifacts
  ox session regenerate OxK3ZN --redact                 # re-redact session
  ox session regenerate --redact --all                  # re-redact all sessions
  ox session regenerate OxK3ZN --redact --dry-run       # preview redaction
  ox session regenerate OxK3ZN --summary                # re-summarize with current prompt`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSessionRegenerate,
}

func init() {
	sessionRegenerateCmd.Flags().Bool("redact", false, "re-apply current REDACT.md rules to session data")
	sessionRegenerateCmd.Flags().Bool("summary", false, "re-generate summary.json using current prompt template (requires claude CLI)")
	sessionRegenerateCmd.Flags().Bool("all", false, "regenerate all sessions")
	sessionRegenerateCmd.Flags().Bool("dry-run", false, "preview what would change without modifying anything (--redact only)")
	sessionRegenerateCmd.Flags().Bool("force", false, "skip confirmation prompts")
}

func runSessionRegenerate(cmd *cobra.Command, args []string) error {
	redact, _ := cmd.Flags().GetBool("redact")
	summary, _ := cmd.Flags().GetBool("summary")
	regenAll, _ := cmd.Flags().GetBool("all")
	force, _ := cmd.Flags().GetBool("force")

	if summary && redact {
		return fmt.Errorf("--summary and --redact cannot be used together")
	}

	if redact {
		return runSessionRegenerateRedact(cmd, args)
	}

	if summary {
		// Single-session only: ledgers can contain 10,000+ sessions (more for
		// large monorepos). Each summary requires an LLM invocation (~60s + API
		// cost), making batch regeneration prohibitively expensive.
		if len(args) == 0 {
			return fmt.Errorf("specify a session name\nRun 'ox session list' to see available sessions")
		}
		return regenerateSingleSessionSummary(args[0])
	}

	// default mode: artifact regeneration (markdown)
	store, projectRoot, err := newSessionStore()
	if err != nil {
		return err
	}

	if regenAll {
		return regenerateAllSessionsArtifacts(store, projectRoot, force)
	}

	if len(args) == 0 {
		return fmt.Errorf("specify a session name or use --all\nRun 'ox session list' to see available sessions")
	}

	return regenerateSingleSessionArtifacts(store, projectRoot, args[0])
}

// --- Artifact regeneration (default mode) ---

func regenerateSingleSessionArtifacts(store *session.Store, projectRoot, name string) error {
	sessionName, err := store.ResolveSessionName(name)
	if err != nil {
		return fmt.Errorf("resolve session name: %w", err)
	}

	storedSession, err := readSessionViaCache(projectRoot, store, sessionName)
	if err != nil {
		return fmt.Errorf("session %q: %w\nRun 'ox session list' to see available sessions", name, err)
	}

	// Resolve the artifact-write path. For sessions authored locally,
	// store.GetSessionPath returns the local managed-store path. For
	// sessions authored by other team members and synced via the ledger,
	// that path doesn't exist on this machine — write directly to the
	// ledger sessions/<name>/ path instead, matching the --summary path.
	sessionPath := artifactWriteDir(store, sessionName)
	if err := regenerateSessionArtifacts(storedSession, sessionPath); err != nil {
		return err
	}

	if err := syncRegeneratedSession(projectRoot, sessionPath, sessionName); err != nil {
		slog.Warn("ledger sync skipped", "session", sessionName, "error", err)
	}

	cli.PrintSuccess(fmt.Sprintf("Regenerated artifacts for %s", sessionName))
	return nil
}

// artifactWriteDir returns the directory where regenerated .md artifacts
// should be written for a session. Locally-authored sessions live in the
// project's managed store; team-uploaded sessions only exist in the ledger.
//
// We prefer the local managed-store path when it exists (so an in-progress
// recording's artifacts stay co-located with raw.jsonl). Otherwise we fall
// back to the ledger sessions/<name>/ directory, which is where any
// team-synced session lives.
func artifactWriteDir(store *session.Store, sessionName string) string {
	if store != nil {
		local := store.GetSessionPath(sessionName)
		if _, err := os.Stat(local); err == nil {
			return local
		}
	}
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		// last resort — return the local path even if missing; caller's
		// write will fail with a clear error.
		if store != nil {
			return store.GetSessionPath(sessionName)
		}
		return sessionName
	}
	return filepath.Join(ledgerPath, "sessions", sessionName)
}

// readSessionViaCache reads raw.jsonl through the cache-only resolver,
// hydrating on demand. Replaces direct calls to store.ReadSession in code
// paths that need to handle ledger-side LFS-pointer stubs (sessions
// authored by other team members).
//
// store.ReadSession would error with ErrSessionNotHydrated for stubs;
// this helper triggers a Batch-API hydrate-to-cache and returns the
// real bytes from the cache path. The in-place git-tracked file is
// untouched (see openSessionContent for the cache-only invariant).
func readSessionViaCache(projectRoot string, store *session.Store, sessionName string) (*session.StoredSession, error) {
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		// Fall back to store.ReadSession when no ledger is available
		// (e.g. tests or detached project state).
		return store.ReadSession(sessionName)
	}
	rawPath, err := openSessionContent(projectRoot, ledgerPath, sessionName, ledgerFileRaw)
	if err != nil {
		return nil, err
	}
	return session.ReadSessionFromPath(rawPath)
}

func regenerateAllSessionsArtifacts(store *session.Store, projectRoot string, force bool) error {
	sessions, err := store.ListAllSessions()
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	if !force {
		if !cli.ConfirmYesNo(fmt.Sprintf("Regenerate artifacts for %d session(s)?", len(sessions)), false) {
			fmt.Println("Canceled.")
			return nil
		}
	}

	var regenerated, skipped int
	for _, info := range sessions {
		sessionName := info.SessionName
		if sessionName == "" {
			skipped++
			continue
		}

		storedSession, readErr := readSessionViaCache(projectRoot, store, sessionName)
		if readErr != nil {
			slog.Warn("skipping unreadable session", "session", sessionName, "error", readErr)
			skipped++
			continue
		}

		sessionPath := artifactWriteDir(store, sessionName)
		if regenErr := regenerateSessionArtifacts(storedSession, sessionPath); regenErr != nil {
			slog.Warn("failed to regenerate session", "session", sessionName, "error", regenErr)
			skipped++
			continue
		}

		regenerated++
	}

	// batch ledger sync: single commit+push for all regenerated sessions
	if regenerated > 0 {
		ledgerPath, ledgerErr := resolveLedgerPath()
		if ledgerErr == nil {
			for _, info := range sessions {
				if info.SessionName == "" {
					continue
				}
				sessionPath := store.GetSessionPath(info.SessionName)
				if _, lfsErr := uploadSessionLFS(projectRoot, sessionPath); lfsErr != nil {
					slog.Debug("LFS re-upload skipped", "session", info.SessionName, "error", lfsErr)
				}
			}
			if pushErr := commitAndPushLedger(ledgerPath, "batch-regenerate"); pushErr != nil {
				slog.Warn("ledger push skipped", "error", pushErr)
			}
		}
	}

	cli.PrintSuccess(fmt.Sprintf("Regenerated %d session(s)", regenerated))
	if skipped > 0 {
		cli.PrintWarning(fmt.Sprintf("Skipped %d session(s) (unreadable or missing raw data)", skipped))
	}

	return nil
}

// regenerateSessionArtifacts regenerates markdown artifacts for a session.
func regenerateSessionArtifacts(storedSession *session.StoredSession, sessionPath string) error {
	if err := regenerateArtifacts(sessionPath, storedSession); err != nil {
		return fmt.Errorf("regenerate artifacts: %w", err)
	}

	slog.Debug("regenerated session artifacts", "path", sessionPath)
	return nil
}

// syncRegeneratedSession re-uploads to LFS and pushes to the ledger for a single session.
func syncRegeneratedSession(projectRoot, sessionPath, sessionName string) error {
	if _, err := uploadSessionLFS(projectRoot, sessionPath); err != nil {
		return fmt.Errorf("LFS upload: %w", err)
	}

	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return fmt.Errorf("resolve ledger: %w", err)
	}

	if err := commitAndPushLedger(ledgerPath, sessionName); err != nil {
		return fmt.Errorf("commit and push: %w", err)
	}

	return nil
}

// hasSubstantiveTurns reports whether the session has at least one user OR
// assistant turn. A session with only header/system entries (e.g., a
// recording that never proceeded past "Loaded coworker: <name>") has nothing
// for the summarizer to work on. Without this preflight the LLM either
// refuses ("there's nothing to summarize"), narrates the absence, or
// fabricates content — all three produce validation-failing output that
// gets persisted as a failure-marker stub on the ledger. Cleaner to fail
// fast with a clear error.
//
// Filed as bd ox-o45g.
func hasSubstantiveTurns(entries []map[string]any) bool {
	for _, e := range entries {
		t, _ := e["type"].(string)
		if t == "user" || t == "assistant" {
			return true
		}
	}
	return false
}

// --- Summary regeneration (--summary) ---

// regenerateSingleSessionSummary re-generates summary.json for a session by
// invoking Claude with the current summary prompt template. The raw.jsonl is
// read (downloaded from LFS if needed), a prompt is built using the shared
// template, and Claude produces a new summary. Downstream artifacts
// (summary.md, session.md) are regenerated from the result.
func regenerateSingleSessionSummary(nameArg string) error {
	projectRoot, err := requireProjectRoot()
	if err != nil {
		return err
	}

	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return err
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	sessionName, err := resolveSessionInDir(sessionsDir, nameArg)
	if err != nil {
		return err
	}

	sessionPath := filepath.Join(sessionsDir, sessionName)

	// Cache-only resolution — see openSessionContent for the load-bearing invariant.
	rawPath, err := openSessionContent(projectRoot, ledgerPath, sessionName, ledgerFileRaw)
	if err != nil {
		return err
	}

	rawSession, err := session.ReadSessionFromPath(rawPath)
	if err != nil {
		return fmt.Errorf("read raw.jsonl: %w", err)
	}

	if !hasSubstantiveTurns(rawSession.Entries) {
		// ox-o45g: skip sessions with no user/assistant turns. The LLM has
		// nothing to summarize and would either refuse, narrate, or generate
		// fabricated content. Surface as a clean exit, not an LLM call.
		return fmt.Errorf("session %s has no substantive user/assistant turns; nothing to summarize", sessionName)
	}

	// Mirror the live `ox session stop` path: tokenopt-compress raw.jsonl
	// into the ledger cache before building the prompt. ConversationOnly
	// keeps user+assistant turns verbatim, compacts tool entries, drops
	// system entries — typically 50-80% smaller. Without this, large
	// sessions (>100k tokens) can blow Claude's context on regenerate.
	// Falls back to rawPath on any failure (helper returns "").
	summaryInputPath := writeOptimizedJSONLForSummary(rawPath, ledgerPath, sessionName)
	if summaryInputPath == "" {
		summaryInputPath = rawPath
	}

	// Build the prompt with sessionPath as ledgerSessionDir so the LLM is
	// instructed to run `ox session push-summary` itself once it's saved
	// the JSON. push-summary handles validation (content + richness),
	// summary.json write, meta.json title update, git add/commit/push.
	//
	// Why delegate to push-summary instead of parsing inline JSON: the
	// shared prompt template tells the LLM to *save* the JSON to a temp
	// file, not return it inline. In Claude Code's `-p` non-interactive
	// mode the result text is prose narration of the work, not the JSON.
	// Trying to ParseSummaryJSON from that prose fails (we tried — both
	// sessions in the Phase 2 driver failed identically with "no valid
	// summary JSON found in LLM output"). Routing through push-summary
	// gets us the same path the rest of the system already trusts.
	summaryPathBefore := filepath.Join(sessionPath, "summary.json")
	mtimeBefore := fileMtime(summaryPathBefore)

	entries := sessionsummary.EntriesFromRaw(rawSession.Entries)
	prompt := sessionsummary.BuildSummaryPrompt(entries, summaryInputPath, sessionPath)

	cli.PrintInfo(fmt.Sprintf("Generating summary for %s...", sessionName))

	if _, err := sessionsummary.InvokeClaude(context.Background(), prompt, sessionPath); err != nil {
		return fmt.Errorf("claude invocation: %w", err)
	}

	// Verify push-summary actually wrote a fresh summary.json. If the LLM
	// silently failed to save / push, mtime won't have advanced and we'd
	// be regenerating downstream artifacts from stale content.
	mtimeAfter := fileMtime(summaryPathBefore)
	if mtimeAfter.IsZero() {
		return fmt.Errorf("summary.json missing after claude run — push-summary did not complete for %s", sessionName)
	}
	if !mtimeBefore.IsZero() && !mtimeAfter.After(mtimeBefore) {
		return fmt.Errorf("summary.json not refreshed for %s — push-summary may have rejected the LLM output (check validation logs)", sessionName)
	}

	// Read the just-written summary so we can regenerate downstream
	// artifacts (summary.md, session.md) and report the quality score.
	freshData, err := os.ReadFile(summaryPathBefore)
	if err != nil {
		return fmt.Errorf("read fresh summary.json for %s: %w", sessionName, err)
	}
	var freshSummary sessionsummary.SummarizeResponse
	if err := json.Unmarshal(freshData, &freshSummary); err != nil {
		return fmt.Errorf("parse fresh summary.json for %s: %w", sessionName, err)
	}

	// regenerate downstream artifacts (summary.md, session.md)
	if err := regenerateArtifacts(sessionPath, rawSession); err != nil {
		slog.Warn("artifact regeneration partially failed", "session", sessionName, "error", err)
	}

	// push-summary already ran git add/commit/push for summary.json +
	// meta.json, but not for the regenerated .md files. Stage them too.
	if err := commitAndPushLedger(ledgerPath, sessionName); err != nil {
		slog.Warn("md-artifact ledger push skipped", "error", err)
	}

	cli.PrintSuccess(fmt.Sprintf("Regenerated summary for %s (quality: %.2f)", sessionName, freshSummary.QualityScore))
	return nil
}

// fileMtime returns the mtime of path or the zero time if the file doesn't
// exist. Used to detect "did this file get rewritten" without needing a
// content diff.
func fileMtime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// --- Redaction mode (--redact) ---

type regenerateResult struct {
	SessionName     string
	EntriesRedacted int
	PatternsFound   []string
	Skipped         bool
}

func runSessionRegenerateRedact(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")

	if len(args) > 0 && all {
		return fmt.Errorf("cannot use both a session name and --all")
	}
	if len(args) == 0 && !all {
		return fmt.Errorf("provide a session name or use --all")
	}

	projectRoot, err := requireProjectRoot()
	if err != nil {
		return err
	}

	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return err
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")

	// build redactor with all 3 REDACT.md layers
	redactor, parseErrs := session.NewRedactorWithCustomRules(projectRoot)
	if len(parseErrs) > 0 {
		for _, pe := range parseErrs {
			fmt.Fprintf(os.Stderr, "warning: REDACT.md parse error in %s line %d: %s\n", pe.Path, pe.Line, pe.Message)
		}
	}

	// confirm before proceeding (skip for dry-run since it's read-only)
	if !dryRun && !force {
		var prompt string
		if all {
			prompt = "This will re-redact ALL sessions in the ledger and re-upload to LFS. Continue?"
		} else {
			prompt = fmt.Sprintf("This will re-redact session %q and re-upload to LFS. Continue?", args[0])
		}
		if !cli.ConfirmYesNo(prompt, false) {
			fmt.Println("Canceled.")
			return nil
		}
	}

	if all {
		return regenerateAllSessionsRedact(projectRoot, ledgerPath, sessionsDir, redactor, dryRun)
	}

	result, err := regenerateSessionRedact(projectRoot, ledgerPath, sessionsDir, args[0], redactor, dryRun, false)
	if err != nil {
		return err
	}

	if dryRun {
		if result.EntriesRedacted == 0 {
			fmt.Printf("Dry run: %s — no secrets found with current rules\n", result.SessionName)
		} else {
			fmt.Printf("Dry run: %s — %d entries would be redacted (patterns: %s)\n",
				result.SessionName, result.EntriesRedacted, strings.Join(result.PatternsFound, ", "))
		}
		return nil
	}

	if result.Skipped {
		fmt.Printf("%s: no changes needed\n", result.SessionName)
	} else {
		fmt.Printf("%s: %d entries redacted, artifacts regenerated\n",
			result.SessionName, result.EntriesRedacted)
	}
	return nil
}

func regenerateAllSessionsRedact(projectRoot, ledgerPath, sessionsDir string, redactor *session.Redactor, dryRun bool) error {
	dirEntries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return fmt.Errorf("read sessions directory: %w", err)
	}

	// collect session dirs that have meta.json
	var sessionNames []string
	for _, e := range dirEntries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(sessionsDir, e.Name(), "meta.json")
		if _, err := os.Stat(metaPath); err != nil {
			continue
		}
		sessionNames = append(sessionNames, e.Name())
	}

	if len(sessionNames) == 0 {
		fmt.Println("No sessions found in ledger")
		return nil
	}

	fmt.Fprintf(os.Stderr, "Re-redacting all sessions (%d found)...\n", len(sessionNames))

	var (
		processed     int
		redactedCount int
		failedCount   int
		modifiedNames []string
	)

	for i, name := range sessionNames {
		result, err := regenerateSessionRedact(projectRoot, ledgerPath, sessionsDir, name, redactor, dryRun, true)
		processed++

		if err != nil {
			failedCount++
			fmt.Fprintf(os.Stderr, "  [%d/%d] %s: error: %s\n", i+1, len(sessionNames), name, err)
			continue
		}

		if dryRun {
			if result.EntriesRedacted > 0 {
				fmt.Fprintf(os.Stderr, "  [%d/%d] %s: %d entries would be redacted\n",
					i+1, len(sessionNames), name, result.EntriesRedacted)
				redactedCount++
			} else {
				fmt.Fprintf(os.Stderr, "  [%d/%d] %s: no changes\n", i+1, len(sessionNames), name)
			}
			continue
		}

		if !result.Skipped {
			redactedCount++
			modifiedNames = append(modifiedNames, name)
			fmt.Fprintf(os.Stderr, "  [%d/%d] %s: %d entries redacted\n",
				i+1, len(sessionNames), name, result.EntriesRedacted)
		} else {
			fmt.Fprintf(os.Stderr, "  [%d/%d] %s: no changes\n", i+1, len(sessionNames), name)
		}
	}

	if dryRun {
		fmt.Printf("\nDry run summary: %d sessions scanned, %d would be affected, %d failed\n",
			processed, redactedCount, failedCount)
		return nil
	}

	// batch commit + push for all modified sessions
	if len(modifiedNames) > 0 {
		fmt.Fprintf(os.Stderr, "\nCommitting and pushing %d updated sessions...\n", len(modifiedNames))
		if err := commitAndPushLedgerBatch(ledgerPath, modifiedNames); err != nil {
			return fmt.Errorf("commit/push failed: %w", err)
		}
	}

	fmt.Printf("\nSummary: %d processed, %d redacted, %d failed\n",
		processed, redactedCount, failedCount)
	return nil
}

// regenerateSessionRedact re-redacts a single session and optionally uploads/commits.
// When batchMode is true, skips per-session commit/push (caller handles batch commit).
func regenerateSessionRedact(projectRoot, ledgerPath, sessionsDir, nameArg string, redactor *session.Redactor, dryRun, batchMode bool) (*regenerateResult, error) {
	sessionName, err := resolveSessionInDir(sessionsDir, nameArg)
	if err != nil {
		return nil, err
	}

	sessionPath := filepath.Join(sessionsDir, sessionName)
	result := &regenerateResult{SessionName: sessionName}

	// read meta.json
	meta, err := lfs.ReadSessionMeta(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("read meta.json for %s: %w", sessionName, err)
	}

	// ensure raw.jsonl is hydrated locally.
	//
	// Note: --redact MUST hydrate in-place (unlike --summary which uses the
	// cache resolver). Redaction is a controlled overwrite-and-reupload
	// cycle: we replace the in-place file with redacted bytes, compute a
	// new OID, re-upload the new content to LFS, then commit a fresh
	// pointer with the new OID. Reading from the cache wouldn't work — the
	// downstream commit needs to write a NEW pointer to the in-place path.
	rawPath := filepath.Join(sessionPath, ledgerFileRaw)
	needHydrate := false
	if _, err := os.Stat(rawPath); err != nil {
		needHydrate = true
	} else if lfs.IsPointerFile(rawPath) {
		needHydrate = true
	}
	if needHydrate {
		if err := downloadFileFromLFS(projectRoot, sessionPath, meta, ledgerFileRaw); err != nil {
			return nil, fmt.Errorf("download %s for %s: %w", ledgerFileRaw, sessionName, err)
		}
	}

	// read raw.jsonl entries as maps (preserves original JSONL structure)
	rawSession, err := session.ReadSessionFromPath(rawPath)
	if err != nil {
		return nil, fmt.Errorf("read %s for %s: %w", ledgerFileRaw, sessionName, err)
	}

	if len(rawSession.Entries) == 0 {
		result.Skipped = true
		return result, nil
	}

	// scan or redact entries
	entriesRedacted := 0
	patternsFound := make(map[string]bool)

	for i := range rawSession.Entries {
		if dryRun {
			entryHit := false
			for _, value := range rawSession.Entries[i] {
				if s, ok := value.(string); ok && s != "" {
					found := redactor.ScanForSecrets(s)
					if len(found) > 0 {
						entryHit = true
						for _, p := range found {
							patternsFound[p] = true
						}
					}
				}
			}
			if entryHit {
				entriesRedacted++
			}
		} else {
			if redactor.RedactMap(rawSession.Entries[i]) {
				entriesRedacted++
			}
		}
	}

	result.EntriesRedacted = entriesRedacted
	for p := range patternsFound {
		result.PatternsFound = append(result.PatternsFound, p)
	}

	if dryRun {
		return result, nil
	}

	// redact summary.json if it exists (before early return so summary-only secrets are caught)
	summaryRedacted := false
	summaryPath := filepath.Join(sessionPath, "summary.json")
	if _, err := os.Stat(summaryPath); err == nil {
		var sumErr error
		summaryRedacted, sumErr = redactSummaryJSON(summaryPath, redactor)
		if sumErr != nil {
			slog.Warn("summary.json redaction failed", "session", sessionName, "error", sumErr)
		}
	}

	if entriesRedacted == 0 && !summaryRedacted {
		result.Skipped = true
		return result, nil
	}

	// re-write raw.jsonl with redacted content
	if entriesRedacted > 0 {
		if err := rewriteRawJSONL(rawPath, rawSession); err != nil {
			return nil, fmt.Errorf("rewrite raw.jsonl for %s: %w", sessionName, err)
		}
	}

	// regenerate downstream artifacts
	if err := regenerateArtifacts(sessionPath, rawSession); err != nil {
		slog.Warn("artifact regeneration partially failed", "session", sessionName, "error", err)
	}

	// upload all content files to LFS
	fileRefs, err := uploadSessionLFS(projectRoot, sessionPath)
	if err != nil {
		return nil, fmt.Errorf("LFS upload for %s: %w", sessionName, err)
	}

	// update meta.json with new OIDs (old blobs orphaned; purge via /api/v1/git/lfs/purge)
	meta.Files = fileRefs
	if err := lfs.WriteSessionMeta(sessionPath, meta); err != nil {
		return nil, fmt.Errorf("write meta.json for %s: %w", sessionName, err)
	}

	// single-session mode: commit + push immediately
	if !batchMode {
		if err := commitAndPushLedger(ledgerPath, sessionName); err != nil {
			return nil, fmt.Errorf("commit/push for %s: %w", sessionName, err)
		}
	}

	return result, nil
}

// --- Shared helpers ---

// rewriteRawJSONL writes the modified StoredSession back to raw.jsonl atomically.
func rewriteRawJSONL(path string, sess *session.StoredSession) error {
	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		f.Close()
		os.Remove(tmpPath) // no-op if already renamed
	}()

	w := bufio.NewWriter(f)

	// write header if present (serialize StoreMeta directly via json.Marshal)
	if sess.Meta != nil {
		header := map[string]any{
			"type":     "header",
			"metadata": sess.Meta,
		}
		line, err := json.Marshal(header)
		if err != nil {
			return fmt.Errorf("marshal header: %w", err)
		}
		w.Write(line)
		w.WriteByte('\n')
	}

	// write entries
	for _, entry := range sess.Entries {
		line, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("marshal entry: %w", err)
		}
		w.Write(line)
		w.WriteByte('\n')
	}

	// write footer if present
	if sess.Footer != nil {
		line, err := json.Marshal(sess.Footer)
		if err != nil {
			return fmt.Errorf("marshal footer: %w", err)
		}
		w.Write(line)
		w.WriteByte('\n')
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}

	return os.Rename(tmpPath, path)
}

// regenerateArtifacts regenerates session.md and summary.md from raw session data.
func regenerateArtifacts(sessionPath string, rawSession *session.StoredSession) error {
	var errs []string

	// session.md
	mdGen := session.NewMarkdownGenerator()
	mdPath := filepath.Join(sessionPath, ledgerFileSessionMD)
	if err := mdGen.GenerateToFile(rawSession, mdPath); err != nil {
		errs = append(errs, fmt.Sprintf("%s: %s", ledgerFileSessionMD, err))
	}

	// summary.md — regenerate from summary.json if available
	summaryJSONPath := filepath.Join(sessionPath, "summary.json")
	if data, err := os.ReadFile(summaryJSONPath); err == nil {
		var summaryResp session.SummarizeResponse
		if json.Unmarshal(data, &summaryResp) == nil {
			summaryView := session.SummarizeResponseToSummaryView(&summaryResp)
			summaryMdGen := session.NewSummaryMarkdownGenerator()
			summaryMdBytes, err := summaryMdGen.Generate(rawSession.Meta, summaryView, rawSession.Entries)
			if err == nil {
				summaryMdPath := filepath.Join(sessionPath, ledgerFileSummaryMD)
				if writeErr := os.WriteFile(summaryMdPath, summaryMdBytes, 0644); writeErr != nil {
					errs = append(errs, fmt.Sprintf("%s: %s", ledgerFileSummaryMD, writeErr))
				}
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("partial failures: %s", strings.Join(errs, "; "))
	}
	return nil
}

// mapEntriesToTyped converts map-based entries to typed Entry structs
// for use with event extraction and other typed APIs.
func mapEntriesToTyped(mapEntries []map[string]any) []session.Entry {
	entries := make([]session.Entry, 0, len(mapEntries))
	for _, m := range mapEntries {
		var entry session.Entry

		if t, ok := m["type"].(string); ok {
			entry.Type = session.SessionEntryType(t)
		}
		if c, ok := m["content"].(string); ok {
			entry.Content = c
		}
		if tn, ok := m["tool_name"].(string); ok {
			entry.ToolName = tn
		}
		if ti, ok := m["tool_input"].(string); ok {
			entry.ToolInput = ti
		}
		if to, ok := m["tool_output"].(string); ok {
			entry.ToolOutput = to
		}

		entries = append(entries, entry)
	}
	return entries
}

// downloadFileFromLFS downloads a single file from LFS by its OID in the meta manifest.
func downloadFileFromLFS(projectRoot, sessionPath string, meta *lfs.SessionMeta, filename string) error {
	ref, ok := meta.Files[filename]
	if !ok {
		return fmt.Errorf("%s not found in session manifest", filename)
	}

	client, err := getLFSClient(projectRoot)
	if err != nil {
		return hydrateHint(err)
	}

	bareOID := ref.BareOID()
	resp, err := client.BatchDownload([]lfs.BatchObject{{OID: bareOID, Size: ref.Size}})
	if err != nil {
		return hydrateHint(err)
	}

	results := lfs.DownloadAll(resp, 1)
	if len(results) == 0 {
		return fmt.Errorf("no download results for %s", filename)
	}

	r := results[0]
	if r.Error != nil {
		return r.Error
	}

	// verify integrity
	computedOID := lfs.ComputeOID(r.Content)
	if computedOID != r.OID {
		return fmt.Errorf("SHA256 mismatch for %s: expected %s got %s", filename, r.OID, computedOID)
	}

	filePath := filepath.Join(sessionPath, filename)
	return os.WriteFile(filePath, r.Content, 0644)
}

// redactSummaryJSON reads summary.json, redacts text fields, and re-writes it.
// Returns true if any changes were made.
func redactSummaryJSON(path string, redactor *session.Redactor) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	var summary session.SummarizeResponse
	if err := json.Unmarshal(data, &summary); err != nil {
		return false, fmt.Errorf("parse summary.json: %w", err)
	}

	changed := false

	if out, found := redactor.RedactString(summary.Title); len(found) > 0 {
		summary.Title = out
		changed = true
	}
	if out, found := redactor.RedactString(summary.Summary); len(found) > 0 {
		summary.Summary = out
		changed = true
	}
	if out, found := redactor.RedactString(summary.Outcome); len(found) > 0 {
		summary.Outcome = out
		changed = true
	}
	if out, found := redactor.RedactString(summary.FinalPlan); len(found) > 0 {
		summary.FinalPlan = out
		changed = true
	}

	for i, action := range summary.KeyActions {
		if out, found := redactor.RedactString(action); len(found) > 0 {
			summary.KeyActions[i] = out
			changed = true
		}
	}
	for i, topic := range summary.TopicsFound {
		if out, found := redactor.RedactString(topic); len(found) > 0 {
			summary.TopicsFound[i] = out
			changed = true
		}
	}
	for i := range summary.AhaMoments {
		if out, found := redactor.RedactString(summary.AhaMoments[i].Highlight); len(found) > 0 {
			summary.AhaMoments[i].Highlight = out
			changed = true
		}
		if out, found := redactor.RedactString(summary.AhaMoments[i].Why); len(found) > 0 {
			summary.AhaMoments[i].Why = out
			changed = true
		}
	}
	for i, diagram := range summary.Diagrams {
		if out, found := redactor.RedactString(diagram); len(found) > 0 {
			summary.Diagrams[i] = out
			changed = true
		}
	}
	for i, title := range summary.ChapterTitles {
		if out, found := redactor.RedactString(title); len(found) > 0 {
			summary.ChapterTitles[i] = out
			changed = true
		}
	}
	for i := range summary.SageoxInsights {
		if out, found := redactor.RedactString(summary.SageoxInsights[i].Topic); len(found) > 0 {
			summary.SageoxInsights[i].Topic = out
			changed = true
		}
		if out, found := redactor.RedactString(summary.SageoxInsights[i].Insight); len(found) > 0 {
			summary.SageoxInsights[i].Insight = out
			changed = true
		}
		if out, found := redactor.RedactString(summary.SageoxInsights[i].Impact); len(found) > 0 {
			summary.SageoxInsights[i].Impact = out
			changed = true
		}
	}

	if !changed {
		return false, nil
	}

	outData, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal summary.json: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, outData, 0644); err != nil {
		return false, err
	}
	return true, os.Rename(tmpPath, path)
}

// commitAndPushLedgerBatch commits all modified meta.json files in one commit and pushes.
func commitAndPushLedgerBatch(ledgerPath string, sessionNames []string) error {
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	// collect files to stage
	var filesToAdd []string
	for _, name := range sessionNames {
		sessionDir := filepath.Join(sessionsDir, name)
		filesToAdd = append(filesToAdd, filepath.Join(sessionDir, "meta.json"))
		// summary.json is git-tracked (not LFS)
		summaryPath := filepath.Join(sessionDir, "summary.json")
		if _, err := os.Stat(summaryPath); err == nil {
			filesToAdd = append(filesToAdd, summaryPath)
		}
		// stage LFS pointer files
		for _, pattern := range []string{"*.jsonl", "*.md"} {
			matches, _ := filepath.Glob(filepath.Join(sessionDir, pattern))
			filesToAdd = append(filesToAdd, matches...)
		}
	}
	filesToAdd = append(filesToAdd, filepath.Join(sessionsDir, ".gitignore"))

	// git add
	addArgs := append([]string{"-C", ledgerPath, "add"}, filesToAdd...)
	addCmd := exec.Command("git", addArgs...)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(output), err)
	}

	// git commit
	commitMsg := fmt.Sprintf("session: re-redact %d sessions", len(sessionNames))
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit failed: %s: %w", string(output), err)
	}

	return pushLedger(context.Background(), ledgerPath)
}
