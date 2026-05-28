package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
)

// sessionPauseOutput is the JSON output format for session pause.
type sessionPauseOutput struct {
	Success      bool   `json:"success"`
	Type         string `json:"type"`
	AgentID      string `json:"agent_id"`
	SessionName  string `json:"session_name,omitempty"`
	Status       string `json:"status"`  // "paused" | "already_paused" | "not_recording"
	Message      string `json:"message"`
	Guidance     string `json:"guidance,omitempty"`
	Seq          int    `json:"seq,omitempty"`           // entry seq at pause point
	PauseCount   int    `json:"pause_count,omitempty"`   // monotonic counter
	SuspendedAt  string `json:"suspended_at,omitempty"`  // RFC3339
}

// runAgentSessionPause suspends an active recording. The local cache continues
// to receive entries; upload at stop time will exclude the suspended range.
//
// Idempotent: pausing an already-suspended session is a no-op with a friendly
// message and exit 0.
//
// See ADR-019 (session lifecycle) and ADR-020 (pause/resume).
func runAgentSessionPause(inst *agentinstance.Instance, _ []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	state, err := session.LoadRecordingStateForAgent(projectRoot, inst.AgentID)
	if err != nil {
		return fmt.Errorf("failed to load recording state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("no active session to pause\nRun 'ox agent %s session start' to begin recording first", inst.AgentID)
	}

	// idempotent: already paused
	if state.SuspendedAt != nil {
		return emitPauseOutput(os.Stdout, &sessionPauseOutput{
			Success:     true,
			Type:        "session_pause",
			AgentID:     inst.AgentID,
			SessionName: session.GetSessionName(state.SessionPath),
			Status:      "already_paused",
			Message:     "session is already paused",
			SuspendedAt: state.SuspendedAt.UTC().Format(time.RFC3339),
			Seq:         state.EntryCount,
			PauseCount:  state.PauseCount,
			Guidance:    "No change. Resume with `ox session resume` (or /ox-session-resume).",
		})
	}

	now := time.Now().UTC()

	// Capture seq + pause count INSIDE the update closure so the persisted
	// LifecycleEvent.Seq matches the EntryCount the .recording.json is about
	// to be saved with. Reading them beforehand opens a TOCTOU window where
	// the tail-watcher appends entries between our read and Update's
	// internal Load-modify-Save — and the persisted seq would silently
	// trail the real entry stream, leaking paused entries into the upload
	// at stop time.
	var (
		seq          int
		pauseCount   int
		sessionName  string
	)
	if err := session.UpdateRecordingStateForAgent(projectRoot, inst.AgentID, func(s *session.RecordingState) {
		seq = s.EntryCount
		s.SuspendedAt = &now
		s.PauseCount++
		pauseCount = s.PauseCount
		sessionName = session.GetSessionName(s.SessionPath)
		s.Lifecycle = append(s.Lifecycle, session.LifecycleEvent{
			Action: session.LifecycleActionPause,
			At:     now,
			Seq:    seq,
		})
	}); err != nil {
		return fmt.Errorf("failed to mark recording suspended: %w", err)
	}

	// write per-agent marker so /clear inheritance and cross-process detection work.
	if err := session.MarkExplicitPause(projectRoot, inst.AgentID, seq); err != nil {
		return fmt.Errorf("recording suspended but marker write failed: %w", err)
	}

	return emitPauseOutput(os.Stdout, &sessionPauseOutput{
		Success:     true,
		Type:        "session_pause",
		AgentID:     inst.AgentID,
		SessionName: sessionName,
		Status:      "paused",
		Message:     fmt.Sprintf("session suspended at entry %d", seq),
		Seq:         seq,
		PauseCount:  pauseCount,
		SuspendedAt: now.Format(time.RFC3339),
		Guidance:    "Recording continues to local cache; the suspended range will be excluded from upload. Resume with `ox session resume` (or /ox-session-resume).",
	})
}

func emitPauseOutput(w io.Writer, output *sessionPauseOutput) error {
	if cfg.Text || cfg.Review {
		fmt.Fprintf(w, "[ox] %s — %s\n", output.Status, output.Message)
		if cfg.Review {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "--- Machine Output ---")
		} else {
			return nil
		}
	}
	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format pause JSON: %w", err)
	}
	fmt.Fprintln(w, string(jsonOut))
	return nil
}
