package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	sessionhtml "github.com/sageox/ox/internal/session/html"
	"github.com/sageox/ox/internal/useragent"
	"github.com/sageox/ox/internal/version"
)

// Agent UX Decision: JSON is the default output format for session commands.
//
// Why: Session commands are typically called by agents to get file paths and
// metadata. Text output was verbose and required parsing. Agents are the primary
// consumers of these commands.
//
// Flag behavior:
//   --text:   Human-readable output for developers reviewing session results
//   --review: Security audit mode showing both human summary and machine output
//   --json:   Explicit JSON (same as default, for clarity)
//
// Priority (highest to lowest):
//   1. --review: outputs both human summary and JSON
//   2. --text: outputs human-readable text only
//   3. default: outputs full JSON

// sessionStartGuidance is behavioral guidance for agents during a recorded session.
// Returned in the session start JSON so all coding agents (not just Claude Code) receive it.
const sessionStartGuidance = `During this recorded session:
1. Plan capture: After creating or revising a plan, immediately save it with: cat <plan-file> | ox agent <id> session plan
2. After stopping: Check the session stop output for plan_path. If empty and you created a plan, save it now with: cat <plan-file> | ox agent <id> session plan
3. Session boundaries: One plan per session. If work shifts to an unrelated feature, suggest stopping this session and starting a new one.`

// sessionStartOutput is the JSON output format for session start.
type sessionStartOutput struct {
	Success bool   `json:"success"`
	Type    string `json:"type"`
	AgentID string `json:"agent_id"`
	Title   string `json:"title,omitempty"`
	Adapter string `json:"adapter"`
	Started string `json:"started"`
	Hint     string `json:"hint,omitempty"`     // suggests how to end recording
	Notice   string `json:"notice,omitempty"`   // one-time notice the agent MUST show to the user
	Guidance string `json:"guidance,omitempty"` // behavioral guidance for the agent during the session
}

// runAgentSessionStart starts recording a session for the agent.
// Usage: ox agent <id> session start [--title "..."]
func runAgentSessionStart(inst *agentinstance.Instance, args []string) error {
	// verify redaction signature before starting - warn if tampered
	warnIfRedactionSignatureInvalid()

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// verify SageOx is initialized in this project
	if !config.IsInitialized(projectRoot) {
		return fmt.Errorf("SageOx not initialized in this project\nRun 'ox init' first to set up session recording")
	}

	// require authentication — sessions need a user identity for the ledger
	if authenticated, _ := auth.IsAuthenticated(); !authenticated {
		return fmt.Errorf("not logged in to SageOx\nRun 'ox login' first to authenticate")
	}

	// check write access before recording — fail fast if user is a viewer
	if err := checkUploadAccess(projectRoot); err != nil {
		if errors.Is(err, api.ErrReadOnly) {
			return fmt.Errorf("you have read-only access to this repo — sessions cannot be uploaded to the ledger\nTo upload sessions, request team membership from an admin")
		}
		// non-ErrReadOnly errors fall through (fail-open)
	}

	// one-time session recording notice (returned to caller via JSON)
	notice := getSessionTermsNotice()

	// check for existing recording state
	existingState, _ := session.LoadRecordingState(projectRoot)
	if existingState != nil {
		if existingState.AgentID == inst.AgentID {
			// same agent — genuine duplicate start, refuse
			return fmt.Errorf("a session is already being recorded\nRun 'ox agent %s session stop' first, then start a new session", inst.AgentID)
		}
		// different agent ID — this is a ghost session from a previous Claude Code
		// instance (e.g., user restarted after hooks install notice). auto-clear it
		// so the new session can start cleanly.
		slog.Info("clearing ghost session from previous agent", "old_agent", existingState.AgentID, "new_agent", inst.AgentID, "started_at", existingState.StartedAt)
		if err := session.ClearRecordingState(projectRoot); err != nil {
			slog.Warn("failed to clear ghost session", "error", err)
		}
	}

	// parse optional title from args (simple parsing: --title "value" or --title=value)
	title := parseTitle(args)

	// detect coding agent adapter
	adapter, err := adapters.DetectAdapter()
	if err != nil {
		if errors.Is(err, adapters.ErrNoAdapterDetected) {
			return fmt.Errorf("no coding agent detected\nSupported agents: Claude Code, Cursor, Windsurf")
		}
		return fmt.Errorf("failed to detect coding agent: %w", err)
	}

	adapterName := adapter.Name()
	useragent.SetAgentType(adapterName)

	// find the agent's session file
	since := time.Now().Add(-5 * time.Minute)
	sessionFile, err := adapter.FindSessionFile(inst.AgentID, since)
	if err != nil {
		if errors.Is(err, adapters.ErrSessionNotFound) {
			return fmt.Errorf("no active %s session found", adapterName)
		}
		return fmt.Errorf("failed to find session file: %w", err)
	}

	// start recording with agent ID from session
	opts := session.StartRecordingOptions{
		AgentID:     inst.AgentID,
		AdapterName: adapterName,
		SessionFile: sessionFile,
		Title:       title,
		Username:    getSessionUsername(),
	}

	state, err := session.StartRecording(projectRoot, opts)
	if err != nil {
		if errors.Is(err, session.ErrAlreadyRecording) {
			return fmt.Errorf("a session is already being recorded\nRun 'ox agent %s session stop' first, then start a new session", inst.AgentID)
		}
		if errors.Is(err, session.ErrNoLedger) {
			return fmt.Errorf("no ledger configured for this project\n\nTo enable session recording:\n  1. Run 'ox init' to set up this repository\n  2. This creates a ledger to store session history\n\nSee 'ox init --help' for options")
		}
		return fmt.Errorf("failed to start recording: %w", err)
	}

	// output format selection (priority: review > text > json default)
	if cfg.Review {
		// security audit mode: human summary first, then JSON
		if notice != "" {
			fmt.Printf("\n  %s\n\n", notice)
		}
		if title != "" {
			cli.PrintSuccess(fmt.Sprintf("%s session recording started: %q", cli.Wordmark(), title))
		} else {
			cli.PrintSuccess(cli.Wordmark() + " session recording started")
		}
		fmt.Printf("  Agent: %s (%s)\n", inst.AgentID, adapterName)
		fmt.Printf("  Started: %s\n", state.StartedAt.Format("15:04:05"))
		fmt.Printf("  Run %s to end recording\n", cli.StyleCommand.Render("/ox-session-stop"))
		fmt.Println()
		fmt.Println("--- Machine Output ---")
		output := sessionStartOutput{
			Success:  true,
			Type:     "session_start",
			AgentID:  inst.AgentID,
			Title:    title,
			Adapter:  adapterName,
			Started:  state.StartedAt.Format(time.RFC3339),
			Hint:     "Run /ox-session-stop to end recording",
			Notice:   notice,
			Guidance: sessionStartGuidance,
		}
		jsonOut, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return fmt.Errorf("format start JSON: %w", err)
		}
		fmt.Println(string(jsonOut))
		return nil
	}

	if cfg.Text {
		// human-readable text output
		if notice != "" {
			fmt.Printf("\n  %s\n\n", notice)
		}
		if title != "" {
			cli.PrintSuccess(fmt.Sprintf("%s session recording started: %q", cli.Wordmark(), title))
		} else {
			cli.PrintSuccess(cli.Wordmark() + " session recording started")
		}
		fmt.Printf("  Agent: %s (%s)\n", inst.AgentID, adapterName)
		fmt.Printf("  Started: %s\n", state.StartedAt.Format("15:04:05"))
		fmt.Printf("  Run %s to end recording\n", cli.StyleCommand.Render("/ox-session-stop"))
		return nil
	}

	// default: JSON output (or explicit --json)
	output := sessionStartOutput{
		Success:  true,
		Type:     "session_start",
		AgentID:  inst.AgentID,
		Title:    title,
		Adapter:  adapterName,
		Started:  state.StartedAt.Format(time.RFC3339),
		Hint:     "Run /ox-session-stop to end recording",
		Notice:   notice,
		Guidance: sessionStartGuidance,
	}
	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format start JSON: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}

