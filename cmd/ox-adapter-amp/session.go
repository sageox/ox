// session.go handles Amp session reading, parsing, discovery, and types.
//
// Current Amp does not persist conversation transcripts to disk. The
// `ox-bridge` Bun plugin (installed by install-hooks into a project's
// .amp/plugins/) writes a JSONL sidecar per thread to
// ~/.cache/amp/ox-sessions/<thread-id>.jsonl as Amp emits plugin
// lifecycle events. This file is what the adapter discovers and tails.
//
// Each line is a JSON object with "type" field: "user", "assistant",
// "tool_use", "tool_result", "system", or "session_meta". Tool entries
// use "tool_name", "tool_input", and "call_id" for correlation. Session
// metadata (model, agent_version) is in a "session_meta" entry.
//
// For users on the legacy pre-2026 Amp (which wrote
// ~/.amp/sessions/*.jsonl directly), the discovery path falls back to
// the old location so existing installs keep working.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// --- types ---

type ampEntry struct {
	Type         string `json:"type"`
	Timestamp    string `json:"timestamp"`
	Content      string `json:"content"`
	ToolName     string `json:"tool_name,omitempty"`
	ToolInput    string `json:"tool_input,omitempty"`
	CallID       string `json:"call_id,omitempty"`
	IsError      bool   `json:"is_error,omitempty"`
	Model        string `json:"model,omitempty"`
	AgentVersion string `json:"agent_version,omitempty"`
}

// --- session reading ---

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	entries, err := readAmpFile(p.SessionFile)
	if err != nil {
		return nil, err
	}
	meta := extractAmpMetadata(p.SessionFile)
	return &adapterprotocol.ReadResult{Entries: entries, Metadata: meta}, nil
}

func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	meta := extractAmpMetadata(p.SessionFile)
	if meta == nil {
		return &adapterprotocol.ReadMetadataResult{}, nil
	}
	return &adapterprotocol.ReadMetadataResult{
		AgentVersion: meta.AgentVersion,
		Model:        meta.Model,
	}, nil
}

func readAmpFile(path string) ([]adapterprotocol.RawEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	var entries []adapterprotocol.RawEntry
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		parsed := parseAmpLine(line)
		if parsed != nil {
			entries = append(entries, *parsed)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	return entries, nil
}

// readAmpFromOffset resumes a tail read of the ox-bridge JSONL sidecar at
// offset. Delegates to the shared adapterruntime.TailJSONL rather than a
// private seek-and-scan loop: the private version this replaced advanced
// the offset to the file's current size even when the final line was a
// partial write-in-progress, silently losing the rest of that turn once
// the agent finished writing it. TailJSONL stops at the last complete
// newline instead.
func readAmpFromOffset(path string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
	return adapterruntime.TailJSONL(path, offset, parseAmpJSONLLine)
}

// parseAmpJSONLLine adapts parseAmpLine (single entry-or-nil, no error) to
// adapterruntime.LineParser's (zero-or-more entries, error) shape.
func parseAmpJSONLLine(line []byte) ([]adapterprotocol.RawEntry, error) {
	entry := parseAmpLine(line)
	if entry == nil {
		return nil, nil
	}
	return []adapterprotocol.RawEntry{*entry}, nil
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseAmpLine(line []byte) *adapterprotocol.RawEntry {
	var raw ampEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil
	}

	ts := parseTS(raw.Timestamp)

	switch raw.Type {
	case "user":
		if raw.Content == "" {
			return nil
		}
		e := adapterruntime.UserEntry(ts, raw.Content)
		return &e

	case "assistant":
		if raw.Content == "" {
			return nil
		}
		e := adapterruntime.AssistantEntry(ts, raw.Content)
		return &e

	case "tool_use":
		e := adapterruntime.ToolUseWithID(ts, raw.ToolName, raw.ToolInput, raw.CallID)
		return &e

	case "tool_result":
		e := adapterruntime.ToolResultWithID(ts, raw.Content, raw.IsError, raw.CallID)
		return &e

	case "system":
		if raw.Content == "" {
			return nil
		}
		e := adapterruntime.SystemEntry(ts, raw.Content)
		return &e
	}

	return nil
}

// --- session discovery ---

// ampSessionsDirs returns the candidate sessions directories in priority
// order. The first existing directory wins. New installs use
// ~/.cache/amp/ox-sessions/ (written by the ox-bridge plugin); legacy
// installs (pre-2026 Amp) used ~/.amp/sessions/ directly.
func ampSessionsDirs(home string) []string {
	return []string{
		filepath.Join(home, ".cache", "amp", "ox-sessions"),
		filepath.Join(home, ".amp", "sessions"),
	}
}

// ampThreadID reduces a native session id to the thread id the ox-bridge
// sidecar is named by. Amp exports a thread to the agent as AMP_THREAD_URL
// (https://ampcode.com/threads/T-…), and ox records that URL as the native
// session id, so it is what comes back for lookup (sageox/ox#1314). A bare
// thread id passes through; anything else is returned as given, for
// ValidateSessionID to judge.
func ampThreadID(id string) string {
	return adapterruntime.AmpThreadID(id)
}

func findAmpSession(_, agentID, since, agentSessionID string) (string, error) {
	if err := adapterruntime.ValidateSessionSource(adapterName, "", agentSessionID); err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	dirs := ampSessionsDirs(home)

	threadID := ampThreadID(agentSessionID)
	// Search both locations, but never substitute a different thread.
	for _, d := range dirs {
		direct := filepath.Join(d, threadID+".jsonl")
		if _, err := os.Stat(direct); err == nil {
			if err := adapterruntime.ValidateSessionSource(adapterName, direct, agentSessionID); err != nil {
				return "", err
			}
			return direct, nil
		}
	}
	return "", fmt.Errorf("no sidecar for thread %s in %s", threadID, strings.Join(dirs, ", "))
}

func extractAmpMetadata(path string) *adapterprotocol.SessionMetadata {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	meta := &adapterprotocol.SessionMetadata{}
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		var raw ampEntry
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		if raw.Type == "session_meta" {
			if raw.Model != "" {
				meta.Model = raw.Model
			}
			if raw.AgentVersion != "" {
				meta.AgentVersion = raw.AgentVersion
			}
			break
		}
	}

	if meta.AgentVersion == "" && meta.Model == "" {
		return nil
	}
	return meta
}
