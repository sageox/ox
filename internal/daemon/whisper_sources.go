package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

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
