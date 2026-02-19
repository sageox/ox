package html

import (
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGenerator(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)
	require.NotNil(t, gen)
}

func TestGenerate_MinimalSession(t *testing.T) {
	gen, _ := NewGenerator()

	tr := &session.StoredSession{
		Meta: &session.StoreMeta{
			AgentType: "claude-code",
			Model:     "claude-sonnet-4",
			Username:  "test@example.com",
		},
		Entries: []map[string]any{
			{
				"type":      "user",
				"content":   "Hello",
				"timestamp": time.Now().Format(time.RFC3339),
			},
			{
				"type":      "assistant",
				"content":   "Hi there!",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		},
	}

	html, err := gen.Generate(tr)
	require.NoError(t, err)

	htmlStr := string(html)

	// check basic structure
	assert.Contains(t, htmlStr, "<!DOCTYPE html>", "Missing DOCTYPE")
	assert.Contains(t, htmlStr, "SageOx", "Missing SageOx branding")
	assert.Contains(t, htmlStr, "sageox.ai", "Missing sageox.ai link")
	assert.Contains(t, htmlStr, "Hello", "Missing user message content")
	assert.Contains(t, htmlStr, "Hi there!", "Missing assistant message content")
}

func TestGenerate_WithToolCall(t *testing.T) {
	gen, _ := NewGenerator()

	tr := &session.StoredSession{
		Meta: &session.StoreMeta{},
		Entries: []map[string]any{
			{
				"type":        "tool",
				"tool_name":   "Bash",
				"tool_input":  "ls -la",
				"tool_output": "file1.txt\nfile2.txt",
				"timestamp":   time.Now().Format(time.RFC3339),
			},
		},
	}

	html, err := gen.Generate(tr)
	require.NoError(t, err)

	htmlStr := string(html)
	assert.Contains(t, htmlStr, "Bash", "Missing tool name")
	assert.Contains(t, htmlStr, "ls -la", "Missing tool input")
}

func TestGenerate_XSSPrevention(t *testing.T) {
	gen, _ := NewGenerator()

	maliciousContent := `<script>alert('xss')</script>`

	tr := &session.StoredSession{
		Meta: &session.StoreMeta{},
		Entries: []map[string]any{
			{
				"type":      "user",
				"content":   maliciousContent,
				"timestamp": time.Now().Format(time.RFC3339),
			},
		},
	}

	htmlBytes, err := gen.Generate(tr)
	require.NoError(t, err)

	htmlStr := string(htmlBytes)
	// goldmark strips raw HTML by default — the malicious alert should not appear
	// (the template itself contains <script> for viewer.js, so we check for the payload)
	assert.False(t, strings.Contains(htmlStr, "alert('xss')"), "XSS vulnerability: script content not stripped")
}

func TestDefaultBrandColors(t *testing.T) {
	colors := DefaultBrandColors()

	assert.Equal(t, "#7A8F78", colors.Primary)
	assert.Equal(t, "#E0A56A", colors.Secondary)
}

func TestGenerate_NilSession(t *testing.T) {
	gen, _ := NewGenerator()

	_, err := gen.Generate(nil)
	assert.Error(t, err, "Generate(nil) should return error")
}

func TestGenerate_EmptySession(t *testing.T) {
	gen, _ := NewGenerator()

	tr := &session.StoredSession{
		Meta:    &session.StoreMeta{},
		Entries: []map[string]any{},
	}

	html, err := gen.Generate(tr)
	require.NoError(t, err)

	htmlStr := string(html)
	assert.Contains(t, htmlStr, "<!DOCTYPE html>", "Empty session should still generate valid HTML")
}

func TestGenerate_WithMetadata(t *testing.T) {
	gen, _ := NewGenerator()

	createdAt := time.Date(2025, 1, 5, 10, 30, 0, 0, time.UTC)
	tr := &session.StoredSession{
		Meta: &session.StoreMeta{
			AgentType:    "claude-code",
			AgentVersion: "1.0.3",
			Model:        "claude-sonnet-4",
			Username:     "developer@example.com",
			CreatedAt:    createdAt,
		},
		Entries: []map[string]any{
			{
				"type":      "user",
				"content":   "Test message",
				"timestamp": createdAt.Format(time.RFC3339),
			},
		},
	}

	html, err := gen.Generate(tr)
	require.NoError(t, err)

	htmlStr := string(html)

	// check metadata appears in output
	assert.Contains(t, htmlStr, "claude-code", "Missing agent type in output")
	assert.Contains(t, htmlStr, "claude-sonnet-4", "Missing model in output")
	assert.Contains(t, htmlStr, "developer@example.com", "Missing username in output")
}

func TestGenerate_MessageTypes(t *testing.T) {
	gen, _ := NewGenerator()

	tr := &session.StoredSession{
		Meta: &session.StoreMeta{},
		Entries: []map[string]any{
			{
				"type":      "user",
				"content":   "User message",
				"timestamp": time.Now().Format(time.RFC3339),
			},
			{
				"type":      "assistant",
				"content":   "Assistant message",
				"timestamp": time.Now().Format(time.RFC3339),
			},
			{
				"type":      "system",
				"content":   "System message",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		},
	}

	html, err := gen.Generate(tr)
	require.NoError(t, err)

	htmlStr := string(html)

	// verify all message types are present with their CSS classes
	assert.Contains(t, htmlStr, "message-user", "Missing user message CSS class")
	assert.Contains(t, htmlStr, "message-assistant", "Missing assistant message CSS class")
	assert.Contains(t, htmlStr, "message-system", "Missing system message CSS class")
}

func TestGenerate_ToolCallDetails(t *testing.T) {
	gen, _ := NewGenerator()

	tr := &session.StoredSession{
		Meta: &session.StoreMeta{},
		Entries: []map[string]any{
			{
				"type":        "tool",
				"tool_name":   "Read",
				"tool_input":  "/path/to/file.go",
				"tool_output": "package main\n\nfunc main() {}",
				"timestamp":   time.Now().Format(time.RFC3339),
			},
		},
	}

	html, err := gen.Generate(tr)
	require.NoError(t, err)

	htmlStr := string(html)

	// check tool details are present
	assert.Contains(t, htmlStr, "Read", "Missing tool name")
	assert.Contains(t, htmlStr, "/path/to/file.go", "Missing tool input")
	assert.Contains(t, htmlStr, "package main", "Missing tool output")
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m 30s"},
		{5 * time.Minute, "5m"},
		{65 * time.Minute, "1h 5m"},
		{2 * time.Hour, "2h"},
		{2*time.Hour + 30*time.Minute, "2h 30m"},
	}

	for _, tt := range tests {
		got := FormatDuration(tt.duration)
		assert.Equal(t, tt.want, got, "FormatDuration(%v)", tt.duration)
	}
}

func TestNormalizeMessageType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"user", "user"},
		{"human", "user"},
		{"USER", "user"},
		{"assistant", "assistant"},
		{"ai", "assistant"},
		{"model", "assistant"},
		{"ASSISTANT", "assistant"},
		{"system", "system"},
		{"SYSTEM", "system"},
		{"tool", "tool"},
		{"tool_use", "tool"},
		{"tool_call", "tool"},
		{"tool_result", "tool_result"},
		{"tool_output", "tool_result"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		got := normalizeMessageType(tt.input)
		assert.Equal(t, tt.want, got, "normalizeMessageType(%q)", tt.input)
	}
}

