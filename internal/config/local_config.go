package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
)

const (
	localConfigFilename = "config.local.toml"
)

// LocalConfig represents machine-specific config stored in .sageox/config.local.toml.
// This file tracks local paths for git repos (ledger and team-context) and is NOT
// committed to version control.
type LocalConfig struct {
	Ledger       *LedgerConfig `toml:"ledger,omitempty"`
	TeamContexts []TeamContext `toml:"team_contexts,omitempty"`
}

// LedgerConfig holds configuration for the local ledger git repo.
// Note: omitempty is intentionally not used on LastSync because go-toml v2
// does not serialize time.Time fields with omitempty correctly.
type LedgerConfig struct {
	Path     string    `toml:"path"`
	LastSync time.Time `toml:"last_sync"`
}

// HasLastSync returns true if LastSync has been set (is non-zero).
func (c *LedgerConfig) HasLastSync() bool {
	return c != nil && !c.LastSync.IsZero()
}

// TeamContext holds configuration for a team context git repo.
// Note: omitempty is intentionally not used on LastSync because go-toml v2
// does not serialize time.Time fields with omitempty correctly.
type TeamContext struct {
	TeamID   string    `toml:"team_id"`
	TeamName string    `toml:"team_name"`
	Path     string    `toml:"path"`
	LastSync time.Time `toml:"last_sync"`
}

// HasLastSync returns true if LastSync has been set (is non-zero).
func (c *TeamContext) HasLastSync() bool {
	return c != nil && !c.LastSync.IsZero()
}

// LoadLocalConfig loads the local configuration from .sageox/config.local.toml
// relative to projectRoot. Returns an empty config if the file does not exist.
func LoadLocalConfig(projectRoot string) (*LocalConfig, error) {
	if projectRoot == "" {
		return nil, errors.New("project root cannot be empty")
	}

	configPath := filepath.Join(projectRoot, sageoxDir, localConfigFilename)

	// return empty config if file doesn't exist
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return &LocalConfig{}, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read local config file=%s: %w", configPath, err)
	}

	var cfg LocalConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse local config file=%s: %w", configPath, err)
	}

	return &cfg, nil
}

// SaveLocalConfig saves the local configuration to .sageox/config.local.toml
// relative to projectRoot. Creates the .sageox directory if it does not exist.
func SaveLocalConfig(projectRoot string, cfg *LocalConfig) error {
	if projectRoot == "" {
		return errors.New("project root cannot be empty")
	}

	if cfg == nil {
		return errors.New("config cannot be nil")
	}

	// prevent creating .sageox/ from scratch - require project to be initialized
	// this prevents commands like ox status from creating artifacts in uninitialized projects
	sageoxPath := filepath.Join(projectRoot, sageoxDir)
	if _, err := os.Stat(sageoxPath); os.IsNotExist(err) {
		return fmt.Errorf("project not initialized: run 'ox init' first")
	}

	// ensure .sageox directory exists (should already exist from check above, but be defensive)
	if err := os.MkdirAll(sageoxPath, 0755); err != nil {
		return fmt.Errorf("create .sageox directory: %w", err)
	}

	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal local config: %w", err)
	}

	configPath := filepath.Join(sageoxPath, localConfigFilename)
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("write local config file=%s: %w", configPath, err)
	}

	return nil
}

// -----------------------------------------------------------------------------
// repo_sageox Sibling Directory Structure
// -----------------------------------------------------------------------------
//
// The repo_sageox directory sits as a sibling to the project and provides
// endpoint-namespaced storage for ledger repos and team symlinks:
//
//   /path/to/myrepo/           <- project root
//   /path/to/myrepo_sageox/    <- sibling sageox directory
//     sageox.ai/               <- endpoint slug (normalized)
//       ledger/                <- ledger git repo
//       teams/
//         team-abc/            <- symlink to ~/.sageox/data/teams/team-abc
//         team-xyz/            <- symlink to ~/.sageox/data/teams/team-xyz
//     staging.sageox.ai/       <- non-production endpoint
//       ledger/
//       teams/
//         team-abc/
//
// -----------------------------------------------------------------------------

// DefaultSageoxSiblingDir returns the CANONICAL base sageox sibling directory for a project.
// This is a sibling directory to the project root that contains endpoint-namespaced
// ledger repos and team symlinks.
//
// Format: <project_parent>/<repo_name>_sageox
//
// Example: /path/to/myrepo -> /path/to/myrepo_sageox
//
// IMPORTANT: This is the ONLY function that should determine the sibling directory location.
// NEVER construct sibling paths manually. Changes to path locations require Ryan's review.
func DefaultSageoxSiblingDir(repoName, projectRoot string) string {
	if projectRoot == "" {
		return ""
	}

	parentDir := filepath.Dir(projectRoot)
	safeName := sanitizeRepoName(repoName)
	return filepath.Join(parentDir, safeName+"_sageox")
}

