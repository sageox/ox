package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
)

// Codex PostToolUse output.
//
// Codex's hook output contract (codex-rs/hooks, rust-v0.154.0):
//
//   - PostToolUse: plain stdout is ignored. A JSON object may carry
//     hookSpecificOutput.additionalContext (given to the model) and an
//     optional systemMessage (shown to the coworker). Unknown fields are
//     rejected, and hookEventName is required inside hookSpecificOutput.
//   - UserPromptSubmit: plain stdout is trimmed and given to the model, so
//     the prompt hook needs no envelope.
//
// So whispers delivered on PostToolUse must ride in that JSON envelope or the
// Codex model never sees them. ox sends no systemMessage: Codex has no status
// line API, and per-call transcript notices drew coworker attention to ox
// plumbing rather than to value. stdout must be ONE document: plain text
// ahead of the JSON makes Codex drop the whole output.

const codexEventPostToolUse = "PostToolUse"

// codexHookOutput is the JSON document a Codex hook prints. systemMessage is
// optional in Codex's schema and ox never sets it, so it is not modeled.
type codexHookOutput struct {
	HookSpecificOutput *codexHookSpecificJSON `json:"hookSpecificOutput,omitempty"`
}

type codexHookSpecificJSON struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// writeCodexHookJSON writes one JSON document carrying the model context.
// Nothing is written when there is none.
func writeCodexHookJSON(w io.Writer, event, modelContext string) {
	modelContext = strings.TrimSpace(modelContext)
	if modelContext == "" {
		return
	}
	out := codexHookOutput{HookSpecificOutput: &codexHookSpecificJSON{HookEventName: event, AdditionalContext: modelContext}}
	// Hook protocol bytes read by Codex, not a terminal --json payload, so
	// cli.PrintJSON's colorizer stays out of it.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		slog.Debug("hook: codex output marshal failed", "event", event, "error", err)
		return
	}
	_, _ = w.Write(buf.Bytes())
}

// emitCodexAfterTool is Codex's PostToolUse stdout: pending whispers go to the
// model as additionalContext.
func emitCodexAfterTool(w io.Writer, agentID string) {
	var whispers bytes.Buffer
	emitWhispers(&whispers, agentID)
	writeCodexHookJSON(w, codexEventPostToolUse, whispers.String())
}
