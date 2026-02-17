package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

// sessionRecoverOutput is the JSON output format for session recover.
type sessionRecoverOutput struct {
	Success          bool   `json:"success"`
	Type             string `json:"type"`
	AgentID          string `json:"agent_id"`
	SessionName      string `json:"session_name,omitempty"`
	RawPath          string `json:"raw_path,omitempty"`
	EntryCount       int    `json:"entry_count,omitempty"`
	Uploaded         bool   `json:"uploaded"`
	SummaryPrompt    string `json:"summary_prompt,omitempty"`
	Message          string `json:"message"`
	LedgerSessionDir string `json:"ledger_session_dir,omitempty"`
}

// runAgentSessionRecover recovers a stale/crashed session.
//
// When an AI coworker crashes or loses context, it may leave behind a stale
// .recording.json without properly running session stop. This command recovers
// whatever session data is available and uploads it to the ledger.
//
// Recovery strategy:
//  1. If the adapter session file still exists → delegate to normal processAgentSession
//  2. If only cache raw.jsonl exists → upload that directly to ledger
//  3. If no data exists → clear stale state and warn
func runAgentSessionRecover(inst *agentinstance.Instance) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// load stale recording state
	state, err := session.LoadRecordingState(projectRoot)
	if err != nil {
		return fmt.Errorf("no recording state found: %w", err)
	}

	if state == nil {
		return fmt.Errorf("no stale recording to recover\nRun 'ox agent %s session start' to begin a new recording", inst.AgentID)
	}

	slog.Info("recovering stale session", "agent_id", state.AgentID, "session_path", state.SessionPath)

	// strategy 1: adapter session file still exists — use normal processing
	if state.SessionFile != "" {
		if _, err := os.Stat(state.SessionFile); err == nil {
			slog.Info("adapter session file found, using normal stop flow")
			return recoverViaNormalStop(inst, projectRoot, state)
		}
	}

	// strategy 2: cache raw.jsonl exists — upload directly
	if state.SessionPath != "" {
		rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
		if _, err := os.Stat(rawPath); err == nil {
			slog.Info("cache raw.jsonl found, recovering from cache")
			return recoverFromCache(inst, projectRoot, state, rawPath)
		}
	}

	// strategy 3: no recoverable data — clear state and warn
	_ = session.ClearRecordingState(projectRoot)

	output := &sessionRecoverOutput{
		Success: true,
		Type:    "session_recover",
		AgentID: inst.AgentID,
		Message: "stale recording cleared (no recoverable session data found)",
	}
	return outputRecoverJSON(output)
}

// recoverViaNormalStop uses the normal session stop flow when the adapter file exists.
func recoverViaNormalStop(inst *agentinstance.Instance, projectRoot string, _ *session.RecordingState) error {
	// stop recording (clears .recording.json and returns final state)
	state, err := session.StopRecording(projectRoot)
	if err != nil {
		// if stop fails, force-clear and continue
		_ = session.ClearRecordingState(projectRoot)
		return fmt.Errorf("failed to stop recording: %w", err)
	}

	// process session through the normal pipeline
	result, err := processAgentSession(projectRoot, state)
	if err != nil {
		_ = doctor.SetNeedsDoctorAgent(projectRoot)
		return fmt.Errorf("failed to process session: %w", err)
	}

	output := &sessionRecoverOutput{
		Success:          true,
		Type:             "session_recover",
		AgentID:          inst.AgentID,
		SessionName:      result.SessionName,
		RawPath:          result.RawPath,
		EntryCount:       result.EntryCount,
		Uploaded:         result.LedgerSessionDir != "",
		SummaryPrompt:    result.SummaryPrompt,
		Message:          "session recovered via normal processing",
		LedgerSessionDir: result.LedgerSessionDir,
	}
	return outputRecoverJSON(output)
}