// runAgentSessionStop stops recording and saves the session.
// Usage: ox agent <id> session stop
func runAgentSessionStop(inst *agentinstance.Instance) error {
	// verify redaction signature before stopping - warn if tampered
	// this is critical as secrets are about to be redacted and saved
	warnIfRedactionSignatureInvalid()

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// check if actually recording
	if !session.IsRecording(projectRoot) {
		return fmt.Errorf("not currently recording\nRun 'ox agent %s session start' to begin recording", inst.AgentID)
	}

	// stop recording and get final state
	state, err := session.StopRecording(projectRoot)
	if err != nil {
		if errors.Is(err, session.ErrNotRecording) {
			return fmt.Errorf("not currently recording\nRun 'ox agent %s session start' to begin recording", inst.AgentID)
		}
		// set marker so future ox agent prime knows doctor is needed
		_ = doctor.SetNeedsDoctorAgent(projectRoot) // session data may be lost
		return fmt.Errorf("failed to stop recording: %w", err)
	}

	duration := formatDurationHuman(state.Duration())

	// process session: read, redact secrets, extract events, save
	var processResult *agentSessionResult
	if state.SessionFile != "" {
		processResult, err = processAgentSession(projectRoot, state)
		if err != nil {
			// set marker so future ox agent prime knows doctor is needed
			_ = doctor.SetNeedsDoctorAgent(projectRoot) // best effort
			// non-fatal - continue with output
			fmt.Fprintf(os.Stderr, "warning: failed to process session: %v\n", err)
		}
	}

	// output format selection (priority: review > text > json default)
	if cfg.Review {
		// security audit mode: human summary first, then JSON
		outputTextSummary(state, duration, processResult)
		fmt.Println()
		fmt.Println("--- Machine Output ---")
		return outputSessionStopJSON(inst, state, duration, processResult)
	}

	if cfg.Text {
		// human-readable text output
		outputTextSummary(state, duration, processResult)
		return nil
	}

	// default: JSON output
	return outputSessionStopJSON(inst, state, duration, processResult)
}

// outputTextSummary renders the human-readable text summary for session stop.
func outputTextSummary(state *session.RecordingState, duration string, processResult *agentSessionResult) {
	if state.Title != "" {
		cli.PrintSuccess(fmt.Sprintf("Recording stopped: %q", state.Title))
	} else {
		cli.PrintSuccess("Recording stopped")
	}

	fmt.Printf("  Duration: %s\n", duration)
	fmt.Printf("  Agent: %s (%s)\n", state.AgentID, state.AdapterName)

	if processResult != nil {
		if processResult.Model != "" {
			fmt.Printf("  Model: %s\n", processResult.Model)
		}

		// show filter mode with event counts
		if processResult.FilterMode != "" && processResult.EventsBeforeFilter > 0 {
			modeDesc := formatFilterModeDescription(processResult.FilterMode)
			fmt.Printf("\n  Mode: %s (%s)\n", processResult.FilterMode, modeDesc)
			fmt.Printf("  Events: %d total -> %d after filtering\n",
				processResult.EventsBeforeFilter,
				processResult.EventsAfterFilter)
		}

		// show generated files with descriptions
		if processResult.RawPath != "" || processResult.HTMLPath != "" || processResult.SummaryMDPath != "" {
			fmt.Println("\n  Generated files:")
			if processResult.RawPath != "" {
				fmt.Printf("    Raw session:     %s\n", processResult.RawPath)
			}
			if processResult.EventsPath != "" {
				fmt.Printf("    Events log:      %s\n", processResult.EventsPath)
			}
			if processResult.HTMLPath != "" {
				fmt.Printf("    HTML viewer:     %s\n", processResult.HTMLPath)
			}
			if processResult.SummaryMDPath != "" {
				fmt.Printf("    Summary:         %s\n", processResult.SummaryMDPath)
			}
			if processResult.SessionMDPath != "" {
				fmt.Printf("    Full session:    %s\n", processResult.SessionMDPath)
			}
			if processResult.PlanPath != "" {
				fmt.Printf("    Plan:            %s\n", processResult.PlanPath)
			}
		}

		if processResult.SecretsRedacted > 0 {
			fmt.Printf("\n  Redacted: %d secrets\n", processResult.SecretsRedacted)
		}
		if processResult.Summary != "" {
			fmt.Printf("\n  Summary: %s\n", processResult.Summary)
		}
	}
}

// outputSessionStopJSON renders JSON output for session stop.
func outputSessionStopJSON(inst *agentinstance.Instance, state *session.RecordingState, duration string, processResult *agentSessionResult) error {
	output := sessionStopOutput{
		Success:  true,
		Type:     "session_stop",
		AgentID:  inst.AgentID,
		Duration: duration,
	}
	if state.Title != "" {
		output.Title = state.Title
	}
	if processResult != nil {
		output.RawPath = processResult.RawPath
		output.EventsPath = processResult.EventsPath
		output.HTMLPath = processResult.HTMLPath
		output.SummaryMDPath = processResult.SummaryMDPath
		output.SessionMDPath = processResult.SessionMDPath
		output.PlanPath = processResult.PlanPath
		output.EntryCount = processResult.EntryCount
		output.SecretsRedacted = processResult.SecretsRedacted
		output.Summary = processResult.Summary
		output.SummaryPrompt = processResult.SummaryPrompt
		output.Model = processResult.Model
		output.AgentVersion = processResult.AgentVersion
		output.FilterMode = processResult.FilterMode
		output.EventsBeforeFilter = processResult.EventsBeforeFilter
		output.EventsAfterFilter = processResult.EventsAfterFilter
		output.LedgerSessionDir = processResult.LedgerSessionDir
		output.UploadWarning = processResult.UploadWarning
	}
	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format stop JSON: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}

// sessionStopOutput is the JSON output format for session stop.
type sessionStopOutput struct {
	Success            bool   `json:"success"`
	Type               string `json:"type"`
	AgentID            string `json:"agent_id"`
	Title              string `json:"title,omitempty"`
	Duration           string `json:"duration"`
	RawPath            string `json:"raw_path,omitempty"`
	EventsPath         string `json:"events_path,omitempty"`
	HTMLPath           string `json:"html_path,omitempty"`
	SummaryMDPath      string `json:"summary_md_path,omitempty"`
	SessionMDPath      string `json:"session_md_path,omitempty"`
	PlanPath           string `json:"plan_path,omitempty"`
	EntryCount         int    `json:"entry_count,omitempty"`
	SecretsRedacted    int    `json:"secrets_redacted,omitempty"`
	Summary            string `json:"summary,omitempty"`
	SummaryPrompt      string `json:"summary_prompt,omitempty"`
	Model              string `json:"model,omitempty"`
	AgentVersion       string `json:"agent_version,omitempty"`
	FilterMode         string `json:"filter_mode,omitempty"`          // "infra" or "all"
	EventsBeforeFilter int    `json:"events_before_filter,omitempty"` // events before filtering
	EventsAfterFilter  int    `json:"events_after_filter,omitempty"`  // events after filtering
	LedgerSessionDir   string `json:"ledger_session_dir,omitempty"`   // path to session dir in ledger
	UploadWarning      string `json:"upload_warning,omitempty"`       // set when ledger upload failed
}

