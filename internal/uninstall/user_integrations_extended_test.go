package uninstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindUserGitHooks(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, homeDir string)
		wantCount int
		wantAgent string
	}{
		{
			name:      "no hooks directory",
			setup:     func(t *testing.T, homeDir string) {},
			wantCount: 0,
		},
		{
			name: "hooks directory with ox prime hook",
			setup: func(t *testing.T, homeDir string) {
				// clear XDG so getUserGitHooksDir falls back to homeDir/.config
				t.Setenv("XDG_CONFIG_HOME", "")
				hooksDir := filepath.Join(homeDir, ".config", "git", "hooks")
				require.NoError(t, os.MkdirAll(hooksDir, 0755))
				hookContent := "#!/bin/sh\nox agent prime\n"
				require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "post-checkout"), []byte(hookContent), 0755))
			},
			wantCount: 1,
			wantAgent: "git",
		},
		{
			name: "hooks directory without ox prime",
			setup: func(t *testing.T, homeDir string) {
				t.Setenv("XDG_CONFIG_HOME", "")
				hooksDir := filepath.Join(homeDir, ".config", "git", "hooks")
				require.NoError(t, os.MkdirAll(hooksDir, 0755))
				hookContent := "#!/bin/sh\necho hello\n"
				require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "post-checkout"), []byte(hookContent), 0755))
			},
			wantCount: 0,
		},
		{
			name: "skips subdirectories",
			setup: func(t *testing.T, homeDir string) {
				t.Setenv("XDG_CONFIG_HOME", "")
				hooksDir := filepath.Join(homeDir, ".config", "git", "hooks")
				require.NoError(t, os.MkdirAll(filepath.Join(hooksDir, "subdir"), 0755))
			},
			wantCount: 0,
		},
		{
			name: "multiple hooks only some with ox prime",
			setup: func(t *testing.T, homeDir string) {
				t.Setenv("XDG_CONFIG_HOME", "")
				hooksDir := filepath.Join(homeDir, ".config", "git", "hooks")
				require.NoError(t, os.MkdirAll(hooksDir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "post-checkout"), []byte("#!/bin/sh\nox agent prime\n"), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte("#!/bin/sh\necho push\n"), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\nox prime --check\n"), 0755))
			},
			wantCount: 2,
		},
		{
			name: "uses XDG_CONFIG_HOME when set",
			setup: func(t *testing.T, homeDir string) {
				xdgDir := filepath.Join(homeDir, "custom-xdg")
				t.Setenv("XDG_CONFIG_HOME", xdgDir)
				hooksDir := filepath.Join(xdgDir, "git", "hooks")
				require.NoError(t, os.MkdirAll(hooksDir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "post-checkout"), []byte("#!/bin/sh\nox agent prime\n"), 0755))
			},
			wantCount: 1,
			wantAgent: "git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			finder := &UserIntegrationsFinder{homeDir: tmpDir}
			tt.setup(t, tmpDir)

			items, err := finder.findUserGitHooks()
			assert.NoError(t, err)
			if !assert.Len(t, items, tt.wantCount) {
				return
			}

			if tt.wantCount > 0 && tt.wantAgent != "" {
				assert.Equal(t, tt.wantAgent, items[0].Agent)
				assert.Equal(t, "hook", items[0].Type)
			}
		})
	}
}

func TestGetUserGitHooksDir(t *testing.T) {
	tests := []struct {
		name       string
		xdgConfig  string
		wantSuffix string
	}{
		{
			name:       "default path when XDG_CONFIG_HOME unset",
			xdgConfig:  "",
			wantSuffix: filepath.Join(".config", "git", "hooks"),
		},
		{
			name:       "custom XDG_CONFIG_HOME",
			xdgConfig:  "/custom/config",
			wantSuffix: filepath.Join("custom", "config", "git", "hooks"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			finder := &UserIntegrationsFinder{homeDir: tmpDir}

			if tt.xdgConfig != "" {
				t.Setenv("XDG_CONFIG_HOME", tt.xdgConfig)
			} else {
				t.Setenv("XDG_CONFIG_HOME", "")
			}

			result := finder.getUserGitHooksDir()
			assert.Contains(t, result, tt.wantSuffix)
		})
	}
}

