package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// RecordingReminderSource periodically checks active recording agents and
// produces whisper entries reminding them that their session is being recorded.
// Includes turn count and duration for visibility into recording health.
//
// On the first tick after recording starts (no prior reminder in store),
// it produces an immediate reminder — this serves as the startup notification
// that the agent sees on its first UserPromptSubmit.
type RecordingReminderSource struct {
	store       *whisperstore.Store
	heartbeat   *HeartbeatHandler
	interval    time.Duration // how often to produce reminders per agent (default 1h)
	tick        time.Duration // how often the scheduler calls Produce (default 1m)
	projectRoot string
}

// NewRecordingReminderSource creates a source that reminds agents about active recording.
func NewRecordingReminderSource(store *whisperstore.Store, heartbeat *HeartbeatHandler, interval time.Duration, projectRoot string) *RecordingReminderSource {
	return &RecordingReminderSource{
		store:       store,
		heartbeat:   heartbeat,
		interval:    interval,
		tick:        1 * time.Minute,
		projectRoot: projectRoot,
	}
}

// SetTick overrides the scheduler tick interval. Useful for testing
// where the default 1-minute tick is too slow.
func (s *RecordingReminderSource) SetTick(d time.Duration) {
	s.tick = d
}

func (s *RecordingReminderSource) Name() string { return "recording-reminder" }

// Interval returns the tick frequency. Defaults to 1 minute — frequent enough
// to catch new recordings quickly, while only producing entries when the
// configured reminder interval has elapsed per agent. Override with SetTick
// for testing.
func (s *RecordingReminderSource) Interval() time.Duration { return s.tick }

func (s *RecordingReminderSource) Produce(_ context.Context) []whisperstore.WhisperEntry {
	if s.heartbeat == nil || s.store == nil {
		return nil
	}
	if !config.RecordingReminderEnabled(s.projectRoot) {
		return nil
	}

	summary := s.heartbeat.GetActivitySummary()
	if len(summary.Agents) == 0 {
		return nil
	}

	now := time.Now()
	var entries []whisperstore.WhisperEntry
	for _, agent := range summary.Agents {
		agentID := agent.Key
		if agentID == "" {
			continue
		}

		state, err := session.LoadRecordingStateForAgent(s.projectRoot, agentID)
		if err != nil || state == nil {
			continue // not recording
		}
		if state.StoppedAt != nil {
			continue // recording already stopped
		}

		if !s.shouldRemind(agentID, agent, now) {
			continue
		}

		content := formatReminderContent(state)

		id, err := uuid.NewV7()
		if err != nil {
			id = uuid.New()
		}

		entries = append(entries, whisperstore.WhisperEntry{
			ID:         id.String(),
			Scope:      "ledger",
			Type:       whisperstore.WhisperTimeBased,
			Source:     "recording-reminder",
			Topic:      "recording-status",
			Content:    content,
			Importance: whisperstore.ImportanceNormal,
			CreatedAt:  now,
			AgentID:    agentID,
		})
	}

	return entries
}

// claudeCodeMinHeartbeats is the heartbeat count threshold before producing
// the first recording reminder for Claude Code. The SessionStart hook does
// not deliver whispers to the model, so we wait until at least one heartbeat
// after SessionStart (count >= 2) to ensure the reminder lands on a
// UserPromptSubmit, which does inject stdout into model context.
//
// TODO: remove this workaround when Claude Code fixes SessionStart stdout
// injection. Gate on agent version when a fix ships.
const claudeCodeMinHeartbeats = 2

// shouldRemind checks the whisper store to decide if an agent needs a reminder.
// Returns true on first call (no prior reminder) or after the configured interval.
func (s *RecordingReminderSource) shouldRemind(agentID string, agent ActivityEntry, now time.Time) bool {
	// Claude Code's SessionStart hook doesn't deliver whispers — wait
	// until the first prompt has fired. Other agents whisper immediately.
	agentType := s.heartbeat.GetAgentType(agentID)
	if agentType == "claude-code" && agent.Count < claudeCodeMinHeartbeats {
		return false
	}

	lastReminder, err := s.store.LatestWhisperTime("recording-reminder", "recording-status", agentID)
	if err != nil {
		return false // store error — skip this tick
	}

	if lastReminder.IsZero() {
		return true // never reminded — produce immediately (startup reminder)
	}

	return now.Sub(lastReminder) >= s.interval
}

// formatReminderContent builds the whisper message from recording state.
// Instructs the agent to surface the recording status to the user in one line.
func formatReminderContent(state *session.RecordingState) string {
	duration := state.Duration()
	turns := state.EntryCount

	return fmt.Sprintf(
		"Tell the user in one short line: \"SageOx recording active: %d turns, %s. Session will be shared with your team.\"",
		turns, formatDuration(duration))
}

// formatDuration renders a duration as human-readable "Xh Ym" or "Xm".
func formatDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60

	if h > 0 && m > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%dh", h)
	}
	if m > 0 {
		return fmt.Sprintf("%dm", m)
	}
	return "<1m"
}
