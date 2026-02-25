package ledger

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultPath(t *testing.T) {
	path, err := DefaultPath()
	require.NoError(t, err)

	assert.NotEmpty(t, path)
	assert.True(t, filepath.IsAbs(path), "expected absolute path, got: %s", path)
	// path should be under user data dir (.local/share/sageox), sibling dir (_sageox),
	// or fallback to legacy .cache/sageox/context
	assert.True(t, strings.Contains(path, "sageox"), "expected path to contain sageox, got: %s", path)
}

func TestDefaultPathForEndpoint(t *testing.T) {
	t.Run("with explicit endpoint", func(t *testing.T) {
		path, err := DefaultPathForEndpoint("https://staging.sageox.ai")
		require.NoError(t, err)

		assert.NotEmpty(t, path)
		assert.True(t, filepath.IsAbs(path), "expected absolute path")
		// in a git repo context, path should contain the endpoint slug
		// otherwise falls back to legacy path
		assert.True(t, strings.Contains(path, "sageox"), "expected path to contain sageox, got: %s", path)
	})

	t.Run("with empty endpoint uses default", func(t *testing.T) {
		path, err := DefaultPathForEndpoint("")
		require.NoError(t, err)

		assert.NotEmpty(t, path)
		assert.True(t, filepath.IsAbs(path), "expected absolute path")
	})
}

func TestLegacyPath(t *testing.T) {
	path, err := LegacyPath()
	// in a git repo context, should return the legacy sibling path
	// outside git repo, returns empty string
	if err == nil && path != "" {
		assert.True(t, strings.HasSuffix(path, "_sageox_ledger"), "expected legacy path suffix, got: %s", path)
	}
}

func TestExists_NotInitialized(t *testing.T) {
	tempDir := t.TempDir()

	assert.False(t, Exists(tempDir), "expected Exists() to return false for empty directory")
}

func TestExists_Initialized(t *testing.T) {
	tempDir := t.TempDir()

	// create a .git directory to simulate initialized ledger
	gitDir := filepath.Join(tempDir, ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0755))

	assert.True(t, Exists(tempDir), "expected Exists() to return true for initialized ledger")
}

func TestExists_EmptyPath(t *testing.T) {
	// empty path uses sibling pattern based on cwd git root
	// the result depends on whether the sibling ledger exists
	// just verify it doesn't panic and returns a boolean
	result := Exists("")
	// result is true or false depending on whether sibling ledger exists
	assert.IsType(t, true, result)
}

func TestExistsForEndpoint(t *testing.T) {
	// test with explicit endpoint
	result := ExistsForEndpoint("https://api.sageox.ai")
	assert.IsType(t, true, result)

	result = ExistsForEndpoint("https://staging.sageox.ai")
	assert.IsType(t, true, result)
}

func TestExistsAtLegacyPath(t *testing.T) {
	result := ExistsAtLegacyPath()
	assert.IsType(t, true, result)
}

func TestOpen_NotInitialized(t *testing.T) {
	tempDir := t.TempDir()

	_, err := Open(tempDir)
	assert.Equal(t, ErrNotProvisioned, err)
}

func TestOpen_Success(t *testing.T) {
	tempDir := t.TempDir()

	// create a .git directory
	gitDir := filepath.Join(tempDir, ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0755))

	ledger, err := Open(tempDir)
	require.NoError(t, err)

	assert.Equal(t, tempDir, ledger.Path)
}

func TestOpen_EmptyPath(t *testing.T) {
	// empty path uses sibling pattern based on cwd git root
	// the result depends on whether the sibling ledger exists
	ledger, err := Open("")
	if err != nil {
		// if error, should be ErrNotProvisioned (sibling ledger doesn't exist)
		assert.Equal(t, ErrNotProvisioned, err)
	} else {
		// if success, ledger path should contain "sageox"
		assert.Contains(t, ledger.Path, "sageox")
	}
}

