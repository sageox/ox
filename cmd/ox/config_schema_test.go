package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 'ox config schema --json' is the contract the SageOx desktop app renders its
// ox CLI settings page from. These tests pin that contract to the AllSettings
// registry so the schema, 'ox config set' and 'ox config --help' cannot drift.

// isolateConfigSchemaEnv gives the test an empty user config and a cwd outside
// any repo, the way the desktop app runs ox (cwd /).
func isolateConfigSchemaEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(config.EnvUserConfig, filepath.Join(home, "user-config.yaml"))
	t.Setenv(config.EnvProjectRoot, "")
	t.Setenv(config.EnvSessionRecording, "")
	t.Setenv("OX_SESSION_PUBLISHING", "")
	t.Chdir(t.TempDir())
}

// runConfigSchemaJSON drives the real 'ox config schema --json' command and
// returns its stdout.
func runConfigSchemaJSON(t *testing.T) []byte {
	t.Helper()
	oldCfg := cfg
	cfg = &config.Config{JSON: true}
	t.Cleanup(func() { cfg = oldCfg })

	var out bytes.Buffer
	configSchemaCmd.SetOut(&out)
	t.Cleanup(func() { configSchemaCmd.SetOut(nil) })
	require.NoError(t, runConfigSchema(configSchemaCmd, nil))
	return out.Bytes()
}

// TestConfigSchema_CoversEveryRegisteredKey — failure prevented: a setting
// added to AllSettings never appears in the desktop app.
func TestConfigSchema_CoversEveryRegisteredKey(t *testing.T) {
	isolateConfigSchemaEnv(t)
	schema := buildConfigSchema("")

	require.Len(t, schema.Settings, len(AllSettings))
	for i, s := range AllSettings {
		got := schema.Settings[i]
		assert.Equal(t, s.Key, got.Key, "schema keeps registry order")
		assert.NotEmpty(t, got.Label, "%s: label", s.Key)
		assert.NotEmpty(t, got.Description, "%s: description", s.Key)
		assert.Equal(t, s.Levels, got.Scopes, "%s: scopes", s.Key)
		for _, l := range got.Scopes {
			assert.Contains(t, []ConfigLevel{ConfigLevelUser, ConfigLevelRepo, ConfigLevelTeam}, l,
				"%s: scope must be a settable level", s.Key)
		}
	}
}

// TestConfigSchema_ValuesMatchWhatSetAccepts — failure prevented: the desktop
// app offers a value 'ox config set' rejects, or hides one it accepts, or
// offers a scope the key cannot be set at.
func TestConfigSchema_ValuesMatchWhatSetAccepts(t *testing.T) {
	isolateConfigSchemaEnv(t)

	for _, s := range buildConfigSchema("").Settings {
		t.Run(s.Key, func(t *testing.T) {
			require.Contains(t, s.Scopes, ConfigLevelUser, "desktop saves at user level")

			switch s.Type {
			case configSchemaTypeEnum:
				require.NotEmpty(t, s.Values)
				for _, v := range s.Values {
					assert.NoError(t, SetConfigValue(s.Key, v, ConfigLevelUser, ""), "listed value %q", v)
				}
				err := SetConfigValue(s.Key, "not-a-listed-value", ConfigLevelUser, "")
				assert.ErrorContains(t, err, "invalid value", "unlisted value must be rejected")
			case configSchemaTypeText:
				assert.Nil(t, s.Values, "text settings have null values")
			default:
				t.Fatalf("unknown type %q", s.Type)
			}

			for _, l := range []ConfigLevel{ConfigLevelRepo, ConfigLevelTeam} {
				if slices.Contains(s.Scopes, l) {
					continue
				}
				// any value that passes validation, so the level check is what fails
				v := s.Default
				err := SetConfigValue(s.Key, v, l, "")
				assert.ErrorContains(t, err, "cannot be set at", "%s is not a listed scope", l)
			}
		})
	}
}

