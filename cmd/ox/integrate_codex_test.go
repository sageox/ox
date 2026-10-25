package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A successful install still needs Codex's trust step; a failed install must
// never tell the coworker that their hooks are ready to approve.
func TestIntegrateCodex_TrustGuidanceRequiresSuccessfulInstall(t *testing.T) {
	skipIntegration(t)
	for _, installFails := range []bool{false, true} {
		name := "installed"
		if installFails {
			name = "install failed"
		}
		t.Run(name, func(t *testing.T) {
			root := setupUninstallAllTest(t, map[string]string{"codex": ".codex"})
			if installFails {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".codex"), []byte("blocked"), 0o644))
			}

			previousCodex, previousUser := integrateCodexFlag, integrateUserFlag
			integrateCodexFlag, integrateUserFlag = true, false
			t.Cleanup(func() {
				integrateCodexFlag, integrateUserFlag = previousCodex, previousUser
			})

			var err error
			output := captureStdoutForPlanCLI(t, func() {
				err = runIntegrateInstall(integrateInstallCmd, nil)
			})
			if installFails {
				require.ErrorContains(t, err, "installing Codex CLI integration")
				assert.NotContains(t, output, "Installed Codex hooks")
				assert.NotContains(t, output, "/hooks")
				return
			}

			require.NoError(t, err)
			assert.FileExists(t, filepath.Join(root, ".codex", "hooks.json"))
			assert.Contains(t, output, "Installed Codex hooks")
			assert.Contains(t, output, "/hooks to review and trust the SageOx hooks")
		})
	}
}

// Re-running setup must remind coworkers to trust existing hooks, while a
// fresh noninteractive selection must not claim that unselected hooks exist.
func TestIntegrateCodex_InteractiveTrustGuidanceMatchesInstalledState(t *testing.T) {
	skipIntegration(t)
	for _, tc := range []struct {
		name            string
		codexInstalled  bool
		claudeInstalled bool
	}{
		{name: "fresh"},
		{name: "already installed", codexInstalled: true, claudeInstalled: true},
		{name: "Codex installed but Claude missing", codexInstalled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := setupUninstallAllTest(t, map[string]string{"codex": ".codex"})
			if tc.claudeInstalled {
				require.NoError(t, InstallProjectClaudeHooks(root))
			}
			if tc.codexInstalled {
				require.NoError(t, installCodexHooks(false))
			}

			var err error
			var output string
			withStdin(t, "", func() {
				output = captureStdoutForPlanCLI(t, func() { err = runIntegrateInteractive() })
			})
			require.NoError(t, err)
			if tc.claudeInstalled && tc.codexInstalled {
				assert.Contains(t, output, "All detected AI coworkers are already integrated")
			} else {
				assert.Contains(t, output, "No new integrations installed")
			}
			if tc.codexInstalled {
				assert.Contains(t, output, "/hooks to review and trust the SageOx hooks")
				assert.FileExists(t, filepath.Join(root, ".codex", "hooks.json"))
			} else {
				assert.NotContains(t, output, "/hooks")
				assert.NoFileExists(t, filepath.Join(root, ".codex", "hooks.json"))
			}
		})
	}
}
