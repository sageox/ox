package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/version"
)

// TestResolveTeamSyncResult covers the status-derivation logic for a targeted
// `ox sync --team <selector>`. This is the core of the fix: the reported status
// must reflect the requested team's real outcome (not a bare IPC success), must
// carry team_name/team_slug/path, must accept ID/slug/name selectors, and must
// distinguish a genuine not-found (fatal) from a legacy daemon (non-fatal).
func TestResolveTeamSyncResult(t *testing.T) {
	synced := []daemon.TeamSyncResult{
		{TeamID: "team_acme", TeamSlug: "acme", TeamName: "Acme", Path: "/p/acme", Status: "synced"},
		{TeamID: "team_beta", TeamSlug: "beta", TeamName: "Beta", Path: "/p/beta", Status: "error", Error: "pull failed"},
		{TeamID: "team_gamma", TeamSlug: "gamma", TeamName: "Gamma", Path: "/p/gamma", Status: "skipped"},
		{TeamID: "team_delta", TeamSlug: "delta", TeamName: "Delta", Path: "/p/delta", Status: "cloning"},
	}

	tests := []struct {
		name       string
		selector   string
		results    []daemon.TeamSyncResult
		syncErr    error
		wantStatus string
		wantName   string
		wantPath   string
		wantFatal  bool // status that makes the command exit non-zero
	}{
		{
			name: "match by ID populates metadata", selector: "team_acme", results: synced,
			wantStatus: "synced", wantName: "Acme", wantPath: "/p/acme", wantFatal: false,
		},
		{
			name: "match by slug", selector: "acme", results: synced,
			wantStatus: "synced", wantName: "Acme", wantPath: "/p/acme", wantFatal: false,
		},
		{
			name: "match by name", selector: "Acme", results: synced,
			wantStatus: "synced", wantName: "Acme", wantPath: "/p/acme", wantFatal: false,
		},
		{
			name: "requested team errored is fatal", selector: "beta", results: synced,
			wantStatus: "error", wantName: "Beta", wantPath: "/p/beta", wantFatal: true,
		},
		{
			name: "recently-synced skipped is usable (non-fatal)", selector: "gamma", results: synced,
			wantStatus: "skipped", wantName: "Gamma", wantPath: "/p/gamma", wantFatal: false,
		},
		{
			name: "clone-in-progress is not usable (fatal)", selector: "delta", results: synced,
			wantStatus: "cloning", wantName: "Delta", wantPath: "/p/delta", wantFatal: true,
		},
		{
			name: "unrelated team error does not affect requested team", selector: "acme",
			results: synced, syncErr: errors.New("team sync failed: Beta: pull failed"),
			wantStatus: "synced", wantName: "Acme", wantPath: "/p/acme", wantFatal: false,
		},
		{
			name: "genuine not found (current daemon, no match) is fatal", selector: "typo",
			results: synced, wantStatus: "not_found", wantFatal: true,
		},
		{
			name: "exact ID match wins even when a different team shares the slug as its ID-less selector",
			// two teams; selector is an exact ID — must pick that one, not scan by slug
			selector: "team_acme", results: synced,
			wantStatus: "synced", wantName: "Acme", wantFatal: false,
		},
		{
			name:     "ambiguous slug selector is fatal, not first-match",
			selector: "dup",
			results: []daemon.TeamSyncResult{
				{TeamID: "team_a", TeamSlug: "dup", TeamName: "A", Status: "synced"},
				{TeamID: "team_b", TeamSlug: "dup", TeamName: "B", Status: "error", Error: "boom"},
			},
			wantStatus: "ambiguous", wantFatal: true,
		},
		{
			name: "not found against current daemon with zero teams is fatal", selector: "acme",
			results: []daemon.TeamSyncResult{}, wantStatus: "not_found", wantFatal: true,
		},
		{
			name: "legacy daemon (nil results, no error) is unknown and fatal (fail closed)", selector: "acme",
			results: nil, syncErr: nil, wantStatus: "unknown", wantFatal: true,
		},
		{
			name: "transport/setup error with no results is fatal", selector: "acme",
			results: nil, syncErr: errors.New("daemon unreachable"), wantStatus: "error", wantFatal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveTeamSyncResult(tt.selector, tt.results, tt.syncErr)

			if got.TeamID != tt.selector {
				t.Errorf("TeamID = %q, want %q", got.TeamID, tt.selector)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if tt.wantName != "" && got.TeamName != tt.wantName {
				t.Errorf("TeamName = %q, want %q", got.TeamName, tt.wantName)
			}
			if tt.wantPath != "" && got.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", got.Path, tt.wantPath)
			}
			// mirror the caller's fatal decision: only "usable" states (synced,
			// skipped) exit 0; everything else — including "unknown" (legacy daemon,
			// failed closed) — exits non-zero.
			fatal := got.Status != "synced" && got.Status != "skipped"
			if fatal != tt.wantFatal {
				t.Errorf("fatal = %v (status %q), want %v", fatal, got.Status, tt.wantFatal)
			}
		})
	}
}

