package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadUserConfig_DefaultsToTipsEnabled(t *testing.T) {
	// use temp dir with no config file
	tmpDir := t.TempDir()

	cfg, err := LoadUserConfigFrom(tmpDir)
	require.NoError(t, err, "unexpected error")

	assert.True(t, cfg.AreTipsEnabled(), "expected tips to be enabled by default")
}

func TestLoadUserConfig_RespectsDisabledTips(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// write config with tips disabled
	content := []byte("tips_enabled: false\n")
	require.NoError(t, os.WriteFile(configPath, content, 0644), "failed to write test config")

	cfg, err := LoadUserConfigFrom(tmpDir)
	require.NoError(t, err, "unexpected error")

	assert.False(t, cfg.AreTipsEnabled(), "expected tips to be disabled")
}

func TestLoadUserConfig_ContextGitDefaults(t *testing.T) {
	// use temp dir with no config file
	tmpDir := t.TempDir()

	cfg, err := LoadUserConfigFrom(tmpDir)
	require.NoError(t, err, "unexpected error")

	// auto_commit defaults to true
	assert.True(t, cfg.GetContextGitAutoCommit(), "expected context_git.auto_commit to default to true")

	// auto_push defaults to false
	assert.False(t, cfg.GetContextGitAutoPush(), "expected context_git.auto_push to default to false")
}

func TestLoadUserConfig_RespectsContextGitSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// write config with context_git settings
	content := []byte(`context_git:
  auto_commit: false
  auto_push: true
`)
	require.NoError(t, os.WriteFile(configPath, content, 0644), "failed to write test config")

	cfg, err := LoadUserConfigFrom(tmpDir)
	require.NoError(t, err, "unexpected error")

	assert.False(t, cfg.GetContextGitAutoCommit(), "expected context_git.auto_commit to be false")

	assert.True(t, cfg.GetContextGitAutoPush(), "expected context_git.auto_push to be true")
}

func TestContextGitConfig_NilReceiver(t *testing.T) {
	var cfg *ContextGitConfig

	// nil receiver should return defaults
	assert.True(t, cfg.IsAutoCommitEnabled(), "expected nil ContextGitConfig.IsAutoCommitEnabled() to return true")

	assert.False(t, cfg.IsAutoPushEnabled(), "expected nil ContextGitConfig.IsAutoPushEnabled() to return false")
}

func TestUserConfig_SetContextGitAutoCommit(t *testing.T) {
	cfg := &UserConfig{}

	// setting on nil ContextGit should create it
	cfg.SetContextGitAutoCommit(false)

	require.NotNil(t, cfg.ContextGit, "expected ContextGit to be created")

	assert.False(t, cfg.GetContextGitAutoCommit(), "expected auto_commit to be false after setting")

	// setting to true
	cfg.SetContextGitAutoCommit(true)
	assert.True(t, cfg.GetContextGitAutoCommit(), "expected auto_commit to be true after setting")
}

func TestUserConfig_SetContextGitAutoPush(t *testing.T) {
	cfg := &UserConfig{}

	// setting on nil ContextGit should create it
	cfg.SetContextGitAutoPush(true)

	require.NotNil(t, cfg.ContextGit, "expected ContextGit to be created")

	assert.True(t, cfg.GetContextGitAutoPush(), "expected auto_push to be true after setting")

	// setting to false
	cfg.SetContextGitAutoPush(false)
	assert.False(t, cfg.GetContextGitAutoPush(), "expected auto_push to be false after setting")
}

func TestSaveAndLoadUserConfig_ContextGit(t *testing.T) {
	// use XDG_CONFIG_HOME to isolate test from real config
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// create and save config with context_git settings
	cfg := &UserConfig{}
	cfg.SetContextGitAutoCommit(false)
	cfg.SetContextGitAutoPush(true)

	require.NoError(t, SaveUserConfig(cfg), "failed to save config")

	// load and verify
	loaded, err := LoadUserConfig()
	require.NoError(t, err, "failed to load config")

	assert.False(t, loaded.GetContextGitAutoCommit(), "expected loaded auto_commit to be false")

	assert.True(t, loaded.GetContextGitAutoPush(), "expected loaded auto_push to be true")
}

