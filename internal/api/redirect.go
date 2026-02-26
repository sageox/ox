package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/logger"
)

// RedirectHeader is the HTTP header name for merge/redirect information
const RedirectHeader = "X-SageOx-Merge"

// RedirectInfo contains redirect information from the server
// when repos or teams have been merged
type RedirectInfo struct {
	Repo   *RedirectMapping `json:"repo,omitempty"`
	Team   *RedirectMapping `json:"team,omitempty"`
	Config *RedirectConfig  `json:"config,omitempty"`
}

// RedirectMapping represents a from -> to ID mapping
type RedirectMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RedirectConfig contains the new config values to apply
type RedirectConfig struct {
	RepoID string `json:"repo_id,omitempty"`
	TeamID string `json:"team_id,omitempty"`
}

// ParseRedirectHeader parses X-Sageox-Redirect header if present
// Returns nil if header is not present or cannot be parsed
func ParseRedirectHeader(header http.Header) *RedirectInfo {
	value := header.Get(RedirectHeader)
	if value == "" {
		return nil
	}

	var info RedirectInfo
	if err := json.Unmarshal([]byte(value), &info); err != nil {
		logger.Debug("failed to parse redirect header", "error", err, "value", value)
		return nil
	}

	return &info
}

// HandleRedirect processes redirect info and updates local config/markers
// This is best-effort: returns nil on success or if nothing to do,
// and logs but does not fail on non-critical errors
func HandleRedirect(projectRoot string, info *RedirectInfo) error {
	if info == nil || projectRoot == "" {
		return nil
	}

	// log warnings about merges
	if info.Repo != nil {
		logger.Warn("repository merged", "from", info.Repo.From, "to", info.Repo.To)
	}
	if info.Team != nil {
		logger.Warn("team merged", "from", info.Team.From, "to", info.Team.To)
	}

	// update config.json
	if info.Config != nil {
		if err := updateProjectConfig(projectRoot, info.Config); err != nil {
			// log but don't fail - config update is best-effort
			logger.Debug("failed to update config for redirect", "error", err)
		}
	}

	// rename marker file if repo changed
	if info.Repo != nil {
		if err := RenameRepoMarker(projectRoot, info.Repo.From, info.Repo.To); err != nil {
			// log but don't fail - marker rename is best-effort
			logger.Debug("failed to rename repo marker", "error", err)
		}
	}

	return nil
}

// updateProjectConfig updates the project config with new IDs
func updateProjectConfig(projectRoot string, cfg *RedirectConfig) error {
	projCfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		return err
	}

	changed := false
	if cfg.RepoID != "" && projCfg.RepoID != cfg.RepoID {
		projCfg.RepoID = cfg.RepoID
		changed = true
	}
	if cfg.TeamID != "" && projCfg.TeamID != cfg.TeamID {
		projCfg.TeamID = cfg.TeamID
		changed = true
	}

	if changed {
		return config.SaveProjectConfig(projectRoot, projCfg)
	}
	return nil
}

// RenameRepoMarker renames the .repo_* marker file from old ID to new ID using VCS
// and updates the repo_id value inside the marker file
func RenameRepoMarker(projectRoot, oldID, newID string) error {
	sageoxDir := filepath.Join(projectRoot, ".sageox")

	// extract UUID suffix from IDs (repo_xxx -> xxx)
	oldUUID := strings.TrimPrefix(oldID, "repo_")
	newUUID := strings.TrimPrefix(newID, "repo_")

	oldPath := filepath.Join(sageoxDir, ".repo_"+oldUUID)
	newPath := filepath.Join(sageoxDir, ".repo_"+newUUID)

	// check if old marker exists
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		return nil // no marker to rename - this is fine
	}

	// check if new marker already exists (already migrated)
	if _, err := os.Stat(newPath); err == nil {
		// new marker already exists, just remove old one using VCS
		return vcsRemove(projectRoot, oldPath)
	}

	// read the old marker file to update its contents
	data, err := os.ReadFile(oldPath)
	if err != nil {
		return err
	}

	// parse marker as generic map to preserve all fields
	var marker map[string]interface{}
	if err := json.Unmarshal(data, &marker); err != nil {
		// if we can't parse it, just move it without modifying
		return vcsMove(projectRoot, oldPath, newPath)
	}

	// update the repo_id field
	marker["repo_id"] = newID

	// marshal back to JSON
	updatedData, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return vcsMove(projectRoot, oldPath, newPath)
	}

	// write updated contents to new path
	if err := os.WriteFile(newPath, updatedData, 0600); err != nil {
		return err
	}

	// add new file to VCS
	if err := vcsAdd(projectRoot, newPath); err != nil {
		logger.Debug("failed to add new marker to VCS", "error", err)
	}

	// remove old file using VCS
	return vcsRemove(projectRoot, oldPath)
}

