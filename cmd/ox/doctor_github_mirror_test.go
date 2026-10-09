package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/githubmirror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// verdict names the outcome of a check result so a table can state it plainly.
func mirrorVerdict(r checkResult) string {
	switch {
	case r.skipped:
		return "skipped"
	case !r.passed:
		return "failed"
	case r.warning && r.priority == "info":
		return "info"
	case r.warning:
		return "warning"
	default:
		return "passed"
	}
}

// TestCheckGitHubMirror pins what `ox doctor` tells a coworker about the
// GitHub mirror relay.
//
// Failure prevented: a relay that has been failing for days (expired sign-in,
// route the server does not have, a refused repo) looking identical to a
// healthy one, so nobody learns the team's GitHub context stopped updating —
// and, the other way, a laptop that was merely closed over a weekend, or a
// mirror that never ran here, being reported as broken.
func TestCheckGitHubMirror(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	in := func(d time.Duration) time.Time { return now.Add(d) }

	tests := []struct {
		name     string
		state    githubmirror.State
		want     string   // verdict
		inMsg    []string // substrings of the row message
		inDetail []string // substrings of the detail line
		notDet   []string // substrings the detail must not carry
	}{
		{
			name:  "healthy relay",
			state: githubmirror.State{Repo: "acme/api", LastAttemptAt: ago(5 * time.Minute), LastSuccessAt: ago(5 * time.Minute), RepoStatus: githubmirror.RepoEnabled, Items: map[string]githubmirror.ItemState{"a": {}, "b": {}}},
			want:  "passed",
			inMsg: []string{"last relayed 5m ago", "2 item(s) tracked"},
		},
		{
			name:  "state saved before the first attempt",
			state: githubmirror.State{Repo: "acme/api"},
			want:  "passed",
			inMsg: []string{"no relay attempted yet"},
		},
		{
			name:  "attempted, nothing failed, nothing succeeded yet",
			state: githubmirror.State{Repo: "acme/api", LastAttemptAt: ago(time.Minute)},
			want:  "passed",
			inMsg: []string{"first relay in progress"},
		},
		{
			name:     "never succeeded and the attempts failed",
			state:    githubmirror.State{Repo: "acme/api", LastAttemptAt: ago(2 * time.Hour), LastError: "relay: 401 not signed in", LastErrorAt: ago(2 * time.Hour)},
			want:     "warning",
			inMsg:    []string{"never relayed successfully"},
			inDetail: []string{"last error 2h ago", "401 not signed in", "retries on its own"},
		},
		{
			name:     "never succeeded, error recorded without a time, still the latest attempt",
			state:    githubmirror.State{Repo: "acme/api", LastAttemptAt: ago(3 * time.Hour), LastError: "dial tcp: lookup failed"},
			want:     "warning",
			inMsg:    []string{"never relayed successfully"},
			inDetail: []string{"last error 3h ago", "lookup failed"},
		},
		{
			name:     "succeeded long ago and the newest event is an error",
			state:    githubmirror.State{Repo: "acme/api", LastAttemptAt: ago(time.Hour), LastSuccessAt: ago(3 * 24 * time.Hour), LastError: "relay: 503 store unavailable", LastErrorAt: ago(time.Hour)},
			want:     "warning",
			inMsg:    []string{"last success 3d ago"},
			inDetail: []string{"last error 1h ago", "503 store unavailable"},
		},
		{
			name:     "stale success and the error carries no time",
			state:    githubmirror.State{Repo: "acme/api", LastAttemptAt: ago(time.Hour), LastSuccessAt: ago(48 * time.Hour), LastError: "boom"},
			want:     "warning",
			inMsg:    []string{"last success 2d ago"},
			inDetail: []string{"last error 1h ago", "boom"},
		},
		{
			name:  "succeeded long ago but the recorded error is older than the success",
			state: githubmirror.State{Repo: "acme/api", LastAttemptAt: ago(3 * 24 * time.Hour), LastSuccessAt: ago(3 * 24 * time.Hour), LastError: "old, since recovered", LastErrorAt: ago(4 * 24 * time.Hour)},
			want:  "passed",
			inMsg: []string{"last relayed 3d ago"},
		},
		{
			name:  "no relay for days and no error: the machine was off, not the mirror",
			state: githubmirror.State{Repo: "acme/api", LastAttemptAt: ago(5 * 24 * time.Hour), LastSuccessAt: ago(5 * 24 * time.Hour)},
			want:  "passed",
			inMsg: []string{"last relayed 5d ago"},
		},
		{
			name:  "a recent error after a recent success is not yet a warning",
			state: githubmirror.State{Repo: "acme/api", LastAttemptAt: ago(time.Minute), LastSuccessAt: ago(2 * time.Hour), LastError: "relay: 429", LastErrorAt: ago(time.Minute)},
			want:  "passed",
		},
		{
			name:     "backing off after a recent error",
			state:    githubmirror.State{Repo: "acme/api", LastAttemptAt: ago(time.Minute), LastSuccessAt: ago(2 * time.Hour), LastError: "relay: 429 busy", LastErrorAt: ago(time.Minute), NextAllowedAt: in(15 * time.Minute)},
			want:     "info",
			inMsg:    []string{"backing off until 2026-10-08 12:15 UTC"},
			inDetail: []string{"reason: relay: 429 busy"},
		},
		{
			name:     "backing off with no reason recorded",
			state:    githubmirror.State{Repo: "acme/api", LastSuccessAt: ago(time.Hour), NextAllowedAt: in(time.Hour)},
			want:     "info",
			inMsg:    []string{"backing off until"},
			inDetail: []string{"no reason recorded"},
		},
		{
			name:  "a backoff window that already passed is nothing to report",
			state: githubmirror.State{Repo: "acme/api", LastSuccessAt: ago(time.Hour), NextAllowedAt: ago(time.Minute)},
			want:  "passed",
		},
		{
			name:     "a failure outranks the backoff it caused, and says when the retry is",
			state:    githubmirror.State{Repo: "acme/api", LastAttemptAt: ago(time.Hour), LastError: "relay: 404 not enabled", LastErrorAt: ago(time.Hour), NextAllowedAt: in(23 * time.Hour)},
			want:     "warning",
			inMsg:    []string{"never relayed successfully"},
			inDetail: []string{"404 not enabled", "next attempt after 2026-10-09 11:00 UTC"},
		},
		{
			name:     "private repo the team has not opted in",
			state:    githubmirror.State{Repo: "acme/secret", RepoStatus: githubmirror.RepoNotOptedIn, LastAttemptAt: ago(time.Hour), NextAllowedAt: in(23 * time.Hour)},
			want:     "info",
			inMsg:    []string{"server declined this repo (not_opted_in)"},
			inDetail: []string{"acme/secret is private", "has not opted in", "next attempt after 2026-10-09 11:00 UTC"},
		},
		{
			name:     "repo not linked to the team",
			state:    githubmirror.State{Repo: "acme/api", RepoStatus: githubmirror.RepoNotLinked, LastAttemptAt: ago(time.Hour)},
			want:     "info",
			inMsg:    []string{"(not_linked)"},
			inDetail: []string{"acme/api is not linked to this team"},
		},
		{
			name:     "repo refused for another reason",
			state:    githubmirror.State{Repo: "acme/api", RepoStatus: githubmirror.RepoNotEligible},
			want:     "info",
			inMsg:    []string{"(not_eligible)"},
			inDetail: []string{"will not mirror acme/api"},
		},
		{
			name:     "a status this client does not know is still named",
			state:    githubmirror.State{Repo: "acme/api", RepoStatus: "quota_exceeded"},
			want:     "info",
			inMsg:    []string{"(quota_exceeded)"},
			inDetail: []string{`"quota_exceeded"`},
		},
		{
			name:     "a refused repo is explained, not alarming, even with an error on file",
			state:    githubmirror.State{Repo: "acme/secret", RepoStatus: githubmirror.RepoNotOptedIn, LastAttemptAt: ago(time.Hour), LastError: "stale transport error", LastErrorAt: ago(2 * time.Hour)},
			want:     "info",
			inMsg:    []string{"(not_opted_in)"},
			notDet:   []string{"stale transport error"},
			inDetail: []string{"has not opted in"},
		},
		{
			name:  "an enabled repo is healthy",
			state: githubmirror.State{Repo: "acme/api", RepoStatus: githubmirror.RepoEnabled, LastSuccessAt: ago(time.Minute), LastAttemptAt: ago(time.Minute)},
			want:  "passed",
		},
		{
			name:     "a multi-line, very long error stays on one bounded line",
			state:    githubmirror.State{Repo: "acme/api", LastAttemptAt: ago(time.Hour), LastError: "relay failed:\n\t" + strings.Repeat("x", 400), LastErrorAt: ago(time.Hour)},
			want:     "warning",
			inDetail: []string{"relay failed: xxx", "…"},
			notDet:   []string{"\n", "\t", strings.Repeat("x", 250)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger := t.TempDir()
			state := tt.state
			require.NoError(t, githubmirror.SaveState(ledger, &state))

			got := checkGitHubMirrorAt(ledger, now)

			assert.Equal(t, tt.want, mirrorVerdict(got), "message=%q detail=%q", got.message, got.detail)
			assert.Equal(t, githubMirrorCheckName, got.name)
			for _, want := range tt.inMsg {
				assert.Contains(t, got.message, want)
			}
			for _, want := range tt.inDetail {
				assert.Contains(t, got.detail, want)
			}
			for _, unwanted := range tt.notDet {
				assert.NotContains(t, got.detail, unwanted)
			}
		})
	}
}

