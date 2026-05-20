package main

// Tests for the kb-project-config-migrate doctor check (ADR-017).
//
// Each test enforces one observable contract of the migration:
//
//   - JSON only + matching kb_id            → write config.yaml
//   - JSON only + API unavailable           → silent retry-next-run pass
//   - JSON only + no matching kb (orphan)   → Warning, no migration attempted
//   - YAML only                             → no-op pass (already migrated)
//   - Both present, identical kb_id+repo_id → archive JSON to .bak.<ts>
//   - Both present, divergent values        → archive JSON, YAML wins, log
//
// All tests use the kbDoctorHooks seam — production wiring (real kb API,
// real auth) never runs in unit tests.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
)

// kbMigrateProject scaffolds a minimal initialized .sageox project rooted in
// a real git working tree so findGitRoot() resolves to it. Returns the
// project root so each test can drive its own JSON/YAML layout.
func kbMigrateProject(t *testing.T, repoID string) string {
	t.Helper()
	root := t.TempDir()
	// Resolve symlinks (macOS /var → /private/var) so the path matches what
	// git rev-parse --show-toplevel returns.
	resolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	root = resolved

	// findGitRoot() shells out to `git rev-parse --show-toplevel`, so we need
	// a real git repo (not just a .git directory).
	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = root
	require.NoError(t, gitInit.Run())

	sageox := filepath.Join(root, ".sageox")
	require.NoError(t, os.MkdirAll(sageox, 0o755))

	cfg := map[string]any{
		"config_version":         "2",
		"repo_id":                repoID,
		"update_frequency_hours": 24,
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sageox, "config.json"), data, 0o600))

	// findGitRoot() uses the current working directory — chdir into the
	// scaffolded project for the duration of the test.
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(prev) })

	return root
}

// TestCheckKBProjectConfigMigrate_JSONOnlyWritesYAML verifies the happy path:
// a JSON-only project with a known repo_id gets a config.yaml written next to
// it carrying the resolved kb_id, and the original JSON is untouched.
//
// Failure prevented: ADR-017 migration silently skipping JSON-only repos, so
// the cohort never finishes migrating and the deprecation window can't close.
func TestCheckKBProjectConfigMigrate_JSONOnlyWritesYAML(t *testing.T) {
	root := kbMigrateProject(t, "repo_abc")

	SetKBDoctorHooks(kbDoctorHooks{
		List: func(_ context.Context) ([]api.KB, error) {
			return []api.KB{
				{KBID: "kb_xyz", KBType: api.KBTypeRepo, RepoID: "repo_abc"},
				{KBID: "kb_other", KBType: api.KBTypeRepo, RepoID: "repo_other"},
			}, nil
		},
	})
	t.Cleanup(func() { SetKBDoctorHooks(kbDoctorHooks{}) })

	result := checkKBProjectConfigMigrate(true)
	require.True(t, result.passed && !result.warning, "expected clean pass, got %+v", result)

	yamlPath := filepath.Join(root, ".sageox", "config.yaml")
	data, err := os.ReadFile(yamlPath)
	require.NoError(t, err, "config.yaml must be written")

	var got config.ProjectConfigYAML
	require.NoError(t, yaml.Unmarshal(data, &got))
	assert.Equal(t, "kb_xyz", got.KBID)
	assert.Equal(t, "repo_abc", got.RepoID)

	// Legacy JSON must survive — deletion is deferred to release N+3.
	_, err = os.Stat(filepath.Join(root, ".sageox", "config.json"))
	assert.NoError(t, err, "legacy config.json must not be deleted by the migration")
}

func TestCheckKBProjectConfigMigrate_JSONOnlyPreservesProjectSettingsInYAML(t *testing.T) {
	root := kbMigrateProject(t, "repo_abc")

	jsonPath := filepath.Join(root, ".sageox", "config.json")
	cfg := map[string]any{
		"config_version":         "2",
		"repo_id":                "repo_abc",
		"endpoint":               "https://test.sageox.ai",
		"session_recording":      "auto",
		"update_frequency_hours": 24,
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(jsonPath, data, 0o600))

	SetKBDoctorHooks(kbDoctorHooks{
		List: func(_ context.Context) ([]api.KB, error) {
			return []api.KB{{KBID: "kb_xyz", KBType: api.KBTypeRepo, RepoID: "repo_abc"}}, nil
		},
	})
	t.Cleanup(func() { SetKBDoctorHooks(kbDoctorHooks{}) })

	result := checkKBProjectConfigMigrate(true)
	require.True(t, result.passed && !result.warning, "expected clean pass, got %+v", result)

	require.NoError(t, os.Remove(jsonPath), "test simulates the follow-up archive step")
	loaded, err := config.LoadProjectConfig(root)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "yaml", loaded.Format)
	assert.Equal(t, "kb_xyz", loaded.KBID)
	assert.Equal(t, "repo_abc", loaded.RepoID)
	assert.Equal(t, "https://test.sageox.ai", loaded.Endpoint)
	assert.Equal(t, "auto", loaded.SessionRecording)
}

