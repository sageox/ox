package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDefaultProjectConfig(t *testing.T) {
	cfg := GetDefaultProjectConfig()

	require.NotNil(t, cfg, "expected non-nil config")

	assert.Equal(t, defaultUpdateFrequencyHours, cfg.UpdateFrequencyHours, "UpdateFrequencyHours")

	assert.Nil(t, cfg.LastUpdateCheckUTC, "expected LastUpdateCheckUTC=nil")
}

func TestLoadProjectConfig_NonExistent(t *testing.T) {
	// create temp directory
	tmpDir := t.TempDir()

	cfg, err := LoadProjectConfig(tmpDir)
	require.NoError(t, err, "expected no error for non-existent config")

	// should return default config
	defaultCfg := GetDefaultProjectConfig()
	assert.Equal(t, defaultCfg.UpdateFrequencyHours, cfg.UpdateFrequencyHours, "expected default UpdateFrequencyHours")
}

func TestLoadProjectConfig_EmptyGitRoot(t *testing.T) {
	_, err := LoadProjectConfig("")
	assert.Error(t, err, "expected error for empty git root")
}

func TestSaveAndLoadProjectConfig(t *testing.T) {
	// create temp directory
	tmpDir := t.TempDir()

	// create config with custom values
	timestamp := time.Now().UTC().Format(time.RFC3339)

	cfg := &ProjectConfig{
		UpdateFrequencyHours: 12,
		LastUpdateCheckUTC:   &timestamp,
	}

	// save config
	require.NoError(t, SaveProjectConfig(tmpDir, cfg), "failed to save config")

	// verify file exists
	configPath := filepath.Join(tmpDir, sageoxDir, projectConfigFilename)
	_, err := os.Stat(configPath)
	require.False(t, os.IsNotExist(err), "config file was not created")

	// load config back
	loadedCfg, err := LoadProjectConfig(tmpDir)
	require.NoError(t, err, "failed to load config")

	// verify values
	assert.Equal(t, 12, loadedCfg.UpdateFrequencyHours, "UpdateFrequencyHours")

	require.NotNil(t, loadedCfg.LastUpdateCheckUTC, "LastUpdateCheckUTC should not be nil")
	assert.Equal(t, timestamp, *loadedCfg.LastUpdateCheckUTC, "LastUpdateCheckUTC")
}

func TestSaveProjectConfig_NilConfig(t *testing.T) {
	tmpDir := t.TempDir()

	err := SaveProjectConfig(tmpDir, nil)
	assert.Error(t, err, "expected error for nil config")
}

func TestSaveProjectConfig_EmptyGitRoot(t *testing.T) {
	cfg := GetDefaultProjectConfig()

	err := SaveProjectConfig("", cfg)
	assert.Error(t, err, "expected error for empty git root")
}

func TestSaveProjectConfig_AppliesDefaults(t *testing.T) {
	tmpDir := t.TempDir()

	// create config with missing defaults
	cfg := &ProjectConfig{
		UpdateFrequencyHours: 0, // invalid, should be set to default
	}

	require.NoError(t, SaveProjectConfig(tmpDir, cfg), "failed to save config")

	// load back and verify defaults were applied
	loadedCfg, err := LoadProjectConfig(tmpDir)
	require.NoError(t, err, "failed to load config")

	assert.Equal(t, defaultUpdateFrequencyHours, loadedCfg.UpdateFrequencyHours, "expected default UpdateFrequencyHours")
}

func TestValidateProjectConfig_Valid(t *testing.T) {
	cfg := GetDefaultProjectConfig()

	errors := ValidateProjectConfig(cfg)
	assert.Empty(t, errors, "expected no validation errors")
}

func TestValidateProjectConfig_NilConfig(t *testing.T) {
	errors := ValidateProjectConfig(nil)
	require.Len(t, errors, 1, "expected 1 validation error")
	assert.Equal(t, "config is nil", errors[0])
}

func TestValidateProjectConfig_InvalidUpdateFrequency(t *testing.T) {
	cfg := GetDefaultProjectConfig()
	cfg.UpdateFrequencyHours = 0

	errors := ValidateProjectConfig(cfg)
	require.Len(t, errors, 1, "expected 1 validation error")
	assert.Equal(t, "update_frequency_hours must be greater than 0", errors[0])
}

func TestValidateProjectConfig_InvalidTimestamp(t *testing.T) {
	cfg := GetDefaultProjectConfig()
	invalidTimestamp := "not-a-timestamp"
	cfg.LastUpdateCheckUTC = &invalidTimestamp

	errors := ValidateProjectConfig(cfg)
	assert.Len(t, errors, 1, "expected 1 validation error")
}

