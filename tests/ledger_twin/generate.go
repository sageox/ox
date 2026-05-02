//go:build ledger_twin

package ledger_twin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/ledger"
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

	if err := generateMurmurs(basePath, manifest.Murmurs); err != nil {
		return fmt.Errorf("generate murmurs: %w", err)
	}

	if err := generateCarts(basePath, manifest.Carts); err != nil {
		return fmt.Errorf("generate carts: %w", err)
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

func generateCarts(basePath string, specs []CartSpec) error {
	cartsDir := filepath.Join(basePath, "carts")
	if err := os.MkdirAll(cartsDir, 0755); err != nil {
		return fmt.Errorf("create carts dir: %w", err)
	}

	for _, spec := range specs {
		data := map[string]any{
			"id":          spec.ID,
			"title":       spec.Title,
			"description": spec.Description,
			"status":      spec.Status,
			"priority":    spec.Priority,
			"issue_type":  spec.IssueType,
			"assignee":    spec.Assignee,
			"creator":     spec.Creator,
			"source":      "cli",
			"created_at":  spec.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		if spec.ClosedAt != nil {
			data["closed_at"] = spec.ClosedAt.UTC().Format("2006-01-02T15:04:05Z")
		}

		jsonData, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal cart %s: %w", spec.ID, err)
		}
		if err := os.WriteFile(filepath.Join(cartsDir, spec.ID+".json"), jsonData, 0644); err != nil {
			return fmt.Errorf("write cart %s: %w", spec.ID, err)
		}
	}
	return nil
}

func generateMurmurs(basePath string, specs []MurmurSpec) error {
	for _, spec := range specs {
		scope := spec.Scope
		if scope == "" {
			scope = "ledger"
		}
		m := ledger.MurmurFile{
			SchemaVersion: "1",
			ID:            spec.ID,
			Timestamp:     spec.Timestamp,
			AgentID:       spec.Dev.AgentID,
			PrincipalID:   spec.Dev.Username,
			PrincipalType: "human",
			Topic:         spec.Topic,
			Importance:    spec.Importance,
			Content:       spec.Content,
			Scope:         scope,
			Metadata:      spec.Metadata,
		}
		if _, err := ledger.WriteMurmur(basePath, m); err != nil {
			return fmt.Errorf("write murmur %s: %w", spec.ID, err)
		}
	}
	return nil
}
