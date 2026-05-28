package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/ephemeral"
	"github.com/sageox/ox/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func clearEphemeralSignalsForConfigTest(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"OX_EPHEMERAL",
		"CLAUDE_CODE_REMOTE",
		"DEVIN_TASK_ID",
		"CODESPACES",
		"CI",
		"GITHUB_ACTIONS",
		"GITLAB_CI",
		"JENKINS_URL",
		"BUILDKITE",
		"CODEBUILD_BUILD_ID",
		"OX_PERSIST_DISK",
		"OX_NO_DAEMON",
		"OX_BROWSER",
	} {
		t.Setenv(key, "")
	}
	// Runtime caps are sync.Once-cached; clear them so a sibling test's
	// probe doesn't pollute this one. Pair with the env-var resets above.
	runtime.Reset()
	t.Cleanup(runtime.Reset)
}

// TestLoad_SeedsEphemeralPreferenceFromUserConfig — Failure prevented: the
// persisted `ephemeral: true` preference is ignored during early config load,
// so commands start the daemon or render interactive UI before user config is
// ever read.
func TestLoad_SeedsEphemeralPreferenceFromUserConfig(t *testing.T) {
	clearEphemeralSignalsForConfigTest(t)
	ephemeral.SetUserConfigPreference(nil)
	t.Cleanup(func() { ephemeral.SetUserConfigPreference(nil) })

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfgDir := filepath.Join(tmpDir, "sageox")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("ephemeral: true\n"), 0o644))

	cfg := Load()
	assert.True(t, cfg.NoInteractive, "ephemeral user config should force non-interactive mode during config load")
	assert.True(t, ephemeral.IsEphemeral(), "Load should publish the user-config preference before early daemon/UI decisions")
}
