package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/sageox/ox/internal/config"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// murmurNudgeContent is the whisper content that nudges agents to self-report.
const murmurNudgeContent = `ACTION REQUIRED: Tell your teammates what you're working on right now. Run this command immediately. Be extremely concise — other agents pay a context token cost to read your murmur. One or two sentences max, 500 bytes limit. Run: ox murmur --topic=wip "describe: what you're building/fixing, which code areas you're modifying, and any architectural changes"`

// MurmurNudgeSource periodically checks if active agents should be nudged
// to self-report what they're working on via ox murmur. Produces a whisper
// entry for each agent that hasn't murmured within the configured interval.
type MurmurNudgeSource struct {
	tracker     *MurmurNudgeTracker
	heartbeat   *HeartbeatHandler
	interval    time.Duration
	projectRoot string // re-checked on every tick to pick up config changes
}

// NewMurmurNudgeSource creates a source that nudges agents to self-report.
func NewMurmurNudgeSource(tracker *MurmurNudgeTracker, heartbeat *HeartbeatHandler, interval time.Duration, projectRoot string) *MurmurNudgeSource {
	// enforce minimum 10 minutes
	if interval < 10*time.Minute {
		interval = 10 * time.Minute
	}
	return &MurmurNudgeSource{
		tracker:     tracker,
		heartbeat:   heartbeat,
		interval:    interval,
		projectRoot: projectRoot,
	}
}

func (s *MurmurNudgeSource) Name() string { return "murmur-nudge" }

// Interval returns 1 minute — the source runs frequently but only produces
// whispers when agents actually need nudging (first nudge after ~1min, then
// every configured interval).
func (s *MurmurNudgeSource) Interval() time.Duration { return 1 * time.Minute }

func (s *MurmurNudgeSource) Produce(_ context.Context) []whisperstore.WhisperEntry {
	if s.heartbeat == nil || s.tracker == nil {
		return nil
	}
	if !config.MurmuringEnabled(s.projectRoot) {
		return nil
	}

	summary := s.heartbeat.GetActivitySummary()
	if len(summary.Agents) == 0 {
		return nil
	}

	var entries []whisperstore.WhisperEntry
	for _, agent := range summary.Agents {
		agentID := agent.Key
		if agentID == "" {
			continue
		}

		if !s.tracker.ShouldNudge(agentID, s.interval) {
			continue
		}

		id, err := uuid.NewV7()
		if err != nil {
			id = uuid.New()
		}

		entries = append(entries, whisperstore.WhisperEntry{
			ID:         id.String(),
			Scope:      "ledger",
			Type:       whisperstore.WhisperTimeBased,
			Source:     "auto-murmur",
			Topic:      "murmur-nudge",
			Content:    murmurNudgeContent,
			Importance: whisperstore.ImportanceNormal,
			CreatedAt:  time.Now(),
			AgentID:    agentID,
		})

		s.tracker.RecordNudge(agentID)
	}

	return entries
}

// ActivitySummarySource produces periodic activity whispers.
// Reports active coworker count and last sync time.
type ActivitySummarySource struct {
	heartbeat *HeartbeatHandler
	scheduler *SyncScheduler
}

// NewActivitySummarySource creates a source that periodically summarizes
// coworker activity and sync status as ambient whispers.
func NewActivitySummarySource(heartbeat *HeartbeatHandler, scheduler *SyncScheduler) *ActivitySummarySource {
	return &ActivitySummarySource{
		heartbeat: heartbeat,
		scheduler: scheduler,
	}
}

func (s *ActivitySummarySource) Name() string { return "activity-summary" }

func (s *ActivitySummarySource) Interval() time.Duration { return 30 * time.Minute }

func (s *ActivitySummarySource) Produce(_ context.Context) []whisperstore.WhisperEntry {
	var activeCount int
	if s.heartbeat != nil {
		summary := s.heartbeat.GetActivitySummary()
		activeCount = len(summary.Agents)
	}

	var lastSyncStr string
	if s.scheduler != nil {
		lastSync := s.scheduler.LastSync()
		if !lastSync.IsZero() {
			ago := time.Since(lastSync).Round(time.Minute)
			lastSyncStr = fmt.Sprintf(", last sync %s ago", ago)
		}
	}

	if activeCount == 0 && lastSyncStr == "" {
		return nil
	}

	content := fmt.Sprintf("%d coworker(s) active on this repo%s", activeCount, lastSyncStr)

	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}

	return []whisperstore.WhisperEntry{{
		ID:         id.String(),
		Scope:      "ledger",
		Type:       whisperstore.WhisperTimeBased,
		Source:     "activity-summary",
		Topic:      "activity",
		Content:    content,
		Importance: whisperstore.ImportanceAmbient,
		CreatedAt:  time.Now(),
	}}
}
