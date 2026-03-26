package main

import (
	"strings"
	"testing"

	"github.com/sageox/ox/internal/daemon"
)

func TestRenderVisibility_Coverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		visibility string
		wantText   string
	}{
		{"public lowercase", "public", "public"},
		{"public uppercase", "Public", "Public"},
		{"private lowercase", "private", "private"},
		{"private uppercase", "Private", "Private"},
		{"unknown value", "internal", "internal"},
		{"empty string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderVisibility(tt.visibility)
			if !strings.Contains(got, tt.wantText) {
				t.Errorf("renderVisibility(%q) = %q, want it to contain %q", tt.visibility, got, tt.wantText)
			}
		})
	}
}

func TestRenderVisibilityWithAccess_ExtraCoverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		visibility  string
		accessLevel string
		wantContain string
	}{
		{"viewer shows read-only", "private", "viewer", "read-only"},
		{"member shows member", "private", "member", "member"},
		{"owner shows owner", "public", "owner", "owner"},
		{"empty access no suffix", "public", "", "public"},
		{"admin shows admin", "private", "admin", "admin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderVisibilityWithAccess(tt.visibility, tt.accessLevel)
			if !strings.Contains(got, tt.wantContain) {
				t.Errorf("renderVisibilityWithAccess(%q, %q) = %q, want it to contain %q",
					tt.visibility, tt.accessLevel, got, tt.wantContain)
			}
		})
	}
}

func TestFormatValue_ExtraCoverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    string
		semantic string
		contains string
	}{
		{"success prefix", "ok", "success", "ok"},
		{"error prefix", "bad", "error", "bad"},
		{"warning prefix", "caution", "warning", "caution"},
		{"highlight no prefix", "important", "highlight", "important"},
		{"muted no prefix", "dim", "muted", "dim"},
		{"default no prefix", "plain", "default", "plain"},
		{"unknown semantic", "xyz", "unknown", "xyz"},
		{"empty value success", "", "success", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatValue(tt.value, tt.semantic)
			if !strings.Contains(got, tt.contains) {
				t.Errorf("formatValue(%q, %q) = %q, want it to contain %q",
					tt.value, tt.semantic, got, tt.contains)
			}
		})
	}
}

func TestRenderTable_MultipleRows(t *testing.T) {
	t.Parallel()

	rows := [][]string{
		{"Status", "active", "success"},
		{"Version", "1.0", "muted"},
		{"Error", "none"},
	}

	got := renderTable("Test Section", rows)
	if !strings.Contains(got, "Test Section") {
		t.Error("renderTable should contain the header")
	}
	if !strings.Contains(got, "active") {
		t.Error("renderTable should contain the value 'active'")
	}
	if !strings.Contains(got, "1.0") {
		t.Error("renderTable should contain the value '1.0'")
	}
	if !strings.Contains(got, "none") {
		t.Error("renderTable should contain the value 'none'")
	}
}

func TestRenderTable_NoRows(t *testing.T) {
	t.Parallel()

	got := renderTable("Empty", [][]string{})
	if !strings.Contains(got, "Empty") {
		t.Error("renderTable with no rows should still contain the header")
	}
}

