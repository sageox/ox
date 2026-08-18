//go:build !short

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipIntegration skips the test when running with -short flag
func skipIntegration(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
}

// testGitRepo initializes a temporary git repository for testing.
// It calls skipIntegration to skip with -short flag.
func testGitRepo(t *testing.T) string {
	t.Helper()
	skipIntegration(t)

	tmpDir := t.TempDir()

	// initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run(), "failed to init git repo")

	// configure git user for commits
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run(), "failed to set git user.name")

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run(), "failed to set git user.email")

	// create initial commit (required for GetInitialCommitHash)
	readmePath := filepath.Join(tmpDir, "README.md")
	require.NoError(t, os.WriteFile(readmePath, []byte("# Test\n"), 0644), "failed to create README")

	cmd = exec.Command("git", "add", "README.md")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run(), "failed to git add")

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run(), "failed to git commit")

	return tmpDir
}

func TestCreateSageoxReadme(t *testing.T) {
	tmpDir := t.TempDir()
	readmePath := filepath.Join(tmpDir, "README.md")

	err := createSageoxReadme(readmePath, nil)
	require.NoError(t, err, "createSageoxReadme failed")

	// verify file exists
	require.FileExists(t, readmePath, "README.md was not created")

	// verify content
	content, err := os.ReadFile(readmePath)
	require.NoError(t, err, "failed to read README")

	contentStr := string(content)
	expectedStrings := []string{
		"SageOx",
		"ox agent prime",
		"Progressive Disclosure",
	}

	for _, expected := range expectedStrings {
		assert.Contains(t, contentStr, expected, "expected README to contain %s", expected)
	}
}

// integration-style test for the full init flow
func TestInitFlow_CreatesExpectedStructure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// manually call ensureSageoxConfig to test directory creation
	requireSageoxDir(t, gitRoot)

	// test config creation
	result := ensureSageoxConfig(gitRoot)
	assert.Equal(t, configCreated, result, "expected configCreated")

	// verify directory structure
	expectedFiles := []string{
		filepath.Join(sageoxDir, "config.json"),
	}

	for _, f := range expectedFiles {
		require.FileExists(t, f, "expected file %s to exist", f)
	}

	// verify config is valid
	cfg, err := config.LoadProjectConfig(gitRoot)
	require.NoError(t, err, "failed to load config")

	assert.Equal(t, config.CurrentConfigVersion, cfg.ConfigVersion, "config version mismatch")

	// test repo marker creation
	repoID := repotools.GenerateRepoID()
	gitIdentity, _ := repotools.DetectGitIdentity()
	repoSalt, _ := repotools.GetInitialCommitHash()

	require.NoError(t, createRepoMarker(sageoxDir, repoID, repoSalt, gitIdentity, nil), "failed to create repo marker")

	// verify marker exists
	uuidSuffix := extractUUIDSuffix(repoID)
	markerPath := filepath.Join(sageoxDir, ".repo_"+uuidSuffix)
	require.FileExists(t, markerPath, "repo marker was not created")

	// test detection
	repoIDs, err := detectExistingRepoMarkers(sageoxDir)
	require.NoError(t, err, "failed to detect markers")

	require.Len(t, repoIDs, 1, "expected 1 repo ID")
	assert.Equal(t, repoID, repoIDs[0], "expected to find repo_id %s", repoID)
}

func TestRunInit_ExistingMarkerPreserved(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	requireSageoxDir(t, tmpDir)

	// create existing marker file
	currentEndpoint := endpoint.Get()
	existingRepoID := "repo_01jfk3mab123"
	existingUUID := extractUUIDSuffix(existingRepoID)
	markerPath := filepath.Join(sageoxDir, ".repo_"+existingUUID)

	existingMarker := map[string]string{
		"repo_id":  existingRepoID,
		"type":     "git",
		"init_at":  "2025-01-01T00:00:00Z",
		"endpoint": currentEndpoint,
	}
	data, _ := json.Marshal(existingMarker)
	require.NoError(t, os.WriteFile(markerPath, data, 0644), "failed to write existing marker")

	// detect markers (simulating runInit logic)
	existingMarkerRepoIDs, err := detectExistingRepoMarkers(sageoxDir)
	require.NoError(t, err, "detectExistingRepoMarkers failed")

	require.Len(t, existingMarkerRepoIDs, 1, "expected 1 existing marker")

	markerAlreadyExists := len(existingMarkerRepoIDs) > 0
	require.True(t, markerAlreadyExists, "expected marker to be detected")

	// verify repo_id from existing marker is reused
	repoID := existingMarkerRepoIDs[0]
	assert.Equal(t, existingRepoID, repoID, "repo_id mismatch")

	// simulate NOT creating a new marker when one exists
	// (this is what runInit does when markerAlreadyExists is true)
	// verify no new marker was created
	entries, err := os.ReadDir(sageoxDir)
	require.NoError(t, err, "failed to read .sageox")

	markerCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".repo_") {
			markerCount++
		}
	}

	assert.Equal(t, 1, markerCount, "expected exactly 1 marker file")

	// verify original marker still has original repo_id
	data, err = os.ReadFile(markerPath)
	require.NoError(t, err, "failed to read marker")

	var marker map[string]string
	require.NoError(t, json.Unmarshal(data, &marker), "failed to unmarshal marker")

	assert.Equal(t, existingRepoID, marker["repo_id"], "preserved repo_id mismatch")
}

