package gitserver

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a plain-http loopback Ledger gets no credential helper (or
// one scoped https://), so every later git op dies with "could not read
// Username ... terminal prompts disabled".
func TestInstallCredentialHelper_Scope(t *testing.T) {
	tests := []struct {
		name      string
		cfg       HelperConfig
		wantKey   string
		wantError bool
	}{
		{"default scheme is https", HelperConfig{Host: "git.sageox.ai"}, "credential.https://git.sageox.ai.helper", false},
		{"explicit https", HelperConfig{Scheme: "https", Host: "git.sageox.ai"}, "credential.https://git.sageox.ai.helper", false},
		{"http loopback with port", HelperConfig{Scheme: "http", Host: "localhost:8080"}, "credential.http://localhost:8080.helper", false},
		{"unsupported scheme", HelperConfig{Scheme: "ftp", Host: "x"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, exec.Command("git", "-C", dir, "init", "-q").Run())
			tt.cfg.Command = "!ox git-credential-helper"
			err := InstallCredentialHelper(dir, tt.cfg)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			got, err := readGitConfigLocal(dir, tt.wantKey)
			require.NoError(t, err)
			assert.Equal(t, "!ox git-credential-helper", got)
		})
	}
}

// Failure prevented: ledger sync migration skips loopback http remotes, or
// starts installing a helper for non-loopback http (credential leak risk).
func TestMigrateLedgerCredentials_HelperScopeByRemote(t *testing.T) {
	tests := []struct {
		remote  string
		wantKey string // "" = no helper expected
	}{
		{"https://git.sageox.ai/team/ledger.git", "credential.https://git.sageox.ai.helper"},
		{"http://localhost:9000/team/ledger.git", "credential.http://localhost:9000.helper"},
		{"http://127.0.0.1:9000/team/ledger.git", "credential.http://127.0.0.1:9000.helper"},
		{"http://example.com/team/ledger.git", ""},
	}
	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, exec.Command("git", "-C", dir, "init", "-q").Run())
			require.NoError(t, exec.Command("git", "-C", dir, "remote", "add", "origin", tt.remote).Run())
			_, err := MigrateLedgerCredentials(dir, "!ox git-credential-helper")
			require.NoError(t, err)
			out, err := exec.Command("git", "-C", dir, "config", "--local", "--get-regexp", `^credential\..*\.helper$`).Output()
			if err != nil {
				// exit 1 is git's "no matching key"; anything else is a failed lookup, not an empty result
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, 1, exitErr.ExitCode(), "git config lookup failed: %v", err)
			}
			if tt.wantKey == "" {
				assert.Empty(t, strings.TrimSpace(string(out)), "no helper for %s", tt.remote)
				return
			}
			assert.Contains(t, string(out), tt.wantKey)
		})
	}
}

// Customer-visible outcome: pushing to a plain-http loopback git server with
// Basic auth succeeds non-interactively once the helper is installed with the
// http scope. Proves the scope really matches (an https scope would prompt and
// fail under GIT_TERMINAL_PROMPT=0).
func TestInstallCredentialHelper_LoopbackHTTPPushNeedsNoPrompt(t *testing.T) {
	execPath, err := exec.Command("git", "--exec-path").Output()
	require.NoError(t, err)
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend not available")
	}

	root := t.TempDir()
	bare := filepath.Join(root, "ledger.git")
	require.NoError(t, exec.Command("git", "init", "-q", "--bare", bare).Run())
	require.NoError(t, exec.Command("git", "-C", bare, "config", "http.receivepack", "true").Run())

	cgiH := &cgi.Handler{Path: backend, Env: []string{
		"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1",
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "ox" || p != "secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="ledger"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		cgiH.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	work := t.TempDir()
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", append([]string{"-C", work}, args...)...)
		// safe: git needs PATH etc.; askpass vars are dropped and global/system config isolated
		var env []string
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "GIT_ASKPASS=") || strings.HasPrefix(kv, "SSH_ASKPASS=") {
				continue
			}
			env = append(env, kv)
		}
		cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL="+os.DevNull, "HOME="+t.TempDir())
		return cmd.CombinedOutput()
	}
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "T"}, {"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
		{"remote", "add", "origin", srv.URL + "/ledger.git"},
	} {
		out, err := git(a...)
		require.NoError(t, err, "%s", out)
	}
	require.NoError(t, os.WriteFile(filepath.Join(work, "f.txt"), []byte("x"), 0o644))
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		out, err := git(a...)
		require.NoError(t, err, "%s", out)
	}

	// without a helper the push must fail on the missing username (the bug)
	out, err := git("push", "origin", "main")
	require.Error(t, err)
	assert.Contains(t, string(out), "terminal prompts disabled")

	// install via the same path clone/sync use for this remote URL
	changed, err := MigrateLedgerCredentials(work, `!f() { echo username=ox; echo password=secret; }; f`)
	require.NoError(t, err)
	_ = changed
	out, err = git("push", "origin", "main")
	require.NoError(t, err, "%s", out)
}