func TestValidateProjectConfig_ValidTimestamp(t *testing.T) {
	cfg := GetDefaultProjectConfig()
	validTimestamp := time.Now().UTC().Format(time.RFC3339)
	cfg.LastUpdateCheckUTC = &validTimestamp

	errors := ValidateProjectConfig(cfg)
	assert.Empty(t, errors, "expected no validation errors for valid timestamp")
}

func TestValidateProjectConfig_MultipleErrors(t *testing.T) {
	invalidTimestamp := "not-a-timestamp"
	cfg := &ProjectConfig{
		UpdateFrequencyHours: -1,
		LastUpdateCheckUTC:   &invalidTimestamp,
	}

	errors := ValidateProjectConfig(cfg)
	assert.Len(t, errors, 2, "expected 2 validation errors")
}

func TestJSONMarshaling(t *testing.T) {
	timestamp := "2025-12-09T10:30:00Z"

	cfg := &ProjectConfig{
		UpdateFrequencyHours: 48,
		LastUpdateCheckUTC:   &timestamp,
	}

	// marshal to JSON
	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err, "failed to marshal config")

	// unmarshal back
	var unmarshaled ProjectConfig
	require.NoError(t, json.Unmarshal(data, &unmarshaled), "failed to unmarshal config")

	// verify fields
	assert.Equal(t, 48, unmarshaled.UpdateFrequencyHours, "UpdateFrequencyHours")
}

func TestJSONMarshaling_OmitsNullFields(t *testing.T) {
	cfg := &ProjectConfig{
		UpdateFrequencyHours: 24,
		LastUpdateCheckUTC:   nil,
	}

	// marshal to JSON
	data, err := json.Marshal(cfg)
	require.NoError(t, err, "failed to marshal config")

	// parse as generic map to check which fields are present
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &raw), "failed to unmarshal to map")

	// last_update_check_utc should not be present
	_, exists := raw["last_update_check_utc"]
	assert.False(t, exists, "expected last_update_check_utc to be omitted when nil")
}

func TestLoadProjectConfig_AppliesDefaults(t *testing.T) {
	tmpDir := t.TempDir()

	// create a minimal config file
	minimalConfig := `{}`

	RequireSageoxDir(t, tmpDir)

	configPath := filepath.Join(tmpDir, sageoxDir, projectConfigFilename)
	require.NoError(t, os.WriteFile(configPath, []byte(minimalConfig), 0644), "failed to write config file")

	// load config
	cfg, err := LoadProjectConfig(tmpDir)
	require.NoError(t, err, "failed to load config")

	// verify defaults were applied
	assert.Equal(t, defaultUpdateFrequencyHours, cfg.UpdateFrequencyHours, "expected default UpdateFrequencyHours")
}

func TestFindProjectConfigPathFromDir_WalksUpDirectories(t *testing.T) {
	// create a temporary directory structure
	tmpDir, err := os.MkdirTemp("", "sageox-test-*")
	require.NoError(t, err, "failed to create temp dir")
	defer os.RemoveAll(tmpDir)

	// create nested directory structure
	nestedDir := filepath.Join(tmpDir, "project", "src", "deep")
	require.NoError(t, os.MkdirAll(nestedDir, 0755), "failed to create nested dir")

	// create .sageox directory at project root
	sageoxDir := filepath.Join(tmpDir, "project", ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755), "failed to create .sageox dir")

	// create config.json
	configPath := filepath.Join(sageoxDir, "config.json")
	configData := []byte(`{"org":"test-org","team":"test-team","project":"test-project"}`)
	require.NoError(t, os.WriteFile(configPath, configData, 0644), "failed to write config file")

	// test from nested directory - should find config at project root
	foundPath, err := findProjectConfigPathFromDir(nestedDir)
	require.NoError(t, err, "unexpected error")

	assert.NotEmpty(t, foundPath, "expected to find config, but got empty path")

	assert.Equal(t, configPath, foundPath, "expected path")
}

func TestFindProjectConfigPathFromDir_StopsAtRoot(t *testing.T) {
	// create a temporary directory without .sageox
	tmpDir, err := os.MkdirTemp("", "sageox-test-*")
	require.NoError(t, err, "failed to create temp dir")
	defer os.RemoveAll(tmpDir)

	// test from directory without config - should return empty string
	foundPath, err := findProjectConfigPathFromDir(tmpDir)
	require.NoError(t, err, "unexpected error")

	assert.Empty(t, foundPath, "expected empty path when no config found")
}