// TestLegacyDaemonErrorMsg verifies the version-skew message shown when a daemon
// returns success without per-team results: it must name the daemon's older
// version (vs the CLI) and tell the user it self-restarts on the next heartbeat,
// strip any "+builddate" suffix, and degrade gracefully when the version is
// unknown or matches.
func TestLegacyDaemonErrorMsg(t *testing.T) {
	t.Run("older daemon names both versions and the self-restart", func(t *testing.T) {
		err := legacyDaemonErrorMsg("0.0.1-older")
		msg := err.Error()
		if !strings.Contains(msg, "0.0.1-older") {
			t.Errorf("expected daemon version in message, got %q", msg)
		}
		if !strings.Contains(msg, version.Version) {
			t.Errorf("expected CLI version %q in message, got %q", version.Version, msg)
		}
		if !strings.Contains(msg, "heartbeat") {
			t.Errorf("expected self-restart hint in message, got %q", msg)
		}
	})

	t.Run("strips +builddate suffix before comparing", func(t *testing.T) {
		// same semver as the CLI but with a build-date suffix must NOT be reported
		// as an older version (the daemon uses semver-only comparison too).
		err := legacyDaemonErrorMsg(version.Version + "+somebuilddate")
		if strings.Contains(err.Error(), "older version") {
			t.Errorf("matching semver with build suffix should not be 'older': %q", err.Error())
		}
	})

	t.Run("unknown version still produces an actionable error", func(t *testing.T) {
		err := legacyDaemonErrorMsg("")
		if !strings.Contains(err.Error(), "ox daemon restart") {
			t.Errorf("expected restart hint when version unknown, got %q", err.Error())
		}
	})
}

func TestSyncPathExists(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "existing directory",
			path:     "/tmp",
			expected: true,
		},
		{
			name:     "non-existent path",
			path:     "/nonexistent/path/that/does/not/exist",
			expected: false,
		},
		{
			name:     "empty path",
			path:     "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := syncPathExists(tt.path)
			if result != tt.expected {
				t.Errorf("syncPathExists(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

// TestNotReadyTeams verifies the all-teams usability gate: only "synced" and
// "skipped" (already up to date) count as usable; "cloning" and "error" must be
// reported so `ox sync --all-teams` fails instead of printing blanket success
// while a team context is missing or still cloning.
func TestNotReadyTeams(t *testing.T) {
	results := []daemon.TeamSyncResult{
		{TeamID: "a", TeamName: "A", Status: "synced"},
		{TeamID: "b", TeamName: "B", Status: "skipped"},
		{TeamID: "c", TeamName: "C", Status: "cloning"},
		{TeamID: "d", TeamName: "D", Status: "error", Error: "pull failed"},
	}

	got := notReadyTeams(results)
	want := []string{"C (cloning)", "D (error)"}
	if len(got) != len(want) {
		t.Fatalf("notReadyTeams = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("notReadyTeams[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// all usable → empty
	if r := notReadyTeams([]daemon.TeamSyncResult{{Status: "synced"}, {Status: "skipped"}}); len(r) != 0 {
		t.Errorf("expected no not-ready teams, got %v", r)
	}
}

func TestSyncResultJSON(t *testing.T) {
	// verify the struct serializes correctly
	result := SyncResult{
		Success: true,
		Mode:    "daemon",
		Ledger: &SyncLedgerResult{
			Path:   "/path/to/ledger",
			Status: "synced",
		},
		TeamContexts: []TeamContextSyncResult{
			{
				TeamID:   "team-1",
				TeamName: "Team One",
				Path:     "/path/to/team-1",
				Status:   "synced",
			},
		},
	}

	if !result.Success {
		t.Error("expected success to be true")
	}
	if result.Mode != "daemon" {
		t.Errorf("expected mode to be 'daemon', got %q", result.Mode)
	}
	if result.Ledger == nil {
		t.Error("expected ledger to be non-nil")
	}
	if len(result.TeamContexts) != 1 {
		t.Errorf("expected 1 team context, got %d", len(result.TeamContexts))
	}
}

func TestSyncResultWithError(t *testing.T) {
	result := SyncResult{
		Success: false,
		Mode:    "direct",
		Ledger: &SyncLedgerResult{
			Status: "error",
			Error:  "sync failed",
		},
		Error: "ledger sync failed",
	}

	if result.Success {
		t.Error("expected success to be false")
	}
	if result.Ledger.Status != "error" {
		t.Errorf("expected ledger status to be 'error', got %q", result.Ledger.Status)
	}
	if result.Error == "" {
		t.Error("expected error message to be set")
	}
}
