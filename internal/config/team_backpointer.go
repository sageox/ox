package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/paths"
)

const (
	// backpointerFile is stored in <team_context>/.sageox/workspaces.jsonl
	// It tracks which workspaces reference this team context.
	// This file is gitignored and local-only.
	backpointerFile = "workspaces.jsonl"
)

// WorkspaceBackpointer tracks a workspace that references a team context.
// Stored as JSONL in <team_context>/.sageox/workspaces.jsonl
type WorkspaceBackpointer struct {
	WorkspaceID string    `json:"workspace_id"`
	ProjectPath string    `json:"project_path"`
	LastActive  time.Time `json:"last_active"`
}

// TeamContextHealth represents the health status of a team context directory.
type TeamContextHealth struct {
	TeamID        string
	Path          string
	Exists        bool
	IsGitRepo     bool
	LastSyncAge   time.Duration
	Workspaces    []WorkspaceBackpointer
	ActiveCount   int  // workspaces active in last 7 days
	StaleCount    int  // workspaces not active in 30+ days
	OrphanedCount int  // workspaces whose project paths no longer exist
	IsOrphaned    bool // no valid workspace references
	IsStale       bool // no activity in 30+ days
}

// RegisterWorkspace adds or updates a workspace backpointer in a team context.
// This should be called when a daemon starts watching a project that uses this team context.
func RegisterWorkspace(teamContextPath, workspaceID, projectPath string) error {
	if teamContextPath == "" {
		return errors.New("team context path cannot be empty")
	}
	if workspaceID == "" {
		return errors.New("workspace ID cannot be empty")
	}

	backpointers, err := LoadBackpointers(teamContextPath)
	if err != nil {
		return fmt.Errorf("load backpointers: %w", err)
	}

	// update or add the workspace
	found := false
	now := time.Now().UTC()
	for i := range backpointers {
		if backpointers[i].WorkspaceID == workspaceID {
			backpointers[i].ProjectPath = projectPath
			backpointers[i].LastActive = now
			found = true
			break
		}
	}
	if !found {
		backpointers = append(backpointers, WorkspaceBackpointer{
			WorkspaceID: workspaceID,
			ProjectPath: projectPath,
			LastActive:  now,
		})
	}

	return SaveBackpointers(teamContextPath, backpointers)
}

// UpdateWorkspaceActivity updates the last active time for a workspace.
// Call this periodically while a daemon is active.
func UpdateWorkspaceActivity(teamContextPath, workspaceID string) error {
	if teamContextPath == "" || workspaceID == "" {
		return nil
	}

	backpointers, err := LoadBackpointers(teamContextPath)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	for i := range backpointers {
		if backpointers[i].WorkspaceID == workspaceID {
			backpointers[i].LastActive = now
			return SaveBackpointers(teamContextPath, backpointers)
		}
	}

	return nil
}

// LoadBackpointers reads workspace backpointers from a team context.
func LoadBackpointers(teamContextPath string) ([]WorkspaceBackpointer, error) {
	if teamContextPath == "" {
		return nil, errors.New("team context path cannot be empty")
	}

	filePath := filepath.Join(teamContextPath, ".sageox", backpointerFile)

	file, err := os.Open(filePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open backpointer file: %w", err)
	}
	defer file.Close()

	var backpointers []WorkspaceBackpointer
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var bp WorkspaceBackpointer
		if err := json.Unmarshal(scanner.Bytes(), &bp); err != nil {
			continue // skip malformed lines
		}
		backpointers = append(backpointers, bp)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read backpointer file: %w", err)
	}

	return backpointers, nil
}

// SaveBackpointers writes workspace backpointers to a team context.
func SaveBackpointers(teamContextPath string, backpointers []WorkspaceBackpointer) error {
	if teamContextPath == "" {
		return errors.New("team context path cannot be empty")
	}

	sageoxDir := filepath.Join(teamContextPath, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		return fmt.Errorf("create .sageox directory: %w", err)
	}

	filePath := filepath.Join(sageoxDir, backpointerFile)
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("create backpointer file: %w", err)
	}
	defer file.Close()

	for _, bp := range backpointers {
		data, err := json.Marshal(bp)
		if err != nil {
			continue
		}
		if _, err := file.Write(data); err != nil {
			return fmt.Errorf("write backpointer file: %w", err)
		}
		if _, err := file.WriteString("\n"); err != nil {
			return fmt.Errorf("write backpointer file: %w", err)
		}
	}

	return nil
}

