package main

// session_amp.go: where an Amp recording starts in its thread's sidecar
// (sageox/ox#1318).
//
// Amp keeps no transcript; the ox-bridge plugin
// (cmd/ox-adapter-amp/plugin/ox-bridge.ts) appends a thread's turns to
// ~/.cache/amp/ox-sessions/<thread-id>.jsonl, writing the coworker's prompt
// at agent.start. The model runs `ox agent prime` only after that, so a
// recording that starts at prime time, or at the sidecar's end, misses the
// prompt that made the model prime. A recording that starts at the
// sidecar's beginning repeats every earlier turn of the thread, which is
// what a re-prime after the orb's idle end would do. So an Amp recording
// starts at the line of the prompt in progress.

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/session/adapters"
)

// ampRecordingStart finds the thread's sidecar and the byte offset its
// recording starts at. ok is false when either is unknown; the recording
// then starts as before, and the stop looks the sidecar up itself.
func ampRecordingStart(projectRoot, agentID, threadURL string) (sidecar string, offset int64, ok bool) {
	// Only a named thread: without one the adapter falls back to the newest
	// sidecar, which may be another thread's.
	if threadURL == "" {
		return "", 0, false
	}
	adapter, err := adapters.GetAdapter(string(agentx.AgentTypeAmp))
	if err != nil {
		return "", 0, false
	}
	sidecar, err = adapter.FindSessionFile(adapters.SessionLookup{
		RepoRoot:       projectRoot,
		AgentID:        agentID,
		AgentSessionID: threadURL,
		Since:          time.Now().Add(-5 * time.Minute),
	})
	if err != nil {
		slog.Debug("amp sidecar not found at prime, the stop will look it up", "agent_id", agentID, "error", err)
		return "", 0, false
	}
	offset, err = ampPromptOffset(sidecar)
	if err != nil {
		slog.Warn("amp sidecar unreadable at prime, recording from the prime instead", "agent_id", agentID, "error", err)
		return "", 0, false
	}
	return sidecar, offset, true
}

// ampPromptOffset returns the byte offset of the last prompt line in an
// ox-bridge sidecar, or the end of its complete lines when it holds no
// prompt. A partial last line is still being written and is never a start.
func ampPromptOffset(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	var end int64
	prompt := int64(-1)
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		var entry struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Type == "user" {
			prompt = end
		}
		end += int64(len(line))
	}
	if prompt < 0 {
		return end, nil
	}
	return prompt, nil
}
