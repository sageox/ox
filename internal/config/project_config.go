package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
)

func init() {
	// Register the project endpoint getter to avoid circular imports.
	// This allows endpoint.GetForProject() to check project config.
	endpoint.ProjectEndpointGetter = func(projectRoot string) string {
		if projectRoot == "" {
			return ""
		}
		cfg, err := LoadProjectConfig(projectRoot)
		if err != nil || cfg == nil {
			return ""
		}
		// Check config.json first
		if cfg.Endpoint != "" {
			return cfg.Endpoint
		}
		// Fall back to marker file endpoint (handles case where config.json
		// endpoint wasn't saved but marker file has it)
		if ep := getEndpointFromMarker(projectRoot); ep != "" {
			return ep
		}
		return ""
	}
}

// repoMarkerEndpoint is a minimal struct to read just the endpoint from marker files
type repoMarkerEndpoint struct {
	Endpoint    string `json:"endpoint,omitempty"`
	APIEndpoint string `json:"api_endpoint,omitempty"` // legacy field
}

// getEndpointFromMarker reads the endpoint from .sageox/.repo_* marker files.
// Returns empty string if no marker file found or endpoint not set.
func getEndpointFromMarker(projectRoot string) string {
	sageoxDir := filepath.Join(projectRoot, sageoxDir)
	entries, err := os.ReadDir(sageoxDir)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// Look for .repo_* marker files
		if len(entry.Name()) > 6 && entry.Name()[:6] == ".repo_" {
			markerPath := filepath.Join(sageoxDir, entry.Name())
			data, err := os.ReadFile(markerPath)
			if err != nil {
				continue
			}
			var marker repoMarkerEndpoint
			if err := json.Unmarshal(data, &marker); err != nil {
				continue
			}
			// Prefer new field, fall back to legacy
			if marker.Endpoint != "" {
				return marker.Endpoint
			}
			if marker.APIEndpoint != "" {
				return marker.APIEndpoint
			}
		}
	}
	return ""
}

// CurrentConfigVersion is the latest config version supported by this ox binary
// Increment this when making breaking changes to .sageox config structure
const CurrentConfigVersion = "2"

// ProjectConfig represents the per-repository configuration stored in .sageox/config.json
type ProjectConfig struct {
	ConfigVersion    string   `json:"config_version,omitempty"` // tracks .sageox config version
	Org              string   `json:"org,omitempty"`
	Team             string   `json:"team,omitempty"`
	Project          string   `json:"project,omitempty"`
	ProjectID        string   `json:"project_id,omitempty"`         // API project ID (prj_xxx)
	WorkspaceID      string   `json:"workspace_id,omitempty"`       // API workspace ID (ws_xxx)
	RepoID           string   `json:"repo_id,omitempty"`            // prefixed UUIDv7 (repo_01jfk3mab...)
	RepoRemoteHashes []string `json:"repo_remote_hashes,omitempty"` // salted SHA256 hashes of remote URLs
	TeamID           string   `json:"team_id,omitempty"`            // team ID from server response
	TeamName         string   `json:"team_name,omitempty"`          // team display name from server response
	Endpoint         string   `json:"endpoint,omitempty"`           // SageOx endpoint URL (matches SAGEOX_ENDPOINT env var)

	// Legacy fields - kept for backward compatibility with old config files
	// TODO: Remove after 2026-01-31 - legacy field support
	APIBaseURL string `json:"api_base_url,omitempty"` // deprecated: use Endpoint
	WebBaseURL string `json:"web_base_url,omitempty"` // deprecated: use Endpoint

	UpdateFrequencyHours     int          `json:"update_frequency_hours"`
	LastUpdateCheckUTC       *string      `json:"last_update_check_utc,omitempty"`
	Attribution              *Attribution `json:"attribution,omitempty"`
	OfflineSnapshotStaleDays int          `json:"offline_snapshot_stale_days,omitempty"` // days until offline snapshot is considered stale (default: 7)
	// BadgeStatus tracks badge state for this specific project.
	// Values: "" (not asked), "added", "declined"
	// This enables per-project tracking of user's badge preference.
	BadgeStatus string `json:"badge_status,omitempty"`

	// SessionRecording controls automatic session recording behavior.
	// Values: "disabled" (no recording), "auto" (automatic recording), "manual" (explicit start required)
	// Empty string defaults to "auto".
	SessionRecording string `json:"session_recording,omitempty"`

	// SessionPublishing controls what happens when a session stops.
	// Values: "auto" (upload to ledger on stop), "manual" (save locally, user uploads explicitly)
	// Empty string defaults to "auto" for backward compatibility.
	SessionPublishing string `json:"session_publishing,omitempty"`
}

