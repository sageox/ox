package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"gopkg.in/yaml.v3"
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
	Endpoint string `json:"endpoint,omitempty"`
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
			if marker.Endpoint != "" {
				return marker.Endpoint
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

	// Murmuring controls automatic work-in-progress signals to teammates.
	// When set to "auto", ox periodically nudges AI coworkers to share what
	// they're working on, so teammates and other AI coworkers on the same repo
	// (or team) hear about in-flight work before PRs or commits appear.
	// Signals propagate via the ledger and are delivered as whispers to active
	// coworkers.
	// Values: "off" (disabled), "auto" (periodic nudges to self-report)
	// Empty string defaults to "off".
	Murmuring       string `json:"murmur_send,omitempty"`
	LegacyMurmuring string `json:"murmuring,omitempty"` // deprecated: read old key on upgrade

	// MurmurReceive controls whether murmurs from other coworkers appear in
	// this project's whisper stream.
	// Values: "on" (receive murmurs as whispers), "off" (suppress murmur whispers)
	// Empty string defaults to "on".
	MurmurReceive     string `json:"murmur_receive,omitempty"`
	RecordingReminder string `json:"recording_reminder,omitempty"`

	// GitHubSync controls GitHub data extraction to the ledger (master toggle).
	// Values: "enabled" (default), "disabled"
	GitHubSync string `json:"github_sync,omitempty"`

	// GitHubSyncPRs controls PR sync independently.
	// Values: "enabled" (default), "disabled"
	GitHubSyncPRs string `json:"github_sync_prs,omitempty"`

	// GitHubSyncIssues controls issue sync independently.
	// Values: "enabled" (default), "disabled"
	GitHubSyncIssues string `json:"github_sync_issues,omitempty"`

	// Hooks holds per-hook-event policy switches that may be set at the
	// repo level to override the user default (or set policy for repos
	// the user hasn't personally configured). See HooksConfig.
	Hooks *HooksConfig `json:"hooks,omitempty" yaml:"hooks,omitempty"`

	// Plan holds the `plan.*` settings namespace for the `ox plan` feature.
	// Pointer so an absent block is distinguishable from explicit zero-values.
	Plan *PlanConfig `json:"plan,omitempty" yaml:"plan,omitempty"`

	// KBID is the immutable knowledge-bubble identifier this project is bound
	// to (ADR-017). Populated when the project has been migrated to the new
	// .sageox/config.yaml format. Empty for legacy JSON-only projects that
	// haven't been migrated yet.
	KBID string `json:"-" yaml:"kb_id,omitempty"`

	// Format records which on-disk format the loader treated as authoritative
	// when constructing this struct. Values: "json", "yaml", "both", "none".
	// Useful for doctor to decide whether a migration is pending.
	// NOT serialized — derived at load time.
	Format string `json:"-" yaml:"-"`
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
	projectConfigYAMLFilename       = "config.yaml"
	sageoxDir                       = ".sageox"
	defaultOfflineSnapshotStaleDays = 7
)

// ProjectConfigYAML is the binding subset of the project-side
// .sageox/config.yaml (ADR-017). The file itself may contain the full
// ProjectConfig shape; yaml.v3 honors json tags, so LoadProjectConfig can
// unmarshal directly into ProjectConfig and preserve existing per-project
// settings. This narrower view is kept for code paths that only need the
// binding fields (kb_id + repo_id), such as doctor reconciliation tests.
type ProjectConfigYAML struct {
	KBID   string `yaml:"kb_id,omitempty"`
	RepoID string `yaml:"repo_id,omitempty"`
}

