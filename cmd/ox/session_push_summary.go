// session_push_summary.go is the inline-summarization endpoint: it accepts a
// summary JSON produced by the calling agent (in response to the summary_prompt
// returned by `ox session stop`), validates and enriches it, then commits and
// pushes summary.json to the ledger.
//
// # Why two execution sites exist for summarization
//
// Delegating summarization back to the calling agent is the right default
// *technically*: the agent already has the conversation in context (so input
// tokens are effectively free), no extra binary is required, and it works for
// every supported agent (Claude Code, Cursor, Aider, Codex CLI, …).
//
// But it blocks the user at exactly the wrong moment. They have just signaled
// "I'm done" and want to close the agent or `/clear` and move on; an inline
// LLM call holds them in the foreground for 30–120s for a ~1–4KB JSON output.
// We should only force the inline path when the user has not specified (and
// ox cannot auto-detect) an LLM agent for daemon callouts.
//
// The daemon-side equivalent lives in
// `internal/daemon/agentwork/session_finalize.go` and calls back into the
// shared `pushSummaryToLedger` flow below — so prompt construction and
// validation live in exactly one place. Today the dispatch is gated behind
// `SAGEOX_ASYNC_SESSION_UPLOAD=1` (see `cmd/ox/agent_session.go`); the plan
// is to lift that to a first-class `agent.summarizer` user-config key with
// `auto` as the default.
//
// See ADR-016 (docs/adr/ADR-016-session-summarization-delegation.md) for the
// full rationale, behavior matrix, and tradeoffs.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/telemetry"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/spf13/cobra"
)

// sessionPushSummaryCmd pushes a raw-summary.json file to the ledger session directory.
// This is called by the agent after generating a summary from the summary_prompt returned
// by `ox session stop`. The summary is small JSON (~1-50KB) and does not need LFS.
var sessionPushSummaryCmd = &cobra.Command{
	Use:   "push-summary",
	Short: "Push session summary to ledger",
	Long: `Push a generated summary JSON file to the session's ledger directory.

Called automatically by coding agents after processing the summary_prompt
from 'ox session stop'. The summary file is committed directly to git
(no LFS needed) alongside meta.json.`,
	RunE: runSessionPushSummary,
}

func init() {
	sessionPushSummaryCmd.Flags().String("file", "", "Path to the summary JSON file (use '-' for stdin)")
	sessionPushSummaryCmd.Flags().String("session-dir", "", "Path to the session directory in the ledger")
	_ = sessionPushSummaryCmd.MarkFlagRequired("file")
	_ = sessionPushSummaryCmd.MarkFlagRequired("session-dir")
}

// pushSummaryOutput is the JSON output format for push-summary.
type pushSummaryOutput struct {
	Success      bool    `json:"success"`
	Type         string  `json:"type"`
	SummaryPath  string  `json:"summary_path,omitempty"`
	Message      string  `json:"message,omitempty"`
	Error        string  `json:"error,omitempty"`
	Disposition  string  `json:"disposition,omitempty"`   // "upload", "local_only", "discard"
	QualityScore float64 `json:"quality_score,omitempty"` // 0.0-1.0 from LLM
}

func runSessionPushSummary(cmd *cobra.Command, args []string) error {
	filePath, _ := cmd.Flags().GetString("file")
	sessionDir, _ := cmd.Flags().GetString("session-dir")

	result := pushSummaryToLedger(filePath, sessionDir)

	jsonOut, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("format JSON: %w", err)
	}
	fmt.Println(string(jsonOut))

	if !result.Success {
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}