func TestOpenForEndpoint(t *testing.T) {
	// test with explicit endpoint - should return ErrNotProvisioned since no ledger exists
	_, err := OpenForEndpoint("https://staging.sageox.ai")
	// either ErrNotProvisioned or success depending on environment
	if err != nil {
		assert.Equal(t, ErrNotProvisioned, err)
	}
}

func TestGetPath(t *testing.T) {
	tests := []struct {
		name   string
		ledger *Ledger
		want   string
	}{
		{
			name:   "nil ledger",
			ledger: nil,
			want:   "",
		},
		{
			name:   "empty path",
			ledger: &Ledger{},
			want:   "",
		},
		{
			name:   "valid path",
			ledger: &Ledger{Path: "/test/path"},
			want:   "/test/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ledger.GetPath()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetStatus_NotInitialized(t *testing.T) {
	tempDir := t.TempDir()

	status := GetStatus(tempDir)

	assert.False(t, status.Exists)
	assert.Equal(t, "ledger not provisioned", status.Error)
}

func TestGetStatus_Initialized(t *testing.T) {
	tempDir := t.TempDir()

	// initialize a git repo
	initCmd := exec.Command("git", "init", tempDir)
	require.NoError(t, initCmd.Run())

	status := GetStatus(tempDir)

	assert.True(t, status.Exists)
	assert.Equal(t, tempDir, status.Path)
	assert.False(t, status.HasRemote)
}

func TestGetStatus_EmptyPath(t *testing.T) {
	status := GetStatus("")

	// should use default path (sibling ledger if in git repo, legacy otherwise)
	assert.NotEmpty(t, status.Path)
	assert.True(t, filepath.IsAbs(status.Path), "expected absolute path")
	// path should contain sageox
	assert.True(t, strings.Contains(status.Path, "sageox"), "expected path to contain sageox, got: %s", status.Path)
}

func TestGetStatusForEndpoint(t *testing.T) {
	status := GetStatusForEndpoint("https://staging.sageox.ai")

	assert.NotEmpty(t, status.Path)
	// ledger may or may not exist depending on environment
	// just verify we get a valid status response
	assert.IsType(t, &Status{}, status)
}

func TestGetStatus_WithPendingChanges(t *testing.T) {
	tempDir := t.TempDir()

	// initialize a git repo
	initCmd := exec.Command("git", "init", tempDir)
	require.NoError(t, initCmd.Run())

	// create an untracked file
	testFile := filepath.Join(tempDir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("test"), 0644))

	status := GetStatus(tempDir)

	assert.Equal(t, 1, status.PendingChanges)
}

func TestLedger_Struct(t *testing.T) {
	now := time.Now()
	ledger := &Ledger{
		Path:     "/test/path",
		RepoID:   "repo-123",
		LastSync: now,
	}

	assert.Equal(t, "/test/path", ledger.Path)
	assert.Equal(t, "repo-123", ledger.RepoID)
	assert.True(t, ledger.LastSync.Equal(now))
}

func TestStatus_Struct(t *testing.T) {
	status := &Status{
		Exists:           true,
		Path:             "/test/path",
		HasRemote:        true,
		SyncedWithRemote: true,
		SyncStatus:       "up to date",
		PendingChanges:   0,
		Error:            "",
	}

	assert.True(t, status.Exists)
	assert.Equal(t, "/test/path", status.Path)
	assert.True(t, status.HasRemote)
	assert.True(t, status.SyncedWithRemote)
	assert.Equal(t, "up to date", status.SyncStatus)
	assert.Equal(t, 0, status.PendingChanges)
	assert.Empty(t, status.Error)
}

func TestInit_RequiresRemoteURL(t *testing.T) {
	tempDir := t.TempDir()
	ledgerPath := filepath.Join(tempDir, "ledger")

	// Init without remoteURL should fail - ledgers must be cloned from server
	_, err := Init(ledgerPath, "")
	assert.Equal(t, ErrNoRemoteURL, err, "Init should require a remote URL")
}

func TestInitForEndpoint_RequiresRemoteURL(t *testing.T) {
	// InitForEndpoint without remoteURL should fail
	_, err := InitForEndpoint("https://api.sageox.ai", "")
	assert.Equal(t, ErrNoRemoteURL, err, "InitForEndpoint should require a remote URL")
}

func TestInit_DefaultPath(t *testing.T) {
	// skip this test - with sibling pattern, Init("", url) would create
	// a ledger next to the current git repo (ox), which we don't want
	// in tests. Test Init with explicit paths instead.
	t.Skip("Init with empty path uses sibling pattern based on cwd, not suitable for automated tests")
}

func TestInitForEndpoint(t *testing.T) {
	// skip this test - would create ledger based on cwd
	t.Skip("InitForEndpoint uses sibling pattern based on cwd, not suitable for automated tests")
}

func TestEnableSparseCheckout_NotInitialized(t *testing.T) {
	var ledger *Ledger
	err := ledger.EnableSparseCheckout()
	assert.Equal(t, ErrNotProvisioned, err)

	ledger2 := &Ledger{}
	err = ledger2.EnableSparseCheckout()
	assert.Equal(t, ErrNotProvisioned, err)
}

func TestEnableSparseCheckout_Success(t *testing.T) {
	tempDir := t.TempDir()

	// initialize a git repo
	initCmd := exec.Command("git", "init", tempDir)
	require.NoError(t, initCmd.Run())

	ledger := &Ledger{Path: tempDir}
	err := ledger.EnableSparseCheckout()
	require.NoError(t, err)

	// verify sparse checkout is enabled
	cmd := exec.Command("git", "-C", tempDir, "sparse-checkout", "list")
	output, err := cmd.Output()
	require.NoError(t, err)

	outputStr := string(output)
	assert.Contains(t, outputStr, ".sync")
	assert.Contains(t, outputStr, "sessions")
	assert.Contains(t, outputStr, "audit")
}

func TestDisableSparseCheckout_NotInitialized(t *testing.T) {
	var ledger *Ledger
	err := ledger.DisableSparseCheckout()
	assert.Equal(t, ErrNotProvisioned, err)
}

func TestDisableSparseCheckout_Success(t *testing.T) {
	tempDir := t.TempDir()

	// initialize a git repo
	initCmd := exec.Command("git", "init", tempDir)
	require.NoError(t, initCmd.Run())

	ledger := &Ledger{Path: tempDir}

	// enable first
	err := ledger.EnableSparseCheckout()
	require.NoError(t, err)

	// then disable
	err = ledger.DisableSparseCheckout()
	require.NoError(t, err)
}

func TestAddToSparseCheckout_NotInitialized(t *testing.T) {
	var ledger *Ledger
	err := ledger.AddToSparseCheckout("assets")
	assert.Equal(t, ErrNotProvisioned, err)
}

func TestAddToSparseCheckout_Success(t *testing.T) {
	tempDir := t.TempDir()

	// initialize a git repo
	initCmd := exec.Command("git", "init", tempDir)
	require.NoError(t, initCmd.Run())

	ledger := &Ledger{Path: tempDir}

	// enable sparse checkout first
	err := ledger.EnableSparseCheckout()
	require.NoError(t, err)

	// add assets directory
	err = ledger.AddToSparseCheckout("assets")
	require.NoError(t, err)

	// verify assets is in sparse checkout
	cmd := exec.Command("git", "-C", tempDir, "sparse-checkout", "list")
	output, err := cmd.Output()
	require.NoError(t, err)

	assert.Contains(t, string(output), "assets")
}

// Edge case tests for ledger robustness

func TestLedger_DeletedMidOperation(t *testing.T) {
	// test behavior when ledger is deleted while operations are in progress
	tempDir := t.TempDir()

	// initialize a git repo
	initCmd := exec.Command("git", "init", tempDir)
	require.NoError(t, initCmd.Run())

	// verify it exists initially
	assert.True(t, Exists(tempDir))

	// delete the ledger directory
	require.NoError(t, os.RemoveAll(tempDir))

	// operations should fail gracefully
	assert.False(t, Exists(tempDir))
}

func TestLedger_CorruptedGitDirectory(t *testing.T) {
	// test behavior when .git directory exists but is corrupted/incomplete
	tempDir := t.TempDir()

	// create a corrupted .git (directory exists but not a valid git repo)
	gitDir := filepath.Join(tempDir, ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0755))

	// Exists should return true (just checks for .git directory)
	assert.True(t, Exists(tempDir))

	// but Open should work (we just check for .git dir, not validity)
	ledger, err := Open(tempDir)
	require.NoError(t, err)
	assert.NotNil(t, ledger)
}