func TestContextGitConfig_PartialSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// write config with only auto_commit set (auto_push should default)
	content := []byte(`context_git:
  auto_commit: false
`)
	require.NoError(t, os.WriteFile(configPath, content, 0644), "failed to write test config")

	cfg, err := LoadUserConfigFrom(tmpDir)
	require.NoError(t, err, "unexpected error")

	assert.False(t, cfg.GetContextGitAutoCommit(), "expected context_git.auto_commit to be false")

	// auto_push should still default to false
	assert.False(t, cfg.GetContextGitAutoPush(), "expected context_git.auto_push to default to false")
}

func TestUserConfig_GetContextGitWithNilContextGit(t *testing.T) {
	cfg := &UserConfig{
		ContextGit: nil,
	}

	// should return defaults when ContextGit is nil
	assert.True(t, cfg.GetContextGitAutoCommit(), "expected GetContextGitAutoCommit to return true with nil ContextGit")

	assert.False(t, cfg.GetContextGitAutoPush(), "expected GetContextGitAutoPush to return false with nil ContextGit")
}

func TestLoadUserConfig_SessionsDefaults(t *testing.T) {
	// use temp dir with no config file
	tmpDir := t.TempDir()

	cfg, err := LoadUserConfigFrom(tmpDir)
	require.NoError(t, err, "unexpected error")

	// sessions.enabled defaults to false
	assert.False(t, cfg.AreSessionsEnabled(), "expected sessions.enabled to default to false")
}

func TestLoadUserConfig_RespectsSessionsSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// write config with sessions enabled
	content := []byte(`sessions:
  enabled: true
`)
	require.NoError(t, os.WriteFile(configPath, content, 0644), "failed to write test config")

	cfg, err := LoadUserConfigFrom(tmpDir)
	require.NoError(t, err, "unexpected error")

	assert.True(t, cfg.AreSessionsEnabled(), "expected sessions.enabled to be true")
}

func TestSessionsConfig_NilReceiver(t *testing.T) {
	var cfg *SessionsConfig

	// nil receiver should return default (false)
	assert.False(t, cfg.IsEnabled(), "expected nil SessionsConfig.IsEnabled() to return false")
}

func TestUserConfig_SetSessionsEnabled(t *testing.T) {
	cfg := &UserConfig{}

	// setting on nil Sessions should create it
	cfg.SetSessionsEnabled(true)

	require.NotNil(t, cfg.Sessions, "expected Sessions to be created")

	assert.True(t, cfg.AreSessionsEnabled(), "expected sessions.enabled to be true after setting")

	// setting to false
	cfg.SetSessionsEnabled(false)
	assert.False(t, cfg.AreSessionsEnabled(), "expected sessions.enabled to be false after setting")
}

func TestSaveAndLoadUserConfig_Sessions(t *testing.T) {
	// use XDG_CONFIG_HOME to isolate test from real config
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// create and save config with sessions enabled
	cfg := &UserConfig{}
	cfg.SetSessionsEnabled(true)

	require.NoError(t, SaveUserConfig(cfg), "failed to save config")

	// load and verify
	loaded, err := LoadUserConfig()
	require.NoError(t, err, "failed to load config")

	assert.True(t, loaded.AreSessionsEnabled(), "expected loaded sessions.enabled to be true")
}

func TestUserConfig_AreSessionsEnabledWithNilSessions(t *testing.T) {
	cfg := &UserConfig{
		Sessions: nil,
	}

	// should return default (false) when Sessions is nil
	assert.False(t, cfg.AreSessionsEnabled(), "expected AreSessionsEnabled to return false with nil Sessions")
}

func TestUserConfig_SessionTermsShown_DefaultFalse(t *testing.T) {
	cfg := &UserConfig{}
	assert.False(t, cfg.HasSeenSessionTerms(), "expected HasSeenSessionTerms to default to false")
}

