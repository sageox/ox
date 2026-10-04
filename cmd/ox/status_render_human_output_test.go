package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubVersionFetcher prevents renderStatusHumanOutput's calm-update-notice
// check from making a real GitHub network call in tests.
func stubVersionFetcher(t *testing.T) {
	t.Helper()
	old := latestReleaseFetcher
	latestReleaseFetcher = func() (string, error) { return "", errors.New("network disabled in test") }
	t.Cleanup(func() { latestReleaseFetcher = old })
}

// TestRenderStatusHumanOutput_AuthHints pins the auth-hint switch that used to
// live directly in statusCmd.RunE (untestable there) and now lives in the
// extracted renderStatusHumanOutput — the same split PR #979 relies on to gate
// the block behind --quiet. gitRoot="" keeps this hermetic: it skips the
// ledger/daemon/coworkers block, which makes real API/network calls.
func TestRenderStatusHumanOutput_AuthHints(t *testing.T) {
	isolateStatusTestAuth(t)
	stubVersionFetcher(t)

	authFile := filepath.Join(t.TempDir(), "auth.json")

	tests := []struct {
		name              string
		envTokenMalformed bool
		authErr           error
		wantSubstr        string
		wantNotSubstr     string
	}{
		{
			name:       "unreachable endpoint warns about connectivity, not login",
			authErr:    errWrap(auth.ErrEndpointUnreachable),
			wantSubstr: "could not reach the endpoint",
		},
		{
			name:          "unreachable endpoint never suggests re-authenticating",
			authErr:       errWrap(auth.ErrEndpointUnreachable),
			wantSubstr:    "auth state is unverified, not invalid",
			wantNotSubstr: "token refresh failed",
		},
		{
			name:          "non-connectivity auth error recommends re-authenticating",
			authErr:       errors.New("token refresh rejected"),
			wantSubstr:    "token refresh failed",
			wantNotSubstr: "could not reach the endpoint",
		},
		{
			name:       "no credential at all recommends ox login",
			authErr:    nil,
			wantSubstr: "ox login",
		},
		{
			name:              "malformed env token gets no extra hint",
			envTokenMalformed: true,
			authErr:           nil,
			wantNotSubstr:     "could not reach the endpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := string(captureRealStdout(t, func() {
				renderStatusHumanOutput("/some/dir", "", false, nil, nil, nil,
					statusBubblesSummary{}, nil, nil, authFile, t.TempDir(), tt.envTokenMalformed, tt.authErr)
			}))

			if tt.wantSubstr != "" {
				assert.Contains(t, out, tt.wantSubstr)
			}
			if tt.wantNotSubstr != "" {
				assert.NotContains(t, out, tt.wantNotSubstr)
			}
		})
	}
}

// errWrap wraps err the way a real call site does (fmt.Errorf("...: %w", err)),
// without pulling in fmt just for one helper.
func errWrap(err error) error {
	return &wrappedErr{err}
}

type wrappedErr struct{ err error }

func (w *wrappedErr) Error() string { return "context: " + w.err.Error() }
func (w *wrappedErr) Unwrap() error { return w.err }

// TestRenderStatusHumanOutput_GitRootBranches covers the project-status action
// hints and the git-repo block (ledger, daemon sync, coworkers, agent tasks)
// that only render when gitRoot != "". Follows the existing
// TestRenderGitReposSection_* pattern: point the project at an unroutable
// endpoint so the section renders its "unavailable" branch instead of making
// a real network call.
func TestRenderStatusHumanOutput_GitRootBranches(t *testing.T) {
	isolateStatusTestAuth(t)
	stubVersionFetcher(t)

	gitRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(gitRoot, ".sageox"), 0755))
	require.NoError(t, config.SaveProjectConfig(gitRoot, &config.ProjectConfig{
		Endpoint: "http://127.0.0.1:1",
	}))
	authFile := filepath.Join(t.TempDir(), "auth.json")

	t.Run("uninitialized project surfaces ox init hint", func(t *testing.T) {
		out := string(captureRealStdout(t, func() {
			renderStatusHumanOutput(gitRoot, gitRoot, false, nil, nil, nil,
				statusBubblesSummary{}, nil, nil, authFile, t.TempDir(), false, nil)
		}))
		assert.Contains(t, out, "ox init")
	})

	t.Run("initialized project without codeStats surfaces ox code index hint", func(t *testing.T) {
		out := string(captureRealStdout(t, func() {
			renderStatusHumanOutput(gitRoot, gitRoot, true, nil, nil, nil,
				statusBubblesSummary{}, nil, nil, authFile, t.TempDir(), false, nil)
		}))
		assert.Contains(t, out, "ox code index")
	})

	t.Run("initialized project with codeStats skips the index hint", func(t *testing.T) {
		stats := &daemon.CodeDBStats{Commits: 5, IndexExists: true}
		out := string(captureRealStdout(t, func() {
			renderStatusHumanOutput(gitRoot, gitRoot, true, nil, nil, nil,
				statusBubblesSummary{}, stats, nil, authFile, t.TempDir(), false, nil)
		}))
		assert.NotContains(t, out, "ox code index")
	})

	t.Run("renders the git-repo block sections without panicking", func(t *testing.T) {
		out := string(captureRealStdout(t, func() {
			renderStatusHumanOutput(gitRoot, gitRoot, true, &config.LocalConfig{}, nil, nil,
				statusBubblesSummary{}, nil, nil, authFile, t.TempDir(), false, nil)
		}))
		assert.Contains(t, out, "Daemon Sync", "the git-repo block must render the daemon sync section")
	})
}

// TestRenderStatusHumanOutput_CalmUpdateNoticeRendersWhenDue covers the
// trailing "update available" line: a fresh (non-stale) cache reporting a
// newer version, on a notify line that has never fired, must print the calm
// notice and record it. A stale/empty cache instead takes the network-refetch
// path, which the other tests in this file stub out entirely — this is the
// one case that needs a real, non-stale cache instead.
func TestRenderStatusHumanOutput_CalmUpdateNoticeRendersWhenDue(t *testing.T) {
	isolateStatusTestAuth(t)
	atTerminal(t)
	useTestCacheDir(t)
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion: "v999.0.0",
		CheckedAt:     time.Now(),
	})

	authFile := filepath.Join(t.TempDir(), "auth.json")
	out := string(captureRealStdout(t, func() {
		renderStatusHumanOutput("/some/dir", "", false, nil, nil, nil,
			statusBubblesSummary{}, nil, nil, authFile, t.TempDir(), false, nil)
	}))

	assert.Contains(t, out, "999.0.0", "a newer cached version due for notice must be printed")
	assert.Contains(t, out, "ox upgrade")
}