func TestGetProjectContext_WithOrgTeamProject(t *testing.T) {
	// create a temporary directory structure
	tmpDir, err := os.MkdirTemp("", "sageox-test-*")
	require.NoError(t, err, "failed to create temp dir")
	defer os.RemoveAll(tmpDir)

	// save a config with org/team/project
	cfg := &ProjectConfig{
		Org:     "ghost-layer",
		Team:    "platform",
		Project: "sageox",
	}

	require.NoError(t, SaveProjectConfig(tmpDir, cfg), "failed to save config")

	// change to the directory
	originalDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmpDir), "failed to change directory")
	defer os.Chdir(originalDir)

	// test GetProjectContext
	loadedCfg, loadedPath, err := GetProjectContext()
	require.NoError(t, err, "unexpected error")

	require.NotNil(t, loadedCfg, "expected config, got nil")

	assert.NotEmpty(t, loadedPath, "expected path, got empty string")

	// verify the org/team/project values
	assert.Equal(t, cfg.Org, loadedCfg.Org, "org")
	assert.Equal(t, cfg.Team, loadedCfg.Team, "team")
	assert.Equal(t, cfg.Project, loadedCfg.Project, "project")
}

func TestGetProjectContext_NoConfigReturnsNil(t *testing.T) {
	// create a temporary directory without .sageox
	tmpDir, err := os.MkdirTemp("", "sageox-test-*")
	require.NoError(t, err, "failed to create temp dir")
	defer os.RemoveAll(tmpDir)

	// change to directory without config
	originalDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmpDir), "failed to change directory")
	defer os.Chdir(originalDir)

	// test GetProjectContext - should return nil without error
	loadedCfg, loadedPath, err := GetProjectContext()
	require.NoError(t, err, "unexpected error")

	assert.Nil(t, loadedCfg, "expected nil config when no config found")

	assert.Empty(t, loadedPath, "expected empty path when no config found")
}

func TestProjectConfig_OrgTeamProjectFields_JSON(t *testing.T) {
	cfg := &ProjectConfig{
		Org:     "test-org",
		Team:    "test-team",
		Project: "test-project",
	}

	// apply defaults to get valid config
	applyDefaults(cfg)

	// marshal to JSON
	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err, "failed to marshal")

	// unmarshal to verify structure
	var jsonData map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &jsonData), "failed to unmarshal")

	// verify the new fields are present
	org, ok := jsonData["org"].(string)
	assert.True(t, ok && org == "test-org", "expected org=test-org")
	team, ok := jsonData["team"].(string)
	assert.True(t, ok && team == "test-team", "expected team=test-team")
	project, ok := jsonData["project"].(string)
	assert.True(t, ok && project == "test-project", "expected project=test-project")
}

func TestProjectConfig_OrgTeamProject_Omitempty(t *testing.T) {
	cfg := &ProjectConfig{}
	applyDefaults(cfg)

	// marshal to JSON
	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err, "failed to marshal")

	// verify the JSON omits empty org/team/project fields
	var jsonData map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &jsonData), "failed to unmarshal")

	_, exists := jsonData["org"]
	assert.False(t, exists, "expected org to be omitted when empty")
	_, exists = jsonData["team"]
	assert.False(t, exists, "expected team to be omitted when empty")
	_, exists = jsonData["project"]
	assert.False(t, exists, "expected project to be omitted when empty")

	// update_frequency_hours should be present with default value
	_, exists = jsonData["update_frequency_hours"]
	assert.True(t, exists, "expected update_frequency_hours to be present")
}

