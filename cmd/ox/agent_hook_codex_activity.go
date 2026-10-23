package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/sageox/ox/internal/oxactivity"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// Codex activity notices.
//
// Codex has no scriptable status line, so ox shows coworkers what it is doing
// through a hook's systemMessage, which the Codex TUI prints in the transcript
// as "↳ Hook · <message>". The message is the same line oxactivity renders for
// the Claude Code status line: "● ox  checked murmurs".
//
// Codex's hook output contract (codex-rs/hooks, rust-v0.154.0):
//
//   - PostToolUse: plain stdout is ignored. A JSON object may carry
//     systemMessage (shown to the coworker) and
//     hookSpecificOutput.additionalContext (given to the model). Unknown
//     fields are rejected, and hookEventName is required inside
//     hookSpecificOutput.
//   - UserPromptSubmit: plain stdout is trimmed and given to the model.
//     JSON's hookSpecificOutput.additionalContext reaches the model through
//     the same append_additional_context call, so wrapping the trimmed text
//     in JSON delivers identical context and adds room for a systemMessage.
//
// Either way stdout must be ONE document: plain text ahead of the JSON makes
// Codex drop the whole output.

const (
	codexEventPostToolUse      = "PostToolUse"
	codexEventUserPromptSubmit = "UserPromptSubmit"
)

// codexHookOutput is the JSON document a Codex hook prints.
type codexHookOutput struct {
	SystemMessage      string                 `json:"systemMessage,omitempty"`
	HookSpecificOutput *codexHookSpecificJSON `json:"hookSpecificOutput,omitempty"`
}

