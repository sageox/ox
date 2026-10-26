package claude

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/constants"
)

func currentStatusLineJSON(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type": "command", "command": constants.OxStatuslineCommandClaudeCode,
		"refreshInterval": constants.OxStatuslineRefreshSeconds,
	})
	require.NoError(t, err)
	return b
}

// TestClassifyStatusLine names every state the installer and doctor act on.
// Failure prevented: a foreign statusLine read as ox's (and replaced), or a
// stale ox one read as current (and never upgraded).
func TestClassifyStatusLine(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]json.RawMessage
		want StatusLineState
	}{
		{"absent", map[string]json.RawMessage{}, StatusLineMissing},
		{"null", map[string]json.RawMessage{StatusLineKey: json.RawMessage(`null`)}, StatusLineMissing},
		{"current", map[string]json.RawMessage{StatusLineKey: currentStatusLineJSON(t)}, StatusLineCurrent},
		{"old ox command", map[string]json.RawMessage{StatusLineKey: json.RawMessage(`{"type":"command","command":"ox statusline"}`)}, StatusLineStale},
		{"ox with other refresh", map[string]json.RawMessage{StatusLineKey: json.RawMessage(`{"type":"command","command":"` + jsonEscape(constants.OxStatuslineCommandClaudeCode) + `","refreshInterval":5}`)}, StatusLineStale},
		{"foreign command", map[string]json.RawMessage{StatusLineKey: json.RawMessage(`{"type":"command","command":"starship"}`)}, StatusLineForeign},
		{"not an object", map[string]json.RawMessage{StatusLineKey: json.RawMessage(`"weird"`)}, StatusLineForeign},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ClassifyStatusLine(tt.raw))
		})
	}
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// TestEnsureOxStatusLine installs, upgrades, and leaves foreign values alone.
func TestEnsureOxStatusLine(t *testing.T) {
	m := map[string]json.RawMessage{}
	changed, err := EnsureOxStatusLine(m)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, StatusLineCurrent, ClassifyStatusLine(m))

	changed, err = EnsureOxStatusLine(m)
	require.NoError(t, err)
	assert.False(t, changed, "idempotent")

	foreign := json.RawMessage(`{"type":"command","command":"starship"}`)
	m = map[string]json.RawMessage{StatusLineKey: foreign}
	changed, err = EnsureOxStatusLine(m)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.JSONEq(t, string(foreign), string(m[StatusLineKey]))

	assert.True(t, RemoveOxStatusLine(map[string]json.RawMessage{StatusLineKey: currentStatusLineJSON(t)}))
	assert.False(t, RemoveOxStatusLine(map[string]json.RawMessage{StatusLineKey: foreign}))
}

func TestIsOxStatuslineCommand(t *testing.T) {
	assert.True(t, IsOxStatuslineCommand(constants.OxStatuslineCommandClaudeCode))
	assert.True(t, IsOxStatuslineCommand("ox statusline"))
	assert.True(t, IsOxStatuslineCommand("/usr/local/bin/ox statusline --claude"))
	assert.True(t, IsOxStatuslineCommand("cd /repo && ox statusline"))
	assert.False(t, IsOxStatuslineCommand("starship"))
	assert.False(t, IsOxStatuslineCommand("fox statusline"), "a foreign binary ending in ox is not ox")
	assert.False(t, IsOxStatuslineCommand("~/bin/box statusline"))
	assert.False(t, IsOxStatuslineCommand("ox statuslinefoo"))
	assert.False(t, IsAnyOxCommand(constants.OxStatuslineCommandClaudeCode), "a status line is not a hook command")
}

// TestStatusLineState_String: doctor and installer logs name the state; an
// unnamed state would print a bare number.
func TestStatusLineState_String(t *testing.T) {
	tests := []struct {
		state StatusLineState
		want  string
	}{
		{StatusLineMissing, "missing"},
		{StatusLineCurrent, "current"},
		{StatusLineStale, "stale"},
		{StatusLineForeign, "foreign"},
		{StatusLineState(99), "foreign"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.state.String())
	}
}

// TestEnsureOxStatusLine_UpgradesStaleKeepingForeignFields: an older ox
// statusLine is brought current without dropping fields the coworker set.
// Failure prevented: an upgrade silently resets a coworker's padding.
func TestEnsureOxStatusLine_UpgradesStaleKeepingForeignFields(t *testing.T) {
	m := map[string]json.RawMessage{
		StatusLineKey: json.RawMessage(`{"type":"command","command":"ox statusline","padding":2}`),
	}
	changed, err := EnsureOxStatusLine(m)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, StatusLineCurrent, ClassifyStatusLine(m))

	var got map[string]any
	require.NoError(t, json.Unmarshal(m[StatusLineKey], &got))
	assert.Equal(t, float64(2), got["padding"], "foreign field survives the upgrade")
	assert.Equal(t, constants.OxStatuslineCommandClaudeCode, got["command"])
	assert.Equal(t, float64(constants.OxStatuslineRefreshSeconds), got["refreshInterval"])
}
