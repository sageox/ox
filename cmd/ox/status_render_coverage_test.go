package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
)

func TestRenderProjectStatus_NotGitRepo(t *testing.T) {
	t.Parallel()

	got := renderProjectStatus("/some/dir", "", false, nil)
	if !strings.Contains(got, "not a git repo") {
		t.Errorf("expected 'not a git repo' in output, got: %s", got)
	}
}

func TestRenderProjectStatus_Initialized(t *testing.T) {
	t.Parallel()

	got := renderProjectStatus("/some/dir", "/some/dir", true, nil)
	if !strings.Contains(got, "/some/dir") {
		t.Errorf("expected repo path in output, got: %s", got)
	}
}

func TestRenderProjectStatus_NotInitialized(t *testing.T) {
	t.Parallel()

	got := renderProjectStatus("/some/dir", "/some/dir", false, nil)
	if !strings.Contains(got, "not initialized") {
		t.Errorf("expected 'not initialized' in output, got: %s", got)
	}
}

func TestRenderProjectStatus_WithCodeStats_Indexing(t *testing.T) {
	t.Parallel()

	stats := &daemon.CodeDBStats{
		IndexExists: true,
		IndexingNow: true,
	}
	got := renderProjectStatus("/dir", "/dir", true, stats)
	if !strings.Contains(got, "indexing") {
		t.Errorf("expected 'indexing' in output, got: %s", got)
	}
}

func TestRenderProjectStatus_WithCodeStats_NoIndex(t *testing.T) {
	t.Parallel()

	stats := &daemon.CodeDBStats{
		IndexExists: false,
	}
	got := renderProjectStatus("/dir", "/dir", true, stats)
	if !strings.Contains(got, "pending") {
		t.Errorf("expected 'pending' in output, got: %s", got)
	}
}

func TestRenderProjectStatus_WithCodeStats_Error(t *testing.T) {
	t.Parallel()

	stats := &daemon.CodeDBStats{
		IndexExists: true,
		LastError:   "disk full",
		Commits:     5,
	}
	got := renderProjectStatus("/dir", "/dir", true, stats)
	if !strings.Contains(got, "disk full") {
		t.Errorf("expected error message in output, got: %s", got)
	}
}

func TestRenderProjectStatus_WithCodeStats_Healthy(t *testing.T) {
	t.Parallel()

	stats := &daemon.CodeDBStats{
		IndexExists: true,
		LastIndexed: time.Now().Add(-5 * time.Minute),
		Commits:     42,
		Symbols:     100,
	}
	got := renderProjectStatus("/dir", "/dir", true, stats)
	if strings.Contains(got, "error") {
		t.Errorf("unexpected 'error' in healthy status output: %s", got)
	}
}

func TestRenderProjectStatus_WithCodeStats_GitHubSources(t *testing.T) {
	t.Parallel()

	stats := &daemon.CodeDBStats{
		IndexExists: true,
		LastIndexed: time.Now().Add(-1 * time.Minute),
		Commits:     10,
		PRs:         5,
		Issues:      3,
	}
	got := renderProjectStatus("/dir", "/dir", true, stats)
	if !strings.Contains(got, "github") {
		t.Errorf("expected 'github' source in output, got: %s", got)
	}
}

func TestGetGitRemoteURL_EmptyPath(t *testing.T) {
	t.Parallel()

	got := getGitRemoteURL("")
	if got != "" {
		t.Errorf("getGitRemoteURL('') = %q, want empty", got)
	}
}

func TestGetGitRemoteURL_NonexistentPath(t *testing.T) {
	t.Parallel()

	got := getGitRemoteURL("/nonexistent/repo/path")
	if got != "" {
		t.Errorf("getGitRemoteURL(nonexistent) = %q, want empty", got)
	}
}

func TestIsDaemonBootstrapping_NilStatus_Coverage(t *testing.T) {
	t.Parallel()

	if isDaemonBootstrapping(nil) {
		t.Error("expected false for nil status")
	}
}

func TestIsDaemonBootstrapping_NotRunning_Coverage(t *testing.T) {
	t.Parallel()

	status := &daemon.StatusData{Running: false}
	if isDaemonBootstrapping(status) {
		t.Error("expected false when not running")
	}
}

func TestIsDaemonBootstrapping_RunningWithSyncs_Coverage(t *testing.T) {
	t.Parallel()

	status := &daemon.StatusData{
		Running:    true,
		TotalSyncs: 5,
		Uptime:     30 * time.Second,
	}
	if isDaemonBootstrapping(status) {
		t.Error("expected false when syncs > 0")
	}
}

func TestIsDaemonBootstrapping_RunningLongUptime(t *testing.T) {
	t.Parallel()

	status := &daemon.StatusData{
		Running:    true,
		TotalSyncs: 0,
		Uptime:     5 * time.Minute,
		Workspaces: map[string][]daemon.WorkspaceSyncStatus{
			"ledger": {{Path: "/some/path"}},
		},
	}
	if isDaemonBootstrapping(status) {
		t.Error("expected false when uptime exceeds bootstrap threshold")
	}
}

func TestIsDaemonBootstrapping_TrueCase(t *testing.T) {
	t.Parallel()

	status := &daemon.StatusData{
		Running:    true,
		TotalSyncs: 0,
		Uptime:     30 * time.Second,
		Workspaces: map[string][]daemon.WorkspaceSyncStatus{
			"ledger": {{Path: "/some/path"}},
		},
	}
	if !isDaemonBootstrapping(status) {
		t.Error("expected true during bootstrap period")
	}
}