// parseTitle extracts --title value from args
func parseTitle(args []string) string {
	for i, arg := range args {
		if arg == "--title" && i+1 < len(args) {
			return args[i+1]
		}
		if len(arg) > 8 && arg[:8] == "--title=" {
			return arg[8:]
		}
	}
	return ""
}

// agentSessionResult contains outcomes from session processing
type agentSessionResult struct {
	RawPath            string
	EventsPath         string
	HTMLPath           string
	SummaryMDPath      string
	SessionMDPath      string
	EntryCount         int
	SecretsRedacted    int
	AgentVersion       string
	Model              string
	Summary            string // local summary text
	SummaryPrompt      string // prompt for calling agent to generate full summary
	FilterMode         string // "infra" or "all"
	EventsBeforeFilter int    // event count before filtering
	EventsAfterFilter  int    // event count after filtering
	PlanPath           string // path to plan.md (empty if no plan captured)
	SessionName        string // ledger session folder name (e.g. 2026-02-06T14-32-ryan-Ox7f3a)
	LedgerSessionDir   string // full path to session dir in ledger (empty if upload failed)
	UploadWarning      string // non-empty when ledger upload failed (explains recovery)
}

// processAgentSession reads, redacts secrets, and saves the session.
// Processes the agent session data into stored artifacts (raw, events, HTML, markdown).
//
// Architecture: cache → ledger two-phase design
//
// Session data is written to a local cache first (fast, never fails), then copied
// to the ledger git repo and uploaded to LFS (network-dependent, can retry).
// This ensures session stop never fails due to network issues.
//
//	Phase 1 (cache): redact secrets → write raw.jsonl, events.jsonl, HTML, markdown
//	Phase 2 (ledger): copy files → LFS upload → write meta.json → git commit+push
//	Cleanup: on phase 2 success, prune the local cache (ledger is source of truth)
//
// raw.jsonl is the critical source of truth — all other artifacts (events, HTML,
// summary, markdown) can be regenerated from it. If phase 2 fails, doctor's
// retrySessionUpload() recovers by re-copying from cache to ledger.
//
// Summary generation is agent-driven (via summary_prompt in session stop output),
// and push-summary writes it to the ledger. Doctor detects missing summaries
// by scanning the ledger directly.
func processAgentSession(projectRoot string, state *session.RecordingState) (*agentSessionResult, error) {
	result := &agentSessionResult{}

	// resolve project endpoint for auth lookups
	projectEndpoint := endpoint.GetForProject(projectRoot)

	// get adapter
	adapter, err := adapters.GetAdapter(state.AdapterName)
	if err != nil {
		return nil, fmt.Errorf("adapter not found: %w", err)
	}

	// read session metadata (agent version, model)
	sessionMeta, _ := adapter.ReadMetadata(state.SessionFile)
	if sessionMeta != nil {
		result.AgentVersion = sessionMeta.AgentVersion
		result.Model = sessionMeta.Model
	}

	// read entries from session file
	rawEntries, err := adapter.Read(state.SessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read session: %w", err)
	}

	if len(rawEntries) == 0 {
		return result, nil // nothing to process
	}
	result.EntryCount = len(rawEntries)

	// get repo ID for context path
	repoID := getRepoIDOrDefault(projectRoot)

	// create store
	contextPath := session.GetContextPath(repoID)
	if contextPath == "" {
		return nil, fmt.Errorf("failed to get context path")
	}

	store, err := session.NewStore(contextPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create store: %w", err)
	}

	// use session name from recording state (created at start time)
	// instead of generating a new name, which would have a different timestamp and
	// potentially different username, causing path mismatches
	filename := session.GetSessionName(state.SessionPath)

	// create redactor for secret scrubbing
	redactor := session.NewRedactor()

	// convert raw entries to session entries and redact secrets
	entries := make([]session.Entry, 0, len(rawEntries))
	for _, raw := range rawEntries {
		entry := session.Entry{
			Timestamp: raw.Timestamp,
			Content:   raw.Content,
			ToolName:  raw.ToolName,
			ToolInput: raw.ToolInput,
		}

		entry.Type = mapRoleToEntryType(raw.Role)
		entries = append(entries, entry)
	}

	// redact secrets from entries (modifies in place)
	result.SecretsRedacted = redactor.RedactEntries(entries)

	// also redact the raw JSON if present
	for i := range rawEntries {
		if len(rawEntries[i].Raw) > 0 {
			var rawData map[string]any
			if json.Unmarshal(rawEntries[i].Raw, &rawData) == nil {
				if redactor.RedactMap(rawData) {
					if redactedJSON, err := json.Marshal(rawData); err == nil {
						rawEntries[i].Raw = redactedJSON
					}
				}
			}
		}
	}

	// write raw session (with secrets redacted)
	rawWriter, err := store.CreateRaw(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to create raw session: %w", err)
	}

	// write header with metadata
	meta := &session.StoreMeta{
		Version:      "1.0",
		CreatedAt:    state.StartedAt,
		AgentID:      state.AgentID,
		AgentType:    state.AdapterName,
		AgentVersion: result.AgentVersion,
		Model:        result.Model,
		Username:     getDisplayName(projectEndpoint),
		RepoID:       repoID,
		OxVersion:    version.Version,
	}
	if err := rawWriter.WriteHeader(meta); err != nil {
		rawWriter.Close()
		return nil, fmt.Errorf("failed to write header: %w", err)
	}

	// write entries
	for _, entry := range entries {
		data := map[string]any{
			"type":      string(entry.Type),
			"content":   entry.Content,
			"timestamp": entry.Timestamp,
		}
		if entry.ToolName != "" {
			data["tool_name"] = entry.ToolName
		}
		if entry.ToolInput != "" {
			data["tool_input"] = entry.ToolInput
		}
		if entry.ToolOutput != "" {
			data["tool_output"] = entry.ToolOutput
		}
		if err := rawWriter.WriteRaw(data); err != nil {
			rawWriter.Close()
			return nil, fmt.Errorf("failed to write entry: %w", err)
		}
	}

	if err := rawWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close raw session: %w", err)
	}
	result.RawPath = rawWriter.FilePath()

	// generate and write events session
	eventLog := session.NewEventLog(entries, state.AgentID, state.AdapterName)

	// apply filtering based on session mode
	result.EventsBeforeFilter = len(eventLog.Events)
	result.FilterMode = state.FilterMode
	if state.FilterMode != "" {
		eventLog.Events = session.FilterEvents(eventLog.Events, session.SessionFilterMode(state.FilterMode))
	}
	result.EventsAfterFilter = len(eventLog.Events)

	eventsWriter, err := store.CreateEvents(filename)
	if err != nil {
		// raw session was saved but events failed - set marker
		_ = doctor.SetNeedsDoctorAgent(projectRoot)
		return nil, fmt.Errorf("failed to create events session: %w", err)
	}

	// write events header
	eventsMeta := &session.StoreMeta{
		Version:      "1.0",
		CreatedAt:    state.StartedAt,
		AgentID:      state.AgentID,
		AgentType:    state.AdapterName,
		AgentVersion: result.AgentVersion,
		Model:        result.Model,
		Username:     getDisplayName(projectEndpoint),
		RepoID:       repoID,
	}
	if err := eventsWriter.WriteHeader(eventsMeta); err != nil {
		eventsWriter.Close()
		// raw session was saved but events header failed - set marker
		_ = doctor.SetNeedsDoctorAgent(projectRoot)
		return nil, fmt.Errorf("failed to write events header: %w", err)
	}

	// write events
	for _, event := range eventLog.Events {
		data := map[string]any{
			"type":      string(event.Type),
			"summary":   event.Summary,
			"timestamp": event.Timestamp,
		}
		if event.Details != "" {
			data["details"] = event.Details
		}
		if event.ErrorMsg != "" {
			data["error"] = event.ErrorMsg
		}
		if event.RelatedFile != "" {
			data["file"] = event.RelatedFile
		}
		if event.Success != nil {
			data["success"] = *event.Success
		}
		if err := eventsWriter.WriteRaw(data); err != nil {
			eventsWriter.Close()
			// raw session was saved but events failed - set marker
			_ = doctor.SetNeedsDoctorAgent(projectRoot)
			return nil, fmt.Errorf("failed to write event: %w", err)
		}
	}

	if err := eventsWriter.Close(); err != nil {
		// raw session was saved but events failed - set marker
		_ = doctor.SetNeedsDoctorAgent(projectRoot)
		return nil, fmt.Errorf("failed to close events session: %w", err)
	}
	result.EventsPath = eventsWriter.FilePath()

	// generate local summary (no server API call - the calling agent will summarize via prompt)
	localSummary := session.LocalSummary(entries)
	summaryView := &sessionhtml.SummaryView{
		Text: localSummary,
	}
	sessionSummaryView := &session.SummaryView{
		Text: localSummary,
	}
	result.Summary = localSummary

	// use the session name from recording state (generated at start time)
	// to ensure cache and ledger directories always match
	sessionName := session.GetSessionName(state.SessionPath)
	result.SessionName = sessionName

	// resolve ledger path early so we can compute the session dir for the summary prompt
	ledgerPath, ledgerErr := resolveLedgerPath()
	if ledgerErr == nil {
		result.LedgerSessionDir = filepath.Join(ledgerPath, "sessions", sessionName)
	}

	result.SummaryPrompt = session.BuildSummaryPrompt(entries, result.RawPath, result.LedgerSessionDir)

	// mark that this session needs summary generation (cleared when push-summary succeeds)
	sessionCacheDir := filepath.Dir(result.RawPath)
	_ = session.WriteNeedsSummaryMarker(sessionCacheDir, result.RawPath, result.LedgerSessionDir)

	// generate HTML viewer with summary
	// failures here are non-fatal but we track them for doctor
	var htmlGenFailed, summaryGenFailed bool
	if result.RawPath != "" {
		htmlGen, err := sessionhtml.NewGenerator()
		if err == nil {
			// read back the raw session
			rawSession, readErr := store.ReadSession(filename)
			if readErr == nil && rawSession != nil {
				htmlPath := filepath.Join(filepath.Dir(result.RawPath), "session.html")
				if genErr := htmlGen.GenerateToFileWithSummary(rawSession, summaryView, htmlPath); genErr == nil {
					result.HTMLPath = htmlPath
				} else {
					htmlGenFailed = true
				}

				// generate summary markdown
				summaryMDPath := strings.TrimSuffix(result.RawPath, ".jsonl") + "-summary.md"
				summaryMDGen := session.NewSummaryMarkdownGenerator()
				summaryMDBytes, summaryMDErr := summaryMDGen.Generate(rawSession.Meta, sessionSummaryView, rawSession.Entries)
				if summaryMDErr == nil {
					if writeErr := os.WriteFile(summaryMDPath, summaryMDBytes, 0644); writeErr == nil {
						result.SummaryMDPath = summaryMDPath
					} else {
						summaryGenFailed = true
					}
				} else {
					summaryGenFailed = true
				}

				// generate full session markdown
				sessionMDPath := strings.TrimSuffix(result.RawPath, ".jsonl") + "-session.md"
				sessionMDGen := session.NewMarkdownGenerator()
				if sessionMDErr := sessionMDGen.GenerateToFile(rawSession, sessionMDPath); sessionMDErr == nil {
					result.SessionMDPath = sessionMDPath
				}
				// session MD failure is not critical, no marker needed
			}
		} else {
			htmlGenFailed = true
		}
	}

	// if HTML or summary generation failed, set marker for doctor
	if htmlGenFailed || summaryGenFailed {
		_ = doctor.SetNeedsDoctorAgent(projectRoot)
	}

	// check for plan.md saved during session (via `ox agent <id> session plan`)
	planSrcPath := filepath.Join(state.SessionPath, "plan.md")
	if _, statErr := os.Stat(planSrcPath); statErr == nil {
		cacheDir := filepath.Dir(result.RawPath)
		planDstPath := filepath.Join(cacheDir, "plan.md")
		if data, readErr := os.ReadFile(planSrcPath); readErr == nil {
			if writeErr := os.WriteFile(planDstPath, data, 0644); writeErr == nil {
				result.PlanPath = planDstPath
			}
		}
	}

	// LFS upload pipeline: upload content files to LFS blob storage,
	// write meta.json to ledger, commit and push.
	// This is best-effort -- session processing already succeeded.
	// No spinner here — bubbletea conflicts with Claude Code's own epoll on stdin.
	if ledgerErr != nil {
		// couldn't resolve ledger path - skip upload
		_ = doctor.SetNeedsDoctorAgent(projectRoot)
		fmt.Fprintf(os.Stderr, "warning: LFS upload skipped (no ledger): %v\n", ledgerErr)
		result.LedgerSessionDir = "" // clear since upload didn't happen
		result.UploadWarning = "Session saved locally but ledger upload skipped (no ledger). Run 'ox doctor' to retry."
	} else {
		uploadErr := uploadSessionToLedger(projectRoot, result, state, ledgerPath, sessionName)
		if uploadErr != nil {
			if errors.Is(uploadErr, api.ErrReadOnly) {
				fmt.Fprintln(os.Stderr, "\nUpload skipped — you have read-only access to this public repo.")
				fmt.Fprintln(os.Stderr, "To upload sessions, request team membership from an admin.")
				// don't set doctor marker, session saved locally
			} else {
				// LFS upload failed - set marker so doctor can retry
				_ = doctor.SetNeedsDoctorAgent(projectRoot)
				fmt.Fprintf(os.Stderr, "warning: LFS upload failed (session saved locally): %v\n", uploadErr)
				result.UploadWarning = "Session saved locally but ledger upload failed. Run 'ox doctor' to retry."
			}
			result.LedgerSessionDir = "" // clear since upload didn't succeed
		} else {
			// all files copied and committed to ledger — prune local cache.
			// the ledger is now the source of truth; doctor checks the ledger
			// directly for missing summaries (not the cache .needs-summary marker).
			if cacheDir := filepath.Dir(result.RawPath); cacheDir != "" && cacheDir != "." {
				if err := os.RemoveAll(cacheDir); err != nil {
					slog.Debug("prune session cache", "dir", cacheDir, "error", err)
				}
			}
		}
	}

	return result, nil
}