// TestCheckGitHubMirror_UnreadableState: a state file doctor cannot read must
// be reported, never read as "no state yet".
// Failure prevented: a corrupt or unreadable state file making the mirror look
// like it was never turned on, hiding a daemon that cannot make progress.
func TestCheckGitHubMirror_UnreadableState(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		plant func(t *testing.T, statePath string)
	}{
		{
			name: "garbage bytes",
			plant: func(t *testing.T, statePath string) {
				require.NoError(t, os.WriteFile(statePath, []byte("\x00\x01 not json"), 0o600))
			},
		},
		{
			name: "truncated json",
			plant: func(t *testing.T, statePath string) {
				require.NoError(t, os.WriteFile(statePath, []byte(`{"version":1,"repo":"acme/api","items":{`), 0o600))
			},
		},
		{
			name: "written by a newer ox",
			plant: func(t *testing.T, statePath string) {
				require.NoError(t, os.WriteFile(statePath, []byte(`{"version":999}`), 0o600))
			},
		},
		{
			// portable stand-in for an unreadable file: a directory where the
			// file belongs fails to read on every platform
			name: "a directory where the file belongs",
			plant: func(t *testing.T, statePath string) {
				require.NoError(t, os.Mkdir(statePath, 0o755))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger := t.TempDir()
			statePath := githubmirror.StatePath(ledger)
			require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
			tt.plant(t, statePath)

			got := checkGitHubMirrorAt(ledger, now)

			assert.Equal(t, "warning", mirrorVerdict(got), "message=%q detail=%q", got.message, got.detail)
			assert.Contains(t, got.message, "relay state unreadable")
			assert.Contains(t, got.detail, "safe to delete")
			assert.NotContains(t, got.detail, "\n")
		})
	}
}

