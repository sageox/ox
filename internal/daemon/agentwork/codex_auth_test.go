//go:build !windows

package agentwork

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file writes an extensionless shell-script fixture named "codex" onto
// PATH and expects checkCodexUsability's exec.LookPath("codex") to find it.
// Windows resolves LookPath via PATHEXT extensions (.exe/.cmd/.bat/...), so
// an extensionless "codex" is never found there and got.Installed would be
// false — see .claude/rules/testing.md "Failure Paths That Render
// Identically To Success" (the PATH-manipulation row) for why an honest
// build-tag skip beats a fixture that can't actually exercise this platform.

func TestCodexAuthenticationStreamsAndExitStatus(t *testing.T) {
	for _, tc := range []struct {
		name, script  string
		authenticated bool
	}{
		{"stdout", "printf 'Logged in using ChatGPT\\n'", true},
		{"stderr", "printf 'Logged in using ChatGPT\\n' >&2", true},
		{"warning_before_status", "printf 'host warning\\n' >&2; printf 'Logged in using ChatGPT\\n' >&2", true},
		{"failed_exit", "printf 'Logged in using ChatGPT\\n'; exit 1", false},
		{"not_logged_in", "printf 'Not logged in\\n' >&2", false},
		{"unrecognized_status", "printf 'unrecognized status\\n'", false},
		{"timeout", "sleep 5; printf 'Logged in using ChatGPT\\n'", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("OPENAI_API_KEY", "")
			got := checkCodexUsability()
			require.True(t, got.Installed)
			require.Equal(t, tc.authenticated, got.Authenticated)
		})
	}
}