func TestUserConfig_SetSessionTermsShown(t *testing.T) {
	cfg := &UserConfig{}

	cfg.SetSessionTermsShown(true)
	assert.True(t, cfg.HasSeenSessionTerms(), "expected HasSeenSessionTerms to be true after setting")

	cfg.SetSessionTermsShown(false)
	assert.False(t, cfg.HasSeenSessionTerms(), "expected HasSeenSessionTerms to be false after unsetting")
}

func TestSaveAndLoadUserConfig_SessionTermsShown(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &UserConfig{}
	cfg.SetSessionTermsShown(true)
	require.NoError(t, SaveUserConfig(cfg), "failed to save config")

	loaded, err := LoadUserConfig()
	require.NoError(t, err, "failed to load config")
	assert.True(t, loaded.HasSeenSessionTerms(), "expected loaded session_terms_shown to be true")
}

func TestLoadUserConfig_RespectsSessionTermsShown(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	content := []byte("session_terms_shown: true\n")
	require.NoError(t, os.WriteFile(configPath, content, 0644))

	cfg, err := LoadUserConfigFrom(tmpDir)
	require.NoError(t, err)
	assert.True(t, cfg.HasSeenSessionTerms())
}

func TestSaveUserConfig_AtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &UserConfig{}
	tipsOff := false
	cfg.TipsEnabled = &tipsOff

	require.NoError(t, SaveUserConfig(cfg), "failed to save config")

	configDir := filepath.Join(tmpDir, "sageox")

	// config.yaml should exist
	_, err := os.Stat(filepath.Join(configDir, "config.yaml"))
	require.NoError(t, err, "config.yaml should exist after save")

	// temp file should NOT exist
	_, err = os.Stat(filepath.Join(configDir, "config.yaml.tmp"))
	assert.True(t, os.IsNotExist(err), "config.yaml.tmp should not exist after successful save")
}

func TestLoadUserConfig_CorruptYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// write corrupt YAML (the bug this whole refactor fixes)
	require.NoError(t, os.WriteFile(configPath, []byte("e"), 0644))

	_, err := LoadUserConfigFrom(tmpDir)
	assert.Error(t, err, "corrupt YAML should return an error")
}

func TestLoadConfig_EnvOnly(t *testing.T) {
	// Load() should read from env vars only, not config files
	t.Setenv("OX_VERBOSE", "1")
	t.Setenv("OX_QUIET", "1")

	cfg := Load()

	assert.True(t, cfg.Verbose, "expected OX_VERBOSE=1 to set Verbose")
	assert.True(t, cfg.Quiet, "expected OX_QUIET=1 to set Quiet")
	assert.False(t, cfg.JSON, "expected JSON to default to false")
}

func TestLoadConfig_CIDetection(t *testing.T) {
	t.Setenv("CI", "true")

	cfg := Load()

	assert.True(t, cfg.NoInteractive, "expected CI=true to set NoInteractive")
}

func TestLoadUserConfig_ViewFormatDefault(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := LoadUserConfigFrom(tmpDir)
	require.NoError(t, err)

	assert.Equal(t, "web", cfg.GetViewFormat(), "expected view_format to default to web")
}

func TestLoadUserConfig_RespectsViewFormat(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	content := []byte("view_format: text\n")
	require.NoError(t, os.WriteFile(configPath, content, 0644))

	cfg, err := LoadUserConfigFrom(tmpDir)
	require.NoError(t, err)

	assert.Equal(t, "text", cfg.GetViewFormat(), "expected view_format to be text")
}

func TestSaveAndLoadUserConfig_ViewFormat(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &UserConfig{ViewFormat: "json"}
	require.NoError(t, SaveUserConfig(cfg))

	loaded, err := LoadUserConfig()
	require.NoError(t, err)
	assert.Equal(t, "json", loaded.GetViewFormat())
}

func TestUserConfig_DisplayName_DefaultEmpty(t *testing.T) {
	cfg := &UserConfig{}
	assert.Equal(t, "", cfg.GetDisplayName(), "expected display_name to default to empty string")
}

func TestUserConfig_SetDisplayName(t *testing.T) {
	cfg := &UserConfig{}

	cfg.SetDisplayName("port8080")
	assert.Equal(t, "port8080", cfg.GetDisplayName())

	cfg.SetDisplayName("")
	assert.Equal(t, "", cfg.GetDisplayName())
}

