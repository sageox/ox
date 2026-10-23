package main

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/oxactivity"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// Hook-side writers for the ox status line. Hooks record what ox just did and
// the newest teammate whisper into the per-agent activity state; `ox
// statusline` only reads it. Both writers are best-effort: a failure is logged
// and never reaches the hook's output.

// statuslineEnabled reads the coworker's statusline setting. Callers load it
// only after they know there is something to record, so a hook that records
// nothing pays no config read.
func statuslineEnabled() bool {
	cfg, err := config.LoadUserConfig()
	if err != nil || cfg == nil {
		return true // the default; a broken config file must not hide the line
	}
	return cfg.IsStatuslineEnabled()
}

// recordStatuslineAction saves the activity a tool call represents ("posted
// murmur") when the call is ox doing coworker-visible work. Anything else —
// non-ox commands, ox plumbing like heartbeats — writes nothing.
func recordStatuslineAction(projectRoot, agentID string, input *agentx.HookInput) {
	if input == nil || projectRoot == "" || agentID == "" {
		return
	}
	recordStatuslineToolCall(projectRoot, agentID, input.ToolName, input.ToolInput, time.Now())
}

func recordStatuslineToolCall(projectRoot, agentID, toolName string, toolInput json.RawMessage, now time.Time) {
	act, ok := oxactivity.Classify(toolName, toolInput)
	if !ok || !statuslineEnabled() {
		return
	}
	state := oxactivity.LoadState(projectRoot, agentID)
	state.LastAction = act.Label
	state.LastActionAt = now
	if err := oxactivity.SaveState(projectRoot, agentID, state); err != nil {
		slog.Debug("statusline: save action failed", "agent_id", agentID, "err", err)
	}
}

// recordStatuslineWhisper saves the newest teammate murmur among the whispers
// just delivered, so the status line can show who said what. It only reads
// entries already selected for delivery; it never touches the whisper cursor.
func recordStatuslineWhisper(projectRoot, agentID string, entries []whisperstore.WhisperEntry) {
	if projectRoot == "" || agentID == "" {
		return
	}
	var newest *whisperstore.WhisperEntry
	for i := range entries {
		e := &entries[i]
		if e.Source != "murmur" {
			continue
		}
		if newest == nil || e.CreatedAt.After(newest.CreatedAt) {
			newest = e
		}
	}
	if newest == nil || !statuslineEnabled() {
		return
	}
	state := oxactivity.LoadState(projectRoot, agentID)
	if !newest.CreatedAt.After(state.LastWhisperAt) {
		return // already showing this one or a newer one
	}
	from := newest.PrincipalID
	if from == "" {
		from = newest.AgentID
	}
	state.LastWhisperFrom = from
	state.LastWhisperTopic = newest.Topic
	state.LastWhisperContent = newest.Content
	state.LastWhisperAt = newest.CreatedAt
	if err := oxactivity.SaveState(projectRoot, agentID, state); err != nil {
		slog.Debug("statusline: save whisper failed", "agent_id", agentID, "err", err)
	}
}