func TestIsDaemonBootstrapping_ExtraCoverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status *daemon.StatusData
		want   bool
	}{
		{
			name:   "nil status",
			status: nil,
			want:   false,
		},
		{
			name: "not running",
			status: &daemon.StatusData{
				Running: false,
			},
			want: false,
		},
		{
			name: "running with syncs",
			status: &daemon.StatusData{
				Running:    true,
				TotalSyncs: 5,
				Uptime:     30_000_000_000, // 30s
				LedgerPath: "/some/path",
			},
			want: false,
		},
		{
			name: "running zero syncs no repos",
			status: &daemon.StatusData{
				Running:    true,
				TotalSyncs: 0,
				Uptime:     10_000_000_000, // 10s
			},
			want: false, // no configured repos => not bootstrapping
		},
		{
			name: "running zero syncs with ledger short uptime",
			status: &daemon.StatusData{
				Running:    true,
				TotalSyncs: 0,
				Uptime:     10_000_000_000, // 10s
				LedgerPath: "/some/ledger",
			},
			want: true,
		},
		{
			name: "running zero syncs with ledger long uptime",
			status: &daemon.StatusData{
				Running:    true,
				TotalSyncs: 0,
				Uptime:     300_000_000_000, // 5min > 3min threshold
				LedgerPath: "/some/ledger",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isDaemonBootstrapping(tt.status)
			if got != tt.want {
				t.Errorf("isDaemonBootstrapping() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDaemonHasConfiguredRepos_ExtraCoverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status *daemon.StatusData
		want   bool
	}{
		{
			name:   "nil status",
			status: nil,
			want:   false,
		},
		{
			name:   "empty status",
			status: &daemon.StatusData{},
			want:   false,
		},
		{
			name: "legacy ledger path only",
			status: &daemon.StatusData{
				LedgerPath: "/some/path",
			},
			want: true,
		},
		{
			name: "legacy team contexts",
			status: &daemon.StatusData{
				TeamContexts: []daemon.TeamContextSyncStatus{
					{TeamID: "team1"},
				},
			},
			want: true,
		},
		{
			name: "workspaces with entries",
			status: &daemon.StatusData{
				Workspaces: map[string][]daemon.WorkspaceSyncStatus{
					"ledger": {{Type: "ledger", Exists: true}},
				},
			},
			want: true,
		},
		{
			name: "workspaces with empty slice",
			status: &daemon.StatusData{
				Workspaces: map[string][]daemon.WorkspaceSyncStatus{
					"ledger": {},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := daemonHasConfiguredRepos(tt.status)
			if got != tt.want {
				t.Errorf("daemonHasConfiguredRepos() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPathExistsStatus_ExtraCoverage(t *testing.T) {
	t.Parallel()

	t.Run("empty path", func(t *testing.T) {
		t.Parallel()
		if pathExistsStatus("") {
			t.Error("expected false for empty path")
		}
	})

	t.Run("nonexistent path", func(t *testing.T) {
		t.Parallel()
		if pathExistsStatus("/nonexistent/path/that/does/not/exist") {
			t.Error("expected false for nonexistent path")
		}
	})

	t.Run("existing directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if !pathExistsStatus(dir) {
			t.Error("expected true for existing directory")
		}
	})
}

func TestShortenPathViaSymlink_EmptyInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		projectRoot string
		fullPath    string
		want        string
	}{
		{"empty project root", "", "/some/path", "/some/path"},
		{"empty full path", "/some/root", "", ""},
		{"both empty", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shortenPathViaSymlink(tt.projectRoot, tt.fullPath)
			if got != tt.want {
				t.Errorf("shortenPathViaSymlink(%q, %q) = %q, want %q",
					tt.projectRoot, tt.fullPath, got, tt.want)
			}
		})
	}
}

func TestShortenPathViaSymlink_NoMatchingSymlink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fullPath := "/some/other/path"
	got := shortenPathViaSymlink(dir, fullPath, ".sageox/ledger", ".sageox/teams/primary")
	if got != fullPath {
		t.Errorf("expected unchanged path %q, got %q", fullPath, got)
	}
}

func TestGetGitRemoteURL_EmptyAndInvalid(t *testing.T) {
	t.Parallel()

	t.Run("empty path returns empty", func(t *testing.T) {
		t.Parallel()
		got := getGitRemoteURL("")
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("nonexistent path returns empty", func(t *testing.T) {
		t.Parallel()
		got := getGitRemoteURL("/nonexistent/repo/path")
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})
}

func TestGetTeamContextRemoteURL_EmptyTeamID(t *testing.T) {
	t.Parallel()

	got := getTeamContextRemoteURL("", "https://sageox.ai")
	if got != "" {
		t.Errorf("expected empty string for empty teamID, got %q", got)
	}
}
