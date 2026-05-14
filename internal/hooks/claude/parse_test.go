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

// TestParseSettingsRaw_LegacyStringHooks verifies that hooks files using the
// old string-value format ("PostToolUse": "command") are parsed without error.
// Failure prevented: ox doctor reports parse errors for repos that installed
// hooks before the array format was adopted.
func TestParseSettingsRaw_LegacyStringHooks(t *testing.T) {
	data := []byte(`{
		"hooks": {
			"PostToolUse": "ox agent hook PostToolUse",
			"Stop": "ox agent hook Stop"
		}
	}`)
	settings, _, err := ParseSettingsRaw(data)
	require.NoError(t, err)
	require.Len(t, settings.Hooks["PostToolUse"], 1)
	assert.Equal(t, "ox agent hook PostToolUse", settings.Hooks["PostToolUse"][0].Hooks[0].Command)
	require.Len(t, settings.Hooks["Stop"], 1)
	assert.Equal(t, "ox agent hook Stop", settings.Hooks["Stop"][0].Hooks[0].Command)
}

// TestParseSettingsRaw_MixedHookFormats verifies that files with a mix of old
// string-format and new array-format hook events parse correctly.
// Failure prevented: mixed-format settings.json (created by upgrading hook install
// while retaining manually-added string hooks) silently breaks session recording.
func TestParseSettingsRaw_MixedHookFormats(t *testing.T) {
	data := []byte(`{
		"hooks": {
			"PostToolUse": "ox agent hook PostToolUse",
			"SessionStart": [{"matcher": "", "hooks": [{"command": "ox agent hook SessionStart", "type": "command"}]}]
		}
	}`)
	settings, _, err := ParseSettingsRaw(data)
	require.NoError(t, err)

	// legacy string entry promoted to array
	require.Len(t, settings.Hooks["PostToolUse"], 1)
	assert.Equal(t, "ox agent hook PostToolUse", settings.Hooks["PostToolUse"][0].Hooks[0].Command)
	assert.Equal(t, HookType, settings.Hooks["PostToolUse"][0].Hooks[0].Type)

	// array entry unchanged
	require.Len(t, settings.Hooks["SessionStart"], 1)
	assert.Equal(t, "ox agent hook SessionStart", settings.Hooks["SessionStart"][0].Hooks[0].Command)
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

// TestMarshalSettings_DoesNotHTMLEscapeUserContent locks in the encoder
// behavior that was broken before ox-647t: the stdlib default
// json.MarshalIndent escapes <, >, & inside any string value, which
// caused a rewrite-on-every-tick loop for users whose permission rules
// contained those bytes. The fix replaces MarshalIndent with an Encoder
// that has SetEscapeHTML(false).
//
// Failure prevented: doctor / autofix mutating user content. If this
// test fails (the encoder re-escapes), the daemon would resume churning
// settings.json on every 30-min tick.
func TestMarshalSettings_DoesNotHTMLEscapeUserContent(t *testing.T) {
	rawMap := map[string]json.RawMessage{
		"permissions": json.RawMessage(`{"allow":["Bash(grep '</script>':*)","Bash(curl 'https://x?a=1&b=2':*)"]}`),
	}
	settings := &Settings{Hooks: map[string][]HookEntry{}}

	out, err := MarshalSettings(settings, rawMap)
	require.NoError(t, err)

	s := string(out)
	assert.Contains(t, s, "</script>", "encoder must not escape literal `<` and `>` (would churn user settings.json on every doctor pass)")
	assert.Contains(t, s, "a=1&b=2", "encoder must not escape literal `&` (same churn issue)")
	// build the six-character escape sequences via concatenation so the
	// source can't accidentally collapse to the literal rune (which is
	// what we want to keep, not what we want to forbid).
	bs := string([]byte{'\\'})
	assert.NotContains(t, s, bs+"u003c", "encoder must not emit the HTML escape for `<`")
	assert.NotContains(t, s, bs+"u003e", "encoder must not emit the HTML escape for `>`")
	assert.NotContains(t, s, bs+"u0026", "encoder must not emit the HTML escape for `&`")
}

// TestMarshalSettings_HasTrailingNewline confirms json.Encoder.Encode's
// always-append-newline behavior is preserved in the output. POSIX-friendly,
// matches what most editors leave on disk, and avoids "editor adds \n →
// ox strips \n" oscillation. The semantic equality guard tolerates either
// form, but the canonical emit should be the editor-friendly one.
func TestMarshalSettings_HasTrailingNewline(t *testing.T) {
	out, err := MarshalSettings(&Settings{Hooks: map[string][]HookEntry{}}, nil)
	require.NoError(t, err)
	assert.True(t, len(out) > 0 && out[len(out)-1] == '\n', "MarshalSettings output must end with a newline; got: %q", string(out))
}

// TestIsCanonicalHooksFormat_StrictArrayShape covers the predicate the doctor
// and autofix guards rely on to decide whether a file needs migration even
// when its in-memory shape is already canonical (because parseHooksMixed
// promotes legacy strings).
func TestIsCanonicalHooksFormat_StrictArrayShape(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty bytes", "", true},
		{"whitespace only", "  \n\t", true},
		{"no hooks key", `{"permissions":{"allow":["Read"]}}`, true},
		{"array shape", `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"command":"x","type":"command"}]}]}}`, true},
		{"legacy string shape", `{"hooks":{"PostToolUse":"echo legacy"}}`, false},
		{"mixed legacy + array", `{"hooks":{"PostToolUse":"x","SessionStart":[{"matcher":"","hooks":[{"command":"y","type":"command"}]}]}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsCanonicalHooksFormat([]byte(tc.in))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestSettingsSemanticallyEqual_IgnoresCosmetics verifies the no-op guard
// tolerates encoder-cosmetic differences that don't affect what Claude Code
// reads. These are exactly the cases that triggered perpetual rewrites
// before the fix.
func TestSettingsSemanticallyEqual_IgnoresCosmetics(t *testing.T) {
	canonical := []byte(`{
  "permissions": {"allow": ["Bash(curl 'a&b':*)"]},
  "hooks": {"SessionStart": [{"matcher": "", "hooks": [{"command": "ox", "type": "command"}]}]}
}
`)
	cases := []struct {
		name  string
		other []byte
		want  bool
	}{
		{
			name: "trailing newline absent",
			other: []byte(`{
  "permissions": {"allow": ["Bash(curl 'a&b':*)"]},
  "hooks": {"SessionStart": [{"matcher": "", "hooks": [{"command": "ox", "type": "command"}]}]}
}`),
			want: true,
		},
		{
			name: "html-escaped & in permissions",
			other: []byte(`{
  "permissions": {"allow": ["Bash(curl 'a&b':*)"]},
  "hooks": {"SessionStart": [{"matcher": "", "hooks": [{"command": "ox", "type": "command"}]}]}
}
`),
			want: true,
		},
		{
			name: "different top-level key order",
			other: []byte(`{
  "hooks": {"SessionStart": [{"matcher": "", "hooks": [{"command": "ox", "type": "command"}]}]},
  "permissions": {"allow": ["Bash(curl 'a&b':*)"]}
}
`),
			want: true,
		},
		{
			name:  "tab indentation inside permissions",
			other: []byte("{\n\t\"permissions\": {\n\t\t\"allow\": [\"Bash(curl 'a&b':*)\"]\n\t},\n\t\"hooks\": {\"SessionStart\": [{\"matcher\": \"\", \"hooks\": [{\"command\": \"ox\", \"type\": \"command\"}]}]}\n}\n"),
			want:  true,
		},
		{
			name: "different hook command",
			other: []byte(`{
  "permissions": {"allow": ["Bash(curl 'a&b':*)"]},
  "hooks": {"SessionStart": [{"matcher": "", "hooks": [{"command": "different", "type": "command"}]}]}
}
`),
			want: false,
		},
		{
			name: "extra permission rule",
			other: []byte(`{
  "permissions": {"allow": ["Bash(curl 'a&b':*)", "Bash(rm:*)"]},
  "hooks": {"SessionStart": [{"matcher": "", "hooks": [{"command": "ox", "type": "command"}]}]}
}
`),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SettingsSemanticallyEqual(canonical, tc.other)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got, "canonical:\n%s\nother:\n%s", canonical, tc.other)
		})
	}
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