func TestExtractContent(t *testing.T) {
	tests := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{
			name:  "direct content",
			entry: map[string]any{"content": "Hello world"},
			want:  "Hello world",
		},
		{
			name:  "nested data content",
			entry: map[string]any{"data": map[string]any{"content": "Nested content"}},
			want:  "Nested content",
		},
		{
			name:  "message field",
			entry: map[string]any{"message": "Message content"},
			want:  "Message content",
		},
		{
			name:  "text field",
			entry: map[string]any{"text": "Text content"},
			want:  "Text content",
		},
		{
			name:  "empty entry",
			entry: map[string]any{},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := session.ExtractContent(tt.entry)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractToolCall(t *testing.T) {
	tests := []struct {
		name    string
		entry   map[string]any
		wantNil bool
		wantVal *ToolCallView
	}{
		{
			name:    "non-tool entry",
			entry:   map[string]any{"type": "user", "content": "Hello"},
			wantNil: true,
		},
		{
			name: "tool entry with name and input",
			entry: map[string]any{
				"type":       "tool",
				"tool_name":  "Bash",
				"tool_input": "ls -la",
			},
			wantNil: false,
			wantVal: &ToolCallView{
				Name:  "Bash",
				Input: "ls -la",
			},
		},
		{
			name: "tool entry with output",
			entry: map[string]any{
				"type":        "tool_result",
				"tool_name":   "Read",
				"tool_output": "file contents",
			},
			wantNil: false,
			wantVal: &ToolCallView{
				Name:   "Read",
				Output: "file contents",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolCall(tt.entry)
			if tt.wantNil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantVal.Name, got.Name)
			if tt.wantVal.Input != "" {
				assert.Equal(t, tt.wantVal.Input, got.Input)
			}
			if tt.wantVal.Output != "" {
				assert.Equal(t, tt.wantVal.Output, got.Output)
			}
		})
	}
}

func TestRenderMarkdown_CodeBlocks(t *testing.T) {
	input := "Here is code:\n\n```go\nfunc main() {}\n```\n"
	result := string(RenderMarkdown(input))

	assert.Contains(t, result, "<pre>", "Should contain pre tag for code block")
	assert.Contains(t, result, "<code", "Should contain code tag for code block")
	assert.Contains(t, result, "func main()", "Should contain the code content")
}

func TestRenderMarkdown_HardWraps(t *testing.T) {
	input := "line one\nline two\nline three"
	result := string(RenderMarkdown(input))

	assert.Contains(t, result, "<br", "Single newlines should become <br> tags")
	assert.Contains(t, result, "line one", "Should contain first line")
	assert.Contains(t, result, "line two", "Should contain second line")
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no ANSI", "hello world", "hello world"},
		{"SGR color code", "\x1b[38;2;224;165;106m\u2726\x1b[m Use ox", "\u2726 Use ox"},
		{"bold and reset", "\x1b[1mBold\x1b[0m Normal", "Bold Normal"},
		{"multiple codes", "\x1b[32mgreen\x1b[0m and \x1b[31mred\x1b[0m", "green and red"},
		{"256-color", "\x1b[38;5;214morange\x1b[0m", "orange"},
		{"empty string", "", ""},
		{"no escape byte", "plain text [38m not ansi", "plain text [38m not ansi"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, StripANSI(tt.input))
		})
	}
}

