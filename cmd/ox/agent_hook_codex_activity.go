package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
)

// Codex hook output.
//
// Codex's hook output contract (codex-rs/hooks, rust-v0.167.0):
//
//   - PostToolUse: plain stdout is ignored. A JSON object may carry
//     hookSpecificOutput.additionalContext (given to the model) and an
//     optional systemMessage (shown to the coworker). Unknown fields are
//     rejected, and hookEventName is required inside hookSpecificOutput.
//   - UserPromptSubmit: plain stdout is trimmed and given to the model, so
//     the prompt hook needs no envelope.
//   - Stop: stdout must be a JSON object. It may carry systemMessage but not
//     hookSpecificOutput, which the Stop schema rejects.
//
// So whispers delivered on PostToolUse must ride in that JSON envelope or the
// Codex model never sees them. The one systemMessage ox sends per tool call is
// the flow lane of an ox call that moved team data (statusline_digest.go),
// shown under that call; every other tool call prints no notice. stdout must
// be ONE document: plain text ahead of the JSON makes Codex drop the whole
// output.

const codexEventPostToolUse = "PostToolUse"

// codexHookOutput is the JSON document a Codex hook prints.
type codexHookOutput struct {
	HookSpecificOutput *codexHookSpecificJSON `json:"hookSpecificOutput,omitempty"`
	// SystemMessage is shown to the coworker: a flow lane, or the Stop digest.
	SystemMessage string `json:"systemMessage,omitempty"`
}

type codexHookSpecificJSON struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// writeCodexHookDoc writes out as the hook's single stdout document.
func writeCodexHookDoc(w io.Writer, event string, out codexHookOutput) {
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

// emitCodexAfterTool is Codex's PostToolUse stdout, one document or nothing:
// pending whispers go to the model as additionalContext, and notice (the
// call's flow lane, "" for none) is shown to the coworker.
func emitCodexAfterTool(w io.Writer, agentID, notice string) {
	var whispers bytes.Buffer
	emitWhispers(&whispers, agentID)
	out := codexHookOutput{SystemMessage: notice}
	if modelContext := strings.TrimSpace(whispers.String()); modelContext != "" {
		out.HookSpecificOutput = &codexHookSpecificJSON{HookEventName: codexEventPostToolUse, AdditionalContext: modelContext}
	}
	if out.HookSpecificOutput == nil && out.SystemMessage == "" {
		return
	}
	writeCodexHookDoc(w, codexEventPostToolUse, out)
}