// uploadSessionToLedger copies content files from cache to ledger, uploads to LFS,
// writes meta.json, and commits+pushes. This is phase 2 of the two-phase design:
// content files are uploaded to LFS blob storage first, then meta.json (containing
// LFS OIDs) is committed to git. Content files themselves are .gitignore'd in the
// ledger repo — only meta.json is tracked by git. Other machines fetch content via LFS.
// If this fails, the session data is safe in the local cache and doctor can retry.
// ledgerPath and sessionName are pre-computed by the caller.
func uploadSessionToLedger(projectRoot string, result *agentSessionResult, state *session.RecordingState, ledgerPath, sessionName string) error {
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	sessionDir := filepath.Join(sessionsDir, sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	// raw.jsonl is the critical source of truth — copy and verify it first.
	// All other artifacts (events, HTML, summary, markdown) can be regenerated
	// from raw.jsonl, so their copy failures are non-fatal.
	if result.RawPath != "" {
		dstPath := filepath.Join(sessionDir, "raw.jsonl")
		data, err := os.ReadFile(result.RawPath)
		if err != nil {
			return fmt.Errorf("read raw.jsonl: %w", err)
		}
		if err := os.WriteFile(dstPath, data, 0644); err != nil {
			return fmt.Errorf("copy raw.jsonl to ledger: %w", err)
		}
	}

	// copy secondary artifacts (best-effort — failures logged but don't abort upload)
	secondaryFiles := map[string]string{
		"events.jsonl": result.EventsPath,
		"session.html": result.HTMLPath,
		"summary.md":   result.SummaryMDPath,
		"session.md":   result.SessionMDPath,
		"plan.md":      result.PlanPath,
	}
	for name, srcPath := range secondaryFiles {
		if srcPath == "" {
			continue
		}
		dstPath := filepath.Join(sessionDir, name)
		data, err := os.ReadFile(srcPath)
		if err != nil {
			slog.Warn("skip secondary artifact", "file", name, "error", err)
			continue
		}
		if err := os.WriteFile(dstPath, data, 0644); err != nil {
			slog.Warn("skip secondary artifact", "file", name, "error", err)
			continue
		}
	}

	// write meta.json first (before LFS upload) to preserve session metadata even if LFS fails
	projectEndpoint := endpoint.GetForProject(projectRoot)
	username := getAuthenticatedUsername(projectEndpoint)
	meta := lfs.NewSessionMeta(sessionName, username, state.AgentID, state.AdapterName, state.StartedAt).
		Model(result.Model).
		Title(state.Title).
		EntryCount(result.EntryCount).
		Summary(result.Summary).
		UserID(auth.GetUserID(projectEndpoint)).
		RepoID(getRepoIDOrDefault(projectRoot)).
		Build()
	if err := lfs.WriteSessionMeta(sessionDir, meta); err != nil {
		return fmt.Errorf("write meta.json: %w", err)
	}

	// upload content files to LFS blob storage
	fileRefs, err := uploadSessionLFS(projectRoot, sessionDir)
	if err != nil {
		if errors.Is(err, api.ErrReadOnly) {
			return err // don't wrap, don't set doctor marker
		}
		return fmt.Errorf("LFS upload: %w", err)
	}

	// update meta.json with LFS file references
	meta.Files = fileRefs
	if err := lfs.WriteSessionMeta(sessionDir, meta); err != nil {
		return fmt.Errorf("update meta.json with LFS refs: %w", err)
	}

	// ensure sessions/.gitignore exists
	if err := ensureSessionsGitignore(sessionsDir); err != nil {
		return fmt.Errorf("ensure .gitignore: %w", err)
	}

	// commit meta.json + .gitignore and push
	if err := commitAndPushLedger(ledgerPath, sessionName); err != nil {
		// set marker - session saved locally but not synced to remote
		_ = doctor.SetNeedsDoctorAgent(projectRoot)
		return fmt.Errorf("commit and push: %w", err)
	}

	return nil
}

// Note: getAuthenticatedUsername is defined in session_helpers.go

// sessionRemindOutput is the JSON output format for session remind.
type sessionRemindOutput struct {
	Success bool   `json:"success"`
	Type    string `json:"type"`
	AgentID string `json:"agent_id"`
	Message string `json:"message"`
}

// runAgentSessionRemind emits reminder info for the agent.
// Usage: ox agent <id> session remind
func runAgentSessionRemind(inst *agentinstance.Instance) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// check if recording is active
	if !session.IsRecording(projectRoot) {
		return fmt.Errorf("not currently recording\nRun 'ox agent %s session start' to begin recording", inst.AgentID)
	}

	state, err := session.LoadRecordingState(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load recording state: %w", err)
	}

	// update last reminder sequence
	if err := session.UpdateRecordingState(projectRoot, func(s *session.RecordingState) {
		s.LastReminderSeq = s.EntryCount
	}); err != nil {
		// non-fatal - continue with reminder
		fmt.Fprintf(os.Stderr, "warning: could not update reminder state: %v\n", err)
	}

	message := fmt.Sprintf("Recording active for %s", formatDurationHuman(state.Duration()))

	// output format selection (priority: review > text > json default)
	if cfg.Review {
		// security audit mode: human summary + JSON
		fmt.Printf("Recording active: %s (%s)\n", formatDurationHuman(state.Duration()), state.AdapterName)
		fmt.Println()
		fmt.Println("--- Machine Output ---")
		output := sessionRemindOutput{
			Success: true,
			Type:    "session_remind",
			AgentID: inst.AgentID,
			Message: message,
		}
		jsonOut, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return fmt.Errorf("format remind JSON: %w", err)
		}
		fmt.Println(string(jsonOut))
		return nil
	}

	if cfg.Text {
		// human-readable text output
		fmt.Printf("Recording active: %s (%s)\n", formatDurationHuman(state.Duration()), state.AdapterName)
		return nil
	}

	// default: JSON output
	output := sessionRemindOutput{
		Success: true,
		Type:    "session_remind",
		AgentID: inst.AgentID,
		Message: message,
	}
	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format remind JSON: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}

