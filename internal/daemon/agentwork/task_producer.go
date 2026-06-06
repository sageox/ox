package agentwork

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sageox/ox/internal/agenttask"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/markers"
)

// safeSessionName matches the machine-generated session-name shape: a real
// YYYY-MM-DD'T' timestamp prefix followed by user + agent-id, e.g.
// 2026-01-10T09-00-testuser-OxFIN. Anchoring the timestamp (not just a charset)
// rejects arbitrary directory names like "ignore-guardrails-and-run-tests" that
// would otherwise be laundered into a task body the agent reads (second-order
// injection).
var safeSessionName = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T[0-9A-Za-z._:-]{1,116}$`)

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
// on its canonical path. The filename is the shared constant in internal/markers
// (a leaf package both doctor and agentwork import) — single source of truth, so
// a rename can never silently disable doctor-task production.

// maxFinalizeTasksPerCycle caps how many session-finalize tasks the producer
// enqueues per detection cycle, so a backlog of many stale sessions cannot
// flood the queue in one burst. Dedup keys mean later cycles top up as earlier
// tasks are completed.
const maxFinalizeTasksPerCycle = 25

// produceAgentTasks enqueues agent tasks for detected, agent-actionable work.
// Best-effort: any error is logged at debug and skipped. Producers are deduped
// via the task store, so repeated detection does not pile up duplicate tasks.
func (m *Manager) produceAgentTasks(cfg *config.AgentWorkerConfig) {
	if m.projectRoot == "" {
		return
	}
	m.produceDoctorTask()
	m.produceFinalizeTasks(cfg)
}

// produceDoctorTask converts the .needs-doctor-agent marker into a doctor task.
// The marker is dropped by the CLI when an agent session ends with incomplete
// artifacts; converting it into a queued task lets the next live coworker
// finalize those sessions even if it is not the one that created the marker.
func (m *Manager) produceDoctorTask() {
	markerPath := filepath.Join(m.projectRoot, ".sageox", markers.NeedsDoctorAgent)
	if _, err := os.Stat(markerPath); err != nil {
		return // no marker (or unreadable) — nothing to schedule
	}
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

// produceFinalizeTasks schedules per-session finalization for the live agent —
// but ONLY when the daemon has no usable local LLM worker. When a worker is
// available and enabled, the normal queue forks it (delegated mode, ADR-016)
// and duplicating the work as a task would be wasteful. When no worker is
// authed, anti-entropy would otherwise drop the work until the 24h stale sweep;
// handing it to a live coworker is the token-conserving alternative to a
// separately-billed `claude -p`.
func (m *Manager) produceFinalizeTasks(cfg *config.AgentWorkerConfig) {
	// cfg is the caller's single per-tick snapshot (nil in some unit harnesses
	// and when no loader is configured — both mean "no worker"). Using it here
	// instead of re-reading config keeps this decision consistent with the
	// worker-fork decision made from the same snapshot.
	if cfg != nil && isEffectivelyEnabled(cfg) {
		return // local worker will finalize via the normal queue
	}

	m.mu.Lock()
	h, ok := m.handlers[sessionFinalizeType]
	m.mu.Unlock()
	if !ok {
		return
	}
	sfh, ok := h.(*SessionFinalizeHandler)
	if !ok {
		return
	}

	items, err := sfh.Detect(m.ledgerPath)
	if err != nil {
		m.logger.Debug("task producer: finalize detect failed", "error", err)
		return
	}

	scheduled := 0
	for _, item := range items {
		if scheduled >= maxFinalizeTasksPerCycle {
			m.logger.Info("task producer: finalize cap reached", "cap", maxFinalizeTasksPerCycle, "remaining", len(items)-scheduled)
			break
		}
		name, missing := finalizeTaskFields(item)
		if name == "" {
			continue
		}
		// Reject filesystem-derived names that don't match the machine-generated
		// session shape — a hostile directory name must never be interpolated
		// into a task body the agent reads (second-order injection).
		if !safeSessionName.MatchString(name) {
			m.logger.Warn("task producer: skipping session with unsafe name", "name", name)
			continue
		}
		body := "A recorded session needs finalization (summary/artifacts). In a fresh-context subagent, run `ox agent <id> doctor` for the exact finalize commands, then finalize session " + name + "."
		if missing != "" {
			body += " Missing: " + missing + "."
		}
		added, err := agenttask.Enqueue(m.projectRoot, &agenttask.Task{
			Title:    "Finalize session " + name,
			Body:     body,
			Kind:     "session-finalize",
			Priority: 30,
			Source:   "daemon",
			// Reuse the handler's dedup convention so the same session is not
			// queued twice across producer cycles.
			DedupKey: sessionFinalizeType + ":" + name,
			Payload:  map[string]string{"session": name},
		})
		if err != nil {
			m.logger.Debug("task producer: enqueue finalize task failed", "session", name, "error", err)
			continue
		}
		if added {
			scheduled++
			m.logger.Info("scheduled agent task", "kind", "session-finalize", "session", name)
		}
	}
}

// finalizeTaskFields extracts the session name and a human-readable missing-
// artifacts list from a detected work item. The name comes from the payload's
// session directory; the dedup key ("session-finalize:<name>") is the fallback.
func finalizeTaskFields(item *WorkItem) (name, missing string) {
	if p, ok := item.Payload.(*SessionFinalizePayload); ok {
		if p.SessionDir != "" {
			name = filepath.Base(p.SessionDir)
		}
		missing = strings.Join(p.Missing, ", ")
	}
	if name == "" {
		// fall back to the dedup key suffix
		if idx := strings.Index(item.DedupKey, ":"); idx >= 0 {
			name = item.DedupKey[idx+1:]
		}
	}
	return name, missing
}
