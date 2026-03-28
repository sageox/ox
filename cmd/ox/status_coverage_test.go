package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
)

func TestIsDaemonBootstrapping(t *testing.T) {
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
			name:   "not running",
			status: &daemon.StatusData{Running: false},
			want:   false,
		},
		{
			name: "running with syncs already done",
			status: &daemon.StatusData{
				Running:    true,
				TotalSyncs: 5,
				Uptime:     30 * time.Second,
				LedgerPath: "/some/path",
			},
			want: false,
		},
		{
			name: "running, zero syncs, within threshold, has repos",
			status: &daemon.StatusData{
				Running:    true,
				TotalSyncs: 0,
				Uptime:     30 * time.Second,
				LedgerPath: "/some/path",
			},
			want: true,
		},
		{
			name: "running, zero syncs, beyond threshold",
			status: &daemon.StatusData{
				Running:    true,
				TotalSyncs: 0,
				Uptime:     5 * time.Minute,
				LedgerPath: "/some/path",
			},
			want: false,
		},
		{
			name: "running, zero syncs, within threshold, no repos",
			status: &daemon.StatusData{
				Running:    true,
				TotalSyncs: 0,
				Uptime:     30 * time.Second,
				// no LedgerPath, no TeamContexts, no Workspaces
			},
			want: false,
		},
		{
			name: "running, zero syncs, within threshold, has workspaces",
			status: &daemon.StatusData{
				Running:    true,
				TotalSyncs: 0,
				Uptime:     10 * time.Second,
				Workspaces: map[string][]daemon.WorkspaceSyncStatus{
					"ledger": {{Path: "/some/path"}},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.status.IsBootstrapping()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDaemonHasConfiguredRepos(t *testing.T) {
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
			name: "has ledger path",
			status: &daemon.StatusData{
				LedgerPath: "/path/to/ledger",
			},
			want: true,
		},
		{
			name: "has team contexts (legacy)",
			status: &daemon.StatusData{
				TeamContexts: []daemon.TeamContextSyncStatus{
					{TeamID: "team_abc"},
				},
			},
			want: true,
		},
		{
			name: "has workspaces",
			status: &daemon.StatusData{
				Workspaces: map[string][]daemon.WorkspaceSyncStatus{
					"team-context": {{Path: "/some/path"}},
				},
			},
			want: true,
		},
		{
			name: "empty workspaces map",
			status: &daemon.StatusData{
				Workspaces: map[string][]daemon.WorkspaceSyncStatus{},
			},
			want: false,
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
			got := tt.status.HasConfiguredRepos()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPathExistsStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want bool
	}{
		{
			name: "existing directory",
			path: os.TempDir(),
			want: true,
		},
		{
			name: "nonexistent path",
			path: "/nonexistent/unlikely/path",
			want: false,
		},
		{
			name: "empty string",
			path: "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pathExistsStatus(tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPathExistsStatus_WithFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	assert.NoError(t, os.WriteFile(filePath, []byte("hello"), 0o644))

	assert.True(t, pathExistsStatus(filePath))
	assert.False(t, pathExistsStatus(filepath.Join(dir, "missing.txt")))
}

func TestShortenPathViaSymlink_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("empty project root", func(t *testing.T) {
		t.Parallel()
		got := shortenPathViaSymlink("", "/some/path")
		assert.Equal(t, "/some/path", got)
	})

	t.Run("empty full path", func(t *testing.T) {
		t.Parallel()
		got := shortenPathViaSymlink("/project", "")
		assert.Equal(t, "", got)
	})

	t.Run("both empty", func(t *testing.T) {
		t.Parallel()
		got := shortenPathViaSymlink("", "")
		assert.Equal(t, "", got)
	})

	t.Run("no matching symlinks returns full path", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		fullPath := "/some/absolute/path"
		got := shortenPathViaSymlink(dir, fullPath, ".sageox/ledger")
		assert.Equal(t, fullPath, got)
	})

	t.Run("symlink matches", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		targetDir := t.TempDir()

		// create .sageox dir
		sageoxDir := filepath.Join(dir, ".sageox")
		assert.NoError(t, os.MkdirAll(sageoxDir, 0o755))

		// create symlink: .sageox/ledger -> targetDir
		symlinkPath := filepath.Join(sageoxDir, "ledger")
		assert.NoError(t, os.Symlink(targetDir, symlinkPath))

		got := shortenPathViaSymlink(dir, targetDir, ".sageox/ledger")
		assert.Equal(t, ".sageox/ledger", got)
	})
}

func TestRenderTable_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("empty rows", func(t *testing.T) {
		t.Parallel()
		result := renderTable("Test", [][]string{})
		assert.Contains(t, result, "Test")
	})

	t.Run("rows with semantic hints", func(t *testing.T) {
		t.Parallel()
		rows := [][]string{
			{"Status", "Active", "success"},
			{"Error", "None", "muted"},
			{"Warning", "Check config", "warning"},
		}
		result := renderTable("Status", rows)
		assert.Contains(t, result, "Active")
		assert.Contains(t, result, "None")
		assert.Contains(t, result, "Check config")
	})

	t.Run("rows without semantic hint use auto-detect", func(t *testing.T) {
		t.Parallel()
		rows := [][]string{
			{"Logged In", "Yes"},
			{"Initialized", "No"},
		}
		result := renderTable("Auth", rows)
		assert.Contains(t, result, "Yes")
		assert.Contains(t, result, "No")
	})
}
