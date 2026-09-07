//go:build !short

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/stretchr/testify/require"
)

// --- Shared command-level E2E harness (bead ox-h58k) ---
//
// runInit and runAgentPrime had no end-to-end test between them. Every existing
// init/prime test exercised a helper in isolation — several carry comments like
// "this simulates the check in runInit" — so any logic living inside those two
// function bodies was uncoverable by construction: it shipped untested and
// dragged the changed-line coverage ratchet down on every PR that touched them.
//
// Both commands want the same four fakes (an endpoint, a credential, an
// isolated HOME, and a git repo), so they get ONE harness rather than two
// bespoke ones. Everything here is local: no network, no real keychain, no
// writes outside t.TempDir().

// oxE2E is a fully isolated ox environment: a git repo to run in, a stub
// SageOx endpoint, and a credential the CLI accepts.
type oxE2E struct {
	Root   string           // git repo the command runs in (also the process cwd)
	Server *httptest.Server // stub SageOx API
	TeamID string
	RepoID string

	mu       sync.Mutex
	requests []string // every path the CLI actually called, in order
}

// Requested returns the API paths the command hit, so a test can assert on the
// real call sequence rather than trusting a mock's own bookkeeping.
func (e *oxE2E) Requested() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.requests...)
}

// newOxE2E builds the isolated environment and leaves the process cwd inside
// the git repo. Every mutation is undone by t.Cleanup.
func newOxE2E(t *testing.T) *oxE2E {
	t.Helper()
	skipIntegration(t)

	env := &oxE2E{TeamID: "team-e2e", RepoID: "repo-e2e"}

	env.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.mu.Lock()
		env.requests = append(env.requests, r.URL.Path)
		env.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case auth.IntrospectEndpoint:
			// ox gates init on server-side introspection, not just a local
			// credential — a local-only fake would never get past runInit.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"active":         true,
				"principal_kind": "user",
				"scope":          "*",
				"token_type":     "Bearer",
				"expires_at":     time.Now().Add(time.Hour).Format(time.RFC3339),
				"user":           map[string]any{"id": "user-e2e", "email": "e2e@example.com"},
			})
		case "/api/v1/repo/init":
			_ = json.NewEncoder(w).Encode(api.RepoInitResponse{
				RepoID: env.RepoID,
				TeamID: env.TeamID,
			})
		default:
			// Unstubbed routes 404 loudly rather than hanging. Requested()
			// records them, so a test can see what the command reached for.
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not stubbed: " + r.URL.Path})
		}
	}))
	t.Cleanup(env.Server.Close)

	// Isolation. HOME matters as much as the XDG vars: paths.DataDir() falls
	// back to SageoxDir() when XDG mode is off, so setting XDG_DATA_HOME alone
	// still lets a test read and write the developer's real ~/.sageox.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("SAGEOX_ENDPOINT", env.Server.URL)

	// No daemon. runInit and runAgentPrime both start one on a real machine, and
	// a spawned daemon OUTLIVES the test: it keeps the test binary's process
	// group alive, so `go test ./cmd/ox/` hangs for minutes after the assertions
	// pass and leaks ox daemons onto the developer's machine. Observed directly —
	// four `ox.test daemon start --foreground` children still running nine
	// minutes into a suite that had nothing left to do.
	t.Setenv("OX_NO_DAEMON", "1")

	require.NoError(t, auth.SaveTokenForEndpoint(env.Server.URL, &auth.StoredToken{
		AccessToken: "e2e-access-token",
		TokenType:   "Bearer",
		Scope:       "*",
		ExpiresAt:   time.Now().Add(time.Hour),
	}))

	env.Root = newE2EGitRepo(t)

	// Commands resolve the repo from the process cwd.
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(env.Root))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	return env
}

// newE2EGitRepo creates a git repo with one commit — ox init requires at least
// one commit for repository fingerprinting.
func newE2EGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// macOS temp dirs are symlinked (/var -> /private/var); resolve now so the
	// path the test asserts on matches the one the CLI records.
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = resolved
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "e2e@example.com")
	run("config", "user.name", "E2E")
	require.NoError(t, os.WriteFile(filepath.Join(resolved, "README.md"), []byte("# e2e\n"), 0o644))
	run("add", "README.md")
	run("commit", "-q", "-m", "initial")
	return resolved
}

// stagedPaths returns everything currently in the git index.
func stagedPaths(t *testing.T, repo string) []string {
	t.Helper()
	cmd := exec.Command("git", "diff", "--cached", "--name-only")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git diff --cached: %s", out)

	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}