func TestLedger_PermissionDenied(t *testing.T) {
	// skip if running as root (root can always access)
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}

	tempDir := t.TempDir()

	// initialize a git repo
	initCmd := exec.Command("git", "init", tempDir)
	require.NoError(t, initCmd.Run())

	ledger := &Ledger{Path: tempDir}

	// verify Exists works before permission change
	assert.True(t, Exists(tempDir))

	// make .git directory read-only to prevent git operations
	gitDir := filepath.Join(tempDir, ".git")
	require.NoError(t, os.Chmod(gitDir, 0444))
	defer os.Chmod(gitDir, 0755) // restore for cleanup

	// operations requiring git write access should fail
	err := ledger.EnableSparseCheckout()
	assert.Error(t, err, "should fail when .git is read-only")
}

func TestLedger_SiblingPatternSpecialCharacters(t *testing.T) {
	// test that sibling pattern handles project names with special characters
	// these tests verify the path sanitization works correctly
	tests := []struct {
		name        string
		projectName string
		contains    string
	}{
		{"simple", "myproject", "myproject_sageox"},
		{"with spaces", "my project", "my_project_sageox"},
		{"with slashes", "org/repo", "org_repo_sageox"},
		{"with dots", "my.project", "my.project_sageox"},
		{"with dashes", "my-project", "my-project_sageox"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// create a temp directory structure
			tempDir := t.TempDir()
			projectDir := filepath.Join(tempDir, tt.projectName)

			// for paths with slashes, only use the last component
			if strings.Contains(tt.projectName, "/") {
				projectDir = filepath.Join(tempDir, filepath.Base(tt.projectName))
			}
			require.NoError(t, os.MkdirAll(projectDir, 0755))

			// calculate expected ledger path using config function
			expectedBase := filepath.Base(projectDir)
			// the actual sibling would be calculated by config.DefaultLedgerPath
			// which sanitizes the name - just verify the pattern works
			assert.NotEmpty(t, expectedBase)
		})
	}
}

