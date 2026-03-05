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

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	sessionhtml "github.com/sageox/ox/internal/session/html"
	"github.com/spf13/cobra"
)

var sessionRegenerateCmd = &cobra.Command{
	Use:   "regenerate [session-name]",
	Short: "Regenerate session artifacts or re-redact session data",
	Long: `Regenerate session artifacts from raw data.

By default, regenerates the session.html file from raw session data.
Useful when the HTML template has been updated and you want to
refresh existing sessions with the new design.

With --redact, re-applies all current REDACT.md rules (team + repo + user
layers), regenerates all downstream artifacts (events.jsonl, session.html,
session.md, summary.md), and uploads updated content to LFS.
Old LFS blobs become orphaned after regeneration. Server-side blob purge
will be handled by the /api/v1/git/lfs/purge cloud API endpoint.

The session name supports partial matching (e.g. agent ID suffix).

Examples:
  ox session regenerate OxK3ZN                          # regenerate HTML
  ox session regenerate --all                           # regenerate all HTML
  ox session regenerate OxK3ZN --redact                 # re-redact session
  ox session regenerate --redact --all                  # re-redact all sessions
  ox session regenerate OxK3ZN --redact --dry-run       # preview redaction`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSessionRegenerate,
}

func init() {
	sessionRegenerateCmd.Flags().Bool("redact", false, "re-apply current REDACT.md rules to session data")
	sessionRegenerateCmd.Flags().Bool("all", false, "regenerate all sessions")
	sessionRegenerateCmd.Flags().Bool("dry-run", false, "preview what would change without modifying anything (--redact only)")
	sessionRegenerateCmd.Flags().Bool("force", false, "skip confirmation prompts")
}

func runSessionRegenerate(cmd *cobra.Command, args []string) error {
	redact, _ := cmd.Flags().GetBool("redact")
	regenAll, _ := cmd.Flags().GetBool("all")
	force, _ := cmd.Flags().GetBool("force")

	if redact {
		return runSessionRegenerateRedact(cmd, args)
	}

	// default mode: HTML-only regeneration
	store, projectRoot, err := newSessionStore()
	if err != nil {
		return err
	}

	if regenAll {
		return regenerateAllSessionsHTML(store, projectRoot, force)
	}

	if len(args) == 0 {
		return fmt.Errorf("specify a session name or use --all\nRun 'ox session list' to see available sessions")
	}

	return regenerateSingleSessionHTML(store, projectRoot, args[0])
}

// --- HTML-only regeneration (default mode) ---

func regenerateSingleSessionHTML(store *session.Store, projectRoot, name string) error {
	sessionName, err := store.ResolveSessionName(name)
	if err != nil {
		return fmt.Errorf("resolve session name: %w", err)
	}

	storedSession, err := store.ReadSession(sessionName)
	if err != nil {
		return fmt.Errorf("session %q not found\nRun 'ox session list' to see available sessions", name)
	}

	sessionPath := store.GetSessionPath(sessionName)
	if err := regenerateSessionHTML(storedSession, sessionPath); err != nil {
		return err
	}

	if err := syncRegeneratedSession(projectRoot, sessionPath, sessionName); err != nil {
		slog.Warn("ledger sync skipped", "session", sessionName, "error", err)
	}

	cli.PrintSuccess(fmt.Sprintf("Regenerated HTML for %s", sessionName))
	return nil
}

func regenerateAllSessionsHTML(store *session.Store, projectRoot string, force bool) error {
	sessions, err := store.ListAllSessions()
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	if !force {
		if !cli.ConfirmYesNo(fmt.Sprintf("Regenerate HTML for %d session(s)?", len(sessions)), false) {
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

		storedSession, readErr := store.ReadSession(sessionName)
		if readErr != nil {
			slog.Warn("skipping unreadable session", "session", sessionName, "error", readErr)
			skipped++
			continue
		}

		sessionPath := store.GetSessionPath(sessionName)
		if regenErr := regenerateSessionHTML(storedSession, sessionPath); regenErr != nil {
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
				htmlPath := filepath.Join(sessionPath, ledgerFileHTML)
				if _, statErr := os.Stat(htmlPath); statErr != nil {
					continue
				}
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

// regenerateSessionHTML deletes any existing session.html and generates a new one.
func regenerateSessionHTML(storedSession *session.StoredSession, sessionPath string) error {
	htmlPath := filepath.Join(sessionPath, ledgerFileHTML)

	if err := os.Remove(htmlPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing HTML: %w", err)
	}

	if err := generateHTML(storedSession, htmlPath); err != nil {
		return fmt.Errorf("generate HTML: %w", err)
	}

	slog.Debug("regenerated session HTML", "path", htmlPath)
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

	// ensure raw.jsonl is available locally
	rawPath := filepath.Join(sessionPath, ledgerFileRaw)
	if _, err := os.Stat(rawPath); err != nil {
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

// regenerateArtifacts regenerates events.jsonl, session.html, session.md, and summary.md
// from the re-redacted raw session data.
func regenerateArtifacts(sessionPath string, rawSession *session.StoredSession) error {
	var errs []string

	// convert map entries to typed entries for event extraction
	entries := mapEntriesToTyped(rawSession.Entries)

	// events.jsonl
	eventLog := session.NewEventLog(entries, "", "")
	eventsPath := filepath.Join(sessionPath, ledgerFileEvents)
	if err := session.WriteEventLog(eventsPath, eventLog); err != nil {
		errs = append(errs, fmt.Sprintf("%s: %s", ledgerFileEvents, err))
	}

	// session.html
	htmlGen, err := sessionhtml.NewGenerator()
	if err == nil {
		htmlPath := filepath.Join(sessionPath, ledgerFileHTML)
		if err := htmlGen.GenerateToFile(rawSession, htmlPath); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", ledgerFileHTML, err))
		}
	} else {
		errs = append(errs, fmt.Sprintf("%s init: %s", ledgerFileHTML, err))
	}

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
			summaryView := &session.SummaryView{
				Text:        summaryResp.Summary,
				KeyActions:  summaryResp.KeyActions,
				Outcome:     summaryResp.Outcome,
				TopicsFound: summaryResp.TopicsFound,
				FinalPlan:   summaryResp.FinalPlan,
				Diagrams:    summaryResp.Diagrams,
			}
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
		filesToAdd = append(filesToAdd, filepath.Join(sessionsDir, name, "meta.json"))
		// summary.json is git-tracked (not LFS)
		summaryPath := filepath.Join(sessionsDir, name, "summary.json")
		if _, err := os.Stat(summaryPath); err == nil {
			filesToAdd = append(filesToAdd, summaryPath)
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
