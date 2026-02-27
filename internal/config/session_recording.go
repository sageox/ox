package config

// SessionRecording constants: disabled -> manual -> auto
const (
	SessionRecordingDisabled = "disabled" // no recording
	SessionRecordingManual   = "manual"   // explicit start required
	SessionRecordingAuto     = "auto"     // automatic recording
)

// SessionPublishing constants
const (
	SessionPublishingAuto   = "auto"   // upload to ledger on session stop (default)
	SessionPublishingManual = "manual" // save locally, user uploads explicitly
)

// ValidSessionRecordingModes lists all valid session recording mode values.
var ValidSessionRecordingModes = []string{SessionRecordingDisabled, SessionRecordingManual, SessionRecordingAuto}

// ValidSessionPublishingModes lists all valid session publishing mode values.
var ValidSessionPublishingModes = []string{SessionPublishingAuto, SessionPublishingManual}

// IsValidSessionRecordingMode returns true if the mode is a recognized value.
func IsValidSessionRecordingMode(mode string) bool {
	switch mode {
	case SessionRecordingDisabled, SessionRecordingManual, SessionRecordingAuto, "":
		return true
	}
	return false
}

// IsValidSessionPublishingMode returns true if the mode is a recognized value.
func IsValidSessionPublishingMode(mode string) bool {
	switch mode {
	case SessionPublishingAuto, SessionPublishingManual, "":
		return true
	}
	return false
}

// NormalizeSessionRecording normalizes session_recording values.
// Returns default (manual) for unrecognized values.
// Maps legacy "none" to "disabled" for backwards compatibility.
func NormalizeSessionRecording(mode string) string {
	switch mode {
	case SessionRecordingDisabled, SessionRecordingManual, SessionRecordingAuto:
		return mode
	case "none":
		return SessionRecordingDisabled
	default:
		return SessionRecordingManual // default to manual (opt-in recording)
	}
}

// NormalizeSessionPublishing normalizes session_publishing values.
// Returns "auto" for unrecognized or empty values (backward compatible default).
func NormalizeSessionPublishing(mode string) string {
	switch mode {
	case SessionPublishingAuto, SessionPublishingManual:
		return mode
	default:
		return SessionPublishingAuto
	}
}

// SessionRecordingSource indicates where the session recording setting came from.
type SessionRecordingSource string

const (
	SessionRecordingSourceDefault SessionRecordingSource = "default" // no config, using default
	SessionRecordingSourceUser    SessionRecordingSource = "user"    // from user config
	SessionRecordingSourceTeam    SessionRecordingSource = "team"    // from team defaults (future)
	SessionRecordingSourceRepo    SessionRecordingSource = "repo"    // from .sageox/config.json
)

// ResolvedSessionRecording contains the effective mode and its source.
type ResolvedSessionRecording struct {
	Mode   string                 // effective mode: "disabled", "manual", or "auto"
	Source SessionRecordingSource // where the setting came from
}

// ShouldRecord returns true if the mode enables any recording.
func (r *ResolvedSessionRecording) ShouldRecord() bool {
	return r.Mode != SessionRecordingDisabled && r.Mode != ""
}

// IsAuto returns true if recording is automatic.
func (r *ResolvedSessionRecording) IsAuto() bool {
	return r.Mode == SessionRecordingAuto
}

// IsManual returns true if recording requires explicit start.
func (r *ResolvedSessionRecording) IsManual() bool {
	return r.Mode == SessionRecordingManual
}

// ResolveSessionRecording determines the effective session recording mode.
// Priority: user config > project config > team config > "manual"
//
// This priority ensures users can always override team/repo settings.
// If user sets "disabled", recording is disabled regardless of team/repo settings.
func ResolveSessionRecording(projectRoot string) *ResolvedSessionRecording {
	// 1. check user config (~/.config/sageox/config.yaml) - USER ALWAYS WINS
	userCfg, err := LoadUserConfig("")
	if err == nil && userCfg != nil && userCfg.Sessions != nil {
		mode := userCfg.Sessions.GetMode()
		if mode != "" && mode != "none" {
			normalized := NormalizeSessionRecording(mode)
			// user setting takes priority, including "disabled"
			return &ResolvedSessionRecording{
				Mode:   normalized,
				Source: SessionRecordingSourceUser,
			}
		}
	}

	// 2. check project config (.sageox/config.json)
	if projectRoot != "" {
		projectCfg, err := LoadProjectConfig(projectRoot)
		if err == nil && projectCfg != nil {
			if projectCfg.SessionRecording != "" {
				return &ResolvedSessionRecording{
					Mode:   NormalizeSessionRecording(projectCfg.SessionRecording),
					Source: SessionRecordingSourceRepo,
				}
			}
		}
	}

	// 3. check team config (from team context)
	if projectRoot != "" {
		if teamMode := loadTeamSessionRecording(projectRoot); teamMode != "" {
			return &ResolvedSessionRecording{
				Mode:   NormalizeSessionRecording(teamMode),
				Source: SessionRecordingSourceTeam,
			}
		}
	}

	// 4. default to manual (users opt-in to recording)
	return &ResolvedSessionRecording{
		Mode:   SessionRecordingManual,
		Source: SessionRecordingSourceDefault,
	}
}

// loadTeamSessionRecording loads session recording setting from team context.
// Checks for config.toml in the team context directory.
// Returns empty string if no team context or no setting configured.
func loadTeamSessionRecording(projectRoot string) string {
	localCfg, err := LoadLocalConfig(projectRoot)
	if err != nil || len(localCfg.TeamContexts) == 0 {
		return ""
	}

	// use the first configured team context
	tc := localCfg.TeamContexts[0]
	if tc.Path == "" {
		return ""
	}

	// look for team config in team context directory
	teamCfg, err := LoadTeamConfig(tc.Path)
	if err != nil || teamCfg == nil {
		return ""
	}

	return teamCfg.SessionRecording
}

// GetSessionRecording is a convenience function that returns just the mode string.
func GetSessionRecording(projectRoot string) string {
	resolved := ResolveSessionRecording(projectRoot)
	return resolved.Mode
}

// ResolvedSessionPublishing contains the effective publishing mode and its source.
type ResolvedSessionPublishing struct {
	Mode   string                 // effective mode: "auto" or "manual"
	Source SessionRecordingSource // where the setting came from (reuses same source type)
}

// ResolveSessionPublishing determines the effective session publishing mode.
// Priority: project config > "auto" (default)
//
// "auto" uploads to ledger on session stop (backward compatible default).
// "manual" saves locally without uploading.
func ResolveSessionPublishing(projectRoot string) *ResolvedSessionPublishing {
	// check project config (.sageox/config.json)
	if projectRoot != "" {
		projectCfg, err := LoadProjectConfig(projectRoot)
		if err == nil && projectCfg != nil {
			if projectCfg.SessionPublishing != "" {
				return &ResolvedSessionPublishing{
					Mode:   NormalizeSessionPublishing(projectCfg.SessionPublishing),
					Source: SessionRecordingSourceRepo,
				}
			}
		}
	}

	// default to auto (upload on stop) for backward compatibility
	return &ResolvedSessionPublishing{
		Mode:   SessionPublishingAuto,
		Source: SessionRecordingSourceDefault,
	}
}

// GetSessionPublishing is a convenience function that returns just the publishing mode string.
func GetSessionPublishing(projectRoot string) string {
	resolved := ResolveSessionPublishing(projectRoot)
	return resolved.Mode
}