// sessionSummarizeOutput is the JSON output format for session summarize.
type sessionSummarizeOutput struct {
	Success       bool     `json:"success"`
	Type          string   `json:"type"`
	AgentID       string   `json:"agent_id"`
	Summary       string   `json:"summary"`
	KeyActions    []string `json:"key_actions,omitempty"`
	Outcome       string   `json:"outcome,omitempty"`
	TopicsFound   []string `json:"topics_found,omitempty"`
	EntryCount    int      `json:"entry_count"`
	FilePath      string   `json:"file_path,omitempty"`
	SummaryPrompt string   `json:"summary_prompt,omitempty"`
}

// runAgentSessionSummarize generates a summary of the session.
// Usage: ox agent <id> session summarize [--file <path>]
func runAgentSessionSummarize(inst *agentinstance.Instance, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// parse optional --file argument and positional session name
	var filePath string
	var sessionName string
	for i, arg := range args {
		if arg == "--file" && i+1 < len(args) {
			filePath = args[i+1]
		}
		if len(arg) > 7 && arg[:7] == "--file=" {
			filePath = arg[7:]
		}
	}
	// first positional arg (not a flag) is the session name
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			sessionName = arg
			break
		}
	}

	var entries []session.Entry
	var entryCount int

	if filePath != "" {
		// read from specified file
		entries, err = readEntriesFromFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read session file: %w", err)
		}
		entryCount = len(entries)
	} else if sessionName != "" {
		// read from named session in the store
		repoID := getRepoIDOrDefault(projectRoot)
		contextPath := session.GetContextPath(repoID)
		if contextPath == "" {
			return fmt.Errorf("no session store found")
		}
		store, err := session.NewStore(contextPath)
		if err != nil {
			return fmt.Errorf("failed to open session store: %w", err)
		}
		stored, err := store.ReadSession(sessionName)
		if err != nil {
			return fmt.Errorf("session not found: %s\nRun 'ox session list' to see available sessions", sessionName)
		}
		filePath = stored.Info.FilePath
		entries = convertStoredEntries(stored.Entries)
		entryCount = len(entries)
	} else {
		// get from current recording or latest session
		state, _ := session.LoadRecordingState(projectRoot)
		if state != nil && state.SessionFile != "" {
			// read from active recording
			adapter, err := adapters.GetAdapter(state.AdapterName)
			if err != nil {
				return fmt.Errorf("adapter not found: %w", err)
			}
			rawEntries, err := adapter.Read(state.SessionFile)
			if err != nil {
				return fmt.Errorf("failed to read session: %w", err)
			}
			entries = convertRawEntries(rawEntries)
			entryCount = len(entries)
		} else {
			// try to get latest session from store
			repoID := getRepoIDOrDefault(projectRoot)
			contextPath := session.GetContextPath(repoID)
			if contextPath == "" {
				return fmt.Errorf("no active recording and no session file specified")
			}
			store, err := session.NewStore(contextPath)
			if err != nil {
				return fmt.Errorf("failed to open session store: %w", err)
			}
			latest, err := store.GetLatestRaw()
			if err != nil {
				return fmt.Errorf("no sessions found: %w", err)
			}
			filePath = latest.FilePath
			stored, err := store.ReadRawSession(latest.Filename)
			if err != nil {
				return fmt.Errorf("failed to read session: %w", err)
			}
			entries = convertStoredEntries(stored.Entries)
			entryCount = len(entries)
		}
	}

	if len(entries) == 0 {
		return fmt.Errorf("no entries found in session")
	}

	// generate local summary (no server API call)
	localSummary := session.LocalSummary(entries)
	summaryResp := &session.SummarizeResponse{
		Summary: localSummary,
		Outcome: "local",
	}

	// build prompt for calling agent to generate full summary
	summaryPrompt := session.BuildSummaryPrompt(entries, filePath, "")

	// output format selection (priority: review > text > json default)
	if cfg.Review {
		// security audit mode: human summary + JSON
		cli.PrintSuccess("Session Summary")
		fmt.Printf("  Entries: %d\n", entryCount)
		fmt.Printf("  Summary: %s\n", summaryResp.Summary)
		fmt.Println()
		fmt.Println("--- Machine Output ---")
		output := sessionSummarizeOutput{
			Success:       true,
			Type:          "session_summary",
			AgentID:       inst.AgentID,
			Summary:       summaryResp.Summary,
			KeyActions:    summaryResp.KeyActions,
			Outcome:       summaryResp.Outcome,
			TopicsFound:   summaryResp.TopicsFound,
			EntryCount:    entryCount,
			FilePath:      filePath,
			SummaryPrompt: summaryPrompt,
		}
		jsonOut, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return fmt.Errorf("format summarize JSON: %w", err)
		}
		fmt.Println(string(jsonOut))
		return nil
	}

	if cfg.Text {
		// human-readable text output
		cli.PrintSuccess("Session Summary")
		fmt.Printf("  Entries: %d\n", entryCount)
		fmt.Printf("  Summary: %s\n", summaryResp.Summary)
		return nil
	}

	// default: JSON output
	output := sessionSummarizeOutput{
		Success:       true,
		Type:          "session_summary",
		AgentID:       inst.AgentID,
		Summary:       summaryResp.Summary,
		KeyActions:    summaryResp.KeyActions,
		Outcome:       summaryResp.Outcome,
		TopicsFound:   summaryResp.TopicsFound,
		EntryCount:    entryCount,
		FilePath:      filePath,
		SummaryPrompt: summaryPrompt,
	}
	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format summarize JSON: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}

