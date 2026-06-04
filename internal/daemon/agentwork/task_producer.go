package agentwork

import (
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/agenttask"
)

// Daemon task producer.
//
// produceAgentTasks bridges the daemon's anti-entropy detection into the
// project-local agent task queue (internal/agenttask). The next interactive AI
// coworker picks the work up and runs it as a fresh-context subagent — instead
// of the daemon forking `claude -p`, which bills against a separate account and
// loses the developer's warm session.
//
// It runs on the daemon's doctor timer (and ForceDetect) regardless of whether
// the daemon's own LLM worker is enabled: scheduling a task for a live agent is
// exactly the fallback for when the daemon cannot run the work itself.
//
// agentwork cannot import internal/doctor (that would cycle:
// daemon → agentwork → doctor → daemon), so the marker is checked via os.Stat
// on its canonical path. Keep this constant in sync with
// internal/doctor.NeedsDoctorAgentMarker.
const needsDoctorAgentMarker = ".needs-doctor-agent"

// produceAgentTasks enqueues agent tasks for detected, agent-actionable work.
// Best-effort: any error is logged at debug and skipped. Producers are deduped
// via the task store, so repeated detection does not pile up duplicate tasks.
func (m *Manager) produceAgentTasks() {
	if m.projectRoot == "" {
		return
	}

	// .needs-doctor-agent marker → doctor task. The marker is dropped by the
	// CLI when an agent session ends with incomplete artifacts; converting it
	// into a queued task lets the next live coworker finalize those sessions
	// even if it is not the one that created the marker.
	markerPath := filepath.Join(m.projectRoot, ".sageox", needsDoctorAgentMarker)
	if _, err := os.Stat(markerPath); err == nil {
		added, err := agenttask.Enqueue(m.projectRoot, &agenttask.Task{
			Title:    "Finalize incomplete SageOx sessions",
			Body:     "Incomplete coding sessions need finalization. In a fresh-context subagent, run `ox agent <id> doctor` to inspect, then follow its finalize/recover steps. This clears the .needs-doctor-agent marker.",
			Kind:     "doctor",
			Priority: 20,
			Source:   "daemon",
			DedupKey: "doctor-agent",
		})
		if err != nil {
			m.logger.Debug("task producer: enqueue doctor task failed", "error", err)
		} else if added {
			m.logger.Info("scheduled agent task", "kind", "doctor", "dedup_key", "doctor-agent")
		}
	}
}
