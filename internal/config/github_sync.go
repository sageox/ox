package config

import "os"

// GitHubSync constants
const (
	GitHubSyncEnabled  = "enabled"
	GitHubSyncDisabled = "disabled"
)

var ValidGitHubSyncModes = []string{GitHubSyncEnabled, GitHubSyncDisabled}

func IsValidGitHubSyncMode(mode string) bool {
	switch mode {
	case GitHubSyncEnabled, GitHubSyncDisabled, "":
		return true
	}
	return false
}

func NormalizeGitHubSync(mode string) string {
	switch mode {
	case GitHubSyncEnabled, GitHubSyncDisabled:
		return mode
	default:
		return GitHubSyncEnabled
	}
}

// ResolveGitHubSync determines the effective master GitHub sync mode.
// Priority: OX_GITHUB_SYNC env > project config > default ("enabled")
func ResolveGitHubSync(projectRoot string) string {
	if envMode := os.Getenv(EnvGitHubSync); envMode != "" {
		return NormalizeGitHubSync(envMode)
	}
	if projectRoot != "" {
		projectCfg, err := LoadProjectConfig(projectRoot)
		if err == nil && projectCfg != nil && projectCfg.GitHubSync != "" {
			return NormalizeGitHubSync(projectCfg.GitHubSync)
		}
	}
	return GitHubSyncEnabled
}

// ResolveGitHubSyncPRs determines if PR sync is enabled.
// Master toggle must be enabled AND per-type toggle must be enabled.
func ResolveGitHubSyncPRs(projectRoot string) string {
	if ResolveGitHubSync(projectRoot) == GitHubSyncDisabled {
		return GitHubSyncDisabled
	}
	if envMode := os.Getenv(EnvGitHubSyncPRs); envMode != "" {
		return NormalizeGitHubSync(envMode)
	}
	if projectRoot != "" {
		projectCfg, err := LoadProjectConfig(projectRoot)
		if err == nil && projectCfg != nil && projectCfg.GitHubSyncPRs != "" {
			return NormalizeGitHubSync(projectCfg.GitHubSyncPRs)
		}
	}
	return GitHubSyncEnabled
}

// ResolveGitHubSyncIssues determines if issue sync is enabled.
// Master toggle must be enabled AND per-type toggle must be enabled.
func ResolveGitHubSyncIssues(projectRoot string) string {
	if ResolveGitHubSync(projectRoot) == GitHubSyncDisabled {
		return GitHubSyncDisabled
	}
	if envMode := os.Getenv(EnvGitHubSyncIssues); envMode != "" {
		return NormalizeGitHubSync(envMode)
	}
	if projectRoot != "" {
		projectCfg, err := LoadProjectConfig(projectRoot)
		if err == nil && projectCfg != nil && projectCfg.GitHubSyncIssues != "" {
			return NormalizeGitHubSync(projectCfg.GitHubSyncIssues)
		}
	}
	return GitHubSyncEnabled
}