// TestLoadProjectConfig_MigratesLegacyEndpointFields tests that old configs with
// api_base_url and web_base_url are migrated to the new endpoint field.
// TODO: Remove after 2026-01-31 when legacy field support is removed
func TestLoadProjectConfig_MigratesLegacyEndpointFields(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	RequireSageoxDir(t, tmpDir)

	// write config with legacy api_base_url field (no endpoint field)
	legacyConfig := map[string]any{
		"config_version":         "2",
		"api_base_url":           "https://legacy.example.com",
		"web_base_url":           "https://web.example.com",
		"update_frequency_hours": 24,
	}
	data, err := json.MarshalIndent(legacyConfig, "", "  ")
	require.NoError(t, err, "failed to marshal legacy config")
	configPath := filepath.Join(tmpDir, sageoxDir, projectConfigFilename)
	require.NoError(t, os.WriteFile(configPath, data, 0600), "failed to write legacy config")

	// load config - should trigger migration
	cfg, err := LoadProjectConfig(tmpDir)
	require.NoError(t, err, "failed to load config")

	// verify endpoint was migrated from api_base_url
	assert.Equal(t, "https://legacy.example.com", cfg.Endpoint, "expected Endpoint to be migrated from api_base_url")

	// verify legacy fields were cleared
	assert.Empty(t, cfg.APIBaseURL, "expected APIBaseURL to be cleared after migration")
	assert.Empty(t, cfg.WebBaseURL, "expected WebBaseURL to be cleared after migration")

	// verify the file was updated with migrated values
	reloadedData, err := os.ReadFile(configPath)
	require.NoError(t, err, "failed to read migrated config")

	var reloadedJSON map[string]any
	require.NoError(t, json.Unmarshal(reloadedData, &reloadedJSON), "failed to unmarshal migrated config")

	// new endpoint field should be present
	endpoint, ok := reloadedJSON["endpoint"].(string)
	assert.True(t, ok && endpoint == "https://legacy.example.com", "expected endpoint field in saved config")

	// legacy fields should be absent (omitempty)
	_, exists := reloadedJSON["api_base_url"]
	assert.False(t, exists, "expected api_base_url to be omitted after migration")
	_, exists = reloadedJSON["web_base_url"]
	assert.False(t, exists, "expected web_base_url to be omitted after migration")
}

// TestSaveProjectConfig_NormalizesEndpoint tests that saving a config with a
// prefixed endpoint normalizes it before writing to disk.
func TestSaveProjectConfig_NormalizesEndpoint(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	cfg := &ProjectConfig{
		ConfigVersion: CurrentConfigVersion,
		Endpoint:      "https://api.sageox.ai",
	}

	require.NoError(t, SaveProjectConfig(tmpDir, cfg), "failed to save config")

	// read raw JSON to verify the on-disk endpoint is normalized
	configPath := filepath.Join(tmpDir, sageoxDir, projectConfigFilename)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err, "failed to read config")

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw), "failed to parse config")

	ep, ok := raw["endpoint"].(string)
	assert.True(t, ok, "expected endpoint in config")
	assert.Equal(t, "https://sageox.ai", ep, "endpoint should be normalized on disk")
}

// TestLoadProjectConfig_NormalizesStoredPrefixedEndpoint tests that loading a config
// with a stored prefixed endpoint normalizes it.
func TestLoadProjectConfig_NormalizesStoredPrefixedEndpoint(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	RequireSageoxDir(t, tmpDir)

	// write raw config with prefixed endpoint (bypass SaveProjectConfig)
	rawConfig := map[string]any{
		"config_version":         CurrentConfigVersion,
		"endpoint":               "https://www.test.sageox.ai",
		"update_frequency_hours": 24,
	}
	data, err := json.MarshalIndent(rawConfig, "", "  ")
	require.NoError(t, err, "failed to marshal config")
	configPath := filepath.Join(tmpDir, sageoxDir, projectConfigFilename)
	require.NoError(t, os.WriteFile(configPath, data, 0600), "failed to write config")

	// load should normalize
	cfg, err := LoadProjectConfig(tmpDir)
	require.NoError(t, err, "failed to load config")
	assert.Equal(t, "https://test.sageox.ai", cfg.Endpoint, "endpoint should be normalized on load")
}

// TestSaveAndLoadProjectConfig_EndpointRoundTrip tests that save→load→save→load
// produces stable, normalized endpoint values.
func TestSaveAndLoadProjectConfig_EndpointRoundTrip(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// save with prefixed endpoint
	cfg := &ProjectConfig{
		ConfigVersion: CurrentConfigVersion,
		Endpoint:      "https://api.sageox.ai",
	}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg), "first save failed")

	// load (should be normalized)
	loaded1, err := LoadProjectConfig(tmpDir)
	require.NoError(t, err, "first load failed")
	assert.Equal(t, "https://sageox.ai", loaded1.Endpoint)

	// save again (should stay normalized)
	require.NoError(t, SaveProjectConfig(tmpDir, loaded1), "second save failed")

	// load again (should be stable)
	loaded2, err := LoadProjectConfig(tmpDir)
	require.NoError(t, err, "second load failed")
	assert.Equal(t, "https://sageox.ai", loaded2.Endpoint, "endpoint should be stable after round-trip")
}