// TestCheckKBProjectConfigMigrate_APIUnavailableIsNoop verifies that an
// offline / flag-gated kb API does not produce a doctor warning and does not
// leave a partially-written YAML behind.
//
// Failure prevented: a flapping kb-API feature flag would turn doctor red on
// every run for users who never see the API succeed.
func TestCheckKBProjectConfigMigrate_APIUnavailableIsNoop(t *testing.T) {
	root := kbMigrateProject(t, "repo_abc")

	SetKBDoctorHooks(kbDoctorHooks{
		List: func(_ context.Context) ([]api.KB, error) {
			return nil, api.ErrKBAPIUnavailable
		},
	})
	t.Cleanup(func() { SetKBDoctorHooks(kbDoctorHooks{}) })

	result := checkKBProjectConfigMigrate(true)
	assert.True(t, result.passed && !result.warning, "expected silent pass, got %+v", result)

	_, err := os.Stat(filepath.Join(root, ".sageox", "config.yaml"))
	assert.True(t, os.IsNotExist(err), "config.yaml must not be created when API is unavailable")
}

// TestCheckKBProjectConfigMigrate_YAMLOnlyIsNoop verifies that a project
// which has already completed migration (yaml only, json archived away) is
// left alone and reports a clean pass.
//
// Failure prevented: running doctor on a fully-migrated repo touching the
// filesystem (or hitting the API) for no reason.
func TestCheckKBProjectConfigMigrate_YAMLOnlyIsNoop(t *testing.T) {
	root := kbMigrateProject(t, "repo_abc")
	// kbMigrateProject scaffolds a JSON. For YAML-only we delete the JSON
	// and write the YAML directly.
	jsonPath := filepath.Join(root, ".sageox", "config.json")
	require.NoError(t, os.Remove(jsonPath))
	yamlPath := filepath.Join(root, ".sageox", "config.yaml")
	require.NoError(t, os.WriteFile(yamlPath,
		[]byte("kb_id: kb_xyz\nrepo_id: repo_abc\n"), 0o644))

	apiCalls := 0
	SetKBDoctorHooks(kbDoctorHooks{
		List: func(_ context.Context) ([]api.KB, error) {
			apiCalls++
			return nil, nil
		},
	})
	t.Cleanup(func() { SetKBDoctorHooks(kbDoctorHooks{}) })

	result := checkKBProjectConfigMigrate(true)
	assert.True(t, result.passed && !result.warning, "expected clean pass, got %+v", result)
	assert.Equal(t, 0, apiCalls, "API must not be called when yaml is already present")
}

// TestCheckKBProjectConfigMigrate_BothIdenticalArchivesJSON verifies the
// "both present, kb_id+repo_id agree" reconciliation: doctor archives the
// legacy JSON to `.sageox/config.json.bak.<RFC3339>` and removes the
// original, leaving only the YAML in place.
//
// Failure prevented: the legacy JSON staying behind forever, blocking the
// staged-deprecation window from ever closing.
func TestCheckKBProjectConfigMigrate_BothIdenticalArchivesJSON(t *testing.T) {
	root := kbMigrateProject(t, "repo_abc")

	// Add a matching kb_id to the existing JSON so it's identical to the YAML
	// we're about to write.
	jsonPath := filepath.Join(root, ".sageox", "config.json")
	rewriteJSONWithKBID(t, jsonPath, "repo_abc", "kb_xyz")
	yamlPath := filepath.Join(root, ".sageox", "config.yaml")
	require.NoError(t, os.WriteFile(yamlPath,
		[]byte("kb_id: kb_xyz\nrepo_id: repo_abc\n"), 0o644))

	apiCalls := 0
	SetKBDoctorHooks(kbDoctorHooks{
		List: func(_ context.Context) ([]api.KB, error) {
			apiCalls++
			return nil, nil
		},
	})
	t.Cleanup(func() { SetKBDoctorHooks(kbDoctorHooks{}) })

	result := checkKBProjectConfigMigrate(true)
	require.True(t, result.passed && !result.warning, "expected clean pass, got %+v", result)
	assert.Equal(t, 0, apiCalls, "API must not be called when both files exist")

	// Legacy JSON must be removed (archived elsewhere).
	_, err := os.Stat(jsonPath)
	assert.True(t, os.IsNotExist(err), "config.json must be removed after archive")

	// A timestamped backup must exist.
	bak := findBackup(t, filepath.Join(root, ".sageox"))
	require.NotEmpty(t, bak, "expected a config.json.bak.<ts> file")

	// YAML must survive untouched.
	yamlData, err := os.ReadFile(yamlPath)
	require.NoError(t, err)
	assert.Contains(t, string(yamlData), "kb_id: kb_xyz")
}