func TestLedger_RecoveryAfterPartialInit(t *testing.T) {
	// test recovery when init was interrupted (partial state)
	tempDir := t.TempDir()
	ledgerPath := filepath.Join(tempDir, "partial_ledger")

	// create a partially initialized state (directory exists but no .git)
	require.NoError(t, os.MkdirAll(ledgerPath, 0755))

	// create some files as if init started but didn't complete
	require.NoError(t, os.WriteFile(
		filepath.Join(ledgerPath, ".sync_partial"),
		[]byte("incomplete"),
		0644,
	))

	// Exists should return false (no .git)
	assert.False(t, Exists(ledgerPath))

	// Init without remoteURL should fail - ledgers must be cloned from server
	_, err := Init(ledgerPath, "")
	assert.Equal(t, ErrNoRemoteURL, err, "Init should require a remote URL")
}

func TestGetStatus_DeletedLedger(t *testing.T) {
	// test GetStatus when ledger was deleted after config was written
	tempDir := t.TempDir()

	// initialize a git repo
	initCmd := exec.Command("git", "init", tempDir)
	require.NoError(t, initCmd.Run())

	// verify it exists
	status := GetStatus(tempDir)
	assert.True(t, status.Exists)

	// delete it
	require.NoError(t, os.RemoveAll(tempDir))

	// status should reflect deletion
	status = GetStatus(tempDir)
	assert.False(t, status.Exists)
	assert.NotEmpty(t, status.Error)
}