// sessionHTMLOutput is the JSON output format for session html.
type sessionHTMLOutput struct {
	Success   bool   `json:"success"`
	Type      string `json:"type"`
	AgentID   string `json:"agent_id"`
	Generated bool   `json:"generated"`
	HTMLPath  string `json:"html_path"`
	Message   string `json:"message"`
}

// runAgentSessionHTML generates or displays info about HTML session viewer.
// Usage: ox agent <id> session html [--file <path>]
func runAgentSessionHTML(inst *agentinstance.Instance, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// parse optional --file argument
	var filePath string
	for i, arg := range args {
		if arg == "--file" && i+1 < len(args) {
			filePath = args[i+1]
		}
		if len(arg) > 7 && arg[:7] == "--file=" {
			filePath = arg[7:]
		}
	}

	// determine source and output paths
	var htmlPath string

	if filePath != "" {
		htmlPath = filepath.Join(filepath.Dir(filePath), "session.html")
	} else {
		// find latest session
		repoID := getRepoIDOrDefault(projectRoot)
		contextPath := session.GetContextPath(repoID)
		if contextPath == "" {
			return fmt.Errorf("no session file specified and no context path found")
		}
		store, err := session.NewStore(contextPath)
		if err != nil {
			return fmt.Errorf("failed to open session store: %w", err)
		}
		latest, err := store.GetLatestRaw()
		if err != nil {
			return fmt.Errorf("no sessions found: %w", err)
		}
		rawPath := latest.FilePath
		htmlPath = filepath.Join(filepath.Dir(rawPath), "session.html")
	}

	// check if HTML already exists
	var generated bool
	if _, err := os.Stat(htmlPath); err == nil {
		generated = true
	}

	var message string
	if generated {
		message = "HTML viewer already exists"
	} else {
		message = "HTML viewer will be generated on session stop"
	}

	// output format selection (priority: review > text > json default)
	if cfg.Review {
		// security audit mode: human summary + JSON
		if generated {
			fmt.Printf("HTML viewer exists: %s\n", htmlPath)
		} else {
			fmt.Printf("HTML viewer will be generated when recording stops.\n")
		}
		fmt.Println()
		fmt.Println("--- Machine Output ---")
		output := sessionHTMLOutput{
			Success:   true,
			Type:      "session_html",
			AgentID:   inst.AgentID,
			Generated: generated,
			HTMLPath:  htmlPath,
			Message:   message,
		}
		jsonOut, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return fmt.Errorf("format HTML JSON: %w", err)
		}
		fmt.Println(string(jsonOut))
		return nil
	}

	if cfg.Text {
		// human-readable text output
		if generated {
			fmt.Printf("HTML viewer exists: %s\n", htmlPath)
		} else {
			fmt.Printf("HTML viewer will be generated when recording stops.\n")
		}
		return nil
	}

	// default: JSON output
	output := sessionHTMLOutput{
		Success:   true,
		Type:      "session_html",
		AgentID:   inst.AgentID,
		Generated: generated,
		HTMLPath:  htmlPath,
		Message:   message,
	}
	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format HTML JSON: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}

// mapRoleToEntryType converts a role string to session.EntryType.
func mapRoleToEntryType(role string) session.EntryType {
	switch role {
	case "user":
		return session.EntryTypeUser
	case "assistant":
		return session.EntryTypeAssistant
	case "system":
		return session.EntryTypeSystem
	case "tool":
		return session.EntryTypeTool
	default:
		return session.EntryTypeSystem
	}
}