// DefaultLedgerPath returns the default path for the ledger repo,
// which is inside the sageox sibling directory, namespaced by endpoint.
//
// Format: <project_parent>/<repo_name>_sageox/<endpoint_slug>/ledger
//
// Example: /path/to/myrepo with endpoint sageox.ai
// -> /path/to/myrepo_sageox/sageox.ai/ledger
func DefaultLedgerPath(repoName, projectRoot, endpointURL string) string {
	siblingDir := DefaultSageoxSiblingDir(repoName, projectRoot)
	if siblingDir == "" {
		return ""
	}

	slug := endpoint.NormalizeSlug(endpointURL)
	if slug == "" {
		slug = endpoint.NormalizeSlug(endpoint.Default)
	}

	return filepath.Join(siblingDir, slug, "ledger")
}

// LegacyLedgerPath returns the old sibling-directory path for the ledger repo.
// This is used for migration detection and backward compatibility.
//
// Format: <project_parent>/<repo_name>_sageox_ledger
func LegacyLedgerPath(repoName, projectRoot string) string {
	if projectRoot == "" {
		return ""
	}

	parentDir := filepath.Dir(projectRoot)
	safeName := sanitizeRepoName(repoName)
	return filepath.Join(parentDir, safeName+"_sageox_ledger")
}

// DefaultTeamSymlinkPath returns the path where team context symlinks should be created.
// This is inside the sageox sibling directory, namespaced by endpoint.
//
// Format: <project_parent>/<repo_name>_sageox/<endpoint_slug>/teams/<team_id>
//
// Example: /path/to/myrepo with endpoint sageox.ai and team abc123
// -> /path/to/myrepo_sageox/sageox.ai/teams/abc123
func DefaultTeamSymlinkPath(repoName, projectRoot, endpointURL, teamID string) string {
	if teamID == "" {
		return ""
	}

	siblingDir := DefaultSageoxSiblingDir(repoName, projectRoot)
	if siblingDir == "" {
		return ""
	}

	slug := endpoint.NormalizeSlug(endpointURL)
	if slug == "" {
		slug = endpoint.NormalizeSlug(endpoint.Default)
	}

	safeTeamID := sanitizeRepoName(teamID)
	return filepath.Join(siblingDir, slug, "teams", safeTeamID)
}

// DefaultTeamContextPath returns the default path for a team context repo.
//
// For production endpoints (api.sageox.ai, app.sageox.ai, www.sageox.ai, sageox.ai):
//
//	~/.sageox/data/teams/<team_id>/
//
// For non-production endpoints:
//
//	~/.sageox/data/<endpoint>/teams/<team_id>/
//
// This centralized location allows all projects to share team contexts and
// enables daemons to discover team contexts at a known location.
//
// Legacy format (for migration): <project_parent>/sageox_team_<team_id>_context
// Use LegacyTeamContextPath() to check for existing sibling-directory team contexts.
//
// The projectRoot parameter is ignored in the new format but kept for API compatibility.
// The endpoint parameter determines the namespace; empty uses the current endpoint.
func DefaultTeamContextPath(teamID, ep string) string {
	if teamID == "" {
		return ""
	}
	return paths.TeamContextDir(teamID, ep)
}

// LegacyTeamContextPath returns the old sibling-directory path for a team context.
// This is used for migration detection and backward compatibility.
// Format: <project_parent>/sageox_team_<team_id>_context
func LegacyTeamContextPath(teamID, projectRoot string) string {
	if projectRoot == "" || teamID == "" {
		return ""
	}

	parentDir := filepath.Dir(projectRoot)
	safeTeamID := sanitizeRepoName(teamID)
	return filepath.Join(parentDir, "sageox_team_"+safeTeamID+"_context")
}

// GetLedgerPath returns the configured ledger path, or the default if not configured.
func (c *LocalConfig) GetLedgerPath(repoName, projectRoot, endpointURL string) string {
	if c != nil && c.Ledger != nil && c.Ledger.Path != "" {
		return c.Ledger.Path
	}
	return DefaultLedgerPath(repoName, projectRoot, endpointURL)
}