// TestCheckGitHubMirror_NotActive: no ledger, or a ledger the daemon never
// wrote mirror state into, is "not active here" — a skip, not a pass and not a
// warning.
// Failure prevented: every coworker whose team does not use the mirror seeing
// a GitHub mirror row (or a warning) for a feature they never turned on.
func TestCheckGitHubMirror_NotActive(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	t.Run("no ledger", func(t *testing.T) {
		got := checkGitHubMirrorAt("", now)
		assert.Equal(t, "skipped", mirrorVerdict(got))
	})

	t.Run("ledger without mirror state", func(t *testing.T) {
		got := checkGitHubMirrorAt(t.TempDir(), now)
		assert.Equal(t, "skipped", mirrorVerdict(got))
		assert.Contains(t, got.message, "not active")
	})

	t.Run("state directory exists but is empty", func(t *testing.T) {
		ledger := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Dir(githubmirror.StatePath(ledger)), 0o755))
		got := checkGitHubMirrorAt(ledger, now)
		assert.Equal(t, "skipped", mirrorVerdict(got))
	})
}

// TestGitHubMirrorCheck_Registration: the check is report-only and runs on the
// default `ox doctor` path.
// Failure prevented: a registered check nobody runs (the ledger git health
// order drifting from the registry), or a future edit giving a read-only
// report a repair that rewrites the daemon's state file.
func TestGitHubMirrorCheck_Registration(t *testing.T) {
	check := GetDoctorCheck(CheckSlugGitHubMirror)
	require.NotNil(t, check, "the check must be registered")
	assert.Equal(t, FixLevelCheckOnly, check.FixLevel, "nothing about the mirror is safe to repair from the CLI")
	assert.Equal(t, ledgerGitHealthCategory, check.Category)
	assert.Contains(t, ledgerGitHealthOrder, CheckSlugGitHubMirror, "a registered check missing from the order never runs")
	require.NotNil(t, check.Run)

	// even a warning-grade state is only reported, never touched
	ledger := t.TempDir()
	require.NoError(t, githubmirror.SaveState(ledger, &githubmirror.State{Repo: "acme/api", LastAttemptAt: time.Now(), LastError: "boom"}))
	before, err := os.ReadFile(githubmirror.StatePath(ledger))
	require.NoError(t, err)
	got := checkGitHubMirrorAt(ledger, time.Now())
	require.Equal(t, "warning", mirrorVerdict(got), "the fixture must be warning-grade for this to prove anything")
	after, err := os.ReadFile(githubmirror.StatePath(ledger))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "doctor must never rewrite the daemon's state file")
}
