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
//
// ARCHITECTURE DECISION: Why config.local.toml stores [[team_contexts]]
//
// Team membership is inherently user-level data, but we store it per-repo because:
//   - Each workspace runs its own daemon. Per-repo config means each daemon writes
//     to its own file with no locking or coordination needed.
//   - Different repos can point to different endpoints (e.g., sageox.ai vs self-hosted).
//     Per-repo config naturally scopes team contexts to the correct endpoint.
//   - A user-level file (~/) would require file locking across concurrent daemons,
//     or namespace partitioning by endpoint — complexity that per-repo config avoids.
//
// The daemon discovers all team contexts the user can access (via GET /api/v1/cli/repos)
// and records them here. This includes cross-team contexts (teams the user belongs to
// beyond the repo's own team). For the repo's own team, FindRepoTeamContext() has a
// fallback that computes the path from config.json's team_id without requiring this file.
//
// FUTURE: Consider replacing this with a user-level file if we move to a single
// global daemon, which would eliminate the multi-writer locking problem.
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
	Slug     string    `toml:"slug,omitempty"`
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

// DefaultLedgerPath returns the CANONICAL path for the ledger git checkout.
// Ledgers are stored in the user directory, shared across all worktrees of a repo.
//
// Format: ~/.local/share/sageox/<endpoint_slug>/ledgers/<repo_id>/
//
// IMPORTANT: This is the ONLY function that should determine the ledger checkout location.
// NEVER construct ledger paths manually. Changes to path locations require Ryan's review.
func DefaultLedgerPath(repoID, endpointURL string) string {
	if repoID == "" || endpointURL == "" {
		return ""
	}
	return paths.LedgersDataDir(repoID, endpointURL)
}

