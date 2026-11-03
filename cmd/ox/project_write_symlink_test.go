//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertPrivateFileUnchanged(t *testing.T, path, content string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestPrimeMarkerRepair_RejectsExternalSymlinks(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		for _, content := range []string{
			"# Private instructions\n",
			OxPrimeCheckMarker + "\n" + legacyPrimeCheckBodies[0] + "\n\n" + OxPrimeLine + "\n",
			"", // dangling link: must not seed a file outside the checkout
		} {
			for _, relative := range []bool{false, true} {
				t.Run(name, func(t *testing.T) {
					gitRoot, outside := t.TempDir(), t.TempDir()
					require.NoError(t, os.Mkdir(filepath.Join(gitRoot, ".sageox"), 0700))
					require.NoError(t, os.WriteFile(filepath.Join(gitRoot, ".sageox", "config.json"), []byte(`{}`), 0600))
					require.True(t, config.IsInitialized(gitRoot))
					target := filepath.Join(outside, "private.md")
					if content != "" {
						require.NoError(t, os.WriteFile(target, []byte(content), 0600))
					}
					linkTarget := target
					if relative {
						var err error
						linkTarget, err = filepath.Rel(gitRoot, target)
						require.NoError(t, err)
					}
					require.NoError(t, os.Symlink(linkTarget, filepath.Join(gitRoot, name)))

					changed, err := EnsureOxPrimeMarker(gitRoot)
					require.Error(t, err)
					assert.False(t, changed)
					if content == "" {
						_, err := os.Stat(target)
						assert.True(t, os.IsNotExist(err))
					} else {
						assertPrivateFileUnchanged(t, target, content)
					}
					got, err := os.Readlink(filepath.Join(gitRoot, name))
					require.NoError(t, err)
					assert.Equal(t, linkTarget, got)
				})
			}
		}
	}
}

func TestPrimeMarkerRepair_PreservesInternalLinksAndMode(t *testing.T) {
	gitRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(gitRoot, "instructions"), 0700))
	target := filepath.Join(gitRoot, "instructions", "agents.md")
	require.NoError(t, os.WriteFile(target, []byte("# Private instructions\n"), 0600))
	require.NoError(t, os.Symlink("instructions/agents.md", filepath.Join(gitRoot, "AGENTS.md")))
	require.NoError(t, os.Symlink("AGENTS.md", filepath.Join(gitRoot, "CLAUDE.md")))
	changed, err := EnsureOxPrimeMarker(gitRoot)
	require.NoError(t, err)
	assert.True(t, changed)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Contains(t, string(data), "# Private instructions")
	assert.Contains(t, string(data), OxPrimeCheckMarker)
	assert.Contains(t, string(data), OxPrimeMarker)
	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		_, err := os.Readlink(filepath.Join(gitRoot, name))
		require.NoError(t, err)
	}
	changed, err = EnsureOxPrimeMarker(gitRoot)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestPrimeClaudeHookRepair_RejectsExternalParent(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run("settings", func(t *testing.T) {
			isolateUserConfig(t)
			gitRoot, outside := t.TempDir(), t.TempDir()
			initGitRepo(t, gitRoot)
			t.Chdir(gitRoot)
			content := `{"permissions":{"allow":["Read"]}}`
			if existing {
				for _, name := range []string{"settings.json", "settings.local.json"} {
					require.NoError(t, os.WriteFile(filepath.Join(outside, name), []byte(content), 0600))
				}
			}
			require.NoError(t, os.Symlink(outside, filepath.Join(gitRoot, ".claude")))
			require.True(t, detectClaudeCode())
			assert.False(t, ensureClaudeHooks(gitRoot))
			require.Error(t, InstallProjectClaudeHooks(gitRoot))
			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			if existing {
				assert.Len(t, entries, 2)
				for _, name := range []string{"settings.json", "settings.local.json"} {
					assertPrivateFileUnchanged(t, filepath.Join(outside, name), content)
				}
			} else {
				assert.Empty(t, entries, "no settings or temporary files may be created outside")
			}
		})
	}
}

func TestProjectClaudeHooks_RejectExternalSettingsLeaf(t *testing.T) {
	for _, name := range []string{"settings.json", "settings.local.json"} {
		t.Run(name, func(t *testing.T) {
			isolateUserConfig(t)
			gitRoot := t.TempDir()
			target := filepath.Join(t.TempDir(), "settings.json")
			// A local file with stale hooks exercises cleanup as well as installation.
			content := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"ox agent prime"}]}]},"model":"private"}`
			require.NoError(t, os.WriteFile(target, []byte(content), 0600))
			require.NoError(t, os.Mkdir(filepath.Join(gitRoot, ".claude"), 0700))
			require.NoError(t, os.Symlink(target, filepath.Join(gitRoot, ".claude", name)))
			require.Error(t, InstallProjectClaudeHooks(gitRoot))
			if name == "settings.local.json" {
				require.Error(t, cleanupLocalSettingsOxHooks(gitRoot))
			}
			_, err := syncProjectStatusLine(gitRoot)
			require.Error(t, err)
			assertPrivateFileUnchanged(t, target, content)
		})
	}
}

func TestProjectClaudeHooks_AllowsInternalParent(t *testing.T) {
	isolateUserConfig(t)
	gitRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(gitRoot, "claude-config"), 0700))
	require.NoError(t, os.Symlink("claude-config", filepath.Join(gitRoot, ".claude")))
	settingsPath := filepath.Join(gitRoot, "claude-config", "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte(`{"model":"private"}`), 0600))
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))
	assert.True(t, HasProjectClaudeHooks(gitRoot))
	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"model": "private"`)
	info, err := os.Stat(settingsPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	link, err := os.Readlink(filepath.Join(gitRoot, ".claude"))
	require.NoError(t, err)
	assert.Equal(t, "claude-config", link)
}