type codexHookSpecificJSON struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// writeCodexHookJSON writes one JSON document carrying the coworker-visible
// notice and the model context. Nothing is written when both are empty.
func writeCodexHookJSON(w io.Writer, event, notice, modelContext string) {
	modelContext = strings.TrimSpace(modelContext)
	if notice == "" && modelContext == "" {
		return
	}
	out := codexHookOutput{SystemMessage: notice}
	if modelContext != "" {
		out.HookSpecificOutput = &codexHookSpecificJSON{HookEventName: event, AdditionalContext: modelContext}
	}
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
// model as additionalContext, and an ox tool call is announced to the coworker.
func emitCodexAfterTool(w io.Writer, ctx *HookContext, agentID string) {
	var whispers bytes.Buffer
	emitWhispers(&whispers, agentID)
	writeCodexHookJSON(w, codexEventPostToolUse, codexToolNotice(ctx), whispers.String())
}

// codexToolNotice returns the activity line for an ox tool call, or "" when
// the call is not ox doing visible work or the statusline setting is off.
func codexToolNotice(ctx *HookContext) string {
	if ctx == nil || ctx.Input == nil || !statuslineEnabled() {
		return ""
	}
	act, ok := oxactivity.Classify(ctx.Input.ToolName, ctx.Input.ToolInput)
	if !ok {
		return ""
	}
	return oxactivity.Render(oxactivity.Segment{Action: act.Label}, codexColorAllowed(ctx))
}

// codexWhisperNotice returns the activity line for the newest teammate murmur
// among the delivered whispers, or "" when there is none or the setting is off.
func codexWhisperNotice(ctx *HookContext, selfAgentID string, delivered []whisperstore.WhisperEntry) string {
	var latest *whisperstore.WhisperEntry
	for i := range delivered {
		e := &delivered[i]
		if e.Source != "murmur" || isOutsideNewsWhisper(*e) || (e.AgentID != "" && e.AgentID == selfAgentID) {
			continue
		}
		if latest == nil || e.CreatedAt.After(latest.CreatedAt) {
			latest = e
		}
	}
	if latest == nil || !statuslineEnabled() {
		return ""
	}
	from := latest.PrincipalID
	if from == "" {
		from = latest.AgentID
	}
	return oxactivity.RenderWidth(oxactivity.Segment{
		WhisperFrom:    from,
		WhisperTopic:   latest.Topic,
		WhisperContent: latest.Content,
	}, codexColorAllowed(ctx), codexNoticeWidth)
}

// codexNoticeWidth caps a whisper notice so a long murmur stays one tidy
// transcript line; the whisper text itself is shortened first.
const codexNoticeWidth = 120

// codexPromptStdout collects handlePrompt's output for Codex so a teammate
// whisper notice can ride along with the model context in one JSON document.
// For every other AI coworker it writes straight through, byte for byte.
type codexPromptStdout struct {
	ctx      *HookContext
	out      io.Writer
	codex    bool
	buf      bytes.Buffer
	whispers []whisperstore.WhisperEntry
}

func newCodexPromptStdout(ctx *HookContext, out io.Writer) *codexPromptStdout {
	return &codexPromptStdout{ctx: ctx, out: out, codex: ctx != nil && ctx.AgentType == "codex"}
}

func (s *codexPromptStdout) Write(p []byte) (int, error) {
	if !s.codex {
		return s.out.Write(p)
	}
	return s.buf.Write(p)
}

// flush writes the collected output. Without a notice the bytes are exactly
// what handlePrompt printed before, so Codex's plain-stdout path is unchanged.
func (s *codexPromptStdout) flush() {
	if !s.codex {
		return
	}
	selfAgentID := ""
	if s.ctx.Marker != nil {
		selfAgentID = s.ctx.Marker.AgentID
	}
	notice := ""
	if len(s.whispers) > 0 {
		notice = codexWhisperNotice(s.ctx, selfAgentID, s.whispers)
	}
	if notice == "" {
		_, _ = s.out.Write(s.buf.Bytes())
		return
	}
	writeCodexHookJSON(s.out, codexEventUserPromptSubmit, notice, s.buf.String())
}

// codexANSIMinVersion is the first Codex release whose TUI parses ANSI in a
// hook systemMessage (openai/codex#50359). Older TUIs print the escape bytes
// as literal text.
var codexANSIMinVersion = [3]int{0, 162, 0}

// codexColorAllowed reports whether the notice may carry color: the coworker
// has not opted out, and the Codex session is the interactive TUI at a version
// that renders ANSI. Anything unknown is plain text, which renders everywhere.
//
// The coworker's own NO_COLOR decides, not the NO_COLOR=1 ox forces on itself
// at startup. The escapes survive ox's stdout ANSI stripper because the JSON
// encoder writes ESC as the six bytes \u001b.
func codexColorAllowed(ctx *HookContext) bool {
	if !oxactivity.ColorAllowedFor(coworkerNoColor) || ctx == nil || ctx.Input == nil {
		return false
	}
	meta, ok := readCodexSessionMeta(codexTranscriptPath(ctx.Input.RawBytes))
	if !ok {
		return false
	}
	return meta.isTUI() && codexVersionAtLeast(meta.CLIVersion, codexANSIMinVersion)
}

// codexTranscriptPath reads transcript_path from a Codex hook payload.
func codexTranscriptPath(raw []byte) string {
	var in struct {
		TranscriptPath *string `json:"transcript_path"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil || in.TranscriptPath == nil {
		return ""
	}
	return *in.TranscriptPath
}

// codexSessionMeta is the part of a Codex rollout's first line ox reads.
type codexSessionMeta struct {
	CLIVersion string `json:"cli_version"`
	Originator string `json:"originator"`
	Source     string `json:"source"`
}

// isTUI reports whether the session is Codex's interactive terminal UI. The
// desktop app and `codex exec` do not render hook messages through the TUI.
func (m codexSessionMeta) isTUI() bool {
	return m.Source == "cli" || m.Originator == "codex-tui"
}

// codexMetaLineLimit caps the session_meta read; the line embeds the base
// instructions (tens of KB).
const codexMetaLineLimit = 1 << 20

// readCodexSessionMeta reads the session_meta record on the first line of a
// Codex rollout file.
func readCodexSessionMeta(path string) (codexSessionMeta, bool) {
	if path == "" {
		return codexSessionMeta{}, false
	}
	f, err := os.Open(path) //nolint:gosec // path comes from the harness's own hook payload
	if err != nil {
		return codexSessionMeta{}, false
	}
	defer f.Close()
	line, err := bufio.NewReader(io.LimitReader(f, codexMetaLineLimit)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return codexSessionMeta{}, false
	}
	var rec struct {
		Type    string           `json:"type"`
		Payload codexSessionMeta `json:"payload"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "session_meta" || rec.Payload.CLIVersion == "" {
		return codexSessionMeta{}, false
	}
	return rec.Payload, true
}

// codexVersionAtLeast compares a "MAJOR.MINOR.PATCH[-pre]" version. A
// prerelease counts as older than its release, and anything unparsable fails.
func codexVersionAtLeast(version string, minimum [3]int) bool {
	core, pre, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(version), "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	var got [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return false
		}
		got[i] = n
	}
	for i := range got {
		if got[i] != minimum[i] {
			return got[i] > minimum[i]
		}
	}
	return pre == ""
}
