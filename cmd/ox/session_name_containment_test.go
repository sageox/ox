package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Session names typed on the command line stay inside the sessions dir ---

// TestResolveSessionInDir_RejectsPathShapedNames verifies that a session name
// must be a single directory entry under the sessions dir.
// Failure prevented: the exact-match Stat accepted "..", "", or "../x" whenever
// that path existed, so `ox session upload|download|lint|regenerate` acted on a
// directory outside the sessions folder (or the folder itself).
func TestResolveSessionInDir_RejectsPathShapedNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	full := "2026-01-06T14-32-ryan-Ox7f3a"
	require.NoError(t, os.MkdirAll(filepath.Join(sessionsDir, full), 0o755))
	// Every traversal below names a directory that exists, so only the name
	// check can stop the resolver from returning it.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "outside", "nested"), 0o755))

	tests := []struct {
		name string
		arg  string
		want string
	}{
		{name: "full directory name", arg: full, want: full},
		{name: "agent ID suffix", arg: "Ox7f3a", want: full},
		{name: "parent", arg: ".."},
		{name: "sibling of the sessions dir", arg: "../outside"},
		{name: "traversal hidden mid-path", arg: full + "/../../outside"},
		{name: "nested path", arg: "../outside/nested"},
		{name: "absolute path", arg: filepath.Join(root, "outside")},
		{name: "current directory", arg: "."},
		{name: "empty", arg: ""},
		{name: "backslash separator", arg: `..\outside`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveSessionInDir(sessionsDir, tt.arg)
			if tt.want == "" {
				require.Error(t, err, "resolved %q to %q", tt.arg, got)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestSessionUpload_PathShapedNameWritesNothingOutsideSessions verifies the
// downstream effect through the real command.
// Failure prevented: `ox session upload ../outside` wrote meta.json into a
// directory outside the sessions folder and tried to upload its raw.jsonl to
// the team's LFS store before failing.
func TestSessionUpload_PathShapedNameWritesNothingOutsideSessions(t *testing.T) {
	// Any network call lands here and fails; nothing reaches a real endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	setupAuthRenderEnv(t, srv.URL, validTeamToken)
	root := createInitializedProjectWithConfig(t, nil)
	hostedTestGit(t, root, "init")
	t.Chdir(root)
	ledger := filepath.Join(root, "ledger")
	hostedTestGit(t, root, "init", ledger)
	require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledger}}))
	require.NoError(t, os.MkdirAll(filepath.Join(ledger, "sessions"), 0o755))

	// A directory that looks like a session but sits outside the sessions dir.
	outside := filepath.Join(ledger, "outside")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	raw := []byte("{\"type\":\"user\",\"content\":\"hello\",\"ts\":\"2026-03-11T10:00:00Z\",\"seq\":1}\n")
	require.NoError(t, os.WriteFile(filepath.Join(outside, "raw.jsonl"), raw, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "summary.md"), []byte("summary\n"), 0o600))

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	err := sessionUploadCmd.RunE(cmd, []string{"../outside"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "single path component")
	assert.NoFileExists(t, filepath.Join(outside, "meta.json"), "upload wrote meta.json outside the sessions dir")
}
