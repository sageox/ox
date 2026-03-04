package badge

import (
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
)

// ShouldSuggest checks if we should suggest the badge.
//
// We only suggest the badge when ALL of these conditions are met:
// 1. User is authenticated (logged in via `ox login`)
// 2. User has NOT previously declined (globally)
// 3. Project has NOT previously declined (per-project)
// 4. Badge is not already present in README
func ShouldSuggest(gitRoot string) bool {
	// 1. check if user is authenticated
	// use project endpoint if available
	ep := endpoint.GetForProject(gitRoot)
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return false
	}

	// 2. check if user previously declined globally
	userCfg, err := config.LoadUserConfig()
	if err != nil || userCfg == nil {
		return false
	}

	if userCfg.Badge != nil && userCfg.Badge.SuggestionStatus == StatusDeclined {
		return false
	}

	// 3. check project-level status
	if gitRoot != "" {
		projectCfg, err := config.LoadProjectConfig(gitRoot)
		if err == nil && projectCfg != nil {
			if projectCfg.BadgeStatus == StatusDeclined || projectCfg.BadgeStatus == StatusAdded {
				return false
			}
		}
	}

	// 4. check if README exists and doesn't already have badge
	if gitRoot != "" {
		readmePath := FindReadme(gitRoot)
		if readmePath == "" {
			return false // no README to add badge to
		}
		if HasBadge(readmePath) {
			// badge already present - mark as added and return false
			MarkAdded(gitRoot)
			return false
		}
	}

	return true
}

// MarkDeclined records that user permanently declined the badge suggestion.
// This is stored at both user-level (global) and project-level.
// We respect this choice and never ask again.
func MarkDeclined(gitRoot string) {
	now := time.Now().UTC().Format(time.RFC3339)

	// update user config (global decline)
	userCfg, err := config.LoadUserConfig()
	if err != nil {
		userCfg = &config.UserConfig{}
	}
	if userCfg.Badge == nil {
		userCfg.Badge = &config.BadgeConfig{}
	}
	userCfg.Badge.SuggestionStatus = StatusDeclined
	userCfg.Badge.LastDeclined = &now
	_ = config.SaveUserConfig(userCfg)

	// update project config (per-project decline)
	if gitRoot != "" {
		projectCfg, err := config.LoadProjectConfig(gitRoot)
		if err == nil && projectCfg != nil {
			projectCfg.BadgeStatus = StatusDeclined
			_ = config.SaveProjectConfig(gitRoot, projectCfg)
		}
	}
}

// MarkAdded records that the badge was added to the project.
// This is stored at both user-level and project-level to avoid re-suggesting.
func MarkAdded(gitRoot string) {
	// update user config
	userCfg, err := config.LoadUserConfig()
	if err != nil {
		userCfg = &config.UserConfig{}
	}
	if userCfg.Badge == nil {
		userCfg.Badge = &config.BadgeConfig{}
	}
	userCfg.Badge.SuggestionStatus = StatusAdded
	_ = config.SaveUserConfig(userCfg)

	// update project config
	if gitRoot != "" {
		projectCfg, err := config.LoadProjectConfig(gitRoot)
		if err == nil && projectCfg != nil {
			projectCfg.BadgeStatus = StatusAdded
			_ = config.SaveProjectConfig(gitRoot, projectCfg)
		}
	}
}

// MarkNotNow records that user deferred the decision (not a permanent decline).
// We may ask again in a future session.
func MarkNotNow(gitRoot string) {
	// for "not now", we just don't change the status
	// this allows us to ask again next time without being too pushy
}