func TestRenderMarkdown_StripsANSI(t *testing.T) {
	// ANSI codes from CLI output should not appear in rendered HTML
	input := "\x1b[38;2;224;165;106m\u2726\x1b[m Use \x1b[38;2;122;143;120mox logout\x1b[m to clear credentials"
	result := string(RenderMarkdown(input))

	assert.NotContains(t, result, "\x1b[", "ANSI escape should be stripped")
	assert.NotContains(t, result, "[38;2;", "ANSI color params should be stripped")
	assert.Contains(t, result, "\u2726", "Content should be preserved")
	assert.Contains(t, result, "ox logout", "Content should be preserved")
}

func TestCleanMessageContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "strips command-message tags",
			input: "hello <command-message>internal stuff</command-message> world",
			want:  "hello  world",
		},
		{
			name:  "extracts slash command from command-name",
			input: "User ran <command-name>/commit</command-name> to save work",
			want:  "User ran /commit to save work",
		},
		{
			name:  "strips system-reminder blocks",
			input: "start <system-reminder>\nlong internal\nmultiline\n</system-reminder> end",
			want:  "start  end",
		},
		{
			name:  "strips all tag types together",
			input: "<system-reminder>noise</system-reminder>Real content<command-message>hidden</command-message>",
			want:  "Real content",
		},
		{
			name:  "returns empty for only tags",
			input: "<system-reminder>all noise</system-reminder>",
			want:  "",
		},
		{
			name:  "strips system_instruction blocks",
			input: "before <system_instruction>internal directive</system_instruction> after",
			want:  "before  after",
		},
		{
			name:  "strips system-instruction (hyphen) blocks",
			input: "before <system-instruction>internal directive</system-instruction> after",
			want:  "before  after",
		},
		{
			name:  "strips all instruction variants together",
			input: "<system-reminder>r</system-reminder><system_instruction>i</system_instruction><system-instruction>h</system-instruction>Real",
			want:  "Real",
		},
		{
			name:  "strips local-command-stdout blocks",
			input: "before <local-command-stdout>## Context Usage\n**Model:** claude-opus</local-command-stdout> after",
			want:  "before  after",
		},
		{
			name:  "strips local-command-caveat blocks",
			input: "<local-command-caveat>Caveat: messages below were generated</local-command-caveat>Real content",
			want:  "Real content",
		},
		{
			name:  "passes through plain text",
			input: "just plain text",
			want:  "just plain text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanMessageContent(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGenerate_SkipsEmptyEntries(t *testing.T) {
	gen, _ := NewGenerator()

	tr := &session.StoredSession{
		Meta: &session.StoreMeta{},
		Entries: []map[string]any{
			{
				"type":      "user",
				"content":   "Hello",
				"timestamp": time.Now().Format(time.RFC3339),
			},
			{
				"type":      "assistant",
				"content":   "\n\n",
				"timestamp": time.Now().Format(time.RFC3339),
			},
			{
				"type":      "assistant",
				"content":   "",
				"timestamp": time.Now().Format(time.RFC3339),
			},
			{
				"type":      "assistant",
				"content":   "<system-reminder>internal noise</system-reminder>",
				"timestamp": time.Now().Format(time.RFC3339),
			},
			{
				"type":      "assistant",
				"content":   "Real response",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		},
	}

	html, err := gen.Generate(tr)
	require.NoError(t, err)

	htmlStr := string(html)
	assert.Contains(t, htmlStr, "Hello", "Should contain user message")
	assert.Contains(t, htmlStr, "Real response", "Should contain real assistant message")

	// empty/noise entries should not appear as message cards
	// count actual message card divs (class="message message-TYPE")
	messageCards := strings.Count(htmlStr, `class="message message-`)
	assert.Equal(t, 2, messageCards, "Should have exactly 2 message cards (user + real response)")
}

func TestBrandColors_AllFieldsSet(t *testing.T) {
	colors := DefaultBrandColors()

	assert.NotEmpty(t, colors.Primary, "Primary color should not be empty")
	assert.NotEmpty(t, colors.Secondary, "Secondary color should not be empty")
	assert.NotEmpty(t, colors.Accent, "Accent color should not be empty")
	assert.NotEmpty(t, colors.Text, "Text color should not be empty")
	assert.NotEmpty(t, colors.TextDim, "TextDim color should not be empty")
	assert.NotEmpty(t, colors.BgDark, "BgDark color should not be empty")
	assert.NotEmpty(t, colors.Error, "Error color should not be empty")
	assert.NotEmpty(t, colors.Info, "Info color should not be empty")
}