// AnalyzeTeamContextHealth checks the health of a team context directory.
func AnalyzeTeamContextHealth(teamID, teamPath string, lastSync time.Time) TeamContextHealth {
	health := TeamContextHealth{
		TeamID: teamID,
		Path:   teamPath,
	}

	// check if path exists
	info, err := os.Stat(teamPath)
	if err != nil || !info.IsDir() {
		return health
	}
	health.Exists = true

	// check if it's a git repo
	gitDir := filepath.Join(teamPath, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		health.IsGitRepo = true
	}

	// calculate sync age
	if !lastSync.IsZero() {
		health.LastSyncAge = time.Since(lastSync)
	}

	// load and analyze backpointers
	backpointers, _ := LoadBackpointers(teamPath)
	health.Workspaces = backpointers

	now := time.Now()
	sevenDaysAgo := now.Add(-7 * 24 * time.Hour)
	thirtyDaysAgo := now.Add(-30 * 24 * time.Hour)

	for _, bp := range backpointers {
		// check if project path still exists
		if _, err := os.Stat(bp.ProjectPath); os.IsNotExist(err) {
			health.OrphanedCount++
			continue
		}

		if bp.LastActive.After(sevenDaysAgo) {
			health.ActiveCount++
		} else if bp.LastActive.Before(thirtyDaysAgo) {
			health.StaleCount++
		}
	}

	// determine overall health status
	validWorkspaces := len(backpointers) - health.OrphanedCount
	health.IsOrphaned = validWorkspaces == 0 && len(backpointers) > 0
	health.IsStale = health.ActiveCount == 0 && health.LastSyncAge > 30*24*time.Hour

	return health
}

// DiscoverTeamContexts finds all team context directories across all endpoints.
// Scans ~/.sageox/data/<endpoint>/teams/ for each endpoint directory.
func DiscoverTeamContexts() ([]string, error) {
	var teamDirs []string

	dataDir := paths.DataDir()
	entries, err := os.ReadDir(dataDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read data directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// check if this is an endpoint directory with a teams subdirectory
		endpointTeamsDir := filepath.Join(dataDir, entry.Name(), "teams")
		endpointTeams, err := discoverTeamsInDir(endpointTeamsDir)
		if err != nil && !os.IsNotExist(err) {
			continue // skip directories that aren't endpoint namespaces
		}
		teamDirs = append(teamDirs, endpointTeams...)
	}

	return teamDirs, nil
}

// discoverTeamsInDir finds team context directories within a teams directory.
func discoverTeamsInDir(teamsDir string) ([]string, error) {
	entries, err := os.ReadDir(teamsDir)
	if err != nil {
		return nil, err
	}

	var teamDirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		teamPath := filepath.Join(teamsDir, entry.Name())
		// verify it's a git repo
		if _, err := os.Stat(filepath.Join(teamPath, ".git")); err == nil {
			teamDirs = append(teamDirs, teamPath)
		}
	}

	return teamDirs, nil
}

// DiscoverLegacyTeamContexts finds team contexts in the old sibling directory format.
// Searches parent directories for sageox_team_*_context directories.
func DiscoverLegacyTeamContexts(projectRoot string) ([]string, error) {
	if projectRoot == "" {
		return nil, nil
	}

	parentDir := filepath.Dir(projectRoot)
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return nil, fmt.Errorf("read parent directory: %w", err)
	}

	var legacyDirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// match sageox_team_*_context pattern
		if len(name) > 20 && name[:12] == "sageox_team_" && name[len(name)-8:] == "_context" {
			teamPath := filepath.Join(parentDir, name)
			// verify it's a git repo
			if _, err := os.Stat(filepath.Join(teamPath, ".git")); err == nil {
				legacyDirs = append(legacyDirs, teamPath)
			}
		}
	}

	return legacyDirs, nil
}

// CleanupOrphanedBackpointers removes backpointers for workspaces that no longer exist.
func CleanupOrphanedBackpointers(teamContextPath string) (int, error) {
	backpointers, err := LoadBackpointers(teamContextPath)
	if err != nil {
		return 0, err
	}

	var validBackpointers []WorkspaceBackpointer
	orphanedCount := 0

	for _, bp := range backpointers {
		if _, err := os.Stat(bp.ProjectPath); os.IsNotExist(err) {
			orphanedCount++
			continue
		}
		validBackpointers = append(validBackpointers, bp)
	}

	if orphanedCount > 0 {
		if err := SaveBackpointers(teamContextPath, validBackpointers); err != nil {
			return 0, err
		}
	}

	return orphanedCount, nil
}