func TestRemoveClaudeHooks(t *testing.T) {
	tests := []struct {
		name       string
		input      map[string]interface{}
		wantEvents []string // events that should remain after removal
	}{
		{
			name: "removes ox prime from SessionStart",
			input: map[string]interface{}{
				"hooks": map[string]interface{}{
					"SessionStart": []map[string]interface{}{
						{
							"hooks": []map[string]interface{}{
								{"command": "ox agent prime", "type": "command"},
							},
							"matcher": "",
						},
					},
				},
			},
			wantEvents: nil, // SessionStart should be deleted entirely
		},
		{
			name: "preserves non-ox hooks in same event",
			input: map[string]interface{}{
				"hooks": map[string]interface{}{
					"SessionStart": []map[string]interface{}{
						{
							"hooks": []map[string]interface{}{
								{"command": "ox agent prime", "type": "command"},
								{"command": "echo hello", "type": "command"},
							},
							"matcher": "",
						},
					},
				},
			},
			wantEvents: []string{"SessionStart"},
		},
		{
			name: "no hooks field",
			input: map[string]interface{}{
				"hooks": nil,
			},
			wantEvents: nil,
		},
		{
			name: "removes from both SessionStart and PreCompact",
			input: map[string]interface{}{
				"hooks": map[string]interface{}{
					"SessionStart": []map[string]interface{}{
						{
							"hooks": []map[string]interface{}{
								{"command": "ox agent prime", "type": "command"},
							},
						},
					},
					"PreCompact": []map[string]interface{}{
						{
							"hooks": []map[string]interface{}{
								{"command": "ox agent prime --user", "type": "command"},
							},
						},
					},
				},
			},
			wantEvents: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := filepath.Join(t.TempDir(), "settings.json")
			data, err := json.MarshalIndent(tt.input, "", "  ")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(tmpFile, data, 0600))

			err = removeClaudeHooks(tmpFile, data)
			require.NoError(t, err)

			// read back and verify
			result, err := os.ReadFile(tmpFile)
			require.NoError(t, err)

			var parsed map[string]interface{}
			require.NoError(t, json.Unmarshal(result, &parsed))

			hooks, _ := parsed["hooks"].(map[string]interface{})

			if tt.wantEvents == nil {
				// either no hooks key or empty hooks map
				if hooks != nil {
					assert.Empty(t, hooks, "expected no hook events remaining")
				}
			} else {
				for _, event := range tt.wantEvents {
					assert.Contains(t, hooks, event, "expected event %s to remain", event)
				}
			}
		})
	}
}

func TestRemoveGeminiHooks(t *testing.T) {
	tests := []struct {
		name           string
		input          map[string]interface{}
		wantHookRemain bool
	}{
		{
			name: "removes ox prime SessionStart",
			input: map[string]interface{}{
				"hooks": map[string]interface{}{
					"SessionStart": map[string]interface{}{
						"command": "ox agent prime",
						"timeout": 30000,
					},
				},
			},
			wantHookRemain: false,
		},
		{
			name: "preserves non-ox SessionStart",
			input: map[string]interface{}{
				"hooks": map[string]interface{}{
					"SessionStart": map[string]interface{}{
						"command": "echo hello",
					},
				},
			},
			wantHookRemain: true,
		},
		{
			name: "no hooks field",
			input: map[string]interface{}{
				"hooks": nil,
			},
			wantHookRemain: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := filepath.Join(t.TempDir(), "settings.json")
			data, err := json.MarshalIndent(tt.input, "", "  ")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(tmpFile, data, 0600))

			err = removeGeminiHooks(tmpFile, data)
			require.NoError(t, err)

			result, err := os.ReadFile(tmpFile)
			require.NoError(t, err)

			var parsed map[string]interface{}
			require.NoError(t, json.Unmarshal(result, &parsed))

			hooks, _ := parsed["hooks"].(map[string]interface{})
			if tt.wantHookRemain {
				assert.Contains(t, hooks, "SessionStart")
			} else {
				if hooks != nil {
					assert.NotContains(t, hooks, "SessionStart")
				}
			}
		})
	}
}