// convertRawEntries converts adapter raw entries to session entries.
func convertRawEntries(rawEntries []adapters.RawEntry) []session.Entry {
	entries := make([]session.Entry, 0, len(rawEntries))
	for _, raw := range rawEntries {
		entry := session.Entry{
			Timestamp: raw.Timestamp,
			Content:   raw.Content,
			ToolName:  raw.ToolName,
			ToolInput: raw.ToolInput,
		}
		entry.Type = mapRoleToEntryType(raw.Role)
		entries = append(entries, entry)
	}
	return entries
}

// convertStoredEntries converts stored session entries to session.Entry.
func convertStoredEntries(stored []map[string]any) []session.Entry {
	entries := make([]session.Entry, 0, len(stored))
	for _, entry := range stored {
		e := session.Entry{}
		if t, ok := entry["type"].(string); ok {
			e.Type = session.EntryType(t)
		}
		if c, ok := entry["content"].(string); ok {
			e.Content = c
		}
		if tn, ok := entry["tool_name"].(string); ok {
			e.ToolName = tn
		}
		if ti, ok := entry["tool_input"].(string); ok {
			e.ToolInput = ti
		}
		// extract timestamp
		if ts, ok := entry["timestamp"].(string); ok && ts != "" {
			if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
				e.Timestamp = parsed
			}
		}
		entries = append(entries, e)
	}
	return entries
}

// sessionRecordInput represents a single entry in the batch record input.
type sessionRecordInput struct {
	Type       string `json:"type"`                  // user, assistant, system, tool
	Content    string `json:"content"`               // message content
	Timestamp  string `json:"ts,omitempty"`          // optional timestamp (RFC3339)
	ToolName   string `json:"tool_name,omitempty"`   // for tool entries
	ToolInput  string `json:"tool_input,omitempty"`  // for tool entries
	ToolOutput string `json:"tool_output,omitempty"` // for tool entries
}

// sessionRecordOutput is the JSON output format for session record.
type sessionRecordOutput struct {
	Success    bool   `json:"success"`
	Type       string `json:"type"`
	AgentID    string `json:"agent_id"`
	Recorded   int    `json:"recorded"`
	TotalCount int    `json:"total_count"`
	SessionID  string `json:"session_id,omitempty"`
}

// runAgentSessionRecord records session entries from batch JSON input.
// Usage: ox agent <id> session record [--entries '<json>'] or via stdin
//
// Batch JSON format (array of entries):
//
//	[
//	  {"type": "user", "content": "Hello"},
//	  {"type": "assistant", "content": "Hi there!"},
//	  {"type": "tool", "tool_name": "bash", "tool_input": "ls", "tool_output": "..."}
//	]
//
// This allows agents to record many events in a single call instead of N calls.
func runAgentSessionRecord(inst *agentinstance.Instance, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// check if recording is active
	if !session.IsRecording(projectRoot) {
		return fmt.Errorf("not currently recording\nRun 'ox agent %s session start' to begin recording", inst.AgentID)
	}

	state, err := session.LoadRecordingState(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load recording state: %w", err)
	}

	// parse entries from --entries flag or stdin
	entries, err := parseRecordEntries(args)
	if err != nil {
		return fmt.Errorf("failed to parse entries: %w", err)
	}

	if len(entries) == 0 {
		return fmt.Errorf("no entries provided\nProvide entries via --entries '<json>' or stdin")
	}

	// record entries to session file
	recorded, err := recordEntriesToSession(state, entries)
	if err != nil {
		return fmt.Errorf("failed to record entries: %w", err)
	}

	// update entry count in recording state
	if err := session.UpdateRecordingState(projectRoot, func(s *session.RecordingState) {
		s.EntryCount += recorded
	}); err != nil {
		// non-fatal - entries were recorded
		fmt.Fprintf(os.Stderr, "warning: could not update entry count: %v\n", err)
	}

	// reload state for updated count
	state, _ = session.LoadRecordingState(projectRoot)
	totalCount := 0
	if state != nil {
		totalCount = state.EntryCount
	}

	// output format selection (priority: review > text > json default)
	if cfg.Review {
		// security audit mode: human summary + JSON
		cli.PrintSuccess(fmt.Sprintf("Recorded %d entries", recorded))
		fmt.Printf("  Total entries: %d\n", totalCount)
		fmt.Println()
		fmt.Println("--- Machine Output ---")
		output := sessionRecordOutput{
			Success:    true,
			Type:       "session_record",
			AgentID:    inst.AgentID,
			Recorded:   recorded,
			TotalCount: totalCount,
			SessionID:  session.GetSessionName(state.SessionPath),
		}
		jsonOut, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return fmt.Errorf("format record JSON: %w", err)
		}
		fmt.Println(string(jsonOut))
		return nil
	}

	if cfg.Text {
		// human-readable text output
		cli.PrintSuccess(fmt.Sprintf("Recorded %d entries", recorded))
		fmt.Printf("  Total entries: %d\n", totalCount)
		return nil
	}

	// default: JSON output
	output := sessionRecordOutput{
		Success:    true,
		Type:       "session_record",
		AgentID:    inst.AgentID,
		Recorded:   recorded,
		TotalCount: totalCount,
		SessionID:  session.GetSessionName(state.SessionPath),
	}
	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format record JSON: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}

// sessionPlanOutput is the JSON output for the plan command.
type sessionPlanOutput struct {
	Success      bool     `json:"success"`
	Type         string   `json:"type"` // always "session_plan"
	AgentID      string   `json:"agent_id"`
	PlanPath     string   `json:"plan_path"`
	SessionID    string   `json:"session_id,omitempty"`
	DiagramCount int      `json:"diagram_count"`
	Diagrams     []string `json:"diagrams,omitempty"` // extracted mermaid diagrams
	Message      string   `json:"message,omitempty"`
}

