package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sageox/ox/internal/config"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

const (
	// earlyNudgeDelay is how long to wait before the first nudge for a new agent.
	earlyNudgeDelay = 1 * time.Minute
	// earlyNudgeHeartbeats is the heartbeat count threshold for early nudge.
	earlyNudgeHeartbeats = 2
)

// murmurNudgeContent is the whisper content that nudges agents to self-report.
const murmurNudgeContent = `Run now: ox murmur --topic=wip "<1-2 sentences: what you're building, which files>"`

// MurmurNudgeSource periodically checks if active agents should be nudged
// to self-report what they're working on via ox murmur. Produces a whisper
// entry for each agent that hasn't murmured within the configured interval.
//
// Uses the whisper store as source of truth for nudge/murmur history,
// so state survives daemon restarts without needing in-memory tracking.
type MurmurNudgeSource struct {
	store        *whisperstore.Store
	heartbeat    *HeartbeatHandler
	interval     time.Duration
	projectRoot  string   // re-checked on every tick to pick up config changes
	pausedAgents sync.Map // agentID → struct{}: agents with murmuring paused
}

// NewMurmurNudgeSource creates a source that nudges agents to self-report.
func NewMurmurNudgeSource(store *whisperstore.Store, heartbeat *HeartbeatHandler, interval time.Duration, projectRoot string) *MurmurNudgeSource {
	// enforce minimum 10 minutes
	if interval < 10*time.Minute {
		interval = 10 * time.Minute
	}
	return &MurmurNudgeSource{
		store:       store,
		heartbeat:   heartbeat,
		interval:    interval,
		projectRoot: projectRoot,
	}
}

// PauseAgent pauses murmur nudging for the given agent.
func (s *MurmurNudgeSource) PauseAgent(agentID string) {
	s.pausedAgents.Store(agentID, struct{}{})
}

// ResumeAgent resumes murmur nudging for the given agent.
func (s *MurmurNudgeSource) ResumeAgent(agentID string) {
	s.pausedAgents.Delete(agentID)
}

// IsAgentPaused returns true if the agent has murmuring paused.
func (s *MurmurNudgeSource) IsAgentPaused(agentID string) bool {
	_, ok := s.pausedAgents.Load(agentID)
	return ok
}

// cleanupPausedAgents removes pause state for agents no longer in the active set.
func (s *MurmurNudgeSource) cleanupPausedAgents(activeAgents []ActivityEntry) {
	active := make(map[string]struct{}, len(activeAgents))
	for _, a := range activeAgents {
		active[a.Key] = struct{}{}
	}
	s.pausedAgents.Range(func(key, _ any) bool {
		if _, ok := active[key.(string)]; !ok {
			s.pausedAgents.Delete(key)
		}
		return true
	})
}

func (s *MurmurNudgeSource) Name() string { return "murmur-nudge" }

// Interval returns 1 minute — the source runs frequently but only produces
// whispers when agents actually need nudging (first nudge after ~1min, then
// every configured interval).
func (s *MurmurNudgeSource) Interval() time.Duration { return 1 * time.Minute }

func (s *MurmurNudgeSource) Produce(_ context.Context) []whisperstore.WhisperEntry {
	if s.heartbeat == nil || s.store == nil {
		return nil
	}
	if !config.MurmuringEnabled(s.projectRoot) {
		return nil
	}

	summary := s.heartbeat.GetActivitySummary()
	if len(summary.Agents) == 0 {
		return nil
	}

	// purge paused state for agents no longer active (prevents unbounded growth)
	s.cleanupPausedAgents(summary.Agents)

	now := time.Now()
	var entries []whisperstore.WhisperEntry
	for _, agent := range summary.Agents {
		agentID := agent.Key
		if agentID == "" {
			continue
		}

		if !s.shouldNudge(agentID, agent, now) {
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
			CreatedAt:  now,
			AgentID:    agentID,
		})
	}

	return entries
}

// shouldNudge checks the whisper store to decide if an agent needs nudging.
// Replaces the in-memory MurmurNudgeTracker — state survives daemon restarts.
func (s *MurmurNudgeSource) shouldNudge(agentID string, agent ActivityEntry, now time.Time) bool {
	// paused agents never receive nudges
	if s.IsAgentPaused(agentID) {
		return false
	}

	// don't nudge until agent has been active for earlyNudgeDelay or has
	// enough heartbeats — prevents nudging agents that just started
	firstSeen := time.Time{}
	if len(agent.Timestamps) > 0 {
		firstSeen = agent.Timestamps[0] // oldest entry in ring buffer
	}
	if firstSeen.IsZero() {
		return false
	}

	earlyReady := now.Sub(firstSeen) >= earlyNudgeDelay || agent.Count >= earlyNudgeHeartbeats

	// check when we last nudged this agent (from store)
	lastNudge, err := s.store.LatestWhisperTime("auto-murmur", "murmur-nudge", agentID)
	if err != nil {
		return false // store error — skip this tick
	}

	// check when this agent last murmured (relayed murmurs are stored with source="murmur")
	lastMurmur, err := s.store.LatestWhisperTime("murmur", "", agentID)
	if err != nil {
		return false
	}

	hasMurmured := !lastMurmur.IsZero()

	// don't re-nudge too soon after last nudge
	if !lastNudge.IsZero() {
		cooldown := s.interval
		if !hasMurmured {
			cooldown = earlyNudgeDelay
		}
		if now.Sub(lastNudge) < cooldown {
			return false
		}
	}

	if !hasMurmured {
		// never murmured — use early nudge logic
		return earlyReady
	}

	// has murmured before — nudge after interval since last murmur
	return now.Sub(lastMurmur) >= s.interval
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
