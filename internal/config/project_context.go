package config

import (
	"errors"
	"path/filepath"

	"github.com/sageox/ox/internal/paths"
)

// ProjectContext provides project configuration with guaranteed endpoint consistency.
// The endpoint is ALWAYS derived from ProjectConfig - never stored elsewhere.
//
// Use LoadProjectContext() to create a ProjectContext for a project root, then access
// configuration and paths through the accessor methods.
type ProjectContext struct {
	root        string
	config      *ProjectConfig
	localConfig *LocalConfig
}

// LoadProjectContext creates a ProjectContext for the given project root.
// Validates the root path and loads both project and local configurations.
//
// Returns error if projectRoot is empty or configuration loading fails.
func LoadProjectContext(projectRoot string) (*ProjectContext, error) {
	if projectRoot == "" {
		return nil, errors.New("project root cannot be empty")
	}

	cfg, err := LoadProjectConfig(projectRoot)
	if err != nil {
		return nil, err
	}

	localCfg, err := LoadLocalConfig(projectRoot)
	if err != nil {
		return nil, err
	}

	return &ProjectContext{
		root:        projectRoot,
		config:      cfg,
		localConfig: localCfg,
	}, nil
}

// Root returns the project root path.
func (p *ProjectContext) Root() string {
	return p.root
}

// Config returns the project config.
func (p *ProjectContext) Config() *ProjectConfig {
	return p.config
}

// LocalConfig returns the local config (machine-specific).
func (p *ProjectContext) LocalConfig() *LocalConfig {
	return p.localConfig
}

// Endpoint returns the project's endpoint (single source of truth).
// Calls config.GetEndpoint() if config exists, otherwise returns empty string.
func (p *ProjectContext) Endpoint() string {
	if p.config != nil {
		return p.config.GetEndpoint()
	}
	return ""
}

// RepoName returns the base name of the project root directory.
func (p *ProjectContext) RepoName() string {
	return filepath.Base(p.root)
}

// RepoID returns the repo ID from project config.
// Returns empty string if config doesn't exist or has no repo ID.
func (p *ProjectContext) RepoID() string {
	if p.config != nil {
		return p.config.RepoID
	}
	return ""
}

// TeamContextDir returns the path to a team context directory.
// Uses the project's endpoint for path resolution.
func (p *ProjectContext) TeamContextDir(teamID string) string {
	return paths.TeamContextDir(teamID, p.Endpoint())
}

// LedgerDir returns the path to the ledger directory for a repository.
// Uses the project's endpoint for path resolution.
func (p *ProjectContext) LedgerDir(repoID string) string {
	return paths.LedgersDataDir(repoID, p.Endpoint())
}

// TeamsDataDir returns the base directory for all team contexts.
// Uses the project's endpoint for path resolution.
func (p *ProjectContext) TeamsDataDir() string {
	return paths.TeamsDataDir(p.Endpoint())
}

// SiblingDir returns the sageox sibling directory for this project.
// Format: <project_parent>/<repo_name>_sageox
func (p *ProjectContext) SiblingDir() string {
	return DefaultSageoxSiblingDir(p.RepoName(), p.root)
}

// DefaultLedgerPath returns the default ledger path for this project.
// Uses the project's endpoint and repo ID for path resolution.
// Format: ~/.local/share/sageox/<endpoint_slug>/ledgers/<repo_id>/
func (p *ProjectContext) DefaultLedgerPath() string {
	return DefaultLedgerPath(p.RepoID(), p.Endpoint())
}

// SiblingLedgerPath returns the deprecated sibling-directory ledger path.
// Used for migration detection only.
func (p *ProjectContext) SiblingLedgerPath() string {
	return SiblingLedgerPath(p.RepoName(), p.root, p.Endpoint())
}