// IsInitialized checks if SageOx is initialized in the given git root directory.
// Returns true if .sageox/config.json OR .sageox/config.yaml exists. Both
// formats co-exist during the staged deprecation window (ADR-017 §7).
//
// TODO(release N+3): drop the JSON fallback once all repos have migrated.
func IsInitialized(gitRoot string) bool {
	jsonPath := filepath.Join(gitRoot, sageoxDir, projectConfigFilename)
	if _, err := os.Stat(jsonPath); err == nil {
		return true
	}
	yamlPath := filepath.Join(gitRoot, sageoxDir, projectConfigYAMLFilename)
	if _, err := os.Stat(yamlPath); err == nil {
		return true
	}
	return false
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

// GetMurmuring returns the murmur_send value, falling back to the legacy "murmuring" key.
func (c *ProjectConfig) GetMurmuring() string {
	if c.Murmuring != "" {
		return c.Murmuring
	}
	return c.LegacyMurmuring
}

// SetMurmuring sets murmur_send and clears the legacy key.
func (c *ProjectConfig) SetMurmuring(value string) {
	c.Murmuring = value
	c.LegacyMurmuring = ""
}

// GetDefaultProjectConfig returns a ProjectConfig with default values
func GetDefaultProjectConfig() *ProjectConfig {
	return &ProjectConfig{
		ConfigVersion:        CurrentConfigVersion,
		UpdateFrequencyHours: defaultUpdateFrequencyHours,
		LastUpdateCheckUTC:   nil,
	}
}

// LoadProjectConfig loads the project configuration from .sageox/config.json,
// then merges in .sageox/config.yaml (ADR-017) when present.
//
// Hybrid read posture during the staged migration window (ADR-017 §7):
//
//   - JSON only present  → Format="json".
//   - YAML only present  → Format="yaml"; unmarshal YAML directly into the
//     canonical struct, preserving all project-scoped settings that were
//     migrated across.
//   - Both present       → Format="both"; JSON is the base, YAML overrides on
//     conflict for every field it carries. This lets the doctor migration
//     write the YAML alongside the legacy JSON without losing information.
//   - Neither present    → return default config with Format unset (callers
//     keep treating this as "not initialized" via IsInitialized).
//
// TODO(release N+3): drop the JSON read path entirely once all repos are
// migrated and the legacy files have been removed.
func LoadProjectConfig(gitRoot string) (*ProjectConfig, error) {
	if gitRoot == "" {
		return nil, errors.New("git root cannot be empty")
	}

	jsonPath := filepath.Join(gitRoot, sageoxDir, projectConfigFilename)
	yamlPath := filepath.Join(gitRoot, sageoxDir, projectConfigYAMLFilename)

	// Distinguish ENOENT (file genuinely missing) from EACCES / other
	// stat failures (parent dir unreadable, broken symlink, etc.). The
	// legacy LoadProjectConfig used os.IsNotExist; mirroring that here
	// keeps ensureSageoxConfig's "permission-denied → configError"
	// contract intact under hostile filesystem state.
	jsonExists := false
	if _, err := os.Stat(jsonPath); err == nil {
		jsonExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("failed to stat config file: %w", err)
	}
	yamlExists := false
	if _, err := os.Stat(yamlPath); err == nil {
		yamlExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("failed to stat yaml config: %w", err)
	}

	// Neither file exists — preserve the legacy "return defaults" contract so
	// callers that load eagerly (daemon, IPC discovery) don't have to special-case
	// uninitialized repos. IsInitialized() is the canonical existence gate.
	if !jsonExists && !yamlExists {
		return GetDefaultProjectConfig(), nil
	}

	cfg := ProjectConfig{}

	if jsonExists {
		data, err := os.ReadFile(jsonPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	if yamlExists {
		data, err := os.ReadFile(yamlPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read yaml config: %w", err)
		}
		if err := mergeProjectConfigYAML(data, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse yaml config: %w", err)
		}
	}

	switch {
	case jsonExists && yamlExists:
		cfg.Format = "both"
	case yamlExists:
		cfg.Format = "yaml"
	default:
		cfg.Format = "json"
	}

	applyDefaults(&cfg)
	cfg.Endpoint = endpoint.NormalizeEndpoint(cfg.Endpoint)

	return &cfg, nil
}

func mergeProjectConfigYAML(data []byte, cfg *ProjectConfig) error {
	if cfg == nil {
		return errors.New("config cannot be nil")
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) > 0 {
		jsonData, err := json.Marshal(raw)
		if err != nil {
			return fmt.Errorf("yaml to json bridge: %w", err)
		}
		if err := json.Unmarshal(jsonData, cfg); err != nil {
			return fmt.Errorf("yaml to project config: %w", err)
		}
	}

	var binding ProjectConfigYAML
	if err := yaml.Unmarshal(data, &binding); err != nil {
		return err
	}
	if binding.KBID != "" {
		cfg.KBID = binding.KBID
	}
	if binding.RepoID != "" {
		cfg.RepoID = binding.RepoID
	}
	return nil
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

// ResolveProjectRootOverride checks OX_PROJECT_ROOT env var for an explicit
// project root override. Returns the resolved path if valid, empty string otherwise.
// This is the single source of truth for the env var — all callers should use this.
func ResolveProjectRootOverride() string {
	override := os.Getenv(EnvProjectRoot)
	if override == "" {
		return ""
	}
	resolved := os.ExpandEnv(override)
	if abs, err := filepath.Abs(resolved); err == nil {
		resolved = abs
	}
	if evaled, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = evaled
	}
	if IsInitialized(resolved) {
		return resolved
	}
	return ""
}

// FindProjectRoot walks up from the current working directory looking for .sageox directory.
// Returns the project root if found, empty string if not found.
// This is useful for finding the project root without requiring a config file to exist.
//
// OX_PROJECT_ROOT env var overrides discovery when set to a valid initialized project.
func FindProjectRoot() string {
	if resolved := ResolveProjectRootOverride(); resolved != "" {
		return resolved
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
			if evaled, err := filepath.EvalSymlinks(currentDir); err == nil {
				return evaled
			}
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

// findProjectConfigPathFromDir walks up from the given directory looking for
// .sageox/config.json, or .sageox/config.yaml when JSON is absent (ADR-017).
// Prefer JSON during the migration window so existing tooling/callers that
// expect a JSON path keep working; fall back to YAML so YAML-only repos are
// still discoverable.
func findProjectConfigPathFromDir(startDir string) (string, error) {
	currentDir := startDir

	for {
		jsonPath := filepath.Join(currentDir, sageoxDir, projectConfigFilename)
		if _, err := os.Stat(jsonPath); err == nil {
			return jsonPath, nil
		}
		yamlPath := filepath.Join(currentDir, sageoxDir, projectConfigYAMLFilename)
		if _, err := os.Stat(yamlPath); err == nil {
			return yamlPath, nil
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

// BackfillProjectConfigFromLocalState writes a minimal .sageox/config.json
// when one is missing but RecoverRepoIDFromLocalState found a repo_id. Returns
// (true, nil) when a file was written, (false, nil) when nothing was needed
// or recoverable, and (false, err) on a write failure.
//
// This is the daemon-side counterpart to ensureSageoxConfig: it lets a daemon
// self-heal a half-initialized project (init committed on a feature branch,
// later reset away) so subsequent CLI calls can recompute the same workspace_id
// the daemon registers under and IPC discovery succeeds.
//
// Caller must verify projectRoot is a real, initialized .sageox/ workspace
// (e.g. via IsInitialized or by checking for .sageox/ + config.local.toml) —
// this function intentionally does NOT create .sageox/ from scratch, mirroring
// SaveLocalConfig's "do not bootstrap an uninitialized project" stance.
func BackfillProjectConfigFromLocalState(projectRoot string) (bool, error) {
	if projectRoot == "" {
		return false, nil
	}
	configPath := filepath.Join(projectRoot, sageoxDir, projectConfigFilename)
	if _, err := os.Stat(configPath); err == nil {
		return false, nil // config.json already present — nothing to do
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat config.json: %w", err)
	}

	// require .sageox/ to already exist — never bootstrap an uninitialized project
	if _, err := os.Stat(filepath.Join(projectRoot, sageoxDir)); err != nil {
		return false, nil
	}

	repoID, _ := RecoverRepoIDFromLocalState(projectRoot)
	if repoID == "" {
		return false, nil
	}

	cfg := GetDefaultProjectConfig()
	cfg.RepoID = repoID
	if err := SaveProjectConfig(projectRoot, cfg); err != nil {
		return false, fmt.Errorf("save backfilled config: %w", err)
	}
	return true, nil
}

// RecoverRepoIDFromLocalState attempts to recover the canonical repo_id for a
// project when .sageox/config.json is missing but other local state survived.
//
// Recovery sources (in priority order):
//  1. .sageox/.repo_<uuid> marker files — exact repo_id is the marker name suffix.
//  2. .sageox/config.local.toml — ledger.path encodes the repo_id (see
//     RepoIDFromLedgerPath).
//
// This is the canonical "init was reverted from git" recovery path: when a user
// runs 'ox init' on a feature branch and later resets to a branch where the
// init commit was never merged, git removes the tracked .sageox/config.json and
// .repo_<uuid> marker, but the gitignored config.local.toml survives — and its
// ledger path still encodes the repo_id. This lets ox doctor and the daemon
// rebuild config.json without losing the link to the existing ledger checkout
// or breaking workspace_id-based daemon discovery.
//
// Returns ("", "") if no recoverable repo_id is found.
func RecoverRepoIDFromLocalState(projectRoot string) (repoID, endpointSlug string) {
	if projectRoot == "" {
		return "", ""
	}

	// 1. .repo_<uuid> marker is authoritative when present.
	sageoxPath := filepath.Join(projectRoot, sageoxDir)
	if entries, err := os.ReadDir(sageoxPath); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".repo_") && len(name) > len(".repo_") {
				return "repo_" + strings.TrimPrefix(name, ".repo_"), ""
			}
		}
	}

	// 2. ledger path in config.local.toml encodes both repo_id and endpoint slug.
	localCfg, err := LoadLocalConfig(projectRoot)
	if err != nil || localCfg == nil || localCfg.Ledger == nil {
		return "", ""
	}
	return RepoIDFromLedgerPath(localCfg.Ledger.Path), EndpointSlugFromLedgerPath(localCfg.Ledger.Path)
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