// TestConfigSchema_FlagsDeprecatedKeys — failure prevented: the desktop app
// presents a setting that never had any effect as a working control.
func TestConfigSchema_FlagsDeprecatedKeys(t *testing.T) {
	isolateConfigSchemaEnv(t)

	var deprecated []string
	for _, s := range buildConfigSchema("").Settings {
		assert.Equal(t, GetSetting(s.Key).Deprecated, s.Deprecated, s.Key)
		if s.Deprecated {
			deprecated = append(deprecated, s.Key)
		}
	}
	assert.ElementsMatch(t, []string{"context_git.auto_commit", "context_git.auto_push"}, deprecated)
}

// TestConfigSchema_WorksOutsideARepo — failure prevented: the desktop app,
// which runs ox from /, gets an error or repo-scoped values instead of the
// coworker's own user-level settings.
func TestConfigSchema_WorksOutsideARepo(t *testing.T) {
	isolateConfigSchemaEnv(t)
	require.NoError(t, SetConfigValue("session_recording", "manual", ConfigLevelUser, ""))

	var schema configSchema
	require.NoError(t, json.Unmarshal(runConfigSchemaJSON(t), &schema))

	require.Len(t, schema.Settings, len(AllSettings))
	for _, s := range schema.Settings {
		if s.Key == "session_recording" {
			assert.Equal(t, "manual", s.Value)
			assert.Equal(t, ConfigLevelUser, s.Source)
			assert.Equal(t, "auto", s.Default)
			continue
		}
		assert.Contains(t, []ConfigLevel{ConfigLevelUser, ConfigLevelDefault}, s.Source,
			"%s: outside a repo only user and default levels apply", s.Key)
	}
}

// TestConfigSchema_JSONShape pins the exact field names and JSON types the
// desktop app parses. Failure prevented: a renamed or retyped field silently
// breaks the settings page.
func TestConfigSchema_JSONShape(t *testing.T) {
	isolateConfigSchemaEnv(t)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(runConfigSchemaJSON(t), &doc))

	assert.ElementsMatch(t, []string{"schema_version", "settings"}, jsonObjectKeys(doc))
	assert.Equal(t, float64(1), doc["schema_version"])

	settings, ok := doc["settings"].([]any)
	require.True(t, ok, "settings is an array")
	require.NotEmpty(t, settings)

	wantKeys := []string{"key", "label", "description", "type", "values", "scopes",
		"deprecated", "value", "source", "default"}
	for _, raw := range settings {
		s, ok := raw.(map[string]any)
		require.True(t, ok)
		key, _ := s["key"].(string)
		assert.ElementsMatch(t, wantKeys, jsonObjectKeys(s), key)

		for _, f := range []string{"key", "label", "description", "type", "value", "source", "default"} {
			assert.IsType(t, "", s[f], "%s.%s is a string", key, f)
		}
		assert.IsType(t, false, s["deprecated"], "%s.deprecated", key)
		assert.IsType(t, []any{}, s["scopes"], "%s.scopes", key)
		switch s["type"] {
		case "enum":
			assert.IsType(t, []any{}, s["values"], "%s.values is an array", key)
		case "text":
			assert.Nil(t, s["values"], "%s.values is null", key)
		default:
			t.Errorf("%s: type %v is neither enum nor text", key, s["type"])
		}
	}

	// attribution.commit is the documented text example.
	for _, raw := range settings {
		if s := raw.(map[string]any); s["key"] == "attribution.commit" {
			assert.Equal(t, "text", s["type"])
		}
	}
}

// TestConfigHelp_ListsExactlyTheRegisteredKeys — failure prevented: 'ox config
// --help' advertises a key 'ox config set' rejects (it once listed "murmuring"
// after the key became murmur_send / murmur_receive) or omits a real one.
func TestConfigHelp_ListsExactlyTheRegisteredKeys(t *testing.T) {
	_, block, ok := strings.Cut(configCmd.Long, "Available settings:\n")
	require.True(t, ok, "help has an Available settings block")
	block, _, _ = strings.Cut(block, "\n\n")

	keyRe := regexp.MustCompile(`^  (\S+)\s`)
	var listed []string
	for _, line := range strings.Split(block, "\n") {
		if m := keyRe.FindStringSubmatch(line); m != nil {
			listed = append(listed, m[1])
		}
	}

	var registered []string
	for _, s := range AllSettings {
		registered = append(registered, s.Key)
	}
	assert.Equal(t, registered, listed)
}

func jsonObjectKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
