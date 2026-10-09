package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setCursorIntegrateFlags(t *testing.T, user bool) {
	t.Helper()
	previousCursor, previousUser := integrateCursorFlag, integrateUserFlag
	integrateCursorFlag, integrateUserFlag = true, user
	t.Cleanup(func() {
		integrateCursorFlag, integrateUserFlag = previousCursor, previousUser
	})
}

func TestIntegrateCursorProjectLifecycle(t *testing.T) {
	repoRoot := setupUninstallAllTest(t, map[string]string{"cursor": ".cursor"})
	setCursorIntegrateFlags(t, false)

	unrelated := filepath.Join(repoRoot, ".cursor", "keep.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(unrelated), 0o755))
	require.NoError(t, os.WriteFile(unrelated, []byte("unrelated\n"), 0o644))

	require.NoError(t, runIntegrateInstall(integrateInstallCmd, nil))
	assert.FileExists(t, filepath.Join(repoRoot, ".cursor", "hooks.json"))
	require.NoError(t, runIntegrateUninstall(integrateUninstallCmd, nil))
	assert.NoFileExists(t, filepath.Join(repoRoot, ".cursor", "hooks.json"))
	data, err := os.ReadFile(unrelated)
	require.NoError(t, err)
	assert.Equal(t, "unrelated\n", string(data))
}

func TestIntegrateCursorRejectsUserScope(t *testing.T) {
	repoRoot := setupUninstallAllTest(t, map[string]string{"cursor": ".cursor"})
	setCursorIntegrateFlags(t, true)

	err := runIntegrateInstall(integrateInstallCmd, nil)
	require.ErrorContains(t, err, "user-level Cursor Agents Window integration is unsupported")
	assert.NoFileExists(t, filepath.Join(repoRoot, ".cursor", "hooks.json"))
	err = runIntegrateUninstall(integrateUninstallCmd, nil)
	require.ErrorContains(t, err, "user-level Cursor Agents Window integration is unsupported")
}

func TestIntegrateCursorInstallsDescriptorSkills(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		name := "materializes default skills"
		if blocked {
			name = "reports asset installation failure"
		}
		t.Run(name, func(t *testing.T) {
			repoRoot := setupUninstallAllTest(t, map[string]string{"cursor": ".cursor"})
			setCursorIntegrateFlags(t, false)
			binary := filepath.Join(os.Getenv("OX_ADAPTER_PATH"), "ox-adapter-cursor")
			script, err := os.ReadFile(binary)
			require.NoError(t, err)
			script = []byte(strings.Replace(string(script), `"capabilities":`,
				`"skill_targets":[{"key":"agents-project","root":".agents/skills","format":"agent-skills/v1","scope":"project","link_policy":"reject"}],"capabilities":`, 1))
			require.NoError(t, os.WriteFile(binary, script, 0o755))
			if blocked {
				require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".agents"), []byte("preserve me\n"), 0o644))
			}

			err = runIntegrateInstall(integrateInstallCmd, nil)
			if blocked {
				require.ErrorContains(t, err, "installing Cursor Agents Window assets")
				data, readErr := os.ReadFile(filepath.Join(repoRoot, ".agents"))
				require.NoError(t, readErr)
				assert.Equal(t, "preserve me\n", string(data))
				return
			}
			require.NoError(t, err)
			assert.FileExists(t, filepath.Join(repoRoot, ".cursor", "hooks.json"))
			names, err := skills.BundleNames(skills.DefaultBundleIDs())
			require.NoError(t, err)
			for _, skill := range names {
				assert.FileExists(t, filepath.Join(repoRoot, ".agents", "skills", skill, skills.SkillFileName))
			}
			desired, targets, err := skillmanager.LoadDesired(repoRoot)
			require.NoError(t, err)
			assert.Contains(t, desired.Targets, "agents-project")
			require.Len(t, targets, 1)
			assert.Equal(t, ".agents/skills", targets[0].Root)
		})
	}
}

func TestIntegrateCursorRequiresProjectRepository(t *testing.T) {
	adapterDir := t.TempDir()
	createFakeAdapterWithHooks(t, adapterDir, "cursor", "0.1.0", "session", ".cursor")
	adapters.Unregister("cursor")
	t.Cleanup(func() { adapters.Unregister("cursor") })
	t.Setenv("OX_ADAPTER_PATH", adapterDir)
	t.Chdir(t.TempDir())
	setCursorIntegrateFlags(t, false)

	err := runIntegrateInstall(integrateInstallCmd, nil)
	require.ErrorContains(t, err, "not in a git repository")
	err = runIntegrateUninstall(integrateUninstallCmd, nil)
	require.ErrorContains(t, err, "not in a git repository")
}

func TestIntegrateCursorFlagIsAnExplicitSelector(t *testing.T) {
	previous := integrateCursorFlag
	integrateCursorFlag = true
	t.Cleanup(func() { integrateCursorFlag = previous })

	assert.True(t, hasAnyAgentFlag())
	installFlag := integrateInstallCmd.Flags().Lookup("cursor")
	uninstallFlag := integrateUninstallCmd.Flags().Lookup("cursor")
	require.NotNil(t, installFlag)
	require.NotNil(t, uninstallFlag)
	assert.False(t, installFlag.Hidden)
	assert.False(t, uninstallFlag.Hidden)
}

func TestUninstallAllIntegrationsRemovesCursor(t *testing.T) {
	repoRoot := setupUninstallAllTest(t, map[string]string{
		"amp":      ".amp",
		"codex":    ".codex",
		"cursor":   ".cursor",
		"gemini":   ".gemini",
		"opencode": ".opencode",
	})
	marker := filepath.Join(repoRoot, ".cursor", "hooks.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o755))
	require.NoError(t, os.WriteFile(marker, []byte("{}\n"), 0o644))

	require.NoError(t, uninstallAllIntegrations(true))
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "--all must dispatch Cursor uninstallation")
}
