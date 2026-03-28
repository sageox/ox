package daemon

import (
	"log/slog"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// MurmurRelay detects new murmur files in ledger and team context repos
// after git pull, converts them to whisper entries, and feeds them to the
// WhisperRegistry. Uses the relayed_murmurs table for dedup across restarts.
type MurmurRelay struct {
	registry      *WhisperRegistry
	localAgentIDs map[string]bool // agent IDs on this machine (skip self-authored murmurs)
	nudgeTracker  *MurmurNudgeTracker
	projectRoot   string // re-checked on every relay to pick up config changes
	lastRelayAt   time.Time
	logger        *slog.Logger
}

// NewMurmurRelay creates a relay that scans murmur files and converts them
// to whisper entries in the registry.
func NewMurmurRelay(registry *WhisperRegistry, projectRoot string, logger *slog.Logger) *MurmurRelay {
	if logger == nil {
		logger = slog.Default()
	}
	return &MurmurRelay{
		registry:      registry,
		localAgentIDs: make(map[string]bool),
		projectRoot:   projectRoot,
		logger:        logger,
	}
}

// SetNudgeTracker sets the nudge tracker so the relay can record when
// agents publish murmurs, suppressing future nudges.
func (r *MurmurRelay) SetNudgeTracker(tracker *MurmurNudgeTracker) {
	r.nudgeTracker = tracker
}

// SetLocalAgentIDs sets the agent IDs running on this machine.
// Murmurs authored by these agents are filtered out to avoid echo.
func (r *MurmurRelay) SetLocalAgentIDs(ids []string) {
	r.localAgentIDs = make(map[string]bool, len(ids))
	for _, id := range ids {
		r.localAgentIDs[id] = true
	}
}

// RelayFromPath scans a directory for murmur files within the default window
// and relays any new ones into the whisper registry.
// Called after git pull on a ledger or team context repo.
// scope must be "ledger" or "team".
// Returns the number of murmurs successfully relayed.
func (r *MurmurRelay) RelayFromPath(baseDir, scope string) int {
	if !config.MurmuringEnabled(r.projectRoot) {
		return 0
	}

	// incremental scan: only look back to lastRelayAt instead of the full 12h window
	windowHours := ledger.DefaultMurmurWindowHours
	if !r.lastRelayAt.IsZero() {
		hoursSince := int(time.Since(r.lastRelayAt).Hours()) + 1 // +1 to cover partial hours
		if hoursSince < 1 {
			hoursSince = 1
		}
		if hoursSince < windowHours {
			windowHours = hoursSince
		}
	}

	murmurs, err := ledger.ReadMurmursInWindow(baseDir, windowHours)
	if err != nil {
		r.logger.Warn("failed to read murmurs", "dir", baseDir, "scope", scope, "err", err)
		return 0
	}

	var relayed int
	for _, m := range murmurs {
		// skip self-authored murmurs
		if m.AgentID != "" && r.localAgentIDs[m.AgentID] {
			continue
		}

		// dedup: skip if already relayed (persisted in SQLite)
		already, err := r.registry.IsRelayed(m.ID, scope)
		if err != nil {
			r.logger.Warn("failed to check relay status", "murmur_id", m.ID, "err", err)
			continue
		}
		if already {
			continue
		}

		entry := whisperstore.WhisperEntry{
			ID:            m.ID,
			Scope:         scope,
			Type:          whisperstore.WhisperTrigger,
			Source:        "murmur",
			Topic:         m.Topic,
			Content:       m.Content,
			Importance:    whisperstore.Importance(m.Importance),
			CreatedAt:     m.Timestamp,
			AgentID:       m.AgentID,
			PrincipalID:   m.PrincipalID,
			PrincipalType: m.PrincipalType,
			Metadata:      m.Metadata,
		}

		if err := r.registry.Add(scope, entry); err != nil {
			r.logger.Warn("failed to add murmur as whisper", "murmur_id", m.ID, "err", err)
			continue
		}

		if err := r.registry.MarkRelayed(m.ID, scope); err != nil {
			r.logger.Warn("failed to mark murmur relayed", "murmur_id", m.ID, "err", err)
		}

		// record murmur for nudge suppression
		if r.nudgeTracker != nil && m.AgentID != "" {
			r.nudgeTracker.RecordMurmur(m.AgentID)
		}

		relayed++
	}

	// update scan cursor even if 0 murmurs found — the window was clean
	r.lastRelayAt = time.Now()

	if relayed > 0 {
		r.logger.Debug("murmurs relayed", "scope", scope, "count", relayed, "dir", baseDir)
	}
	return relayed
}