func TestDaemonHasConfiguredRepos_EmptyWorkspaces_Coverage(t *testing.T) {
	t.Parallel()

	status := &daemon.StatusData{
		Workspaces: map[string][]daemon.WorkspaceSyncStatus{},
	}
	if daemonHasConfiguredRepos(status) {
		t.Error("expected false with empty workspaces")
	}
}

func TestDaemonHasConfiguredRepos_LegacyLedgerPath_Coverage(t *testing.T) {
	t.Parallel()

	status := &daemon.StatusData{
		LedgerPath: "/some/ledger",
	}
	if !daemonHasConfiguredRepos(status) {
		t.Error("expected true with legacy LedgerPath")
	}
}

func TestDaemonHasConfiguredRepos_LegacyTeamContexts_Coverage(t *testing.T) {
	t.Parallel()

	status := &daemon.StatusData{
		TeamContexts: []daemon.TeamContextSyncStatus{
			{TeamID: "team-1"},
		},
	}
	if !daemonHasConfiguredRepos(status) {
		t.Error("expected true with legacy TeamContexts")
	}
}

func TestPathExistsStatus_EmptyPath(t *testing.T) {
	t.Parallel()

	if pathExistsStatus("") {
		t.Error("expected false for empty path")
	}
}

func TestPathExistsStatus_NonexistentPath(t *testing.T) {
	t.Parallel()

	if pathExistsStatus("/this/path/does/not/exist/ever") {
		t.Error("expected false for nonexistent path")
	}
}

func TestPathExistsStatus_ExistingPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if !pathExistsStatus(dir) {
		t.Error("expected true for existing directory")
	}
}

func TestFormatValue_AllSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		semantic string
		prefix   string
	}{
		{"success", "✓"},
		{"error", "✗"},
		{"warning", "⚠"},
	}

	for _, tt := range tests {
		t.Run(tt.semantic, func(t *testing.T) {
			t.Parallel()
			got := formatValue("test-value", tt.semantic)
			if !strings.Contains(got, tt.prefix) {
				t.Errorf("formatValue(%q, %q) missing prefix %q, got: %s", "test-value", tt.semantic, tt.prefix, got)
			}
			if !strings.Contains(got, "test-value") {
				t.Errorf("formatValue() missing original value in output")
			}
		})
	}
}

func TestRenderVisibilityWithAccess_AllCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		visibility string
		access     string
		wantParts  []string
	}{
		{"viewer access", "private", "viewer", []string{"read-only"}},
		{"member access", "private", "member", []string{"member"}},
		{"admin access", "public", "admin", []string{"admin"}},
		{"empty access", "private", "", []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderVisibilityWithAccess(tt.visibility, tt.access)
			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("renderVisibilityWithAccess(%q, %q) missing %q in: %s",
						tt.visibility, tt.access, part, got)
				}
			}
		})
	}
}

func TestRenderTable_EmptyRows(t *testing.T) {
	t.Parallel()

	got := renderTable("Test Header", [][]string{})
	if !strings.Contains(got, "Test Header") {
		t.Error("expected header in output")
	}
}

func TestRenderTable_WithSemanticHint(t *testing.T) {
	t.Parallel()

	rows := [][]string{
		{"Status", "connected", "success"},
		{"Error", "timeout", "error"},
		{"Info", "pending", "warning"},
	}
	got := renderTable("Connection", rows)
	if !strings.Contains(got, "Connection") {
		t.Error("expected header in output")
	}
	for _, row := range rows {
		if !strings.Contains(got, row[1]) {
			t.Errorf("expected value %q in output", row[1])
		}
	}
}

func TestFormatSize_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{"zero", 0, "0 B"},
		{"one byte", 1, "1 B"},
		{"1023 bytes", 1023, "1023 B"},
		{"exactly 1 KB", 1024, "1.0 KB"},
		{"1.5 KB", 1536, "1.5 KB"},
		{"exactly 1 MB", 1024 * 1024, "1.0 MB"},
		{"exactly 1 GB", 1024 * 1024 * 1024, "1.0 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatSize(tt.bytes)
			if got != tt.want {
				t.Errorf("formatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestRenderProjectStatus_IndexFailedNoCommits(t *testing.T) {
	t.Parallel()

	stats := &daemon.CodeDBStats{
		IndexExists: true,
		LastError:   "something went wrong",
		Commits:     0,
	}
	got := renderProjectStatus("/dir", "/dir", true, stats)
	// when index exists but no commits and there's an error, should show "pending"
	if !strings.Contains(got, "pending") {
		t.Errorf("expected 'pending' for failed initial index, got: %s", got)
	}
}

func TestRenderProjectStatus_WithSymbolsSource(t *testing.T) {
	t.Parallel()

	stats := &daemon.CodeDBStats{
		IndexExists: true,
		LastIndexed: time.Now().Add(-1 * time.Minute),
		Symbols:     50,
	}
	got := renderProjectStatus("/dir", "/dir", true, stats)
	if !strings.Contains(got, "symbols") {
		t.Errorf("expected 'symbols' source in output, got: %s", got)
	}
}