// runAgentSessionPlan saves a plan document for the current session.
// Reads plan content from stdin (pipe-friendly for agents).
// Usage: echo '## Plan...' | ox agent <id> session plan
func runAgentSessionPlan(inst *agentinstance.Instance) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// read plan content from stdin
	planContent, err := readPlanFromStdin()
	if err != nil {
		return fmt.Errorf("failed to read plan from stdin: %w", err)
	}

	if strings.TrimSpace(planContent) == "" {
		return fmt.Errorf("no plan content provided\nUsage: echo '## Plan...' | ox agent %s session plan", inst.AgentID)
	}

	// extract mermaid diagrams
	diagrams := session.ExtractMermaidBlocks(planContent)

	// determine where to save the plan
	var planPath string
	var sessionID string

	// check if there's an active recording - save to that session folder
	if session.IsRecording(projectRoot) {
		state, err := session.LoadRecordingState(projectRoot)
		if err == nil && state.SessionPath != "" {
			planPath = filepath.Join(state.SessionPath, "plan.md")
			sessionID = session.GetSessionName(state.SessionPath)
		}
	}

	// if no active recording, create a new session folder
	if planPath == "" {
		store, _, err := newSessionStore()
		if err != nil {
			return fmt.Errorf("failed to access session store: %w", err)
		}

		planProjectRoot, _ := findProjectRoot()
		planEndpoint := endpoint.GetForProject(planProjectRoot)
		username := getAuthenticatedUsername(planEndpoint)
		if username == "" {
			username = "anonymous"
		}

		sessionName := session.GenerateSessionName(inst.AgentID, username)
		sessionID = sessionName

		// create session directory
		sessionPath := filepath.Join(store.BasePath(), sessionName)
		if err := os.MkdirAll(sessionPath, 0755); err != nil {
			return fmt.Errorf("create session dir: %w", err)
		}

		planPath = filepath.Join(sessionPath, "plan.md")
	}

	// write plan to file
	if err := os.WriteFile(planPath, []byte(planContent), 0644); err != nil {
		return fmt.Errorf("write plan file: %w", err)
	}

	// output format selection
	if cfg.Review {
		cli.PrintSuccess("Plan saved")
		fmt.Printf("  Path: %s\n", planPath)
		fmt.Printf("  Diagrams: %d\n", len(diagrams))
		fmt.Println()
		fmt.Println("--- Machine Output ---")
		output := sessionPlanOutput{
			Success:      true,
			Type:         "session_plan",
			AgentID:      inst.AgentID,
			PlanPath:     planPath,
			SessionID:    sessionID,
			DiagramCount: len(diagrams),
			Diagrams:     diagrams,
		}
		jsonOut, _ := json.MarshalIndent(output, "", "  ")
		fmt.Println(string(jsonOut))
		return nil
	}

	if cfg.Text {
		cli.PrintSuccess("Plan saved")
		fmt.Printf("  Path: %s\n", planPath)
		fmt.Printf("  Diagrams: %d\n", len(diagrams))
		return nil
	}

	// default: JSON output
	output := sessionPlanOutput{
		Success:      true,
		Type:         "session_plan",
		AgentID:      inst.AgentID,
		PlanPath:     planPath,
		SessionID:    sessionID,
		DiagramCount: len(diagrams),
		Diagrams:     diagrams,
	}
	jsonOut, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(jsonOut))
	return nil
}

// readPlanFromStdin reads all content from stdin.
func readPlanFromStdin() (string, error) {
	// check if stdin has data (non-interactive mode)
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		// stdin is a terminal, no piped input
		return "", fmt.Errorf("no input piped to stdin")
	}

	var buf strings.Builder
	scanner := bufio.NewScanner(os.Stdin)
	// increase buffer size for large plans
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		buf.WriteString(scanner.Text())
		buf.WriteString("\n")
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// parseRecordEntries parses entries from --entries flag or stdin.
func parseRecordEntries(args []string) ([]sessionRecordInput, error) {
	var jsonData string

	// check for --entries flag
	for i, arg := range args {
		if arg == "--entries" && i+1 < len(args) {
			jsonData = args[i+1]
			break
		}
		if len(arg) > 10 && arg[:10] == "--entries=" {
			jsonData = arg[10:]
			break
		}
	}

	// if no --entries flag, read from stdin
	if jsonData == "" {
		// check if stdin has data (non-blocking check)
		stat, err := os.Stdin.Stat()
		if err != nil {
			return nil, fmt.Errorf("check stdin: %w", err)
		}

		// only read from stdin if it's a pipe or has data
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			data, err := os.ReadFile("/dev/stdin")
			if err != nil {
				return nil, fmt.Errorf("read stdin: %w", err)
			}
			jsonData = string(data)
		}
	}

	if jsonData == "" {
		return nil, nil
	}

	// trim whitespace
	jsonData = strings.TrimSpace(jsonData)

	// parse JSON - support both array and single object
	var entries []sessionRecordInput

	if strings.HasPrefix(jsonData, "[") {
		// array of entries
		if err := json.Unmarshal([]byte(jsonData), &entries); err != nil {
			return nil, fmt.Errorf("parse JSON array: %w", err)
		}
	} else if strings.HasPrefix(jsonData, "{") {
		// single entry
		var entry sessionRecordInput
		if err := json.Unmarshal([]byte(jsonData), &entry); err != nil {
			return nil, fmt.Errorf("parse JSON object: %w", err)
		}
		entries = []sessionRecordInput{entry}
	} else {
		return nil, fmt.Errorf("invalid JSON: expected array or object")
	}

	return entries, nil
}

// recordEntriesToSession appends entries to the raw session file.
func recordEntriesToSession(state *session.RecordingState, entries []sessionRecordInput) (int, error) {
	if state == nil || state.SessionPath == "" {
		return 0, fmt.Errorf("invalid recording state")
	}

	// open raw session file for append
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	f, err := os.OpenFile(rawPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return 0, fmt.Errorf("open raw session: %w", err)
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	recorded := 0

	for i, entry := range entries {
		// parse timestamp or use current time
		ts := time.Now()
		if entry.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
				ts = parsed
			}
		}

		// build entry data
		data := map[string]any{
			"type":      entry.Type,
			"content":   entry.Content,
			"timestamp": ts,
			"seq":       state.EntryCount + i,
		}

		if entry.ToolName != "" {
			data["tool_name"] = entry.ToolName
		}
		if entry.ToolInput != "" {
			data["tool_input"] = entry.ToolInput
		}
		if entry.ToolOutput != "" {
			data["tool_output"] = entry.ToolOutput
		}

		if err := encoder.Encode(data); err != nil {
			return recorded, fmt.Errorf("write entry %d: %w", i, err)
		}
		recorded++
	}

	// sync to disk
	if err := f.Sync(); err != nil {
		return recorded, fmt.Errorf("sync session: %w", err)
	}

	return recorded, nil
}

// readEntriesFromFile reads session entries from a JSONL file.
func readEntriesFromFile(filePath string) ([]session.Entry, error) {
	// walk up from filePath to find the sessions directory
	dir := filepath.Dir(filePath)
	sessionName := ""
	for dir != "/" && dir != "." {
		parent := filepath.Dir(dir)
		if filepath.Base(parent) == "sessions" {
			// dir is the session folder, parent's parent is the context dir
			sessionName = filepath.Base(dir)
			dir = filepath.Dir(parent) // context dir (parent of sessions/)
			break
		}
		dir = parent
	}

	store, err := session.NewStore(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to create store: %w", err)
	}

	stored, err := store.ReadSession(sessionName)
	if err != nil {
		return nil, fmt.Errorf("failed to read session: %w", err)
	}

	return convertStoredEntries(stored.Entries), nil
}

// sessionTermsNotice is the one-time transparency notice about session recording.
const sessionTermsNotice = "When you record a session, the full conversation between you and " +
	"your AI coworker is saved to your team's ledger and processed by " +
	"SageOx. This helps your team learn from each other's work.\n\n" +
	"Avoid sharing credentials, API keys, or secrets during recorded " +
	"sessions \u2014 the full conversation content is stored."

// getSessionTermsNotice returns the notice text if it hasn't been shown yet,
// and marks it as shown. Returns empty string if already seen.
// Best-effort: config load/save errors return empty (don't block session start).
func getSessionTermsNotice() string {
	userCfg, err := config.LoadUserConfig("")
	if err != nil {
		return ""
	}
	if userCfg.HasSeenSessionTerms() {
		return ""
	}

	userCfg.SetSessionTermsShown(true)
	_ = config.SaveUserConfig(userCfg)

	return sessionTermsNotice
}

// formatFilterModeDescription returns a human-readable description of the filter mode.
func formatFilterModeDescription(mode string) string {
	switch mode {
	case "infra":
		return "infrastructure events only"
	case "all":
		return "all events"
	case "none":
		return "disabled"
	default:
		return mode
	}
}
