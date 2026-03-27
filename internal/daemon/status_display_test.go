package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatNotRunning_InProject(t *testing.T) {
	t.Parallel()
	got := FormatNotRunning(true)
	assert.Contains(t, got, "Not running")
	assert.Contains(t, got, "ox daemon start")
}

func TestFormatNotRunning_OutsideProject(t *testing.T) {
	t.Parallel()
	got := FormatNotRunning(false)
	assert.Contains(t, got, "Not running")
	assert.Contains(t, got, "ox init")
}

func TestFormatStarting(t *testing.T) {
	t.Parallel()
	got := FormatStarting()
	assert.NotEmpty(t, got)
	assert.Contains(t, got, "Starting")
}

func TestFormatDurationCompact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "<1s"},
		{"seconds", 45 * time.Second, "45s"},
		{"minutes", 5*time.Minute + 30*time.Second, "5m 30s"},
		{"hours", 2*time.Hour + 15*time.Minute, "2h 15m"},
		{"many hours", 51 * time.Hour, "51h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatDurationCompact(tt.d)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatRelativeTime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"seconds ago", 5 * time.Second, "5s ago"},
		{"minutes ago", 3 * time.Minute, "3m ago"},
		{"hours ago", 2 * time.Hour, "2h ago"},
		{"days ago", 48 * time.Hour, "2d ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatRelativeTime(tt.d)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestShortenPath_StatusDisplay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"short path", "/tmp/project"},
		{"home path", "/Users/user/Documents/Code/project"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shortenPath(tt.input)
			if tt.input == "" {
				assert.Empty(t, got)
			} else {
				assert.NotEmpty(t, got, "non-empty input should produce non-empty output")
			}
		})
	}
}

func TestFormatKV(t *testing.T) {
	t.Parallel()
	got := formatKV("PID", "12345")
	assert.Contains(t, got, "PID")
	assert.Contains(t, got, "12345")
}

func TestSemverOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"v0.15.0", "v0.15.0"},
		{"0.15.0", "0.15.0"},
		{"v1.2.3-beta", "v1.2.3-beta"},
		{"v1.2.3+build123", "v1.2.3"},
		{"0.1.0+abc", "0.1.0"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := semverOnly(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatVersionMatch(t *testing.T) {
	t.Parallel()

	t.Run("matching versions", func(t *testing.T) {
		got := formatVersionMatch("0.15.0", "0.15.0")
		assert.NotEmpty(t, got)
	})

	t.Run("mismatched versions", func(t *testing.T) {
		got := formatVersionMatch("0.15.0", "0.14.0")
		assert.NotEmpty(t, got)
	})

	t.Run("empty daemon version", func(t *testing.T) {
		got := formatVersionMatch("", "0.15.0")
		assert.NotEmpty(t, got)
	})
}

func TestDetermineHealth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status *StatusData
		want   HealthStatus
	}{
		{
			"healthy - no errors",
			&StatusData{Running: true},
			HealthHealthy,
		},
		{
			"warning - some errors",
			&StatusData{Running: true, RecentErrorCount: 4},
			HealthWarning,
		},
		{
			"critical - error threshold",
			&StatusData{Running: true, RecentErrorCount: 5},
			HealthCritical,
		},
		{
			"critical - above threshold",
			&StatusData{Running: true, RecentErrorCount: 10},
			HealthCritical,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := determineHealth(tt.status)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCountWorkspaces(t *testing.T) {
	t.Parallel()

	t.Run("empty status", func(t *testing.T) {
		assert.Equal(t, 0, countWorkspaces(&StatusData{}))
	})

	t.Run("with workspaces", func(t *testing.T) {
		status := &StatusData{
			Workspaces: map[string][]WorkspaceSyncStatus{
				"ledger":       {{ID: "ws-1"}},
				"team-context": {{ID: "ws-2"}, {ID: "ws-3"}},
			},
		}
		assert.Equal(t, 3, countWorkspaces(status))
	})
}

func TestFormatDaemonList_Empty(t *testing.T) {
	t.Parallel()
	got := FormatDaemonList(nil)
	assert.Contains(t, got, "No")
}

func TestFormatDaemonList_WithDaemons(t *testing.T) {
	t.Parallel()
	daemons := []DaemonInfo{
		{PID: 1234, SocketPath: "/tmp/daemon.sock"},
		{PID: 5678, SocketPath: "/tmp/daemon2.sock"},
	}
	got := FormatDaemonList(daemons)
	assert.Contains(t, got, "1234")
	assert.Contains(t, got, "5678")
}

func TestStripANSI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no ansi", "hello world", "hello world"},
		{"with ansi", "\033[31mred\033[0m", "red"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stripANSI(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatWSStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ws   WorkspaceSyncStatus
	}{
		{"synced", WorkspaceSyncStatus{ID: "ws-1", Exists: true}},
		{"error", WorkspaceSyncStatus{ID: "ws-err", LastErr: "fail"}},
		{"not exists", WorkspaceSyncStatus{ID: "ws-missing", Exists: false}},
		{"empty", WorkspaceSyncStatus{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatWSStatus(tt.ws)
			assert.NotEmpty(t, got)
		})
	}
}

func TestHumanizeError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		expected string
	}{
		{
			name:     "dns timeout from git fetch",
			raw:      "fetch failed: fatal: unable to access 'https://git.sageox.ai/repo.git/': Resolving timed out after 805206 milliseconds (exit status 128)",
			expected: "DNS resolution timed out (network may be offline or DNS is unreachable)",
		},
		{
			name:     "stale project path",
			raw:      "open local repo /Users/ryan/conductor/workspaces/ox/edinburgh-v1: repository does not exist",
			expected: "Project path no longer exists: edinburgh-v1 (workspace may have moved)",
		},
		{
			name:     "could not resolve host",
			raw:      "fetch failed: fatal: unable to access 'https://git.sageox.ai/repo.git/': Could not resolve host: git.sageox.ai",
			expected: "DNS resolution timed out (network may be offline or DNS is unreachable)",
		},
		{
			name:     "connection refused",
			raw:      "fetch failed: fatal: unable to access 'https://git.sageox.ai/repo.git/': Failed to connect: Connection refused",
			expected: "Server unreachable (connection refused)",
		},
		{
			name:     "no such host",
			raw:      "fetch failed: fatal: unable to access 'https://git.sageox.ai/repo.git/': No such host",
			expected: "Server unreachable (DNS lookup failed)",
		},
		{
			name:     "unknown error passes through",
			raw:      "some unknown error happened",
			expected: "some unknown error happened",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := humanizeError(tt.raw)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestGetErrorHint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      string
		wantHint bool
	}{
		{
			name:     "timeout gets retry hint",
			err:      "Git sync timed out (network may be offline)",
			wantHint: true,
		},
		{
			name:     "stale path gets heartbeat hint",
			err:      "Project path no longer exists: edinburgh-v1 (workspace may have moved)",
			wantHint: true,
		},
		{
			name:     "authentication gets credential hint",
			err:      "authentication required",
			wantHint: true,
		},
		{
			name:     "unknown error gets no hint",
			err:      "something else",
			wantHint: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hint := getErrorHint(tt.err)
			if tt.wantHint {
				assert.NotEmpty(t, hint)
			} else {
				assert.Empty(t, hint)
			}
		})
	}
}
