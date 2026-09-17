package agentwork

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
