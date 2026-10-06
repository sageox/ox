//go:build !windows

package agentwork

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Like codex_auth_test.go, this puts an extensionless "claude" script on PATH,
// which Windows LookPath never finds (it requires a PATHEXT extension).

// Failure prevented: Claude Desktop writes its account into .claude.json while
// the claude CLI holds no credentials, so import previewed sessions as ready
// and every summary then failed with "Not logged in", and the daemon picked a
// summarizer that could not run.
func TestClaudeUsabilityAsksTheCLI(t *testing.T) {
	const desktopAccount = `{"oauthAccount":{"emailAddress":"x"}}`
	const loggedOut = `printf '{"loggedIn": false, "authMethod": "none"}\n'; exit 1`
	const noAuthCommand = `printf "error: unknown command 'auth'\n" >&2; exit 1`
	for _, tc := range []struct {
		name, script, config, apiKey string
		slow                         bool
		authenticated                bool
	}{
		{name: "desktop account, CLI logged out", script: loggedOut, config: desktopAccount},
		{name: "CLI logged in, no account file", script: `printf '{"loggedIn": true, "authMethod": "claude.ai"}\n'`, authenticated: true},
		{name: "API key wins over a logged-out CLI", script: loggedOut, apiKey: "sk-test", authenticated: true},
		{name: "CLI without auth status falls back to the account file", script: noAuthCommand, config: desktopAccount, authenticated: true},
		{name: "CLI without auth status and no account", script: noAuthCommand, config: `{}`},
		{name: "unrecognized answer falls back to the account file", script: `printf 'Logged in\n'`, config: desktopAccount, authenticated: true},
		{name: "CLI that never answers falls back to the account file", script: `sleep 5`, config: desktopAccount, slow: true, authenticated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.slow && testing.Short() {
				t.Skip("short: waits out the 2s auth status timeout")
			}
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0o700))
			config := t.TempDir()
			if tc.config != "" {
				require.NoError(t, os.WriteFile(filepath.Join(config, ".claude.json"), []byte(tc.config), 0o600))
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("ANTHROPIC_API_KEY", tc.apiKey)
			t.Setenv("CLAUDE_CONFIG_DIR", config)

			got := CheckAgentUsability("claude")
			require.True(t, got.Installed)
			require.Equal(t, tc.authenticated, got.Authenticated, got.AuthDetail)
		})
	}
}