// SiblingLedgerPath returns the DEPRECATED sibling-directory path for the ledger repo.
// This is used for migration detection when upgrading from the sibling to user-directory layout.
//
// Format: <project_parent>/<repo_name>_sageox/<endpoint_slug>/ledger
func SiblingLedgerPath(repoName, projectRoot, endpointURL string) string {
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
func (c *LocalConfig) GetLedgerPath(repoID, endpointURL string) string {
	if c != nil && c.Ledger != nil && c.Ledger.Path != "" {
		return c.Ledger.Path
	}
	return DefaultLedgerPath(repoID, endpointURL)
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

// FindRepoTeamContext returns the team context that belongs to this repo's team.
// Loads ProjectConfig.TeamID and matches it against LocalConfig.TeamContexts.
// Falls back to the first configured team context if no match is found.
//
// If no team contexts are configured in config.local.toml (daemon hasn't synced yet),
// falls back to computing the expected path from config.json's team_id + endpoint.
// This ensures the repo's own team context is discoverable immediately after ox init,
// without waiting for the daemon to populate [[team_contexts]].
func FindRepoTeamContext(projectRoot string) *TeamContext {
	localCfg, err := LoadLocalConfig(projectRoot)
	if err != nil {
		localCfg = &LocalConfig{}
	}

	projectCfg, err := LoadProjectConfig(projectRoot)
	if err != nil || projectCfg == nil {
		// no project config — can only use local config entries
		if len(localCfg.TeamContexts) > 0 {
			return &localCfg.TeamContexts[0]
		}
		return nil
	}

	// try matching project's team_id against local config entries
	if projectCfg.TeamID != "" && len(localCfg.TeamContexts) > 0 {
		if tc := localCfg.GetTeamContext(projectCfg.TeamID); tc != nil {
			return tc
		}
	}

	// fallback: any configured team context
	if len(localCfg.TeamContexts) > 0 {
		return &localCfg.TeamContexts[0]
	}

	// fallback: compute path from config.json team_id + endpoint (no daemon needed)
	if projectCfg.TeamID != "" {
		ep := endpoint.GetForProject(projectRoot)
		if ep == "" {
			return nil
		}
		computedPath := DefaultTeamContextPath(projectCfg.TeamID, ep)
		if computedPath == "" {
			return nil
		}
		// only return if the directory actually exists on disk
		if _, err := os.Stat(computedPath); err != nil {
			return nil
		}
		return &TeamContext{
			TeamID:   projectCfg.TeamID,
			TeamName: projectCfg.TeamName,
			Path:     computedPath,
		}
	}

	return nil
}

// IsRepoTeamContext returns true if the given team ID matches the repo's owning team.
func IsRepoTeamContext(projectRoot, teamID string) bool {
	if projectRoot == "" || teamID == "" {
		return false
	}
	projectCfg, err := LoadProjectConfig(projectRoot)
	if err != nil || projectCfg == nil {
		return false
	}
	return projectCfg.TeamID == teamID
}

// FindAllTeamContexts returns all team contexts available to the user.
// Primary source is LocalConfig.TeamContexts (populated by daemon).
// Falls back to scanning paths.TeamsDataDir(endpoint) subdirectories.
// Returns empty slice (not nil) if none found.
func FindAllTeamContexts(projectRoot string) []TeamContext {
	// try LocalConfig first (daemon-populated, authoritative)
	localCfg, err := LoadLocalConfig(projectRoot)
	if err == nil && localCfg != nil && len(localCfg.TeamContexts) > 0 {
		var result []TeamContext
		for _, tc := range localCfg.TeamContexts {
			if tc.Path == "" {
				continue
			}
			if _, err := os.Stat(tc.Path); err == nil {
				result = append(result, tc)
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	// fallback: scan TeamsDataDir for subdirectories
	projectCfg, err := LoadProjectConfig(projectRoot)
	if err != nil || projectCfg == nil {
		return []TeamContext{}
	}

	ep := projectCfg.Endpoint
	if ep == "" {
		ep = endpoint.GetForProject(projectRoot)
	}
	if ep == "" {
		return []TeamContext{}
	}

	teamsDir := paths.TeamsDataDir(ep)
	entries, err := os.ReadDir(teamsDir)
	if err != nil {
		return []TeamContext{}
	}

	var result []TeamContext
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		teamPath := filepath.Join(teamsDir, entry.Name())
		result = append(result, TeamContext{
			TeamID: entry.Name(),
			Path:   teamPath,
		})
	}
	return result
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

// -----------------------------------------------------------------------------
// .sageox/ Project Symlinks
// -----------------------------------------------------------------------------
// These symlinks live inside the project's .sageox/ directory (gitignored) and
// provide discoverable paths to user-directory data (ledger, team context).

// createOrUpdateSymlink is a helper that creates or updates a symlink atomically.
// Returns nil on Windows (symlinks require Developer Mode).
func createOrUpdateSymlink(symlinkPath, targetPath string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	// ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(symlinkPath), 0755); err != nil {
		return fmt.Errorf("create symlink parent dir=%s: %w", filepath.Dir(symlinkPath), err)
	}

	// check if symlink already points to the right target
	if info, err := os.Lstat(symlinkPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			existing, _ := os.Readlink(symlinkPath)
			if existing == targetPath {
				return nil
			}
			// wrong target, remove and recreate
			_ = os.Remove(symlinkPath)
		} else {
			return fmt.Errorf("path exists and is not a symlink: %s", symlinkPath)
		}
	}

	return os.Symlink(targetPath, symlinkPath)
}

// CreateOrUpdateProjectSymlink creates or updates a symlink inside the project root.
// The rel path is relative to projectRoot (e.g. ".sageox/ledger").
// If the symlink already points to targetPath, this is a no-op.
func CreateOrUpdateProjectSymlink(projectRoot, rel, targetPath string) error {
	if projectRoot == "" || rel == "" || targetPath == "" {
		return nil
	}
	return createOrUpdateSymlink(filepath.Join(projectRoot, rel), targetPath)
}

// CreateProjectLedgerSymlink creates .sageox/ledger -> user-dir ledger path.
func CreateProjectLedgerSymlink(projectRoot, repoID, ep string) error {
	if projectRoot == "" || repoID == "" || ep == "" {
		return nil
	}

	symlinkPath := filepath.Join(projectRoot, ".sageox", "ledger")
	targetPath := DefaultLedgerPath(repoID, ep)
	if targetPath == "" {
		return nil
	}

	return createOrUpdateSymlink(symlinkPath, targetPath)
}

// CreateProjectTeamSymlinks creates .sageox/teams/<team_id> and .sageox/teams/primary.
//
// Today we only support a single team per repo, but that is not a hard requirement.
// The "primary" symlink always points to the repo's team for convenient access.
func CreateProjectTeamSymlinks(projectRoot, teamID, ep string) error {
	if projectRoot == "" || teamID == "" || ep == "" {
		return nil
	}

	teamsDir := filepath.Join(projectRoot, ".sageox", "teams")
	targetPath := paths.TeamContextDir(teamID, ep)
	if targetPath == "" {
		return nil
	}

	// .sageox/teams/<team_id> -> user-dir team path
	teamSymlink := filepath.Join(teamsDir, sanitizeRepoName(teamID))
	if err := createOrUpdateSymlink(teamSymlink, targetPath); err != nil {
		return fmt.Errorf("create team symlink: %w", err)
	}

	// .sageox/teams/primary -> same target (convenience alias)
	// today we only support a single team per repo, but that is not a hard requirement
	primarySymlink := filepath.Join(teamsDir, "primary")
	if err := createOrUpdateSymlink(primarySymlink, targetPath); err != nil {
		return fmt.Errorf("create primary team symlink: %w", err)
	}

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
