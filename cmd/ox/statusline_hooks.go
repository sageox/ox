package main

import (
	"log/slog"
	"time"

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

// recordStatuslineActivity saves an already-classified ox activity ("posted
// murmur") as the latest action under key, which scopes the state to the
// harness session the hook ran in (oxactivity.StateKey), so each Claude Code
// or Codex window shows only what ox did for it. The caller classifies the
// tool call once (recordOxToolCall); this writer only gates on the statusline
// setting.
func recordStatuslineActivity(projectRoot, key string, act oxactivity.Activity, now time.Time) {
	if projectRoot == "" || key == "" || !statuslineEnabled() {
		return
	}
	err := oxactivity.UpdateState(projectRoot, key, func(state *oxactivity.State) bool {
		if now.Before(state.LastActionAt) {
			return false // an overlapping hook already recorded a newer action
		}
		state.LastAction = act.Label
		state.LastActionAt = now
		return true
	})
	if err != nil {
		slog.Debug("statusline: save action failed", "key", key, "err", err)
	}
}

// recordStatuslineWhisper saves the newest teammate murmur among the whispers
// just delivered, so the status line can show who said what. It only reads
// entries already selected for delivery; it never touches the whisper cursor.
// key is an oxactivity.StateKey: the whisper is shown in the session it was
// delivered to.
func recordStatuslineWhisper(projectRoot, key string, entries []whisperstore.WhisperEntry) {
	if projectRoot == "" || key == "" {
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
	from := newest.PrincipalID
	if from == "" {
		from = newest.AgentID
	}
	err := oxactivity.UpdateState(projectRoot, key, func(state *oxactivity.State) bool {
		if !newest.CreatedAt.After(state.LastWhisperAt) {
			return false // already showing this one or a newer one
		}
		state.LastWhisperFrom = from
		state.LastWhisperTopic = newest.Topic
		state.LastWhisperContent = newest.Content
		state.LastWhisperAt = newest.CreatedAt
		return true
	})
	if err != nil {
		slog.Debug("statusline: save whisper failed", "key", key, "err", err)
	}
}
