package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

// The export's missing fields must not be replaced with guesses from its text.
func TestCursorParserPreservesExportedBlockOrderAndOmissions(t *testing.T) {
	line := `{"role":"assistant","message":{"content":[{"type":"text","text":"before"},{"type":"tool_use","name":"Shell","input":{"n":9007199254740993}},{"type":"text","text":"after"},{"type":"tool_use","name":"Shell","input":{"n":9007199254740993}}]}}`
	entries, err := parseCursorLine([]byte(line))
	require.NoError(t, err)
	require.Equal(t, []adapterprotocol.RawEntry{
		{Role: "assistant", Content: "before"},
		{Role: "tool", ToolName: "Shell", ToolInput: `{"n":9007199254740993}`},
		{Role: "assistant", Content: "after"},
		{Role: "tool", ToolName: "Shell", ToolInput: `{"n":9007199254740993}`},
	}, entries)

	text := "<timestamp>Monday, Sep 14, 2026, 11:35 AM (UTC-7)</timestamp>\n<user_query>exit 7 failed</user_query>"
	encoded, err := json.Marshal(text)
	require.NoError(t, err)
	entries, err = parseCursorLine([]byte(`{"role":"user","message":{"content":[{"type":"text","text":` + string(encoded) + `}]}}`))
	require.NoError(t, err)
	require.Equal(t, []adapterprotocol.RawEntry{{Role: "user", Content: text}}, entries)
}

// A single unsupported block must not quietly discard part of a conversation.
func TestCursorParserRejectsUnsupportedCompleteRecords(t *testing.T) {
	cases := map[string]string{
		"malformed":          `{"role":`,
		"null":               `null`,
		"array":              `[]`,
		"unknown row":        `{"type":"future"}`,
		"unknown role":       `{"role":"system","message":{"content":[]}}`,
		"missing message":    `{"role":"user"}`,
		"null content":       `{"role":"user","message":{"content":null}}`,
		"string content":     `{"role":"user","message":{"content":"text"}}`,
		"missing text":       `{"role":"user","message":{"content":[{"type":"text"}]}}`,
		"null text":          `{"role":"user","message":{"content":[{"type":"text","text":null}]}}`,
		"numeric text":       `{"role":"user","message":{"content":[{"type":"text","text":7}]}}`,
		"missing tool input": `{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Shell"}]}}`,
		"empty tool name":    `{"role":"assistant","message":{"content":[{"type":"tool_use","name":" ","input":{}}]}}`,
		"user tool":          `{"role":"user","message":{"content":[{"type":"tool_use","name":"Shell","input":{}}]}}`,
		"unknown block":      `{"role":"assistant","message":{"content":[{"type":"future","secret":"do-not-echo"}]}}`,
		"mixed unknown":      `{"role":"assistant","message":{"content":[{"type":"text","text":"valid"},{"type":"tool_result","content":"do-not-echo"}]}}`,
		"missing status":     `{"type":"turn_ended"}`,
		"conflicting role":   `{"type":"turn_ended","status":"success","role":"user"}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			entries, err := parseCursorLine([]byte(line))
			require.ErrorIs(t, err, errCursorSourceFormat)
			require.Nil(t, entries)
			require.NotContains(t, err.Error(), "do-not-echo")
		})
	}
}

func TestCursorParserRecognizesEmptyContentAndTurnMetadata(t *testing.T) {
	for _, line := range []string{
		`{"type":"turn_ended","status":"success"}`,
		`{"type":"turn_ended","status":"aborted","extra":true}`,
		`{"role":"user","message":{"content":[]}}`,
	} {
		entries, err := parseCursorLine([]byte(line))
		require.NoError(t, err)
		require.Empty(t, entries)
	}
	entries, err := parseCursorLine([]byte(`{"role":"assistant","future_key":true,"message":{"content":[{"type":"text","text":""}]}}`))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Empty(t, entries[0].Content)
	entries, err = parseCursorLine([]byte(`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Shell","input":null}]}}`))
	require.NoError(t, err)
	require.Equal(t, "null", strings.TrimSpace(entries[0].ToolInput))
}
