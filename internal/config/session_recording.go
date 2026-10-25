package config

import (
	"log/slog"
	"os"
)

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
	SessionRecordingSourceEnv     SessionRecordingSource = "env"     // from OX_SESSION_RECORDING env var
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
//
// Sessions record into the project ledger — a conversation store. Knowledge
// Bubbles play no part in recording resolution (ox ADR-028; the ADR-017
// current-KB chain was removed under epic ox-6hvs).
//
// Precedence (low → high):
//  1. default (auto for ox-initialized repos, manual otherwise).
//  2. team config (team context config.toml).
//  3. project config (.sageox/config.json).
//  4. user config (~/.config/sageox/config.yaml).
//  5. OX_SESSION_RECORDING env var (highest — the pipeline/automation escape hatch).
func ResolveSessionRecording(projectRoot string) *ResolvedSessionRecording {
	// 0. OX_SESSION_RECORDING env var — highest priority (pipelines / automation).
	//
	// Deliberately NOT symmetric with OX_SESSION_PUBLISHING below, which
	// rejects unrecognized values. Here an unknown value normalizes to
	// "manual", which is the RESTRICTIVE direction — a typo records less, not
	// more — and TestResolveSessionRecording_EnvVarOverridesAll pins that as
	// intended. Publishing cannot borrow the same rule because its unknown
	// value normalizes to "auto", i.e. a typo would re-enable uploads.
	if envMode := os.Getenv(EnvSessionRecording); envMode != "" {
		return &ResolvedSessionRecording{
			Mode:   NormalizeSessionRecording(envMode),
			Source: SessionRecordingSourceEnv,
		}
	}

	// user layer (raw — pre-normalization, empty means "unset")
	userMode := loadUserSessionRecordingMode()
	if userMode != "" && userMode != "none" {
		return &ResolvedSessionRecording{
			Mode:   NormalizeSessionRecording(userMode),
			Source: SessionRecordingSourceUser,
		}
	}

	// project config (.sageox/config.json)
	isInitialized := projectRoot != "" && IsInitialized(projectRoot)
	if isInitialized {
		projectCfg, err := LoadProjectConfig(projectRoot)
		if err == nil && projectCfg != nil && projectCfg.SessionRecording != "" {
			return &ResolvedSessionRecording{
				Mode:   NormalizeSessionRecording(projectCfg.SessionRecording),
				Source: SessionRecordingSourceRepo,
			}
		}
	}

	// team config (from team context)
	if projectRoot != "" {
		if teamMode := loadTeamSessionRecording(projectRoot); teamMode != "" {
			return &ResolvedSessionRecording{
				Mode:   NormalizeSessionRecording(teamMode),
				Source: SessionRecordingSourceTeam,
			}
		}
	}

	// ox-initialized repos default to auto; non-initialized default to manual.
	if isInitialized {
		return &ResolvedSessionRecording{
			Mode:   SessionRecordingAuto,
			Source: SessionRecordingSourceRepo,
		}
	}
	return &ResolvedSessionRecording{
		Mode:   SessionRecordingManual,
		Source: SessionRecordingSourceDefault,
	}
}

// loadUserSessionRecordingMode returns the normalized user-layer mode, or "" if unset.
//
// SessionsConfig.GetMode() returns "none" both when the field is absent AND when
// it's explicitly set to "none". To preserve the legacy resolver semantics
// (absence = inherit, explicit "none" = veto), we inspect the raw fields:
//   - explicit Mode == "none" or Enabled == &false → "disabled" (veto)
//   - everything else producing "none" → "" (unset / inherit)
func loadUserSessionRecordingMode() string {
	uc, err := LoadUserConfig()
	if err != nil || uc == nil || uc.Sessions == nil {
		return ""
	}
	mode := uc.Sessions.GetMode()
	switch mode {
	case "", "none":
		// only treat as an explicit veto if a raw field is actually set
		if uc.Sessions.Mode == "none" || (uc.Sessions.Enabled != nil && !*uc.Sessions.Enabled) {
			return SessionRecordingDisabled
		}
		return ""
	default:
		return NormalizeSessionRecording(mode)
	}
}