// NeedsUpgrade returns true if the config version is older than CurrentConfigVersion
func (c *ProjectConfig) NeedsUpgrade() bool {
	return c.ConfigVersion == "" || c.ConfigVersion < CurrentConfigVersion
}

// SetCurrentVersion sets the config version to the current version
func (c *ProjectConfig) SetCurrentVersion() {
	c.ConfigVersion = CurrentConfigVersion
}

const (
	defaultUpdateFrequencyHours     = 24
	projectConfigFilename           = "config.json"
	sageoxDir                       = ".sageox"
	defaultOfflineSnapshotStaleDays = 7
)

// IsInitialized checks if SageOx is initialized in the given git root directory.
// Returns true if .sageox/config.json exists (the canonical file created by ox init).
func IsInitialized(gitRoot string) bool {
	configPath := filepath.Join(gitRoot, sageoxDir, projectConfigFilename)
	_, err := os.Stat(configPath)
	return err == nil
}

// IsInitializedInCwd checks if SageOx is initialized by walking up from current directory.
// Returns true if .sageox/config.json is found in current dir or any parent.
func IsInitializedInCwd() bool {
	return FindProjectRoot() != ""
}

// GetOfflineSnapshotStaleThreshold returns the offline snapshot staleness threshold as a duration
func (c *ProjectConfig) GetOfflineSnapshotStaleThreshold() time.Duration {
	days := c.OfflineSnapshotStaleDays
	if days <= 0 {
		days = defaultOfflineSnapshotStaleDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// GetEndpoint returns the SageOx endpoint URL.
// Priority: config.Endpoint > endpoint.Get() (from SAGEOX_ENDPOINT env or default)
func (c *ProjectConfig) GetEndpoint() string {
	if c != nil && c.Endpoint != "" {
		return c.Endpoint
	}
	return endpoint.Get()
}

// GitCredentials returns the git credentials scoped to this project's endpoint.
func (c *ProjectConfig) GitCredentials() (*gitserver.GitCredentials, error) {
	return gitserver.LoadCredentialsForEndpoint(c.GetEndpoint())
}

// GetDefaultProjectConfig returns a ProjectConfig with default values
func GetDefaultProjectConfig() *ProjectConfig {
	return &ProjectConfig{
		ConfigVersion:        CurrentConfigVersion,
		UpdateFrequencyHours: defaultUpdateFrequencyHours,
		LastUpdateCheckUTC:   nil,
	}
}

// LoadProjectConfig loads the project configuration from .sageox/config.json relative to gitRoot
func LoadProjectConfig(gitRoot string) (*ProjectConfig, error) {
	if gitRoot == "" {
		return nil, errors.New("git root cannot be empty")
	}

	configPath := filepath.Join(gitRoot, sageoxDir, projectConfigFilename)

	// check if file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// return default config if file doesn't exist
		return GetDefaultProjectConfig(), nil
	}

	// read the file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// parse JSON
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// apply defaults for missing fields
	applyDefaults(&cfg)

	// migrate legacy fields to new Endpoint field
	// TODO: Remove after 2026-01-31 - legacy field migration
	if cfg.Endpoint == "" && cfg.APIBaseURL != "" {
		cfg.Endpoint = endpoint.NormalizeEndpoint(cfg.APIBaseURL)
		cfg.APIBaseURL = "" // clear legacy field
		cfg.WebBaseURL = "" // clear legacy field
		// save migrated config
		if err := SaveProjectConfig(gitRoot, &cfg); err != nil {
			// log but don't fail - config still works
			fmt.Fprintf(os.Stderr, "warning: could not save migrated config: %v\n", err)
		}
	}

	// normalize endpoint defensively (handles configs saved by older versions)
	cfg.Endpoint = endpoint.NormalizeEndpoint(cfg.Endpoint)

	return &cfg, nil
}

// SaveProjectConfig saves the project configuration to .sageox/config.json relative to gitRoot
func SaveProjectConfig(gitRoot string, cfg *ProjectConfig) error {
	if gitRoot == "" {
		return errors.New("git root cannot be empty")
	}

	if cfg == nil {
		return errors.New("config cannot be nil")
	}

	// apply defaults before saving
	applyDefaults(cfg)

	// normalize endpoint before persisting
	cfg.Endpoint = endpoint.NormalizeEndpoint(cfg.Endpoint)

	// ensure .sageox directory exists
	sageoxPath := filepath.Join(gitRoot, sageoxDir)
	if err := os.MkdirAll(sageoxPath, 0755); err != nil {
		return fmt.Errorf("failed to create .sageox directory: %w", err)
	}

	// marshal to JSON with indentation
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// write to file
	configPath := filepath.Join(sageoxPath, projectConfigFilename)
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// ValidateProjectConfig validates the project configuration and returns a list of validation errors
func ValidateProjectConfig(cfg *ProjectConfig) []string {
	if cfg == nil {
		return []string{"config is nil"}
	}

	var errors []string

	// validate update frequency
	if cfg.UpdateFrequencyHours <= 0 {
		errors = append(errors, "update_frequency_hours must be greater than 0")
	}

	// validate last_update_check_utc if present
	if cfg.LastUpdateCheckUTC != nil && *cfg.LastUpdateCheckUTC != "" {
		if _, err := time.Parse(time.RFC3339, *cfg.LastUpdateCheckUTC); err != nil {
			errors = append(errors, fmt.Sprintf("last_update_check_utc is not a valid ISO 8601 timestamp: %s", *cfg.LastUpdateCheckUTC))
		}
	}

	return errors
}

// applyDefaults applies default values to missing fields in the config
func applyDefaults(cfg *ProjectConfig) {
	if cfg.UpdateFrequencyHours <= 0 {
		cfg.UpdateFrequencyHours = defaultUpdateFrequencyHours
	}
}

// FindProjectConfigPath walks up from the current working directory looking for .sageox/config.json
// Returns the path to the config file if found, empty string if not found
// Stops at filesystem root
func FindProjectConfigPath() (string, error) {
	// get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current working directory: %w", err)
	}

	return findProjectConfigPathFromDir(cwd)
}

// FindProjectRoot walks up from the current working directory looking for .sageox directory.
// Returns the project root if found, empty string if not found.
// This is useful for finding the project root without requiring a config file to exist.
//
// OX_PROJECT_ROOT env var overrides discovery when set to a valid initialized project.
func FindProjectRoot() string {
	if override := os.Getenv("OX_PROJECT_ROOT"); override != "" {
		resolved := os.ExpandEnv(override)
		if abs, err := filepath.Abs(resolved); err == nil {
			resolved = abs
		}
		if IsInitialized(resolved) {
			return resolved
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	currentDir := cwd
	for {
		// check if .sageox directory exists
		sageoxPath := filepath.Join(currentDir, sageoxDir)
		if info, err := os.Stat(sageoxPath); err == nil && info.IsDir() {
			return currentDir
		}

		// get parent directory
		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			return "" // reached filesystem root
		}
		currentDir = parentDir
	}
}

// findProjectConfigPathFromDir walks up from the given directory looking for .sageox/config.json
func findProjectConfigPathFromDir(startDir string) (string, error) {
	currentDir := startDir

	for {
		// check if .sageox/config.json exists in current directory
		configPath := filepath.Join(currentDir, sageoxDir, projectConfigFilename)
		if _, err := os.Stat(configPath); err == nil {
			return configPath, nil
		}

		// get parent directory
		parentDir := filepath.Dir(currentDir)

		// check if we've reached the filesystem root
		if parentDir == currentDir {
			return "", nil
		}

		currentDir = parentDir
	}
}

// GetProjectContext is a convenience function that finds and loads the project config
// Returns (config, configPath, error)
// If config is not found, returns (nil, "", nil) - not an error
func GetProjectContext() (*ProjectConfig, string, error) {
	configPath, err := FindProjectConfigPath()
	if err != nil {
		return nil, "", err
	}

	// if no config found, return nil without error
	if configPath == "" {
		return nil, "", nil
	}

	// extract git root from config path
	// config path format: /path/to/repo/.sageox/config.json
	gitRoot := filepath.Dir(filepath.Dir(configPath))

	cfg, err := LoadProjectConfig(gitRoot)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load project config from %s: %w", configPath, err)
	}

	return cfg, configPath, nil
}

// GetRepoID returns the repo ID for the given project root.
// Returns empty string if the config doesn't exist or has no repo ID.
// The repo ID is a prefixed UUIDv7 (repo_01jfk3mab...) used for
// identifying the repo in context storage and API calls.
func GetRepoID(projectRoot string) string {
	if projectRoot == "" {
		return ""
	}

	cfg, err := LoadProjectConfig(projectRoot)
	if err != nil || cfg == nil {
		return ""
	}

	return cfg.RepoID
}
