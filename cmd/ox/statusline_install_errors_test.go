package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readOnlyDir makes dir unwritable for the test and restores it on cleanup.
// Skipped where permissions do not bite (Windows, root).
func readOnlyDir(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced here")
	}
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// TestSyncProjectStatusLine_Errors surfaces every file problem instead of
// silently leaving the coworker's status line half-configured. Failure
// prevented: a corrupt or read-only .claude makes `ox config set statusline`
// and `ox doctor` report success while nothing changed.
func TestSyncProjectStatusLine_Errors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, gitRoot string)
	}{
		{
			name: "unreadable shared settings.json",
			setup: func(t *testing.T, gitRoot string) {
				writeProjectSettings(t, gitRoot, `{`)
			},
		},
		{
			name: "shared settings.json cannot be rewritten",
			setup: func(t *testing.T, gitRoot string) {
				// an ox statusLine in the shared file must be moved out, but the
				// directory refuses the write
				writeProjectSettings(t, gitRoot, `{"statusLine":{"type":"command","command":"ox statusline"}}`)
				readOnlyDir(t, filepath.Join(gitRoot, ".claude"))
			},
		},
		{
			name: "unreadable settings.local.json",
			setup: func(t *testing.T, gitRoot string) {
				require.NoError(t, InstallProjectClaudeHooks(gitRoot))
				writeLocalSettings(t, gitRoot, `{`)
			},
		},
		{
			name: "settings.local.json cannot be removed when turning off",
			setup: func(t *testing.T, gitRoot string) {
				require.NoError(t, InstallProjectClaudeHooks(gitRoot))
				require.NoError(t, SetConfigValue("statusline", "off", ConfigLevelUser, ""))
				// the local file holds only ox's statusLine, so off deletes it
				readOnlyDir(t, filepath.Join(gitRoot, ".claude"))
			},
		},
		{
			name: "settings.local.json cannot be written",
			setup: func(t *testing.T, gitRoot string) {
				writeProjectSettings(t, gitRoot, `{}`)
				writeLocalSettings(t, gitRoot, `{"model":"opus"}`)
				if runtime.GOOS == "windows" || os.Geteuid() == 0 {
					t.Skip("file permissions are not enforced here")
				}
				require.NoError(t, os.Chmod(getLocalClaudeSettingsPath(gitRoot), 0o444))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateUserConfig(t)
			gitRoot := t.TempDir()
			tt.setup(t, gitRoot)
			_, err := syncProjectStatusLine(gitRoot)
			assert.Error(t, err)
		})
	}
}

// TestStatusLineCallers_SurfaceLocalReadError: the hook installer and the
// doctor check both stop on a corrupt settings.local.json rather than
// overwrite whatever the coworker had in it.
func TestStatusLineCallers_SurfaceLocalReadError(t *testing.T) {
	isolateUserConfig(t)
	gitRoot := t.TempDir()
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))
	writeLocalSettings(t, gitRoot, `{"statusLine": nope}`)

	assert.Error(t, InstallProjectClaudeHooks(gitRoot))

	res := checkClaudeStatusLineAt(gitRoot, true)
	assert.True(t, res.warning, "a file ox cannot read is a warning, not a repair")
	assert.Contains(t, res.message, "read error")

	data, err := os.ReadFile(getLocalClaudeSettingsPath(gitRoot))
	require.NoError(t, err)
	assert.Equal(t, `{"statusLine": nope}`, string(data), "the unreadable file is left exactly as it was")
}

// TestApplyStatuslineSetting_WarnsOnFailure: `ox config set statusline` from
// inside a project tells the coworker when the project files could not be
// updated instead of pretending the change took effect.
func TestApplyStatuslineSetting_WarnsOnFailure(t *testing.T) {
	isolateUserConfig(t)
	gitRoot := t.TempDir()
	initGitRepo(t, gitRoot)
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))
	writeLocalSettings(t, gitRoot, `{`)
	t.Chdir(gitRoot)

	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = stderr
	t.Cleanup(func() { os.Stderr = orig })

	applyStatuslineSetting()

	os.Stderr = orig
	require.NoError(t, stderr.Close())
	out, err := os.ReadFile(stderr.Name())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(out), "warning: could not update "), "got %q", out)
	assert.Contains(t, string(out), "settings.local.json")
}