func TestLoadUserConfig_RespectsDisplayName(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	content := []byte("display_name: port8080\n")
	require.NoError(t, os.WriteFile(configPath, content, 0644))

	cfg, err := LoadUserConfigFrom(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "port8080", cfg.GetDisplayName())
}

func TestSaveAndLoadUserConfig_DisplayName(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &UserConfig{}
	cfg.SetDisplayName("cooldev")
	require.NoError(t, SaveUserConfig(cfg))

	loaded, err := LoadUserConfig()
	require.NoError(t, err)
	assert.Equal(t, "cooldev", loaded.GetDisplayName())
}

func TestSanitizeDisplayName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"clean passthrough", "port8080", "port8080"},
		{"trims whitespace", "  port8080  ", "port8080"},
		{"newline becomes space", "port\n8080", "port 8080"},
		{"tab becomes space", "port\t8080", "port 8080"},
		{"null byte becomes space", "port\x008080", "port 8080"},
		{"collapses internal spaces", "Person   A.", "Person A."},
		{"whitespace only becomes empty", "   ", ""},
		{"control chars only becomes empty", "\n\t\r", ""},
		{"unicode preserved", "Jos\u00e9", "Jos\u00e9"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SanitizeDisplayName(tt.input))
		})
	}
}

func TestValidateDisplayName(t *testing.T) {
	// empty is valid (means auto-derive)
	assert.NoError(t, ValidateDisplayName(""))

	// normal names are valid
	assert.NoError(t, ValidateDisplayName("port8080"))
	assert.NoError(t, ValidateDisplayName("Person A."))

	// exactly 40 chars is valid
	assert.NoError(t, ValidateDisplayName(strings.Repeat("a", MaxDisplayNameLength)))

	// 41 chars is too long
	assert.Error(t, ValidateDisplayName(strings.Repeat("a", MaxDisplayNameLength+1)))

	// control chars stripped before length check
	padded := strings.Repeat("a", MaxDisplayNameLength) + "\n"
	assert.NoError(t, ValidateDisplayName(padded), "control chars should be stripped before length check")
}

func TestSetDisplayName_Sanitizes(t *testing.T) {
	cfg := &UserConfig{}

	cfg.SetDisplayName("  hello\tworld  ")
	assert.Equal(t, "hello world", cfg.GetDisplayName())

	cfg.SetDisplayName("   ")
	assert.Equal(t, "", cfg.GetDisplayName())
}

func TestLoadUserConfig_OxUserConfigEnv(t *testing.T) {
	t.Run("loads config from explicit file path", func(t *testing.T) {
		configFile := filepath.Join(t.TempDir(), "custom-config.yaml")
		content := []byte("display_name: pipeline-bot\nsessions:\n  mode: auto\n")
		require.NoError(t, os.WriteFile(configFile, content, 0644))

		t.Setenv("OX_USER_CONFIG", configFile)

		cfg, err := LoadUserConfig()
		require.NoError(t, err)
		assert.Equal(t, "pipeline-bot", cfg.GetDisplayName())
		assert.Equal(t, "auto", cfg.Sessions.GetMode())
	})

	t.Run("missing file returns empty config", func(t *testing.T) {
		t.Setenv("OX_USER_CONFIG", filepath.Join(t.TempDir(), "nonexistent.yaml"))

		cfg, err := LoadUserConfig()
		require.NoError(t, err)
		assert.Equal(t, "", cfg.GetDisplayName())
	})

	t.Run("explicit configDir takes precedence over env var", func(t *testing.T) {
		// env var points to one config
		envFile := filepath.Join(t.TempDir(), "env-config.yaml")
		require.NoError(t, os.WriteFile(envFile, []byte("display_name: from-env\n"), 0644))
		t.Setenv("OX_USER_CONFIG", envFile)

		// explicit configDir points to another
		explicitDir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(explicitDir, "config.yaml"),
			[]byte("display_name: from-dir\n"), 0644))

		cfg, err := LoadUserConfigFrom(explicitDir)
		require.NoError(t, err)
		assert.Equal(t, "from-dir", cfg.GetDisplayName())
	})
}