// pushSummaryToLedger validates inputs, copies the summary file, and commits+pushes.
func pushSummaryToLedger(filePath, sessionDir string) *pushSummaryOutput {
	pushStart := time.Now()

	// capture meta.json mtime before we modify it — represents when session stop
	// finished uploading initial artifacts. Used to compute stop-to-visible latency.
	var stopUploadedAt time.Time
	if info, err := os.Stat(filepath.Join(sessionDir, "meta.json")); err == nil {
		stopUploadedAt = info.ModTime()
	}

	// read summary data from file or stdin
	var data []byte
	var err error
	if filePath == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(filePath)
	}
	if err != nil {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("read summary file: %v", err),
		}
	}

	if !json.Valid(data) {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   "file is not valid JSON",
		}
	}

	// parse and validate the summary JSON. Detect whether quality_score was
	// actually present in the file so a missing field is treated as "unscored"
	// (default to upload) rather than flowing through the discard gate as an
	// explicit 0 — otherwise pre-existing summary.json files written by older
	// ox versions (no quality_score field) would be silently discarded (#525).
	var rawFields map[string]json.RawMessage
	_ = json.Unmarshal(data, &rawFields)
	_, qualityScorePresent := rawFields["quality_score"]

	var summaryParsed session.SummarizeResponse
	_ = json.Unmarshal(data, &summaryParsed)

	// validate summary content for agent meta-output contamination
	if valErr := sessionsummary.ValidateSummaryContent(&summaryParsed); valErr != nil {
		slog.Warn("summary content validation failed", "error", valErr)
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("summary content invalid: %v", valErr),
		}
	}

	// Read meta once; reused below for richness validation and sageox score injection.
	meta, _ := lfs.ReadSessionMeta(sessionDir)

	// Enforce richness schema on non-trivial sessions. The prompt asks for
	// key_actions / aha_moments / etc.; rejecting summaries that skip them
	// is essentially free quality — output tokens cost nothing compared to
	// the input tokens already paid to ingest the session. See
	// sessionsummary.ValidateSummaryRichness for thresholds.
	entryCount := 0
	if meta != nil {
		entryCount = meta.EntryCount
	}
	if richErr := sessionsummary.ValidateSummaryRichness(&summaryParsed, entryCount); richErr != nil {
		slog.Warn("summary richness validation failed", "error", richErr, "entry_count", entryCount)
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("summary too thin: %v", richErr),
		}
	}

	// inject sageox contribution score for the session's recorded agent
	if meta != nil && meta.AgentID != "" {
		if scoreFile, _ := session.ReadSageoxScore(meta.AgentID); scoreFile != nil {
			summaryParsed.SageoxScore = &scoreFile.Score
			summaryParsed.SageoxScoreCategory = string(scoreFile.Category)
			summaryParsed.SageoxScoreReason = scoreFile.Reason
		}
	}

	// load quality thresholds from user config
	userCfg, _ := config.LoadUserConfig()
	awCfg := userCfg.GetAgentWorkerConfig()
	uploadThreshold := (&config.AgentWorkerConfig{}).GetQualityUploadThreshold()
	discardThreshold := (&config.AgentWorkerConfig{}).GetQualityDiscardThreshold()
	if awCfg != nil {
		uploadThreshold = awCfg.GetQualityUploadThreshold()
		discardThreshold = awCfg.GetQualityDiscardThreshold()
	}

	// unscored summaries (field absent from JSON) default to upload; scored
	// summaries flow through the quality thresholds.
	var disposition session.QualityDisposition
	if qualityScorePresent {
		disposition = session.EvaluateQuality(summaryParsed.QualityScore, uploadThreshold, discardThreshold)
	} else {
		disposition = session.QualityUpload
	}

	if disposition == session.QualityDiscard {
		slog.Info("session below discard threshold, skipping push",
			"quality_score", summaryParsed.QualityScore,
			"threshold", discardThreshold,
			"reason", summaryParsed.ScoreReason,
		)
		return &pushSummaryOutput{
			Success:      true,
			Type:         "push_summary",
			Message:      "session discarded (below quality threshold)",
			Disposition:  string(session.QualityDiscard),
			QualityScore: summaryParsed.QualityScore,
		}
	}

	// validate --session-dir exists
	if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("session directory does not exist: %s", sessionDir),
		}
	}

	// resolve ledger path by walking up from session-dir to find the git root
	ledgerPath, err := findGitRootFrom(sessionDir)
	if err != nil {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("find ledger git root: %v", err),
		}
	}

	// preserve computed fields (files_changed, chapters) from existing summary.json
	// that was enriched by WriteSessionArtifacts during session stop. The LLM-generated
	// summary doesn't include these fields, and raw.jsonl may be an LFS stub by now,
	// so we must carry them forward rather than re-computing.
	summaryDst := filepath.Join(sessionDir, "summary.json")
	preserveComputedFields(summaryDst, &summaryParsed)

	// write merged summary to ledger
	mergedData, mergeErr := json.MarshalIndent(summaryParsed, "", "  ")
	if mergeErr != nil {
		mergedData = data // fall back to original LLM summary
	}
	if err := os.WriteFile(summaryDst, mergedData, 0644); err != nil {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("write summary.json: %v", err),
		}
	}

	// Update meta.json title/summary from the AI-generated summary.json.
	//
	// ox-wstd: this used to be gated on `summaryObj.Title != ""`. The
	// guard caused sticky failure tombstones — once a validator-failure
	// stub had landed in meta.summary (pre-ox-qqka), no later success
	// could overwrite it because the guard short-circuited. 14 sessions
	// on the SageOx Internal ledger had successful summary.json regen
	// but stale meta.summary forever.
	//
	// Always call UpdateMetaSummary now. An empty title is a meaningful
	// signal: it CLEARS stale state (e.g. a previous failed attempt left
	// a non-empty title behind). UpdateMetaSummary writes both Title and
	// Summary, so this also keeps the two fields consistent.
	metaUpdated := false
	var summaryObj struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(data, &summaryObj); err == nil {
		if err := lfs.UpdateMetaSummary(sessionDir, summaryObj.Title); err != nil {
			slog.Debug("update meta.json summary", "error", err)
		} else {
			metaUpdated = true
		}
	}

	// Register summary.json in meta.Files with Storage=git so the manifest
	// is the single source of truth for "what artifacts belong to this
	// session". commitAndPushLedger and the redact path iterate meta.Files
	// to decide what to stage; without this entry summary.json would only
	// be staged via the explicit summaryDst arg below (today's behavior),
	// which keeps drifting every time a new code path is added.
	//
	// Best-effort: if meta.json is missing or unwritable, fall through —
	// the summary.json file itself is still committed via the explicit
	// add below, and doctor will reconcile the manifest later. We also
	// re-mark metaUpdated so meta.json gets staged in the same commit.
	if updated := registerGitArtifactInMeta(sessionDir, "summary.json", int64(len(mergedData))); updated {
		metaUpdated = true
	}

	// extract session name from session dir path for commit message
	sessionName := filepath.Base(sessionDir)

	// if below upload threshold, keep locally but don't push
	if disposition == session.QualityLocalOnly {
		slog.Info("session below upload threshold, keeping locally",
			"session", sessionName,
			"quality_score", summaryParsed.QualityScore,
			"threshold", uploadThreshold,
			"reason", summaryParsed.ScoreReason,
		)
		clearNeedsSummaryMarkerForSession(sessionName)
		return &pushSummaryOutput{
			Success:      true,
			Type:         "push_summary",
			SummaryPath:  summaryDst,
			Message:      "summary saved locally (below upload threshold)",
			Disposition:  string(session.QualityLocalOnly),
			QualityScore: summaryParsed.QualityScore,
		}
	}

	// disposition == QualityUpload — commit and push to ledger
	// ensure .gitignore is in place before any commit to prevent cache file leakage
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	// git add the summary file (and meta.json if updated)
	// --sparse: ledger repos use sparse-checkout
	addArgs := []string{"-C", ledgerPath, "add", "--sparse", summaryDst}
	if metaUpdated {
		addArgs = append(addArgs, filepath.Join(sessionDir, "meta.json"))
	}
	addCmd := exec.Command("git", addArgs...)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("git add: %s: %v", strings.TrimSpace(string(output)), err),
		}
	}

	// git commit
	commitMsg := fmt.Sprintf("summary: %s", sessionName)
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		outStr := string(output)
		if strings.Contains(outStr, "nothing to commit") {
			clearNeedsSummaryMarkerForSession(sessionName)
			return &pushSummaryOutput{
				Success:      true,
				Type:         "push_summary",
				SummaryPath:  summaryDst,
				Message:      "summary already committed",
				Disposition:  string(session.QualityUpload),
				QualityScore: summaryParsed.QualityScore,
			}
		}
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("git commit: %s: %v", strings.TrimSpace(outStr), err),
		}
	}

	// push using existing retry logic
	slog.Info("pushing summary to ledger", "session", sessionName)
	if err := pushLedger(context.Background(), ledgerPath); err != nil {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("git push: %v", err),
		}
	}

	// clear .needs-summary marker in cache if we can find it
	clearNeedsSummaryMarkerForSession(sessionName)

	// emit stop-to-visible telemetry: total time from session stop upload to summary push
	if cliCtx != nil && cliCtx.TelemetryClient != nil {
		meta := map[string]string{
			"push_ms":       strconv.FormatInt(time.Since(pushStart).Milliseconds(), 10),
			"quality_score": strconv.FormatFloat(summaryParsed.QualityScore, 'f', 2, 64),
		}
		if !stopUploadedAt.IsZero() {
			meta["stop_to_visible_ms"] = strconv.FormatInt(time.Since(stopUploadedAt).Milliseconds(), 10)
		}
		cliCtx.TelemetryClient.TrackAsync(telemetry.Event{
			Type:     telemetry.EventSessionPushSummary,
			Success:  true,
			Metadata: meta,
		})
	}

	return &pushSummaryOutput{
		Success:      true,
		Type:         "push_summary",
		SummaryPath:  summaryDst,
		Message:      "summary pushed to ledger",
		Disposition:  string(session.QualityUpload),
		QualityScore: summaryParsed.QualityScore,
	}
}