// TestLoadProjectConfig_MigratesLegacyWithPrefixedAPIBaseURL tests that legacy migration
// normalizes the endpoint when migrating from api_base_url.
func TestLoadProjectConfig_MigratesLegacyWithPrefixedAPIBaseURL(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	RequireSageoxDir(t, tmpDir)

	// write config with prefixed legacy api_base_url
	legacyConfig := map[string]any{
		"config_version":         CurrentConfigVersion,
		"api_base_url":           "https://api.staging.sageox.ai",
		"web_base_url":           "https://web.staging.sageox.ai",
		"update_frequency_hours": 24,
	}
	data, err := json.MarshalIndent(legacyConfig, "", "  ")
	require.NoError(t, err, "failed to marshal legacy config")
	configPath := filepath.Join(tmpDir, sageoxDir, projectConfigFilename)
	require.NoError(t, os.WriteFile(configPath, data, 0600), "failed to write legacy config")

	// load should migrate and normalize
	cfg, err := LoadProjectConfig(tmpDir)
	require.NoError(t, err, "failed to load config")

	assert.Equal(t, "https://staging.sageox.ai", cfg.Endpoint,
		"migrated endpoint should have api. prefix stripped")
	assert.Empty(t, cfg.APIBaseURL, "legacy field should be cleared")
}

// TestLoadProjectConfig_NoMigrationWhenEndpointSet tests that migration doesn't
// happen when the new endpoint field is already set.
func TestLoadProjectConfig_NoMigrationWhenEndpointSet(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	RequireSageoxDir(t, tmpDir)

	// write config with both endpoint and legacy fields (endpoint takes precedence)
	configWithBoth := map[string]any{
		"config_version":         "2",
		"endpoint":               "https://new.example.com",
		"api_base_url":           "https://old.example.com", // should be ignored
		"update_frequency_hours": 24,
	}
	data, err := json.MarshalIndent(configWithBoth, "", "  ")
	require.NoError(t, err, "failed to marshal config")
	configPath := filepath.Join(tmpDir, sageoxDir, projectConfigFilename)
	require.NoError(t, os.WriteFile(configPath, data, 0600), "failed to write config")

	// load config - should NOT trigger migration since endpoint is set
	cfg, err := LoadProjectConfig(tmpDir)
	require.NoError(t, err, "failed to load config")

	// endpoint should be the new value, not migrated from api_base_url
	assert.Equal(t, "https://new.example.com", cfg.Endpoint, "expected Endpoint to be preserved")
}

func TestFindProjectRoot_OxProjectRootEnv(t *testing.T) {
	t.Run("overrides cwd discovery", func(t *testing.T) {
		projectDir := CreateInitializedProjectWithConfig(t, nil)

		otherDir := t.TempDir()
		originalCwd, _ := os.Getwd()
		defer os.Chdir(originalCwd)
		require.NoError(t, os.Chdir(otherDir))

		t.Setenv("OX_PROJECT_ROOT", projectDir)

		root := FindProjectRoot()

		expectedRoot, _ := filepath.EvalSymlinks(projectDir)
		actualRoot, _ := filepath.EvalSymlinks(root)
		assert.Equal(t, expectedRoot, actualRoot)
	})

	t.Run("invalid path falls back to cwd discovery", func(t *testing.T) {
		// cwd project uses .sageox/ dir only (matching walk-up behavior)
		projectDir := CreateInitializedProject(t)

		originalCwd, _ := os.Getwd()
		defer os.Chdir(originalCwd)
		require.NoError(t, os.Chdir(projectDir))

		t.Setenv("OX_PROJECT_ROOT", t.TempDir()) // no .sageox

		root := FindProjectRoot()

		expectedRoot, _ := filepath.EvalSymlinks(projectDir)
		actualRoot, _ := filepath.EvalSymlinks(root)
		assert.Equal(t, expectedRoot, actualRoot)
	})

	t.Run("not set uses normal discovery", func(t *testing.T) {
		projectDir := CreateInitializedProject(t)

		originalCwd, _ := os.Getwd()
		defer os.Chdir(originalCwd)
		require.NoError(t, os.Chdir(projectDir))

		t.Setenv("OX_PROJECT_ROOT", "")

		root := FindProjectRoot()

		expectedRoot, _ := filepath.EvalSymlinks(projectDir)
		actualRoot, _ := filepath.EvalSymlinks(root)
		assert.Equal(t, expectedRoot, actualRoot)
	})
}
