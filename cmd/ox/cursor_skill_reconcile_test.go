package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorDescriptorSkillsReconcileWithoutLegacyCapability(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds the Cursor adapter binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("adapter invocation recorder uses a POSIX shell")
	}

	realAdapter := buildCursorSkillsAdapter(t)
	adapterDir := t.TempDir()
	callLog := filepath.Join(t.TempDir(), "calls")
	wrapper := filepath.Join(adapterDir, "ox-adapter-cursor")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$1" >> %s
exec %s "$@"
`, quoteCursorSkillsShell(callLog), quoteCursorSkillsShell(realAdapter))
	require.NoError(t, os.WriteFile(wrapper, []byte(script), 0o755))

	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".cursor", "projects"), 0o755))
	t.Setenv("HOME", home)
	t.Setenv("OX_ADAPTER_PATH", adapterDir)

	compiled, err := adapters.NewExternalAdapter(realAdapter)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compiled.Close() })
	assert.False(t, compiled.HasCapability(adapterprotocol.CapSkillsInstaller),
		"Cursor uses host-owned descriptors and must not advertise the legacy skills RPC")
	require.Len(t, compiled.Info().SkillTargets, 1)
	assert.Equal(t, ".agents/skills", compiled.Info().SkillTargets[0].Root)

	repoRoot := cursorSkillsRepo(t)
	detected, err := detectedSkillTargets(repoRoot)
	require.NoError(t, err)
	require.Len(t, detected, 1)
	assert.Equal(t, compiled.Info().SkillTargets[0], detected[0])

	installAgentHooks(repoRoot, true, map[string]bool{"cursor": true})
	_, err = os.Stat(filepath.Join(repoRoot, ".cursor", "hooks.json"))
	require.NoError(t, err, "selected Cursor adapter did not install its project hooks")

	defaultNames, err := skills.BundleNames(skills.DefaultBundleIDs())
	require.NoError(t, err)
	for _, name := range defaultNames {
		path := filepath.Join(repoRoot, ".agents", "skills", name, skills.SkillFileName)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("default Cursor skill %q was not projected: %v", name, err)
		}
	}
	desired, targets, err := skillmanager.LoadDesired(repoRoot)
	require.NoError(t, err)
	assert.Contains(t, desired.Targets, "agents-project")
	require.Len(t, targets, 1)
	assert.Equal(t, compiled.Info().SkillTargets[0], targets[0])

	calls, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Contains(t, string(calls), "install-hooks\n")
	assert.NotContains(t, string(calls), "install-skills\n",
		"descriptor-based projection must not call the legacy skills RPC")
}

func TestCapabilityOnlyAdapterRetainsLegacySkillsRPC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("legacy adapter fixture uses a POSIX shell")
	}

	adapterDir := t.TempDir()
	callLog := filepath.Join(t.TempDir(), "calls")
	binary := filepath.Join(adapterDir, "ox-adapter-legacy-skills")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  info)
    printf '%%s\n' '{"protocol_version":1,"name":"legacy-skills","display_name":"Legacy skills","version":"0.1.0","type":"session","capabilities":["skills_installer"]}'
    ;;
  install-skills)
    printf 'install-skills\n' >> %s
    printf '%%s\n' '{"installed":true}'
    ;;
  *)
    printf '%%s\n' '{}'
    ;;
esac
`, quoteCursorSkillsShell(callLog))
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o755))
	t.Setenv("OX_ADAPTER_PATH", adapterDir)

	repoRoot := cursorSkillsRepo(t)
	installAgentHooks(repoRoot, true, map[string]bool{"legacy-skills": true})
	calls, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Equal(t, "install-skills\n", string(calls))

	desired, targets, err := skillmanager.LoadDesired(repoRoot)
	require.NoError(t, err)
	assert.Empty(t, desired.Targets)
	assert.Empty(t, targets)
}

func TestCursorUninstallAllCleansPartialHooks(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds the Cursor adapter binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("adapter wrapper uses a POSIX shell")
	}
	realAdapter := buildCursorSkillsAdapter(t)
	for _, kind := range []string{"partial", "stale", "unrelated only"} {
		t.Run(kind, func(t *testing.T) {
			repoRoot := setupUninstallAllTest(t, map[string]string{
				"amp": ".amp", "codex": ".codex", "cursor": ".cursor",
				"gemini": ".gemini", "opencode": ".opencode",
			})
			wrapper := filepath.Join(os.Getenv("OX_ADAPTER_PATH"), "ox-adapter-cursor")
			require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quoteCursorSkillsShell(realAdapter)+" \"$@\"\n"), 0o755))
			entries := []map[string]any{{"command": "echo keep"}}
			if kind != "unrelated only" {
				executable := realAdapter
				if kind == "stale" {
					executable = filepath.Join(repoRoot, "old", "ox-adapter-cursor")
				}
				entries = append(entries, map[string]any{"command": quoteCursorSkillsShell(executable) + " hook stop", "timeout": 20})
			}
			before, err := json.Marshal(map[string]any{"version": 1, "hooks": map[string]any{"stop": entries}})
			require.NoError(t, err)
			path := filepath.Join(repoRoot, ".cursor", "hooks.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, before, 0o644))
			assert.False(t, checkExternalAdapterHooks("cursor", false), "an incomplete installation remains unhealthy")

			var uninstallErr error
			output := captureStdoutForPlanCLI(t, func() {
				withStdin(t, "", func() { uninstallErr = uninstallAllIntegrations(false) })
			})
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after, "discovery and an unanswered prompt must not mutate hooks")
			if kind == "unrelated only" {
				require.NoError(t, uninstallErr)
				assert.Contains(t, output, "No integrations found")
				return
			}
			require.ErrorIs(t, uninstallErr, cli.ErrConfirmationRequired)
			assert.Contains(t, output, "Cursor Agents Window (project)")
			require.NoError(t, uninstallAllIntegrations(true))
			after, err = os.ReadFile(path)
			require.NoError(t, err)
			assert.JSONEq(t, `{"version":1,"hooks":{"stop":[{"command":"echo keep"}]}}`, string(after))
			assert.False(t, hasExternalAdapterHooksToRemove("cursor", false))
		})
	}
}

func buildCursorSkillsAdapter(t *testing.T) string {
	t.Helper()
	repoRoot := findOxRepoRootForTest(t)
	binary := filepath.Join(t.TempDir(), "cursor-adapter-real")
	command := exec.Command("go", "build", "-o", binary, "./cmd/ox-adapter-cursor")
	command.Dir = repoRoot
	output, err := command.CombinedOutput()
	require.NoError(t, err, "build Cursor adapter: %s", output)
	return binary
}

func cursorSkillsRepo(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".git", "hooks"), 0o755))
	return repoRoot
}

func quoteCursorSkillsShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