// loadTeamSessionRecording loads session recording setting from team context.
// Checks for config.toml in the team context directory.
// Returns empty string if no team context or no setting configured.
func loadTeamSessionRecording(projectRoot string) string {
	tc := FindRepoTeamContext(projectRoot)
	if tc == nil || tc.Path == "" {
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
//
// Precedence (low → high), mirroring ResolveSessionRecording exactly
// (contract D13, bead ox-6p5y.13):
//  1. default (auto).
//  2. team config (team context config.toml).
//  3. project config (.sageox/config.json).
//  4. user config (~/.config/sageox/config.yaml).
//  5. OX_SESSION_PUBLISHING env var (highest — the pipeline/automation escape hatch).
//
// "auto" uploads to ledger on session stop (backward compatible default).
// "manual" holds the session locally until the user runs an explicit
// upload — suppressing not just the transcript upload but the mid-session
// draft placeholder and the session-started cloud notification too (D12;
// enforced by their own call sites, not here).
//
// User config deliberately beats project and team config so an individual
// can opt out of publishing regardless of what the repo or team says.
// Before this, 'ox config set session_publishing' had nowhere to write that
// took effect at all: the setting wasn't in the catalog, and even
// hand-editing the user config file was inert because this resolver only
// ever consulted project config.
func ResolveSessionPublishing(projectRoot string) *ResolvedSessionPublishing {
	// OX_SESSION_PUBLISHING env var — highest priority (pipelines / automation).
	//
	// An UNRECOGNIZED value is ignored rather than normalized. Normalizing it
	// would map a typo like "manul" to "auto" — and because env outranks every
	// other layer, that silently overrides a deliberate `session_publishing:
	// manual` and re-enables uploads. A misspelled privacy control must never
	// fail open; fall through and let the lower layers decide.
	if envMode := os.Getenv(EnvSessionPublishing); envMode != "" {
		if IsValidSessionPublishingMode(envMode) {
			return &ResolvedSessionPublishing{
				Mode:   NormalizeSessionPublishing(envMode),
				Source: SessionRecordingSourceEnv,
			}
		}
		slog.Warn("ignoring unrecognized "+EnvSessionPublishing,
			"value", envMode, "valid", ValidSessionPublishingModes)
	}

	// user config (~/.config/sageox/config.yaml).
	if userCfg, err := LoadUserConfig(); err == nil && userCfg != nil && userCfg.SessionPublishing != "" {
		return &ResolvedSessionPublishing{
			Mode:   NormalizeSessionPublishing(userCfg.SessionPublishing),
			Source: SessionRecordingSourceUser,
		}
	}

	// project config (.sageox/config.json)
	if projectRoot != "" {
		projectCfg, err := LoadProjectConfig(projectRoot)
		if err == nil && projectCfg != nil && projectCfg.SessionPublishing != "" {
			return &ResolvedSessionPublishing{
				Mode:   NormalizeSessionPublishing(projectCfg.SessionPublishing),
				Source: SessionRecordingSourceRepo,
			}
		}
	}

	// team config (from team context)
	if projectRoot != "" {
		if teamMode := loadTeamSessionPublishing(projectRoot); teamMode != "" {
			return &ResolvedSessionPublishing{
				Mode:   NormalizeSessionPublishing(teamMode),
				Source: SessionRecordingSourceTeam,
			}
		}
	}

	// default to auto (upload on stop) for backward compatibility
	return &ResolvedSessionPublishing{
		Mode:   SessionPublishingAuto,
		Source: SessionRecordingSourceDefault,
	}
}

// loadTeamSessionPublishing loads the session publishing setting from team
// context. Mirrors loadTeamSessionRecording exactly. Returns empty string
// if no team context or no setting configured.
func loadTeamSessionPublishing(projectRoot string) string {
	tc := FindRepoTeamContext(projectRoot)
	if tc == nil || tc.Path == "" {
		return ""
	}

	teamCfg, err := LoadTeamConfig(tc.Path)
	if err != nil || teamCfg == nil {
		return ""
	}

	return teamCfg.SessionPublishing
}

// GetSessionPublishing is a convenience function that returns just the publishing mode string.
func GetSessionPublishing(projectRoot string) string {
	resolved := ResolveSessionPublishing(projectRoot)
	return resolved.Mode
}