// recoverFromCache uploads raw.jsonl from cache when the adapter file is gone.
// raw.jsonl is the source of truth — all other artifacts (events, HTML, summary)
// can be regenerated from it. This ensures no session data is lost even when
// the AI coworker's original session file has been cleaned up.
func recoverFromCache(inst *agentinstance.Instance, projectRoot string, state *session.RecordingState, rawPath string) error {
	// read raw session to get entry count and entries for summary prompt
	stored, err := session.ReadSessionFromPath(rawPath)
	if err != nil {
		_ = session.ClearRecordingState(projectRoot)
		return fmt.Errorf("failed to read cached session: %w", err)
	}

	entryCount := len(stored.Entries)
	entries := convertStoredMapEntries(stored.Entries)

	// resolve ledger path for upload
	ledgerPath, ledgerErr := resolveLedgerPath()

	sessionName := session.GenerateSessionName(state.AgentID, getSessionUsername())
	var ledgerSessionDir string
	var uploaded bool

	if ledgerErr == nil {
		ledgerSessionDir = filepath.Join(ledgerPath, "sessions", sessionName)
		if err := os.MkdirAll(ledgerSessionDir, 0755); err != nil {
			slog.Warn("create ledger session dir failed", "error", err)
		} else {
			// copy raw.jsonl to ledger (the critical artifact)
			data, err := os.ReadFile(rawPath)
			if err == nil {
				dstPath := filepath.Join(ledgerSessionDir, "raw.jsonl")
				if err := os.WriteFile(dstPath, data, 0644); err != nil {
					slog.Warn("copy raw.jsonl to ledger failed", "error", err)
				}
			}

			// copy other artifacts if they exist in cache
			cacheDir := filepath.Dir(rawPath)
			for _, name := range []string{"events.jsonl", "session.html", "summary.md", "session.md", "summary.json"} {
				src := filepath.Join(cacheDir, name)
				if srcData, err := os.ReadFile(src); err == nil {
					dst := filepath.Join(ledgerSessionDir, name)
					_ = os.WriteFile(dst, srcData, 0644)
				}
			}

			// LFS upload + meta.json + commit+push
			fileRefs, uploadErr := uploadSessionLFS(projectRoot, ledgerSessionDir)
			if uploadErr != nil {
				slog.Warn("LFS upload failed during recovery", "error", uploadErr)
				_ = doctor.SetNeedsDoctorAgent(projectRoot)
			} else {
				recoverEndpoint := endpoint.GetForProject(projectRoot)
				username := getAuthenticatedUsername(recoverEndpoint)
				meta := &lfs.SessionMeta{
					Version:     "1.0",
					SessionName: sessionName,
					Username:    username,
					AgentID:     state.AgentID,
					AgentType:   state.AdapterName,
					CreatedAt:   state.StartedAt,
					EntryCount:  entryCount,
					Files:       fileRefs,
				}
				if err := lfs.WriteSessionMeta(ledgerSessionDir, meta); err != nil {
					slog.Warn("write meta.json failed", "error", err)
					_ = doctor.SetNeedsDoctorAgent(projectRoot)
				} else {
					sessionsDir := filepath.Join(ledgerPath, "sessions")
					_ = ensureSessionsGitignore(sessionsDir)
					if err := commitAndPushLedger(ledgerPath, sessionName); err != nil {
						slog.Warn("commit+push failed during recovery", "error", err)
						_ = doctor.SetNeedsDoctorAgent(projectRoot)
					} else {
						uploaded = true
					}
				}
			}
		}
	} else {
		_ = doctor.SetNeedsDoctorAgent(projectRoot)
		slog.Warn("no ledger available for recovery upload", "error", ledgerErr)
	}

	// build summary prompt for the agent to generate summary.json
	var summaryPrompt string
	if len(entries) > 0 {
		summaryPrompt = session.BuildSummaryPrompt(entries, rawPath, ledgerSessionDir)

		// write needs-summary marker
		cacheDir := filepath.Dir(rawPath)
		_ = session.WriteNeedsSummaryMarker(cacheDir, rawPath, ledgerSessionDir)
	}

	// clear stale recording state
	_ = session.ClearRecordingState(projectRoot)

	// all files copied and committed to ledger — prune local cache
	if uploaded {
		cacheDir := filepath.Dir(rawPath)
		if cacheDir != "" && cacheDir != "." {
			if err := os.RemoveAll(cacheDir); err != nil {
				slog.Debug("prune session cache after recovery", "dir", cacheDir, "error", err)
			}
		}
	}

	if !uploaded {
		ledgerSessionDir = ""
	}

	output := &sessionRecoverOutput{
		Success:          true,
		Type:             "session_recover",
		AgentID:          inst.AgentID,
		SessionName:      sessionName,
		RawPath:          rawPath,
		EntryCount:       entryCount,
		Uploaded:         uploaded,
		SummaryPrompt:    summaryPrompt,
		Message:          "session recovered from cache",
		LedgerSessionDir: ledgerSessionDir,
	}
	return outputRecoverJSON(output)
}

func outputRecoverJSON(output *sessionRecoverOutput) error {
	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format recover JSON: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}
