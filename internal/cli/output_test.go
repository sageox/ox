package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFormatKBSlug pins the canonical "#<slug>" human-display formatter
// used across kb list, kb show, and kb hydrate. Failure prevented: drift
// between command surfaces showing a slug without the prefix, or double-
// prefixing when a caller passes "#foo" by mistake.
func TestFormatKBSlug(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"bare", "marketing", "#marketing"},
		{"already-prefixed", "#marketing", "#marketing"},
		{"hyphenated", "ryan-personal", "#ryan-personal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatKBSlug(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFormatTipText_HighlightsBackticks(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{
			name:     "single command",
			input:    "Run `ox doctor` to check your setup",
			contains: "ox doctor",
		},
		{
			name:     "multiple commands",
			input:    "Use `ox login` or `ox status` to get started",
			contains: "ox login",
		},
		{
			name:     "no backticks",
			input:    "This is plain text",
			contains: "This is plain text",
		},
		{
			name:     "empty string",
			input:    "",
			contains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatTipText(tt.input)
			assert.Contains(t, result, tt.contains, "FormatTipText() result does not contain expected text")
		})
	}
}

func TestFormatTipText_RemovesBackticks(t *testing.T) {
	tests := []struct {
		name             string
		input            string
		shouldNotContain string
	}{
		{
			name:             "removes backticks from single command",
			input:            "Run `ox doctor` to check your setup",
			shouldNotContain: "`ox doctor`",
		},
		{
			name:             "removes backticks from multiple commands",
			input:            "Use `ox login` or `ox status` to get started",
			shouldNotContain: "`ox login`",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatTipText(tt.input)
			assert.NotContains(t, result, tt.shouldNotContain, "FormatTipText() should remove backticks")
		})
	}
}

func TestPrintTip_OutputsToStderr(t *testing.T) {
	// save original stderr
	oldStderr := os.Stderr
	defer func() { os.Stderr = oldStderr }()

	// create pipe to capture stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	// reset json mode
	SetJSONMode(false)

	// print tip
	PrintTip("Run `ox doctor` to check your setup")

	// close writer and read captured output
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// verify output contains tip marker and text
	assert.True(t, strings.Contains(output, "✦"), "PrintTip() output does not contain tip icon")
	assert.Contains(t, output, "ox doctor", "PrintTip() output does not contain expected text")
}

func TestSuggestionBox_OmitsRunLineWhenFixEmpty(t *testing.T) {
	SetJSONMode(false)

	result := SuggestionBox("Title", "Message body", "")
	assert.NotContains(t, result, "Run:", "SuggestionBox should omit 'Run:' when fix is empty")
	assert.Contains(t, result, "Title", "SuggestionBox should contain the title")
	assert.Contains(t, result, "Message body", "SuggestionBox should contain the message")
}

func TestSuggestionBox_IncludesRunLineWhenFixPresent(t *testing.T) {
	SetJSONMode(false)

	result := SuggestionBox("Title", "Message body", "ox doctor")
	assert.Contains(t, result, "Run:", "SuggestionBox should include 'Run:' when fix is provided")
	assert.Contains(t, result, "ox doctor", "SuggestionBox should include the fix command")
}

func TestSuggestionBox_ReturnsEmptyInJSONMode(t *testing.T) {
	SetJSONMode(true)
	defer SetJSONMode(false)

	result := SuggestionBox("Title", "Message", "ox fix")
	assert.Empty(t, result, "SuggestionBox should return empty in JSON mode")
}

func TestPrintTip_RespectsJSONMode(t *testing.T) {
	// save original stderr
	oldStderr := os.Stderr
	defer func() { os.Stderr = oldStderr }()

	// create pipe to capture stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	// enable json mode
	SetJSONMode(true)
	defer SetJSONMode(false)

	// print tip
	PrintTip("Run `ox doctor` to check your setup")

	// close writer and read captured output
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// verify no output in JSON mode
	assert.Empty(t, output, "PrintTip() should not output in JSON mode")
}