func TestRemoveHooksFromFile(t *testing.T) {
	tests := []struct {
		name    string
		item    UserIntegrationItem
		content string
		wantErr bool
	}{
		{
			name: "claude agent",
			item: UserIntegrationItem{
				Type:  "hook",
				Agent: "claude",
			},
			content: `{"hooks":{"SessionStart":[{"hooks":[{"command":"ox agent prime","type":"command"}]}]}}`,
		},
		{
			name: "gemini agent",
			item: UserIntegrationItem{
				Type:  "hook",
				Agent: "gemini",
			},
			content: `{"hooks":{"SessionStart":{"command":"ox agent prime"}}}`,
		},
		{
			name: "unsupported agent",
			item: UserIntegrationItem{
				Type:  "hook",
				Agent: "unknown",
			},
			content: `{}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := filepath.Join(t.TempDir(), "settings.json")
			require.NoError(t, os.WriteFile(tmpFile, []byte(tt.content), 0600))
			tt.item.Path = tmpFile

			err := removeHooksFromFile(tt.item)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRemoveUserIntegrations_ClaudeHookRemoval(t *testing.T) {
	tmpDir := t.TempDir()

	// create a claude settings file with ox prime hooks
	settingsPath := filepath.Join(tmpDir, "settings.json")
	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []map[string]interface{}{
				{
					"hooks": []map[string]interface{}{
						{"command": "ox agent prime", "type": "command"},
					},
				},
			},
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(settingsPath, data, 0600))

	items := []UserIntegrationItem{
		{
			Path:  settingsPath,
			Type:  "hook",
			Agent: "claude",
		},
	}

	err = RemoveUserIntegrations(items, false)
	require.NoError(t, err)

	// file should still exist (hooks edited in-place, not deleted)
	_, err = os.Stat(settingsPath)
	assert.False(t, os.IsNotExist(err), "settings file should still exist")

	// verify hooks were removed
	result, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(result, &parsed))
	hooks, _ := parsed["hooks"].(map[string]interface{})
	if hooks != nil {
		assert.NotContains(t, hooks, "SessionStart")
	}
}

func TestRemoveUserIntegrations_GeminiHookRemoval(t *testing.T) {
	tmpDir := t.TempDir()

	settingsPath := filepath.Join(tmpDir, "settings.json")
	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": map[string]interface{}{
				"command": "ox agent prime",
			},
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(settingsPath, data, 0600))

	items := []UserIntegrationItem{
		{
			Path:  settingsPath,
			Type:  "hook",
			Agent: "gemini",
		},
	}

	err = RemoveUserIntegrations(items, false)
	require.NoError(t, err)

	// verify hook removed
	result, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(result, &parsed))
	hooks, _ := parsed["hooks"].(map[string]interface{})
	if hooks != nil {
		assert.NotContains(t, hooks, "SessionStart")
	}
}

func TestRemoveUserIntegrations_AlreadyRemoved(t *testing.T) {
	// item points to nonexistent path - should skip gracefully
	items := []UserIntegrationItem{
		{
			Path:  filepath.Join(t.TempDir(), "nonexistent"),
			Type:  "plugin",
			Agent: "test",
		},
	}

	err := RemoveUserIntegrations(items, false)
	assert.NoError(t, err, "should handle already-removed items gracefully")
}

func TestRemoveUserIntegrations_EmptyList(t *testing.T) {
	err := RemoveUserIntegrations(nil, false)
	assert.NoError(t, err)

	err = RemoveUserIntegrations([]UserIntegrationItem{}, false)
	assert.NoError(t, err)
}

func TestGetPlatformInfo(t *testing.T) {
	result := GetPlatformInfo()

	// verify it returns a non-empty string for the current platform
	assert.NotEmpty(t, result)

	switch runtime.GOOS {
	case "darwin":
		assert.Equal(t, "macOS", result)
	case "linux":
		assert.Equal(t, "Linux", result)
	case "windows":
		assert.Equal(t, "Windows", result)
	default:
		assert.Equal(t, runtime.GOOS, result)
	}
}

func TestShouldRemoveUserConfig(t *testing.T) {
	// this function checks if the user config directory exists;
	// we can at least verify it returns a boolean without panicking
	result := ShouldRemoveUserConfig()
	// on a dev machine the config dir likely exists, but we just test it doesn't panic
	assert.IsType(t, true, result)
}

func TestNewUserIntegrationsFinder(t *testing.T) {
	finder, err := NewUserIntegrationsFinder()
	require.NoError(t, err)
	assert.NotNil(t, finder)
	assert.NotEmpty(t, finder.homeDir)
}

func TestFindAll(t *testing.T) {
	tmpDir := t.TempDir()
	finder := &UserIntegrationsFinder{homeDir: tmpDir}

	// empty home dir - should find nothing but not error
	items, err := finder.FindAll()
	assert.NoError(t, err)
	assert.Empty(t, items)
}

func TestFindAll_WithMultipleIntegrations(t *testing.T) {
	tmpDir := t.TempDir()
	finder := &UserIntegrationsFinder{homeDir: tmpDir}

	// set up opencode plugin
	pluginDir := filepath.Join(tmpDir, ".config", "opencode", "plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "ox-prime.ts"), []byte("plugin"), 0644))

	// set up CLAUDE.md with ox prime
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("Run ox agent prime"), 0644))

	items, err := finder.FindAll()
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(items), 2, "should find opencode plugin and CLAUDE.md")

	// verify agents found
	agents := make(map[string]bool)
	for _, item := range items {
		agents[item.Agent] = true
	}
	assert.True(t, agents["opencode"])
	assert.True(t, agents["claude"])
}

func TestFindClaudeHooks_EmptySettingsFile(t *testing.T) {
	tmpDir := t.TempDir()
	finder := &UserIntegrationsFinder{homeDir: tmpDir}

	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// empty settings file
	settingsPath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte(""), 0600))

	items, err := finder.findClaudeHooks()
	assert.NoError(t, err)
	assert.Empty(t, items)
}

func TestFindClaudeHooks_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	finder := &UserIntegrationsFinder{homeDir: tmpDir}

	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	settingsPath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte("{invalid json}"), 0600))

	items, err := finder.findClaudeHooks()
	assert.Error(t, err)
	assert.Nil(t, items)
}

func TestFindGeminiHooks_EmptySettingsFile(t *testing.T) {
	tmpDir := t.TempDir()
	finder := &UserIntegrationsFinder{homeDir: tmpDir}

	geminiDir := filepath.Join(tmpDir, ".gemini")
	require.NoError(t, os.MkdirAll(geminiDir, 0755))

	settingsPath := filepath.Join(geminiDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte(""), 0600))

	items, err := finder.findGeminiHooks()
	assert.NoError(t, err)
	assert.Empty(t, items)
}

func TestFindGeminiHooks_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	finder := &UserIntegrationsFinder{homeDir: tmpDir}

	geminiDir := filepath.Join(tmpDir, ".gemini")
	require.NoError(t, os.MkdirAll(geminiDir, 0755))

	settingsPath := filepath.Join(geminiDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte("not json"), 0600))

	items, err := finder.findGeminiHooks()
	assert.Error(t, err)
	assert.Nil(t, items)
}

func TestFindGeminiHooks_NonOxSessionStart(t *testing.T) {
	tmpDir := t.TempDir()
	finder := &UserIntegrationsFinder{homeDir: tmpDir}

	geminiDir := filepath.Join(tmpDir, ".gemini")
	require.NoError(t, os.MkdirAll(geminiDir, 0755))

	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": map[string]interface{}{
				"command": "echo not ox",
			},
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(geminiDir, "settings.json"), data, 0600))

	items, err := finder.findGeminiHooks()
	assert.NoError(t, err)
	assert.Empty(t, items)
}

func TestRemoveClaudeHooks_InvalidJSON(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(tmpFile, []byte("bad"), 0600))

	err := removeClaudeHooks(tmpFile, []byte("bad"))
	assert.Error(t, err)
}

func TestRemoveGeminiHooks_InvalidJSON(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(tmpFile, []byte("bad"), 0600))

	err := removeGeminiHooks(tmpFile, []byte("bad"))
	assert.Error(t, err)
}

func TestIndexOfSubstring(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		substr string
		want   int
	}{
		{name: "found at start", s: "hello world", substr: "hello", want: 0},
		{name: "found in middle", s: "hello world", substr: "world", want: 6},
		{name: "not found", s: "hello", substr: "xyz", want: -1},
		{name: "empty substr", s: "hello", substr: "", want: 0},
		{name: "substr longer than s", s: "hi", substr: "hello", want: -1},
		{name: "exact match", s: "hello", substr: "hello", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := indexOfSubstring(tt.s, tt.substr)
			assert.Equal(t, tt.want, got)
		})
	}
}
