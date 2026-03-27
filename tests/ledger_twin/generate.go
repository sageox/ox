//go:build ledger_twin

package ledger_twin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// GenerateTwinLedger writes a complete fake ledger to basePath.
func GenerateTwinLedger(basePath string, manifest *TwinManifest) error {
	sessionsDir := filepath.Join(basePath, "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}

	for _, spec := range manifest.Sessions {
		if err := writeSession(sessionsDir, spec); err != nil {
			return fmt.Errorf("write session %s: %w", spec.SessionID, err)
		}
	}
	return nil
}

func writeSession(sessionsDir string, spec SessionSpec) error {
	folderName := fmt.Sprintf("%s-%s-%s",
		spec.Timestamp.Format("2006-01-02T15-04"),
		spec.Dev.Username,
		spec.SessionID)

	dir := filepath.Join(sessionsDir, folderName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	if err := writeMeta(dir, folderName, spec); err != nil {
		return err
	}
	if err := writeRawJSONL(dir, spec); err != nil {
		return err
	}
	if err := writeSummaryJSON(dir, spec); err != nil {
		return err
	}
	if spec.Recording || spec.IsSubagent {
		if err := writeRecording(dir, folderName, spec); err != nil {
			return err
		}
	}
	return nil
}

func writeMeta(dir, folderName string, spec SessionSpec) error {
	meta := map[string]any{
		"version":      "1.0",
		"session_name": folderName,
		"username":     spec.Dev.Email,
		"agent_id":     spec.Dev.AgentID,
		"agent_type":   "claude",
		"model":        "claude-opus-4-6",
		"created_at":   spec.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
		"summary":      spec.Title,
		"entry_count":  len(spec.Files) * 3,
		"files":        map[string]any{},
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), data, 0644)
}

func writeRawJSONL(dir string, spec SessionSpec) error {
	f, err := os.Create(filepath.Join(dir, "raw.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)

	// Header — matches real session format
	_ = enc.Encode(map[string]any{
		"type": "header",
		"metadata": map[string]any{
			"version":    "1.0",
			"agent_type": "claude-code",
			"agent_id":   spec.Dev.AgentID,
			"username":   spec.Dev.Email,
		},
	})

	// Noise: user message (skipped by parser)
	_ = enc.Encode(map[string]any{
		"type":      "user",
		"content":   "Please work on " + spec.Title,
		"timestamp": spec.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
	})

	// Noise: assistant message (skipped by parser)
	_ = enc.Encode(map[string]any{
		"type":      "assistant",
		"content":   "I'll start working on that now.",
		"timestamp": spec.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
	})

	if len(spec.Files) > 0 {
		// Noise: Read tool call (skipped — not edit/write/multiedit)
		toolInput, _ := json.Marshal(map[string]string{"file_path": spec.Files[0].AbsPath})
		_ = enc.Encode(map[string]any{
			"type":       "tool",
			"tool_name":  "Read",
			"tool_input": string(toolInput),
			"timestamp":  spec.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}

	// Actual file touches — same fields as real Claude Code sessions
	for _, ft := range spec.Files {
		input, _ := json.Marshal(map[string]string{"file_path": ft.AbsPath})
		_ = enc.Encode(map[string]any{
			"type":       "tool",
			"tool_name":  ft.ToolName,
			"tool_input": string(input),
			"timestamp":  spec.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}

	return nil
}

func writeSummaryJSON(dir string, spec SessionSpec) error {
	summary := map[string]any{
		"title":   spec.Title,
		"summary": spec.Summary,
		"outcome": "success",
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "summary.json"), data, 0644)
}

func writeRecording(dir, folderName string, spec SessionSpec) error {
	parentPID := 99999
	if spec.ParentPID > 0 {
		parentPID = spec.ParentPID
	}

	rec := map[string]any{
		"agent_id":     spec.Dev.AgentID,
		"started_at":   spec.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
		"adapter_name": "claude",
		"session_path": dir,
		"entry_count":  10,
		"parent_pid":   parentPID,
	}

	if spec.IsSubagent {
		rec["origin"] = "subagent"
		rec["parent_session_path"] = filepath.Dir(dir) + "/" + folderName + "-parent"
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".recording.json"), data, 0644)
}
