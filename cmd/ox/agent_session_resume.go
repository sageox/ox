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

// sessionResumeOutput is the JSON output format for session resume.
type sessionResumeOutput struct {
	Success         bool   `json:"success"`
	Type            string `json:"type"`
	AgentID         string `json:"agent_id"`
	SessionName     string `json:"session_name,omitempty"`
	Status          string `json:"status"` // "resumed" | "not_suspended"
	Message         string `json:"message"`
	Guidance        string `json:"guidance,omitempty"`
	ResumedSeq      int    `json:"resumed_seq,omitempty"`      // entry seq at resume point
	ExcludedEntries int    `json:"excluded_entries,omitempty"` // count of entries in the closed range
	PausedDuration  string `json:"paused_duration,omitempty"`  // human-readable
}

// runAgentSessionResume reverses a prior pause. Appends a resume LifecycleEvent
// with the current EntryCount as seq, clears SuspendedAt, and removes the
// pause marker.
//
// Idempotent: resuming a non-suspended session is a no-op with a friendly
// message and exit 0.
func runAgentSessionResume(inst *agentinstance.Instance, _ []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	state, err := session.LoadRecordingStateForAgent(projectRoot, inst.AgentID)
	if err != nil {
		return fmt.Errorf("failed to load recording state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("no active session to resume\nRun 'ox agent %s session start' to begin recording first", inst.AgentID)
	}

	// idempotent: not suspended
	if state.SuspendedAt == nil {
		return emitResumeOutput(os.Stdout, &sessionResumeOutput{
			Success:     true,
			Type:        "session_resume",
			AgentID:     inst.AgentID,
			SessionName: session.GetSessionName(state.SessionPath),
			Status:      "not_suspended",
			Message:     "session is not suspended; nothing to resume",
			Guidance:    "Already recording. Pause with `ox session pause` to suspend.",
		})
	}

	pausedAt := *state.SuspendedAt
	now := time.Now().UTC()

	// Capture resumeSeq + excluded count INSIDE the update closure so the
	// persisted LifecycleEvent.Seq AND the excluded-range calculation both
	// see the same EntryCount that .recording.json is about to be saved
	// with. Reading either beforehand opens a TOCTOU window where entries
	// appended while still SuspendedAt != nil get treated as resumed and
	// silently uploaded — the exact failure ADR-020's "temporal redaction
	// scope" claims to prevent.
	var (
		resumeSeq   int
		excluded    int
		sessionName string
	)
	if err := session.UpdateRecordingStateForAgent(projectRoot, inst.AgentID, func(s *session.RecordingState) {
		resumeSeq = s.EntryCount
		excluded = computeExcludedSinceLastPause(s.Lifecycle, resumeSeq)
		sessionName = session.GetSessionName(s.SessionPath)
		s.SuspendedAt = nil
		s.Lifecycle = append(s.Lifecycle, session.LifecycleEvent{
			Action: session.LifecycleActionResume,
			At:     now,
			Seq:    resumeSeq,
		})
	}); err != nil {
		return fmt.Errorf("failed to clear suspended state: %w", err)
	}

	// best-effort marker removal; idempotent if marker missing
	session.ClearExplicitPause(projectRoot, inst.AgentID)

	pausedDur := now.Sub(pausedAt)
	return emitResumeOutput(os.Stdout, &sessionResumeOutput{
		Success:         true,
		Type:            "session_resume",
		AgentID:         inst.AgentID,
		SessionName:     sessionName,
		Status:          "resumed",
		Message:         fmt.Sprintf("session resumed at entry %d", resumeSeq),
		ResumedSeq:      resumeSeq,
		ExcludedEntries: excluded,
		PausedDuration:  formatPausedDuration(pausedDur),
		Guidance:        "Recording resumed. Paused range will be excluded from upload at stop time.",
	})
}

// computeExcludedSinceLastPause walks the lifecycle in reverse to find the
// most recent open pause and returns the count of entries that would be
// excluded between that pause seq and the given resume seq.
//
// "Open" pause = not yet matched by a resume / stop / clear-finalize / expired
// event later in the timeline.
func computeExcludedSinceLastPause(lifecycle []session.LifecycleEvent, resumeSeq int) int {
	for i := len(lifecycle) - 1; i >= 0; i-- {
		switch lifecycle[i].Action {
		case session.LifecycleActionPause:
			diff := resumeSeq - lifecycle[i].Seq
			if diff < 0 {
				return 0
			}
			return diff
		case session.LifecycleActionResume,
			session.LifecycleActionStop,
			session.LifecycleActionClearFinalize,
			session.LifecycleActionExpired:
			// any of these closes a pause; further-back pauses are not "open"
			return 0
		}
	}
	return 0
}

// formatPausedDuration formats a paused-window duration for the resume
// notice. Mirrors formatClearDuration but applied to pause windows.
func formatPausedDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

func emitResumeOutput(w io.Writer, output *sessionResumeOutput) error {
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
		return fmt.Errorf("format resume JSON: %w", err)
	}
	fmt.Fprintln(w, string(jsonOut))
	return nil
}