// SetLedgerPath sets the ledger path in the config.
func (c *LocalConfig) SetLedgerPath(path string) {
	if c.Ledger == nil {
		c.Ledger = &LedgerConfig{}
	}
	c.Ledger.Path = path
}

// UpdateLedgerLastSync updates the last sync time for the ledger.
// No-ops if Ledger is nil (consistent with UpdateTeamContextLastSync behavior).
func (c *LocalConfig) UpdateLedgerLastSync() {
	if c.Ledger == nil {
		return
	}
	c.Ledger.LastSync = time.Now().UTC()
}

// GetTeamContext returns the team context config for the given team ID, or nil if not found.
func (c *LocalConfig) GetTeamContext(teamID string) *TeamContext {
	if c == nil {
		return nil
	}
	for i := range c.TeamContexts {
		if c.TeamContexts[i].TeamID == teamID {
			return &c.TeamContexts[i]
		}
	}
	return nil
}

// GetTeamContextPath returns the configured team context path, or the default if not configured.
// The endpoint parameter is used for the default path when no explicit path is configured.
func (c *LocalConfig) GetTeamContextPath(teamID, ep string) string {
	if tc := c.GetTeamContext(teamID); tc != nil && tc.Path != "" {
		return tc.Path
	}
	return DefaultTeamContextPath(teamID, ep)
}

// SetTeamContext adds or updates a team context configuration.
func (c *LocalConfig) SetTeamContext(teamID, teamName, path string) {
	for i := range c.TeamContexts {
		if c.TeamContexts[i].TeamID == teamID {
			c.TeamContexts[i].TeamName = teamName
			c.TeamContexts[i].Path = path
			return
		}
	}
	c.TeamContexts = append(c.TeamContexts, TeamContext{
		TeamID:   teamID,
		TeamName: teamName,
		Path:     path,
	})
}

// UpdateTeamContextLastSync updates the last sync time for a team context.
func (c *LocalConfig) UpdateTeamContextLastSync(teamID string) {
	for i := range c.TeamContexts {
		if c.TeamContexts[i].TeamID == teamID {
			c.TeamContexts[i].LastSync = time.Now().UTC()
			return
		}
	}
}

// RemoveTeamContext removes a team context configuration by team ID.
func (c *LocalConfig) RemoveTeamContext(teamID string) {
	for i := range c.TeamContexts {
		if c.TeamContexts[i].TeamID == teamID {
			c.TeamContexts = append(c.TeamContexts[:i], c.TeamContexts[i+1:]...)
			return
		}
	}
}

// GetConfiguredEndpoints returns all unique endpoints configured for a project.
// Collects from: project config only (local config no longer has endpoint fields).
// Returns empty slice if no endpoints are configured.
func GetConfiguredEndpoints(projectRoot string) []string {
	if projectRoot == "" {
		return nil
	}

	// only check project config - local config no longer has endpoint fields
	if projectCfg, err := LoadProjectConfig(projectRoot); err == nil && projectCfg != nil && projectCfg.Endpoint != "" {
		return []string{projectCfg.Endpoint}
	}

	return nil
}

// -----------------------------------------------------------------------------
// Team Symlink Management
// -----------------------------------------------------------------------------

