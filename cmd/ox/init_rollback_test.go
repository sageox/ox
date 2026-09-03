package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRollbackInit_RemovesCreatedFiles(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	// create files that would be created during init
	files := []string{
		filepath.Join(sageoxDir, ".repo_abc123"),
		filepath.Join(sageoxDir, "config.json"),
		filepath.Join(sageoxDir, "README.md"),
		filepath.Join(sageoxDir, ".gitignore"),
	}

	for _, f := range files {
		require.NoError(t, os.WriteFile(f, []byte("test content"), 0644), "failed to create %s", f)
	}

	// verify files exist
	for _, f := range files {
		require.FileExists(t, f, "file %s should exist before rollback", f)
	}

	// rollback using tracker
	tracker := newInitTracker(tmpDir)
	tracker.isFreshInit = true
	tracker.createdFiles = files
	tracker.createdDirs = []string{sageoxDir}
	tracker.rollback(true)

	// verify files are removed
	for _, f := range files {
		assert.NoFileExists(t, f, "file %s should be removed after rollback", f)
	}

	// verify directory is removed (it should be empty now)
	assert.NoDirExists(t, sageoxDir, "directory .sageox should be removed after rollback")
}

func TestRollbackInit_PreservesNonEmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	// create some files that will be in createdFiles (to be removed)
	createdFiles := []string{
		filepath.Join(sageoxDir, "README.md"),
		filepath.Join(sageoxDir, ".gitignore"),
	}

	for _, f := range createdFiles {
		require.NoError(t, os.WriteFile(f, []byte("test content"), 0644), "failed to create %s", f)
	}

	// create an extra file that is NOT in createdFiles (simulating pre-existing file)
	preExistingFile := filepath.Join(sageoxDir, "config.json")
	require.NoError(t, os.WriteFile(preExistingFile, []byte("pre-existing"), 0644), "failed to create pre-existing file")

	// rollback - but only for createdFiles
	tracker := newInitTracker(tmpDir)
	tracker.isFreshInit = true
	tracker.createdFiles = createdFiles
	tracker.createdDirs = []string{sageoxDir}
	tracker.rollback(true)

	// verify created files are removed
	for _, f := range createdFiles {
		assert.NoFileExists(t, f, "file %s should be removed after rollback", f)
	}

	// verify pre-existing file is still there
	assert.FileExists(t, preExistingFile, "pre-existing file should NOT be removed")

	// verify directory is NOT removed (because it still has files)
	assert.DirExists(t, sageoxDir, "directory should NOT be removed when it still has files")
}

func TestRollbackInit_OnlyFilesNoDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	// create files that were created during this init
	createdFiles := []string{
		filepath.Join(sageoxDir, "README.md"),
		filepath.Join(sageoxDir, ".gitignore"),
	}

	for _, f := range createdFiles {
		require.NoError(t, os.WriteFile(f, []byte("test content"), 0644), "failed to create %s", f)
	}

	// rollback with isFreshInit=false (simulating re-init on existing .sageox/)
	tracker := newInitTracker(tmpDir)
	tracker.createdFiles = createdFiles
	tracker.rollback(true)

	// verify created files are removed
	for _, f := range createdFiles {
		assert.NoFileExists(t, f, "file %s should be removed after rollback", f)
	}

	// verify directory is preserved (since isFreshInit is false)
	assert.DirExists(t, sageoxDir, "directory should be preserved when not a fresh init")
}