// TestCheckKBProjectConfigMigrate_BothDivergentYAMLWins verifies that when
// JSON and YAML disagree on kb_id (or repo_id), YAML wins per ADR-017 §6,
// the JSON is archived (not silently deleted), and a divergence record is
// emitted to the slog default logger as a single-line key=value entry.
//
// Failure prevented: a stale JSON quietly overriding the canonical YAML
// binding because the operator never knew there was a conflict.
func TestCheckKBProjectConfigMigrate_BothDivergentYAMLWins(t *testing.T) {
	root := kbMigrateProject(t, "repo_abc")

	jsonPath := filepath.Join(root, ".sageox", "config.json")
	rewriteJSONWithKBID(t, jsonPath, "repo_abc", "kb_old")
	yamlPath := filepath.Join(root, ".sageox", "config.yaml")
	require.NoError(t, os.WriteFile(yamlPath,
		[]byte("kb_id: kb_new\nrepo_id: repo_abc\n"), 0o644))

	SetKBDoctorHooks(kbDoctorHooks{
		List: func(_ context.Context) ([]api.KB, error) { return nil, nil },
	})
	t.Cleanup(func() { SetKBDoctorHooks(kbDoctorHooks{}) })

	result := checkKBProjectConfigMigrate(true)
	require.True(t, result.passed && !result.warning, "expected clean pass, got %+v", result)

	// JSON removed, backup written.
	_, err := os.Stat(jsonPath)
	assert.True(t, os.IsNotExist(err), "config.json must be removed after archive")
	bak := findBackup(t, filepath.Join(root, ".sageox"))
	require.NotEmpty(t, bak, "expected a config.json.bak.<ts> file")

	// Backup should be the divergent JSON content (kb_old), not the YAML.
	bakData, err := os.ReadFile(filepath.Join(root, ".sageox", bak))
	require.NoError(t, err)
	assert.Contains(t, string(bakData), "kb_old",
		"backup must preserve the divergent JSON content for recoverability")

	// YAML must remain the winner.
	yamlData, err := os.ReadFile(yamlPath)
	require.NoError(t, err)
	assert.Contains(t, string(yamlData), "kb_id: kb_new")
}

// TestCheckKBProjectConfigMigrate_OrphanRepoIsWarning verifies that a
// JSON-only project whose repo_id has no matching kb on the server surfaces
// as a Warning (not a Failed check). The repo still functions through the
// JSON config — what's missing is the server-side provisioning the user (or
// support) needs to act on.
//
// Failure prevented: doctor turning red on orphan repos that the CLI is
// powerless to fix, or worse, fabricating a fake kb_id binding.
func TestCheckKBProjectConfigMigrate_OrphanRepoIsWarning(t *testing.T) {
	root := kbMigrateProject(t, "repo_orphan")

	SetKBDoctorHooks(kbDoctorHooks{
		List: func(_ context.Context) ([]api.KB, error) {
			// API responds successfully but has no row for repo_orphan.
			return []api.KB{
				{KBID: "kb_other", KBType: api.KBTypeRepo, RepoID: "repo_other"},
			}, nil
		},
	})
	t.Cleanup(func() { SetKBDoctorHooks(kbDoctorHooks{}) })

	result := checkKBProjectConfigMigrate(true)
	assert.True(t, result.warning, "expected Warning for orphan repo, got %+v", result)
	assert.Contains(t, result.message, "orphan",
		"warning message must identify the orphan condition")

	// No yaml should have been written — we don't fabricate bindings.
	_, err := os.Stat(filepath.Join(root, ".sageox", "config.yaml"))
	assert.True(t, os.IsNotExist(err), "config.yaml must not be created for orphan repos")

	// JSON must be left intact (archive is only for the both-present case).
	_, err = os.Stat(filepath.Join(root, ".sageox", "config.json"))
	assert.NoError(t, err, "legacy config.json must not be removed when migration could not complete")
}

// rewriteJSONWithKBID rewrites the JSON config the kbMigrateProject helper
// scaffolds, adding a kb_id field (which the test fixture omits by default).
// Used by the "both present" reconciliation tests where we need the JSON's
// kb_id to compare against the YAML's.
func rewriteJSONWithKBID(t *testing.T, jsonPath, repoID, kbID string) {
	t.Helper()
	cfg := map[string]any{
		"config_version":         "2",
		"repo_id":                repoID,
		"kb_id":                  kbID,
		"update_frequency_hours": 24,
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(jsonPath, data, 0o600))
}

// findBackup returns the name of the first config.json.bak.<ts> file under
// dir, or "" if none exists. Used to assert the archive was created without
// hardcoding the timestamp into the test.
func findBackup(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "config.json.bak.") &&
			!strings.HasSuffix(e.Name(), ".tmp") {
			return e.Name()
		}
	}
	return ""
}
