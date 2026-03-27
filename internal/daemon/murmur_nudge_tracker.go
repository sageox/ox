package daemon

import (
	"sync"
	"time"
)

// earlyNudgeDelay is how long to wait before the first nudge for a new agent.
const earlyNudgeDelay = 1 * time.Minute

// earlyNudgeHeartbeats is an alternative trigger: nudge after this many
// heartbeats if the time threshold hasn't been reached yet.
const earlyNudgeHeartbeats = 2

// MurmurNudgeTracker tracks when agents last murmured and when they were
// last nudged, so the daemon can periodically prompt agents to self-report
// what they're working on.
type MurmurNudgeTracker struct {
	mu             sync.Mutex
	lastMurmur     map[string]time.Time // agent_id -> last murmur timestamp
	lastNudge      map[string]time.Time // agent_id -> last nudge timestamp
	firstSeen      map[string]time.Time // agent_id -> when agent first appeared
	heartbeatCount map[string]int       // agent_id -> heartbeats since last murmur
}

// NewMurmurNudgeTracker creates a new tracker.
func NewMurmurNudgeTracker() *MurmurNudgeTracker {
	return &MurmurNudgeTracker{
		lastMurmur:     make(map[string]time.Time),
		lastNudge:      make(map[string]time.Time),
		firstSeen:      make(map[string]time.Time),
		heartbeatCount: make(map[string]int),
	}
}

// RecordMurmur records that the agent just published a murmur.
// Resets the heartbeat counter so the next nudge cycle starts fresh.
func (t *MurmurNudgeTracker) RecordMurmur(agentID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastMurmur[agentID] = time.Now()
	t.heartbeatCount[agentID] = 0
}

// RecordHeartbeat increments the heartbeat counter for an agent.
// Also records firstSeen if this is the first heartbeat.
func (t *MurmurNudgeTracker) RecordHeartbeat(agentID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.firstSeen[agentID]; !ok {
		t.firstSeen[agentID] = time.Now()
	}
	t.heartbeatCount[agentID]++
}

// ShouldNudge returns true if the agent should be nudged to murmur.
//
// First murmur (never murmured before): nudge after earlyNudgeDelay since
// firstSeen OR earlyNudgeHeartbeats heartbeats, whichever comes first.
//
// Subsequent: nudge after interval since last murmur.
//
// Never re-nudge until interval elapses after last nudge (prevents spam
// if agent ignores the nudge).
func (t *MurmurNudgeTracker) ShouldNudge(agentID string, interval time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()

	// don't re-nudge too soon after the last nudge
	if lastNudge, ok := t.lastNudge[agentID]; ok {
		// for the first nudge (no prior murmur), use earlyNudgeDelay as the
		// re-nudge cooldown; for subsequent nudges, use the full interval
		cooldown := interval
		if _, hasMurmured := t.lastMurmur[agentID]; !hasMurmured {
			cooldown = earlyNudgeDelay
		}
		if now.Sub(lastNudge) < cooldown {
			return false
		}
	}

	lastMurmur, hasMurmured := t.lastMurmur[agentID]

	if !hasMurmured {
		// never murmured — use early nudge logic
		firstSeen, exists := t.firstSeen[agentID]
		if !exists {
			return false // never seen this agent
		}
		hbCount := t.heartbeatCount[agentID]
		return now.Sub(firstSeen) >= earlyNudgeDelay || hbCount >= earlyNudgeHeartbeats
	}

	// has murmured before — nudge after interval
	return now.Sub(lastMurmur) >= interval
}

// RecordNudge records that a nudge was sent to the agent.
func (t *MurmurNudgeTracker) RecordNudge(agentID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastNudge[agentID] = time.Now()
}

// Cleanup removes tracking state for agents with no recent activity.
// Uses the most recent timestamp across firstSeen, lastMurmur, and lastNudge
// so that long-lived active agents are not prematurely evicted.
func (t *MurmurNudgeTracker) Cleanup(maxAge time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for id, seen := range t.firstSeen {
		latest := seen
		if lm, ok := t.lastMurmur[id]; ok && lm.After(latest) {
			latest = lm
		}
		if ln, ok := t.lastNudge[id]; ok && ln.After(latest) {
			latest = ln
		}
		if latest.Before(cutoff) {
			delete(t.lastMurmur, id)
			delete(t.lastNudge, id)
			delete(t.firstSeen, id)
			delete(t.heartbeatCount, id)
		}
	}
}