// TestRollbackInit_SkipsMissingAndSparesUntracked verifies rollback removes only
// what it tracked, and is not derailed by tracked paths that were never created.
//
// Failure prevented: a tracked file may never exist if init failed early.
// Aborting on the first missing path leaves the files that DID get created
// behind; deleting beyond the tracked set destroys the user's own files.
func TestRollbackInit_SkipsMissingAndSparesUntracked(t *testing.T) {
	tests := []struct {
		name string
		// tracked returns the paths to record as created, given the temp dir
		// and the path of a real file that exists on disk.
		tracked      func(dir, real string) []string
		isFreshInit  bool
		wantRealGone bool
	}{
		{
			name:         "an empty tracker removes nothing",
			tracked:      func(dir, real string) []string { return nil },
			wantRealGone: false,
		},
		{
			name:         "an empty tracker on a fresh init still removes nothing",
			tracked:      func(dir, real string) []string { return nil },
			isFreshInit:  true,
			wantRealGone: false,
		},
		{
			name: "missing tracked paths are skipped and the real one still goes",
			tracked: func(dir, real string) []string {
				// rollback walks createdFiles in reverse, so `real` must be
				// first for the missing paths to be reached before it. With
				// `real` last it is removed first and a rollback that aborts
				// on the first os.Remove error still passes.
				return []string{
					real,
					filepath.Join(dir, "nonexistent1.txt"),
					filepath.Join(dir, "nonexistent2.txt"),
				}
			},
			wantRealGone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			sentinel := filepath.Join(dir, "pre-existing.txt")
			if err := os.WriteFile(sentinel, []byte("keep me\n"), 0o644); err != nil {
				t.Fatalf("write sentinel: %v", err)
			}
			real := filepath.Join(dir, "created.txt")
			if err := os.WriteFile(real, []byte("remove me\n"), 0o644); err != nil {
				t.Fatalf("write tracked file: %v", err)
			}

			tracker := newInitTracker(dir)
			tracker.isFreshInit = tt.isFreshInit
			tracker.createdFiles = tt.tracked(dir, real)
			tracker.rollback(true)

			_, err := os.Stat(real)
			if tt.wantRealGone && !os.IsNotExist(err) {
				t.Error("rollback stopped at the first missing file and left a tracked file behind")
			}
			if !tt.wantRealGone && err != nil {
				t.Errorf("rollback removed an untracked file: %v", err)
			}
			// The sentinel is never tracked in any case.
			if _, err := os.Stat(sentinel); err != nil {
				t.Errorf("rollback removed an untracked file: %v", err)
			}
			if _, err := os.Stat(dir); err != nil {
				t.Errorf("rollback removed the tracker root: %v", err)
			}
		})
	}
}

func TestRollbackInit_ReInitScenario(t *testing.T) {
	// scenario: .sageox/ already exists with config.json,
	// but README.md and .gitignore are missing and get created during init
	// API fails -> should rollback README.md and .gitignore but preserve .sageox/ and config.json

	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	preExistingConfig := filepath.Join(sageoxDir, "config.json")
	require.NoError(t, os.WriteFile(preExistingConfig, []byte(`{"existing": true}`), 0644), "failed to create config.json")

	// simulate init creating new files (but NOT config.json since it already existed)
	newReadme := filepath.Join(sageoxDir, "README.md")
	newGitignore := filepath.Join(sageoxDir, ".gitignore")

	require.NoError(t, os.WriteFile(newReadme, []byte("# README"), 0644), "failed to create README.md")
	require.NoError(t, os.WriteFile(newGitignore, []byte("*.log"), 0644), "failed to create .gitignore")

	// rollback with isFreshInit=false (directory was pre-existing)
	tracker := newInitTracker(tmpDir)
	tracker.trackCreatedFile(newReadme)
	tracker.trackCreatedFile(newGitignore)
	tracker.rollback(true)

	// README.md should be removed
	assert.NoFileExists(t, newReadme, "README.md should be removed after rollback")

	// .gitignore should be removed
	assert.NoFileExists(t, newGitignore, ".gitignore should be removed after rollback")

	// config.json should still exist
	assert.FileExists(t, preExistingConfig, "config.json should be preserved (was pre-existing)")

	// .sageox/ should still exist
	assert.DirExists(t, sageoxDir, ".sageox/ should be preserved (was pre-existing)")
}

func TestRollbackInit_RestoresModifiedFiles(t *testing.T) {
	// scenario: config.json exists with original content,
	// init modifies it with repo_id, API fails -> should restore original content

	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	configPath := filepath.Join(sageoxDir, "config.json")
	originalContent := []byte(`{"config_version": "1.0", "repo_id": ""}`)
	require.NoError(t, os.WriteFile(configPath, originalContent, 0644), "failed to create config.json")

	// track modification before changing
	tracker := newInitTracker(tmpDir)
	tracker.trackModifiedFile(configPath)

	// simulate init modifying the config with repo_id
	modifiedContent := []byte(`{"config_version": "1.0", "repo_id": "repo_abc123"}`)
	require.NoError(t, os.WriteFile(configPath, modifiedContent, 0644), "failed to modify config.json")

	// rollback should restore original content
	tracker.rollback(true)

	// verify config.json was restored to original content
	restoredContent, err := os.ReadFile(configPath)
	require.NoError(t, err, "failed to read config.json")

	assert.Equal(t, string(originalContent), string(restoredContent), "config.json should be restored to original content")
}

