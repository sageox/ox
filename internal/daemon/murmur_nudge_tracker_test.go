package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMurmurNudgeTracker_NewAgentEarlyNudge(t *testing.T) {
	t.Parallel()
	tracker := NewMurmurNudgeTracker()
	interval := 15 * time.Minute

	// unknown agent — should not nudge
	assert.False(t, tracker.ShouldNudge("agent-1", interval))

	// record heartbeat — first seen, but not enough time/heartbeats
	tracker.RecordHeartbeat("agent-1")
	assert.False(t, tracker.ShouldNudge("agent-1", interval))

	// second heartbeat triggers early nudge
	tracker.RecordHeartbeat("agent-1")
	assert.True(t, tracker.ShouldNudge("agent-1", interval))
}

func TestMurmurNudgeTracker_TimeBasedEarlyNudge(t *testing.T) {
	t.Parallel()
	tracker := NewMurmurNudgeTracker()
	interval := 15 * time.Minute

	// simulate agent seen 2 minutes ago with only 1 heartbeat
	tracker.mu.Lock()
	tracker.firstSeen["agent-1"] = time.Now().Add(-2 * time.Minute)
	tracker.heartbeatCount["agent-1"] = 1
	tracker.mu.Unlock()

	// 2 min > earlyNudgeDelay (1 min), should nudge even with 1 heartbeat
	assert.True(t, tracker.ShouldNudge("agent-1", interval))
}

func TestMurmurNudgeTracker_RegularInterval(t *testing.T) {
	t.Parallel()
	tracker := NewMurmurNudgeTracker()
	interval := 15 * time.Minute

	// agent has murmured recently — should not nudge
	tracker.RecordHeartbeat("agent-1")
	tracker.RecordMurmur("agent-1")
	assert.False(t, tracker.ShouldNudge("agent-1", interval))

	// simulate murmur 16 minutes ago
	tracker.mu.Lock()
	tracker.lastMurmur["agent-1"] = time.Now().Add(-16 * time.Minute)
	tracker.mu.Unlock()
	assert.True(t, tracker.ShouldNudge("agent-1", interval))
}

func TestMurmurNudgeTracker_NudgeCooldown(t *testing.T) {
	t.Parallel()
	tracker := NewMurmurNudgeTracker()
	interval := 15 * time.Minute

	// set up an agent that should be nudged
	tracker.mu.Lock()
	tracker.firstSeen["agent-1"] = time.Now().Add(-2 * time.Minute)
	tracker.heartbeatCount["agent-1"] = 5
	tracker.mu.Unlock()

	assert.True(t, tracker.ShouldNudge("agent-1", interval))

	// record nudge — should not nudge again immediately
	tracker.RecordNudge("agent-1")
	assert.False(t, tracker.ShouldNudge("agent-1", interval))
}

func TestMurmurNudgeTracker_MurmurResetsHeartbeats(t *testing.T) {
	t.Parallel()
	tracker := NewMurmurNudgeTracker()

	tracker.RecordHeartbeat("agent-1")
	tracker.RecordHeartbeat("agent-1")
	tracker.RecordMurmur("agent-1")

	tracker.mu.Lock()
	assert.Equal(t, 0, tracker.heartbeatCount["agent-1"])
	tracker.mu.Unlock()
}

func TestMurmurNudgeTracker_Cleanup(t *testing.T) {
	t.Parallel()
	tracker := NewMurmurNudgeTracker()

	// add an agent seen 2 hours ago
	tracker.mu.Lock()
	tracker.firstSeen["old-agent"] = time.Now().Add(-2 * time.Hour)
	tracker.lastMurmur["old-agent"] = time.Now().Add(-2 * time.Hour)
	tracker.lastNudge["old-agent"] = time.Now().Add(-2 * time.Hour)
	tracker.heartbeatCount["old-agent"] = 10
	tracker.mu.Unlock()

	// add a recent agent
	tracker.RecordHeartbeat("new-agent")

	tracker.Cleanup(1 * time.Hour)

	tracker.mu.Lock()
	_, hasOld := tracker.firstSeen["old-agent"]
	_, hasNew := tracker.firstSeen["new-agent"]
	tracker.mu.Unlock()

	assert.False(t, hasOld, "old agent should be cleaned up")
	assert.True(t, hasNew, "new agent should still exist")
}

func TestMurmurNudgeTracker_Cleanup_RecentMurmurKeepsOldAgent(t *testing.T) {
	t.Parallel()
	tracker := NewMurmurNudgeTracker()

	// agent started 2 hours ago but murmured recently
	tracker.mu.Lock()
	tracker.firstSeen["long-lived"] = time.Now().Add(-2 * time.Hour)
	tracker.lastMurmur["long-lived"] = time.Now().Add(-5 * time.Minute)
	tracker.lastNudge["long-lived"] = time.Now().Add(-10 * time.Minute)
	tracker.heartbeatCount["long-lived"] = 50
	tracker.mu.Unlock()

	tracker.Cleanup(1 * time.Hour)

	tracker.mu.Lock()
	_, hasAgent := tracker.firstSeen["long-lived"]
	tracker.mu.Unlock()

	assert.True(t, hasAgent, "agent with recent murmur should not be cleaned up")
}

func TestMurmurNudgeTracker_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	tracker := NewMurmurNudgeTracker()
	interval := 15 * time.Minute

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			tracker.RecordHeartbeat("agent-1")
			tracker.ShouldNudge("agent-1", interval)
			tracker.RecordMurmur("agent-1")
			tracker.RecordNudge("agent-1")
		}
	}()

	for i := 0; i < 100; i++ {
		tracker.RecordHeartbeat("agent-2")
		tracker.ShouldNudge("agent-2", interval)
	}
	<-done
}