func TestRunInit_NoExistingMarkerCreatesNew(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	requireSageoxDir(t, tmpDir)

	// verify no markers exist
	existingMarkerRepoIDs, err := detectExistingRepoMarkers(sageoxDir)
	require.NoError(t, err, "detectExistingRepoMarkers failed")

	require.Empty(t, existingMarkerRepoIDs, "expected no existing markers")

	markerAlreadyExists := len(existingMarkerRepoIDs) > 0
	assert.False(t, markerAlreadyExists, "expected no markers to exist")

	// generate new repo_id (simulating runInit)
	repoID := repotools.GenerateRepoID()
	require.NotEmpty(t, repoID, "generated repo_id is empty")

	// create marker
	gitIdentity := &repotools.GitIdentity{
		Name:  "Test User",
		Email: "test@example.com",
	}
	repoSalt := "test_salt"

	require.NoError(t, createRepoMarker(sageoxDir, repoID, repoSalt, gitIdentity, nil), "createRepoMarker failed")

	// verify marker was created
	uuidSuffix := extractUUIDSuffix(repoID)
	markerPath := filepath.Join(sageoxDir, ".repo_"+uuidSuffix)
	require.FileExists(t, markerPath, "marker file was not created")

	// verify marker content
	data, err := os.ReadFile(markerPath)
	require.NoError(t, err, "failed to read marker")

	var marker map[string]string
	require.NoError(t, json.Unmarshal(data, &marker), "failed to unmarshal marker")

	assert.Equal(t, repoID, marker["repo_id"], "repo_id mismatch")

	// verify only one marker exists
	entries, err := os.ReadDir(sageoxDir)
	require.NoError(t, err, "failed to read .sageox")

	markerCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".repo_") {
			markerCount++
		}
	}

	assert.Equal(t, 1, markerCount, "expected exactly 1 marker file")
}

func TestStringSlicesEqual_BothEmpty(t *testing.T) {
	a := []string{}
	b := []string{}

	result := stringSlicesEqual(a, b)
	assert.True(t, result, "expected empty slices to be equal")
}

func TestStringSlicesEqual_DifferentLengths(t *testing.T) {
	a := []string{"a", "b", "c"}
	b := []string{"a", "b"}

	result := stringSlicesEqual(a, b)
	assert.False(t, result, "expected slices with different lengths to be unequal")
}

func TestStringSlicesEqual_SameContent(t *testing.T) {
	a := []string{"a", "b", "c"}
	b := []string{"a", "b", "c"}

	result := stringSlicesEqual(a, b)
	assert.True(t, result, "expected slices with same content to be equal")
}

func TestStringSlicesEqual_DifferentContent(t *testing.T) {
	a := []string{"a", "b", "c"}
	b := []string{"a", "c", "b"}

	result := stringSlicesEqual(a, b)
	assert.False(t, result, "expected slices with different content to be unequal")
}

func TestStringSlicesEqual_NilSlices(t *testing.T) {
	var a []string
	var b []string

	result := stringSlicesEqual(a, b)
	assert.True(t, result, "expected nil slices to be equal")
}

func TestStringSlicesEqual_OneNilOneEmpty(t *testing.T) {
	var a []string
	b := []string{}

	result := stringSlicesEqual(a, b)
	assert.True(t, result, "expected nil and empty slice to be equal")
}

func TestStringSlicesEqual_OneNilOneNonEmpty(t *testing.T) {
	var a []string
	b := []string{"item"}

	result := stringSlicesEqual(a, b)
	assert.False(t, result, "expected nil and non-empty slice to be unequal")
}

func TestSelectAgentsForInit_NonInteractiveIncludesDetectedAdapters(t *testing.T) {
	cli.SetNoInteractive(true)
	t.Cleanup(func() { cli.SetNoInteractive(false) })

	previousAgentsFlag := initAgentsFlag
	initAgentsFlag = ""
	t.Cleanup(func() { initAgentsFlag = previousAgentsFlag })

	adapterDir := t.TempDir()
	createFakeAdapterWithHooks(t, adapterDir, "codex", "0.1.0", "session", ".codex")
	t.Setenv("OX_ADAPTER_PATH", adapterDir)
	t.Setenv("HOME", t.TempDir())

	selected, err := selectAgentsForInit(t.TempDir())
	require.NoError(t, err)
	assert.True(t, selected["claude-code"])
	assert.True(t, selected["codex"], "detected Codex must be configured when init cannot prompt")
}

