package claude

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSettingsRaw_EmptyData(t *testing.T) {
	settings, rawMap, err := ParseSettingsRaw([]byte{})
	require.NoError(t, err)
	assert.NotNil(t, settings.Hooks)
	assert.Empty(t, settings.Hooks)
	assert.NotNil(t, rawMap)
	assert.Empty(t, rawMap)
}

func TestParseSettingsRaw_NilData(t *testing.T) {
	settings, rawMap, err := ParseSettingsRaw(nil)
	require.NoError(t, err)
	assert.NotNil(t, settings.Hooks)
	assert.Empty(t, rawMap)
}

func TestParseSettingsRaw_InvalidJSON(t *testing.T) {
	_, _, err := ParseSettingsRaw([]byte("{not json}"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse settings")
}

func TestParseSettingsRaw_NoHooksKey(t *testing.T) {
	data := []byte(`{"permissions": {"allow": ["Read"]}}`)
	settings, rawMap, err := ParseSettingsRaw(data)
	require.NoError(t, err)
	assert.NotNil(t, settings.Hooks)
	assert.Empty(t, settings.Hooks)
	assert.Contains(t, rawMap, "permissions")
}

func TestParseSettingsRaw_WithHooks(t *testing.T) {
	data := []byte(`{
		"permissions": {"allow": ["Read"]},
		"hooks": {
			"SessionStart": [{"matcher": "", "hooks": [{"command": "echo hi", "type": "command"}]}]
		},
		"custom_key": 42
	}`)
	settings, rawMap, err := ParseSettingsRaw(data)
	require.NoError(t, err)

	assert.Len(t, settings.Hooks["SessionStart"], 1)
	assert.Equal(t, "echo hi", settings.Hooks["SessionStart"][0].Hooks[0].Command)
	assert.Contains(t, rawMap, "permissions")
	assert.Contains(t, rawMap, "custom_key")
}

func TestParseSettingsRaw_InvalidHooksJSON(t *testing.T) {
	data := []byte(`{"hooks": "not-a-map"}`)
	_, _, err := ParseSettingsRaw(data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse hooks")
}

func TestMarshalSettings_EmptyHooksRemovesKey(t *testing.T) {
	settings := &Settings{Hooks: map[string][]HookEntry{}}
	rawMap := map[string]json.RawMessage{
		"permissions": json.RawMessage(`{"allow":["Read"]}`),
		"hooks":       json.RawMessage(`{}`),
	}

	data, err := MarshalSettings(settings, rawMap)
	require.NoError(t, err)
	assert.NotContains(t, string(data), `"hooks"`)
	assert.Contains(t, string(data), `"permissions"`)
}

func TestMarshalSettings_WithHooks(t *testing.T) {
	settings := &Settings{
		Hooks: map[string][]HookEntry{
			"SessionStart": {{
				Matcher: "",
				Hooks:   []Hook{{Command: "test", Type: "command"}},
			}},
		},
	}

	data, err := MarshalSettings(settings, nil)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"hooks"`)
	assert.Contains(t, string(data), "test")
}

func TestMarshalSettings_NilRawMap(t *testing.T) {
	settings := &Settings{
		Hooks: map[string][]HookEntry{
			"SessionStart": {{Hooks: []Hook{{Command: "cmd", Type: "command"}}}},
		},
	}

	data, err := MarshalSettings(settings, nil)
	require.NoError(t, err)
	assert.Contains(t, string(data), "cmd")
}

func TestParseAndMarshalRoundtrip(t *testing.T) {
	original := []byte(`{
  "permissions": {"allow": ["Read", "Write"]},
  "hooks": {
    "SessionStart": [
      {"matcher": "", "hooks": [{"command": "echo start", "type": "command"}]},
      {"matcher": "test", "hooks": [{"command": "echo test", "type": "command"}]}
    ]
  },
  "other": "value"
}`)

	settings, rawMap, err := ParseSettingsRaw(original)
	require.NoError(t, err)

	data, err := MarshalSettings(settings, rawMap)
	require.NoError(t, err)

	// re-parse and verify structural equivalence
	settings2, rawMap2, err := ParseSettingsRaw(data)
	require.NoError(t, err)
	assert.Equal(t, len(settings.Hooks), len(settings2.Hooks))
	assert.Equal(t, len(rawMap), len(rawMap2))
	assert.Contains(t, rawMap2, "other")
}