// clearNeedsSummaryMarkerForSession clears the .needs-summary marker and prunes
// the session cache directory. The cache was intentionally kept alive after upload
// so that push-summary could read raw.jsonl (the ledger copy becomes an LFS stub).
// Now that push-summary is done, the cache can be safely removed.
func clearNeedsSummaryMarkerForSession(sessionName string) {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return
	}

	repoID := getRepoIDOrDefault(gitRoot)
	contextPath := session.GetContextPath(repoID)
	if contextPath == "" {
		return
	}

	cacheSessionDir := filepath.Join(contextPath, "sessions", sessionName)
	if err := session.ClearNeedsSummaryMarker(cacheSessionDir); err != nil {
		slog.Debug("clear needs-summary marker", "error", err, "session", sessionName)
	}

	// prune cache now that push-summary is complete
	if cacheSessionDir != "" && cacheSessionDir != "." {
		if err := os.RemoveAll(cacheSessionDir); err != nil {
			slog.Debug("prune session cache after push-summary", "dir", cacheSessionDir, "error", err)
		}
	}
}

// preserveComputedFields reads the existing summary.json and carries forward
// files_changed and chapters into the new summary if the new summary lacks them.
// These fields were computed by WriteSessionArtifacts during session stop (when
// raw.jsonl was still in cache). By push-summary time, raw.jsonl may be an LFS
// stub, so re-computing is not reliable.
func preserveComputedFields(summaryPath string, newSummary *session.SummarizeResponse) {
	existingData, err := os.ReadFile(summaryPath)
	if err != nil {
		return
	}
	var existing session.SummarizeResponse
	if json.Unmarshal(existingData, &existing) != nil {
		return
	}
	if len(newSummary.FilesChanged) == 0 && len(existing.FilesChanged) > 0 {
		newSummary.FilesChanged = existing.FilesChanged
	}
	if len(newSummary.Chapters) == 0 && len(existing.Chapters) > 0 {
		newSummary.Chapters = existing.Chapters
	}
}

