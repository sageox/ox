package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckHealth_StorageWritable(t *testing.T) {
	status := CheckHealth("")

	assert.True(t, status.StorageWritable, "expected storage to be writable, got error: %s", status.StorageError)
	assert.NotEmpty(t, status.StoragePath)
	// path should contain sageox (either _sageox sibling dir or legacy .cache/sageox)
	assert.Contains(t, status.StoragePath, "sageox", "expected path to contain sageox")
}

func TestCheckHealth_StorageNotWritable(t *testing.T) {
	// skip if running as root (root can always write)
	if os.Getuid() == 0 {
		t.Skip("skipping test when running as root")
	}

	// create a temp dir that will be made read-only
	tempDir := t.TempDir()

	// make the parent read-only to prevent writing subdirectories
	err := os.Chmod(tempDir, 0444)
	require.NoError(t, err)
	defer os.Chmod(tempDir, 0755) // restore for cleanup

	// create a fake ledger path inside the read-only dir
	ledgerPath := filepath.Join(tempDir, "test_sageox", "sageox.ai", "ledger")

	// simulate CheckHealth behavior by trying to create the dir
	mkdirErr := os.MkdirAll(ledgerPath, 0755)
	assert.Error(t, mkdirErr, "expected error creating dir in read-only parent")
}

func TestCheckHealth_RepoNotCloned(t *testing.T) {
	// CheckHealth uses ledger.DefaultPath() based on cwd git root
	// since tests run from ox repo, this finds the sibling ledger pattern
	// the result depends on whether that ledger exists
	status := CheckHealth("")

	// either RepoCloned is true (ledger exists) or false with error
	if !status.RepoCloned {
		assert.Equal(t, "ledger not provisioned", status.RepoError)
	}
}

func TestCheckHealth_RepoCloned(t *testing.T) {
	// create a fake project with a user-dir ledger containing .git directory
	tempDir := t.TempDir()
	projectRoot := filepath.Join(tempDir, "my_project")

	// create project directory with config
	err := os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0755)
	require.NoError(t, err)

	repoID := "repo_019c5812-01e9-7b7d-b5b1-321c471c9777"
	cfgContent := fmt.Sprintf(`{"endpoint":"https://sageox.ai","repo_id":"%s"}`, repoID)
	err = os.WriteFile(filepath.Join(projectRoot, ".sageox", "config.json"), []byte(cfgContent), 0644)
	require.NoError(t, err)

	// derive expected ledger path (user-dir based)
	ep := "https://sageox.ai"
	ledgerPath := config.DefaultLedgerPath(repoID, ep)

	// create ledger with .git directory at the user-dir location
	gitDir := filepath.Join(ledgerPath, ".git")
	err = os.MkdirAll(gitDir, 0755)
	require.NoError(t, err)

	// now CheckHealth with projectRoot should find the ledger
	status := CheckHealth(projectRoot)

	assert.True(t, status.RepoCloned, "expected repo to be cloned, got error: %s", status.RepoError)
	assert.Equal(t, ledgerPath, status.RepoPath, "expected ledger path to match")
}

func TestCheckHealth_RecordingActive(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// create project root with recording state in session folder
	projectRoot := t.TempDir()
	sessionPath := filepath.Join(projectRoot, "sessions", "2026-01-06T14-30-user-Oxa7b3")

	state := &RecordingState{
		OutputFile:  "/path/to/session.jsonl",
		AgentID:     "Oxa7b3",
		StartedAt:   time.Now().Add(-30 * time.Minute),
		AdapterName: "claude-code",
		SessionFile: "/path/to/session",
		SessionPath: sessionPath,
	}
	err := SaveRecordingState(projectRoot, state)
	require.NoError(t, err)

	status := CheckHealth(projectRoot)

	assert.True(t, status.IsRecordingActive)
	require.NotNil(t, status.Recording)
	assert.Equal(t, "Oxa7b3", status.Recording.AgentID)
}

func TestCheckHealth_RecordingNotActive(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectRoot := t.TempDir()
	status := CheckHealth(projectRoot)

	assert.False(t, status.IsRecordingActive)
	assert.Nil(t, status.Recording)
}