// CreateTeamSymlink creates a symlink from repo_sageox/<endpoint>/teams/<team_id>
// to the XDG team context directory (~/.sageox/data/<endpoint>/teams/<team_id>).
//
// The symlink allows agents and tools working within the repository to access
// team context data through a path relative to the project, while the actual
// data lives in the centralized XDG location.
//
// Returns nil on Windows (symlinks require Developer Mode).
// Returns error if symlink creation fails.
func CreateTeamSymlink(repoName, projectRoot, teamID, ep string) error {
	// skip on Windows - symlinks require Developer Mode
	if runtime.GOOS == "windows" {
		return nil
	}

	if projectRoot == "" {
		return errors.New("project root cannot be empty")
	}
	if teamID == "" {
		return errors.New("team ID cannot be empty")
	}

	// determine symlink source (sibling dir) and target (XDG location)
	symlinkPath := DefaultTeamSymlinkPath(repoName, projectRoot, ep, teamID)
	targetPath := paths.TeamContextDir(teamID, ep)

	if symlinkPath == "" || targetPath == "" {
		return errors.New("failed to determine symlink paths")
	}

	// ensure parent directory exists
	parentDir := filepath.Dir(symlinkPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("create symlink parent dir=%s: %w", parentDir, err)
	}

	// check if symlink already exists
	if info, err := os.Lstat(symlinkPath); err == nil {
		// path exists - check if it's a symlink
		if info.Mode()&os.ModeSymlink != 0 {
			// it's a symlink - check if it points to the right target
			existingTarget, err := os.Readlink(symlinkPath)
			if err != nil {
				return fmt.Errorf("read existing symlink=%s: %w", symlinkPath, err)
			}

			// if already pointing to correct target, done
			if existingTarget == targetPath {
				return nil
			}

			// pointing to wrong target - remove and recreate
			if err := os.Remove(symlinkPath); err != nil {
				return fmt.Errorf("remove stale symlink=%s: %w", symlinkPath, err)
			}
		} else {
			// it's not a symlink (file or directory)
			return fmt.Errorf("path exists and is not a symlink: %s", symlinkPath)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check symlink path=%s: %w", symlinkPath, err)
	}

	// create the symlink
	if err := os.Symlink(targetPath, symlinkPath); err != nil {
		return fmt.Errorf("create symlink %s -> %s: %w", symlinkPath, targetPath, err)
	}

	return nil
}

// UpdateTeamSymlink updates an existing team symlink to point to a new target.
// This is useful when the endpoint changes and the symlink target needs updating.
//
// Returns nil on Windows.
// Returns error if the symlink doesn't exist or update fails.
func UpdateTeamSymlink(repoName, projectRoot, teamID, oldEndpoint, newEndpoint string) error {
	// skip on Windows
	if runtime.GOOS == "windows" {
		return nil
	}

	if projectRoot == "" {
		return errors.New("project root cannot be empty")
	}
	if teamID == "" {
		return errors.New("team ID cannot be empty")
	}

	// remove old symlink if it exists and endpoint changed
	if oldEndpoint != newEndpoint {
		oldPath := DefaultTeamSymlinkPath(repoName, projectRoot, oldEndpoint, teamID)
		if oldPath != "" {
			if info, err := os.Lstat(oldPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
				if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove old symlink=%s: %w", oldPath, err)
				}
				// clean up empty parent directories
				cleanupEmptyParentDirs(oldPath, DefaultSageoxSiblingDir(repoName, projectRoot))
			}
		}
	}

	// create new symlink with the new endpoint
	return CreateTeamSymlink(repoName, projectRoot, teamID, newEndpoint)
}

// RemoveTeamSymlink removes the team symlink from the sibling directory.
// This only removes the symlink, not the actual team context data.
//
// Returns nil on Windows.
// Returns nil if the symlink doesn't exist.
// Returns error if removal fails.
func RemoveTeamSymlink(repoName, projectRoot, teamID, ep string) error {
	// skip on Windows
	if runtime.GOOS == "windows" {
		return nil
	}

	if projectRoot == "" || teamID == "" {
		return nil // nothing to remove
	}

	symlinkPath := DefaultTeamSymlinkPath(repoName, projectRoot, ep, teamID)
	if symlinkPath == "" {
		return nil
	}

	// check if it's actually a symlink before removing
	info, err := os.Lstat(symlinkPath)
	if os.IsNotExist(err) {
		return nil // already gone
	}
	if err != nil {
		return fmt.Errorf("check symlink=%s: %w", symlinkPath, err)
	}

	if info.Mode()&os.ModeSymlink == 0 {
		// not a symlink - don't remove it
		return fmt.Errorf("path is not a symlink: %s", symlinkPath)
	}

	if err := os.Remove(symlinkPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove symlink=%s: %w", symlinkPath, err)
	}

	// clean up empty parent directories up to the sibling dir
	siblingDir := DefaultSageoxSiblingDir(repoName, projectRoot)
	cleanupEmptyParentDirs(symlinkPath, siblingDir)

	return nil
}

// cleanupEmptyParentDirs removes empty parent directories up to but not including stopAt.
func cleanupEmptyParentDirs(path, stopAt string) {
	dir := filepath.Dir(path)
	for dir != stopAt && strings.HasPrefix(dir, stopAt) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break // not empty or error
		}
		if err := os.Remove(dir); err != nil {
			break // can't remove
		}
		dir = filepath.Dir(dir)
	}
}

// sanitizeRepoName removes or replaces characters that are unsafe for directory names.
func sanitizeRepoName(name string) string {
	if name == "" {
		return "unknown"
	}

	// replace unsafe characters with underscores
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		" ", "_",
	)
	sanitized := replacer.Replace(name)

	// remove leading/trailing underscores and dots
	sanitized = strings.Trim(sanitized, "_.")

	if sanitized == "" {
		return "unknown"
	}

	return sanitized
}