// vcsType represents the type of version control system
type vcsType int

const (
	vcsNone vcsType = iota
	vcsGit
	vcsSVN
)

// vcsMove moves a file using the appropriate VCS command (git mv or svn mv)
func vcsMove(projectRoot, oldPath, newPath string) error {
	switch detectVCS(projectRoot) {
	case vcsGit:
		return gitMove(projectRoot, oldPath, newPath)
	case vcsSVN:
		return svnMove(projectRoot, oldPath, newPath)
	default:
		return os.Rename(oldPath, newPath)
	}
}

// vcsRemove removes a file using the appropriate VCS command
func vcsRemove(projectRoot, path string) error {
	switch detectVCS(projectRoot) {
	case vcsGit:
		return gitRemove(projectRoot, path)
	case vcsSVN:
		return svnRemove(projectRoot, path)
	default:
		return os.Remove(path)
	}
}

// vcsAdd adds a file to version control
func vcsAdd(projectRoot, path string) error {
	switch detectVCS(projectRoot) {
	case vcsGit:
		return gitAdd(projectRoot, path)
	case vcsSVN:
		return svnAdd(projectRoot, path)
	default:
		return nil // nothing to do for non-VCS repos
	}
}

// detectVCS detects the version control system used in the project
func detectVCS(projectRoot string) vcsType {
	// check for git (.git directory)
	gitDir := filepath.Join(projectRoot, ".git")
	if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
		return vcsGit
	}

	// check for svn (.svn directory)
	svnDir := filepath.Join(projectRoot, ".svn")
	if info, err := os.Stat(svnDir); err == nil && info.IsDir() {
		return vcsSVN
	}

	return vcsNone
}

// gitMove runs git mv to rename a file
func gitMove(projectRoot, oldPath, newPath string) error {
	cmd := exec.Command("git", "mv", oldPath, newPath)
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Debug("git mv failed", "error", err, "output", string(output))
		// fallback to os.Rename if git mv fails
		return os.Rename(oldPath, newPath)
	}
	return nil
}

// gitRemove runs git rm to remove a file
func gitRemove(projectRoot, path string) error {
	cmd := exec.Command("git", "rm", "-f", path)
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Debug("git rm failed", "error", err, "output", string(output))
		// fallback to os.Remove if git rm fails
		return os.Remove(path)
	}
	return nil
}

// gitAdd runs git add to add a file
func gitAdd(projectRoot, path string) error {
	cmd := exec.Command("git", "add", path)
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Debug("git add failed", "error", err, "output", string(output))
		return err
	}
	return nil
}

// svnAdd runs svn add to add a file
func svnAdd(projectRoot, path string) error {
	cmd := exec.Command("svn", "add", path)
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Debug("svn add failed", "error", err, "output", string(output))
		return err
	}
	return nil
}

// svnMove runs svn mv to rename a file
func svnMove(projectRoot, oldPath, newPath string) error {
	cmd := exec.Command("svn", "mv", oldPath, newPath)
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Debug("svn mv failed", "error", err, "output", string(output))
		// fallback to os.Rename if svn mv fails
		return os.Rename(oldPath, newPath)
	}
	return nil
}

// svnRemove runs svn rm to remove a file
func svnRemove(projectRoot, path string) error {
	cmd := exec.Command("svn", "rm", "--force", path)
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Debug("svn rm failed", "error", err, "output", string(output))
		// fallback to os.Remove if svn rm fails
		return os.Remove(path)
	}
	return nil
}
