package doctor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDaemonRunningCheck_Name(t *testing.T) {
	check := NewDaemonRunningCheck()
	assert.Equal(t, "daemon running", check.Name())
}

func TestDaemonRunningCheck_Run_NotRunning(t *testing.T) {
	// isolate from real daemon: prevent config.FindProjectRoot walk-up
	t.Chdir(t.TempDir())

	check := NewDaemonRunningCheck()
	result := check.Run(context.Background(), false)

	// when daemon is not running, expect skip status
	assert.Equal(t, StatusSkip, result.Status)
	assert.Equal(t, "not running", result.Message)
}

func TestDaemonResponsiveCheck_Name(t *testing.T) {
	check := NewDaemonResponsiveCheck()
	assert.Equal(t, "daemon responsive", check.Name())
}

func TestDaemonResponsiveCheck_Run_NotRunning(t *testing.T) {
	t.Chdir(t.TempDir())

	check := NewDaemonResponsiveCheck()
	result := check.Run(context.Background(), false)

	// when daemon is not running, expect skip (empty message)
	assert.Equal(t, StatusSkip, result.Status)
}

func TestDaemonSyncStatusCheck_Name(t *testing.T) {
	check := NewDaemonSyncStatusCheck()
	assert.Equal(t, "last sync", check.Name())
}

func TestDaemonSyncStatusCheck_Run_NotRunning(t *testing.T) {
	t.Chdir(t.TempDir())

	check := NewDaemonSyncStatusCheck()
	result := check.Run(context.Background(), false)

	// when daemon is not running, expect skip
	assert.Equal(t, StatusSkip, result.Status)
}

func TestDaemonUptimeCheck_Name(t *testing.T) {
	check := NewDaemonUptimeCheck()
	assert.Equal(t, "uptime", check.Name())
}

func TestDaemonUptimeCheck_Run_NotRunning(t *testing.T) {
	t.Chdir(t.TempDir())

	check := NewDaemonUptimeCheck()
	result := check.Run(context.Background(), false)

	// when daemon is not running, expect skip
	assert.Equal(t, StatusSkip, result.Status)
}

func TestDaemonSyncErrorsCheck_Name(t *testing.T) {
	check := NewDaemonSyncErrorsCheck()
	assert.Equal(t, "sync errors", check.Name())
}

func TestDaemonSyncErrorsCheck_Run_NotRunning(t *testing.T) {
	t.Chdir(t.TempDir())

	check := NewDaemonSyncErrorsCheck()
	result := check.Run(context.Background(), false)

	// when daemon is not running, expect skip
	assert.Equal(t, StatusSkip, result.Status)
}

func TestDaemonHeartbeatCheck_Name(t *testing.T) {
	check := NewDaemonHeartbeatCheck("workspace", "repo_123", "test-repo", "sageox.ai")
	assert.Equal(t, "heartbeat (test-repo)", check.Name())
}

func TestDaemonHeartbeatCheck_EmptyIdentifier(t *testing.T) {
	check := NewDaemonHeartbeatCheck("workspace", "", "test-repo", "sageox.ai")
	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
}

func TestDaemonHeartbeatCheck_EmptyEndpoint(t *testing.T) {
	check := NewDaemonHeartbeatCheck("workspace", "repo_123", "test-repo", "")
	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
}

func TestDaemonBootstrapGraceConstant(t *testing.T) {
	// verify the bootstrap grace constant is 3 minutes
	assert.Equal(t, 3*time.Minute, DaemonBootstrapGrace)
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		seconds  int
		expected string
	}{
		{"seconds", 30, "30s"},
		{"minutes", 300, "5m"},
		{"hours", 7200, "2h"},
		{"days", 172800, "2d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := formatDuration(time.Duration(tt.seconds) * time.Second)
			assert.Equal(t, tt.expected, d)
		})
	}
}
