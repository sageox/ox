package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sageox/ox/internal/agenttask"
)

// Agent-task surfacing. Tasks reach the calling agent through the
// UserPromptSubmit hook only — the single reliable channel into Claude's
// context mid-session (see agent_hook.go's handlePrompt). emitAgentTasks is
// invoked there on every prompt.
//
// It is throttled so a stable queue does not repeat the same block every turn:
// a per-agent cursor records the signature (hash of the sorted ready task ids)
// last surfaced, and the block is re-emitted only when the ready set changes or
// after a re-nudge interval if work is still pending.

const (
	// taskRenudgeInterval re-surfaces a still-pending, unchanged queue so the
	// agent is reminded without being spammed every turn.
	taskRenudgeInterval = 30 * time.Minute
	// maxSurfacedTasks caps how many tasks are listed inline to keep context lean.
	maxSurfacedTasks = 5
)

// taskSeenCursor records the last ready-set signature surfaced to an agent.
type taskSeenCursor struct {
	Signature string    `json:"signature"`
	At        time.Time `json:"at"`
}

// emitAgentTasks writes a throttled <system-reminder> block listing ready tasks
// the given agent can pick up. Best-effort: any error (no store, no tasks, I/O
// failure) results in no output and no disruption to the prompt hook.
func emitAgentTasks(w io.Writer, projectRoot, agentID string) {
	if projectRoot == "" || agentID == "" {
		return
	}

	store, err := agenttask.NewStore(projectRoot)
	if err != nil {
		return
	}

	agentType := os.Getenv("AGENT_ENV")
	ready, err := store.Ready(agentType)
	if err != nil || len(ready) == 0 {
		return
	}

	sig := readySignature(ready)
	cursor := readTaskCursor(projectRoot, agentID)
	if !shouldSurface(sig, cursor) {
		return
	}

	writeTaskReminder(w, ready)
	writeTaskCursor(projectRoot, agentID, taskSeenCursor{Signature: sig, At: time.Now()})
}

// shouldSurface decides whether to emit given the prior cursor: emit on a
// changed ready set, or when the unchanged set has gone stale past the re-nudge
// interval.
func shouldSurface(sig string, cursor taskSeenCursor) bool {
	if cursor.Signature != sig {
		return true
	}
	return time.Since(cursor.At) > taskRenudgeInterval
}

// readySignature hashes the sorted ready task ids so the same pending set maps
// to a stable signature across turns.
func readySignature(tasks []*agenttask.Task) string {
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)
	h := fnv.New64a()
	for _, id := range ids {
		_, _ = h.Write([]byte(id))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum64())
}

func writeTaskReminder(w io.Writer, ready []*agenttask.Task) {
	fmt.Fprintln(w, "<system-reminder>")
	fmt.Fprintf(w, "SageOx has %d scheduled agent task(s) ready for an AI coworker to execute.\n", len(ready))
	fmt.Fprintln(w, "These are background chores (doctoring, session finalization, anti-entropy) scheduled by the daemon or other internal producers — NOT the user's request.")
	fmt.Fprintln(w, "Run each in a SUBAGENT with a fresh context so it does not consume your main context window or derail the user's current work.")

	shown := ready
	if len(shown) > maxSurfacedTasks {
		shown = shown[:maxSurfacedTasks]
	}
	for _, t := range shown {
		fmt.Fprintf(w, "<task id=%q priority=\"%d\"", t.ID, t.Priority)
		if t.Kind != "" {
			fmt.Fprintf(w, " kind=%q", t.Kind)
		}
		fmt.Fprintf(w, ">%s</task>\n", escapeXML(t.Title))
	}
	if len(ready) > maxSurfacedTasks {
		fmt.Fprintf(w, "(+%d more — see `ox agent <id> tasks list`)\n", len(ready)-maxSurfacedTasks)
	}

	fmt.Fprintln(w, "Claim and execute: `ox agent <id> tasks next` → dispatch to a subagent → `ox agent <id> tasks done <task-id>`.")
	fmt.Fprintln(w, "</system-reminder>")
}

// taskCursorPath returns the per-agent throttle cursor path under the
// gitignored .sageox/cache/ directory.
func taskCursorPath(projectRoot, agentID string) string {
	return filepath.Join(projectRoot, ".sageox", "cache", "agent_tasks_seen", agentID+".json")
}

func readTaskCursor(projectRoot, agentID string) taskSeenCursor {
	var c taskSeenCursor
	data, err := os.ReadFile(taskCursorPath(projectRoot, agentID))
	if err != nil {
		return c
	}
	_ = json.Unmarshal(data, &c)
	return c
}

func writeTaskCursor(projectRoot, agentID string, c taskSeenCursor) {
	path := taskCursorPath(projectRoot, agentID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}