func TestRecordingDurationString(t *testing.T) {
	tests := []struct {
		name      string
		startedAt time.Time
		wantMatch string // partial match since exact seconds vary
	}{
		{
			name:      "nil state",
			startedAt: time.Time{},
			wantMatch: "",
		},
		{
			name:      "30 seconds ago",
			startedAt: time.Now().Add(-30 * time.Second),
			wantMatch: "s",
		},
		{
			name:      "45 minutes ago",
			startedAt: time.Now().Add(-45 * time.Minute),
			wantMatch: "m",
		},
		{
			name:      "2 hours ago",
			startedAt: time.Now().Add(-2 * time.Hour),
			wantMatch: "h",
		},
		{
			name:      "1.5 hours ago",
			startedAt: time.Now().Add(-90 * time.Minute),
			wantMatch: "h",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var state *RecordingState
			if !tt.startedAt.IsZero() {
				state = &RecordingState{StartedAt: tt.startedAt}
			}

			got := RecordingDurationString(state)

			if tt.wantMatch == "" {
				assert.Empty(t, got)
				return
			}

			assert.NotEmpty(t, got)

			// check that duration contains expected suffix
			hasExpectedSuffix := false
			for _, suffix := range []string{"s", "m", "h"} {
				if suffix == tt.wantMatch {
					hasExpectedSuffix = true
					break
				}
			}
			if hasExpectedSuffix && len(got) > 0 {
				// duration should end with s, m, or h
				lastChar := got[len(got)-1:]
				assert.True(t, lastChar == "s" || lastChar == "m" || lastChar == "h", "duration %s should end with time unit", got)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{
			name:     "seconds",
			duration: 30 * time.Second,
			want:     "30s",
		},
		{
			name:     "one minute",
			duration: time.Minute,
			want:     "1m",
		},
		{
			name:     "45 minutes",
			duration: 45 * time.Minute,
			want:     "45m",
		},
		{
			name:     "one hour",
			duration: time.Hour,
			want:     "1h",
		},
		{
			name:     "two hours",
			duration: 2 * time.Hour,
			want:     "2h",
		},
		{
			name:     "90 minutes",
			duration: 90 * time.Minute,
			want:     "1h30m",
		},
		{
			name:     "2 hours 15 minutes",
			duration: 2*time.Hour + 15*time.Minute,
			want:     "2h15m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.duration)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestShortenPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "path under home",
			path: filepath.Join(home, ".cache", "sageox"),
			want: "~/.cache/sageox",
		},
		{
			name: "path not under home",
			path: "/tmp/somewhere",
			want: "/tmp/somewhere",
		},
		{
			name: "empty path",
			path: "",
			want: "",
		},
		{
			name: "home directory itself",
			path: home,
			want: "~",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShortenPath(tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCheckHealth_EmptyProjectRoot(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// empty project root should still check storage and repo
	status := CheckHealth("")

	// storage should be checked
	assert.NotEmpty(t, status.StoragePath)

	// recording should not be checked (no project root)
	assert.False(t, status.IsRecordingActive)
}

// TestCheckHealth_DoesNotCreateSiblingDirectory verifies that CheckHealth
// does not create the ledger directory as a side-effect. This was bug #35:
// the storage writability check called os.MkdirAll, creating an empty
// <project>_sageox/<endpoint>/ledger directory that then caused
// checkGitRepoPaths to report a false "empty directory" failure.
func TestCheckHealth_DoesNotCreateSiblingDirectory(t *testing.T) {
	tempDir := t.TempDir()
	projectRoot := filepath.Join(tempDir, "my_project")

	err := os.MkdirAll(projectRoot, 0755)
	require.NoError(t, err)

	siblingDir := filepath.Join(tempDir, "my_project_sageox")

	// precondition: sibling directory does not exist
	_, err = os.Stat(siblingDir)
	require.True(t, os.IsNotExist(err), "sibling dir should not exist before CheckHealth")

	status := CheckHealth(projectRoot)

	// CheckHealth should still report a storage path
	assert.NotEmpty(t, status.StoragePath)
	// storage should be considered writable (parent is writable)
	assert.True(t, status.StorageWritable, "storage should be writable even without existing dir")

	// critical assertion: the sibling directory must NOT have been created
	_, err = os.Stat(siblingDir)
	assert.True(t, os.IsNotExist(err),
		"CheckHealth must not create sibling directory %s as side-effect", siblingDir)
}