// registerGitArtifactInMeta records a git-stored (non-LFS) artifact in
// the session's meta.json Files manifest. Returns true if meta.json was
// successfully read and rewritten so the caller knows to stage meta.json
// in the next commit.
//
// Best-effort: any failure (missing meta.json, unwritable, invalid JSON)
// returns false. The artifact file is still committed by the caller's
// explicit `git add`; the manifest entry is a follow-up that doctor can
// reconcile if it doesn't land here.
//
// Idempotent: if meta.Files[filename] already records a Storage=git entry
// with the same size, no rewrite is performed.
func registerGitArtifactInMeta(sessionDir, filename string, size int64) bool {
	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		slog.Debug("registerGitArtifactInMeta: meta.json unreadable", "session", sessionDir, "filename", filename, "error", err)
		return false
	}
	if meta.Files == nil {
		meta.Files = make(map[string]lfs.FileRef)
	}
	if existing, ok := meta.Files[filename]; ok && existing.IsGit() && existing.Size == size {
		return false // already up-to-date
	}
	meta.Files[filename] = lfs.NewGitFileRef(size)
	if err := lfs.WriteSessionMetaOnly(sessionDir, meta); err != nil {
		slog.Debug("registerGitArtifactInMeta: write meta.json failed", "session", sessionDir, "error", err)
		return false
	}
	return true
}

// findGitRootFrom walks up from a directory to find the .git root.
func findGitRootFrom(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	for {
		gitDir := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no git root found above %s", dir)
		}
		dir = parent
	}
}
