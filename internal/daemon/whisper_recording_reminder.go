package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/constants"
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

func (s *RecordingReminderSource) Name() string { return whisperstore.SourceRecordingReminder }

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

	// Resolve the destination the session is shared with, once per tick — a
	// project-level fact shared by every recording agent in this root. We name
	// both the team and the repo ("<team>'s <repo> ledger") so the privacy
	// scope is unmissable. Each part degrades independently when unset.
	teamName, repoName := "", ""
	if cfg, err := config.LoadProjectConfig(s.projectRoot); err == nil && cfg != nil {
		teamName = cfg.TeamName
		repoName = cfg.Project
	}
	if repoName == "" {
		repoName = repoNameFromPath(s.projectRoot)
	}
	dest := recordingDestination(teamName, repoName)

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

		remind, isFirst := s.shouldRemind(agentID, agent, now)
		if !remind {
			continue
		}

		content := formatReminderContent(state, dest, isFirst)

		id, err := uuid.NewV7()
		if err != nil {
			id = uuid.New()
		}

		entries = append(entries, whisperstore.WhisperEntry{
			ID:         id.String(),
			Scope:      "ledger",
			Type:       whisperstore.WhisperTimeBased,
			Source:     whisperstore.SourceRecordingReminder,
			Topic:      "recording-status",
			Content:    content,
			Importance: whisperstore.ImportanceNormal,
			CreatedAt:  now,
			// AgentID is the intended recipient. Unlike murmurs and
			// announcements (which fan out to every listener), this whisper's
			// Content embeds this agent's own turn count and duration — it
			// is meaningless and misleading if rendered by any other agent.
			// Store.GetWhispers uses Source == whisperstore.SourceRecordingReminder
			// together with this AgentID to route delivery to the recipient
			// only (see #538). Keep Source tied to the constant above; the
			// store's SQL filter is keyed on it.
			AgentID: agentID,
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
// Returns remind=true on first call (no prior reminder) or after the configured
// interval. isFirst reports whether this is the startup reminder (no prior
// reminder in the store), which selects the richer first-fire copy.
func (s *RecordingReminderSource) shouldRemind(agentID string, agent ActivityEntry, now time.Time) (remind bool, isFirst bool) {
	// Claude Code's SessionStart hook doesn't deliver whispers — wait
	// until the first prompt has fired. Other agents whisper immediately.
	agentType := s.heartbeat.GetAgentType(agentID)
	if agentType == "claude-code" && agent.Count < claudeCodeMinHeartbeats {
		return false, false
	}

	lastReminder, err := s.store.LatestWhisperTime(whisperstore.SourceRecordingReminder, "recording-status", agentID)
	if err != nil {
		return false, false // store error — skip this tick
	}

	if lastReminder.IsZero() {
		return true, true // never reminded — produce immediately (startup reminder)
	}

	return now.Sub(lastReminder) >= s.interval, false
}

// recordingDestination names where the session is shared, as "<team>'s <repo>
// ledger". The user's #1 ask was clarity on *which* team — naming the team and
// the repo together makes the privacy scope unmissable. Each part degrades
// independently so we never render a dangling possessive or a bare "your team":
//
//	team + repo → "Acme Eng's web-app ledger"
//	team only   → "Acme Eng's ledger"
//	repo only   → "the web-app ledger"
//	neither     → "your team's ledger"
func recordingDestination(teamName, repoName string) string {
	switch {
	case teamName != "" && repoName != "":
		return fmt.Sprintf("%s's %s ledger", teamName, repoName)
	case teamName != "":
		return fmt.Sprintf("%s's ledger", teamName)
	case repoName != "":
		return fmt.Sprintf("the %s ledger", repoName)
	default:
		return "your team's ledger"
	}
}

// repoNameFromPath derives a human repo name from the project root when the
// config has no project slug. Returns "" for degenerate roots ("/", ".", "").
func repoNameFromPath(projectRoot string) string {
	base := filepath.Base(projectRoot)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return ""
	}
	return base
}

// formatReminderContent builds the whisper message from recording state.
// Instructs the agent to surface the recording status to the user in one line.
//
// dest is the pre-resolved destination phrase from recordingDestination. The
// first fire is richer — it establishes consent and offers a stop hint plus a
// learn-more link — while periodic fires stay terse to avoid alarm-fatigue over
// a long session. Separators are middots, not periods: periods read as multiple
// alarming sentences on every hourly repeat.
func formatReminderContent(state *session.RecordingState, dest string, isFirst bool) string {
	const tell = "Tell the user in one short line: "

	if isFirst {
		// First fire: counts are ~0 at t=0 (near-zero info), so spend the line
		// on who-sees-this + how-to-stop + where-to-learn instead.
		return fmt.Sprintf(
			tell+"\"Recording to %s · your teammates can read this session · stop: /ox-session-stop · %s\"",
			dest, constants.RecordingDocsURL)
	}

	// Periodic fire: reassert destination + elapsed; no link, no stop hint
	// (repeating them every hour trains the user to filter the whole line out).
	return fmt.Sprintf(tell+"\"Recording to %s · %d turns, %s\"", dest, state.EntryCount, formatDuration(state.Duration()))
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