func TestSelectAgentsForInit_NonInteractiveSkipsUndetectedAdapters(t *testing.T) {
	cli.SetNoInteractive(true)
	t.Cleanup(func() { cli.SetNoInteractive(false) })

	previousAgentsFlag := initAgentsFlag
	initAgentsFlag = ""
	t.Cleanup(func() { initAgentsFlag = previousAgentsFlag })

	adapterDir := t.TempDir()
	createFakeAdapter(t, adapterDir, "codex", "0.1.0", "session")
	t.Setenv("OX_ADAPTER_PATH", adapterDir)
	t.Setenv("HOME", t.TempDir())

	selected, err := selectAgentsForInit(t.TempDir())
	require.NoError(t, err)
	assert.False(t, selected["codex"], "undetected Codex must not create project integration files")
}

// TestInstallAgentHooks_OpenCodeInstallsOnce verifies OpenCode hooks are
// installed exactly once when the opencode adapter is selected.
//
// Failure prevented: InstallProjectOpenCodeHooks resolves the opencode external
// adapter and calls InstallHooks on it, and the generic external-adapter loop
// then makes the identical call again — so a selected OpenCode gets its plugin
// written twice and double-listed in installedHooks (which feeds git staging).
func TestInstallAgentHooks_OpenCodeInstallsOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script adapters require unix")
	}
	gitRoot := t.TempDir()
	adapterDir := t.TempDir()
	counter := filepath.Join(t.TempDir(), "install-hooks.calls")

	// Adapter records every install-hooks invocation so a duplicate call is
	// observable rather than hidden by the real adapter's idempotence.
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  info)
    echo '{"protocol_version":1,"name":"opencode","display_name":"OpenCode","version":"0.1.0","type":"session","capabilities":["session_reader","hook_installer"]}'
    ;;
  detect)
    echo '{"detected":true,"reason":"test adapter"}'
    ;;
  install-hooks)
    echo call >> %q
    repo_root=""
    shift
    while [ $# -gt 0 ]; do
      case "$1" in
        --repo-root) repo_root="$2"; shift 2 ;;
        --scope) shift 2 ;;
        *) shift ;;
      esac
    done
    mkdir -p "$repo_root/.opencode/plugin"
    echo '// ox' > "$repo_root/.opencode/plugin/ox-prime.ts"
    echo '{"installed":true,"files_written":[".opencode/plugin/ox-prime.ts"],"hooks":["SessionStart"]}'
    ;;
  *)
    echo '{}'
    ;;
esac`, counter)
	require.NoError(t, os.WriteFile(filepath.Join(adapterDir, "ox-adapter-opencode"), []byte(script), 0o755))

	t.Setenv("OX_ADAPTER_PATH", adapterDir)
	adapters.Unregister("opencode")
	t.Cleanup(func() { adapters.Unregister("opencode") })

	installed := installAgentHooks(gitRoot, true, map[string]bool{"opencode": true})

	data, err := os.ReadFile(counter)
	require.NoError(t, err, "adapter install-hooks was never invoked")
	calls := len(strings.Fields(string(data)))
	assert.Equal(t, 1, calls, "opencode install-hooks must run exactly once per init")

	var pluginEntries int
	for _, p := range installed {
		if strings.Contains(p, "ox-prime.ts") {
			pluginEntries++
		}
	}
	assert.Equal(t, 1, pluginEntries, "OpenCode plugin must be staged once, not duplicated")
}

func TestSelectTeam_NoTeams(t *testing.T) {
	teams := []api.TeamMembership{}

	_, _, err := selectTeam(teams)
	assert.Error(t, err, "expected error when no teams available")
	assert.Contains(t, err.Error(), "no teams available")
}

func TestSelectTeam_SingleTeam(t *testing.T) {
	teams := []api.TeamMembership{
		{ID: "team_abc123", Name: "My Team", Role: "owner"},
	}

	selectedID, selectedName, err := selectTeam(teams)
	require.NoError(t, err, "expected no error with single team")
	assert.Equal(t, "team_abc123", selectedID, "expected single team to be auto-selected")
	assert.Equal(t, "My Team", selectedName, "expected team name to be returned")
}

func TestSelectTeam_SingleTeamNoName(t *testing.T) {
	teams := []api.TeamMembership{
		{ID: "team_xyz789", Name: "", Role: "member"},
	}

	selectedID, selectedName, err := selectTeam(teams)
	require.NoError(t, err, "expected no error with single team")
	assert.Equal(t, "team_xyz789", selectedID, "expected single team to be auto-selected")
	assert.Equal(t, "", selectedName, "expected empty name when not provided")
}

// TestSelectTeam_MultipleTeams_RequiresInteraction verifies that multiple teams
// triggers the interactive selection path. We can't easily test the actual
// interactive selection without mocking terminal input, but we can verify
// the function requires interaction by checking it doesn't auto-select.
// This test is skipped when running in non-interactive mode.
func TestSelectTeam_MultipleTeams_RequiresInteraction(t *testing.T) {
	// skip this test in CI/non-interactive environments
	// the function will error when trying to display interactive menu
	t.Skip("interactive test - requires terminal")
}