func TestRollbackInit_RestoresAndRemoves(t *testing.T) {
	// scenario: config.json modified + new files created
	// API fails -> should restore config.json AND remove new files

	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	// pre-existing config
	configPath := filepath.Join(sageoxDir, "config.json")
	originalContent := []byte(`{"original": true}`)
	require.NoError(t, os.WriteFile(configPath, originalContent, 0644), "failed to create config.json")

	// track modification before changing
	tracker := newInitTracker(tmpDir)
	tracker.trackModifiedFile(configPath)

	// modify config
	require.NoError(t, os.WriteFile(configPath, []byte(`{"modified": true}`), 0644), "failed to modify config.json")

	// create new files
	newMarker := filepath.Join(sageoxDir, ".repo_abc123")
	require.NoError(t, os.WriteFile(newMarker, []byte(`{"repo_id": "repo_abc123"}`), 0644), "failed to create marker")
	tracker.trackCreatedFile(newMarker)

	// rollback
	tracker.rollback(true)

	// marker should be removed
	assert.NoFileExists(t, newMarker, "marker should be removed after rollback")

	// config should be restored
	restoredContent, err := os.ReadFile(configPath)
	require.NoError(t, err, "failed to read config.json")
	assert.Equal(t, string(originalContent), string(restoredContent), "config.json should be restored")
}

func TestRollbackInit_RollsBackAgentFiles(t *testing.T) {
	// scenario: AGENTS.md created, hook files installed, API fails
	// -> all should be cleaned up

	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)

	// simulate AGENTS.md being created by injectOxPrime
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte("# Agent Config"), 0644))

	// simulate hook file being created
	hookDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(hookDir, 0755))
	hookPath := filepath.Join(hookDir, "settings.json")
	require.NoError(t, os.WriteFile(hookPath, []byte("{}"), 0644))

	tracker := newInitTracker(tmpDir)
	tracker.trackCreatedFile(agentsPath)
	tracker.trackCreatedFile(hookPath)
	tracker.rollback(true)

	assert.NoFileExists(t, agentsPath, "AGENTS.md should be removed after rollback")
	assert.NoFileExists(t, hookPath, "hook file should be removed after rollback")
}

func TestRollbackInit_RollsBackGitattributes(t *testing.T) {
	// scenario: .gitattributes existed, was modified during init, API fails
	// -> should restore original content

	tmpDir := t.TempDir()

	gitattrsPath := filepath.Join(tmpDir, ".gitattributes")
	originalContent := []byte("*.png binary\n")
	require.NoError(t, os.WriteFile(gitattrsPath, originalContent, 0644))

	tracker := newInitTracker(tmpDir)
	tracker.trackModifiedFile(gitattrsPath)

	// simulate EnsureGitattributes adding entries
	require.NoError(t, os.WriteFile(gitattrsPath, []byte("*.png binary\n.sageox/** linguist-generated\n"), 0644))

	tracker.rollback(true)

	restoredContent, err := os.ReadFile(gitattrsPath)
	require.NoError(t, err)
	assert.Equal(t, string(originalContent), string(restoredContent), ".gitattributes should be restored")
}

func TestRollbackInit_RollsBackCreatedGitattributes(t *testing.T) {
	// scenario: .gitattributes didn't exist, was created during init, API fails
	// -> should be deleted

	tmpDir := t.TempDir()

	gitattrsPath := filepath.Join(tmpDir, ".gitattributes")
	require.NoError(t, os.WriteFile(gitattrsPath, []byte(".sageox/** linguist-generated\n"), 0644))

	tracker := newInitTracker(tmpDir)
	tracker.trackCreatedFile(gitattrsPath)
	tracker.rollback(true)

	assert.NoFileExists(t, gitattrsPath, ".gitattributes should be removed after rollback")
}
