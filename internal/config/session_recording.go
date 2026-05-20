package config

import (
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/api/kbconfig"
	"github.com/sageox/ox/internal/paths"
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
	SessionRecordingSourceKB      SessionRecordingSource = "kb"      // from KB-local .sageox/config.yaml
)

// ResolvedSessionRecording contains the effective mode and its source.
type ResolvedSessionRecording struct {
	Mode   string                 // effective mode: "disabled", "manual", or "auto"
	Source SessionRecordingSource // where the setting came from

	// SafetyInversion is true when "disabled" was forced by the either-side veto
	// in kbconfig.ResolveEffectiveMode — i.e. the precedence-winning layer was
	// NOT disabled, but the other layer was, and that other layer's veto
	// overrode standard precedence. Surfaced for doctor/diagnostics so the
	// origin layer can be reported accurately.
	SafetyInversion bool

	// KBID is the current KB binding's id, if any. Empty when the caller did
	// not pass a KB binding (legacy path).
	KBID string

	// KBType is the current KB binding's type, if any. Used for default-mode
	// resolution via kbconfig.DefaultSessionRecordingMode.
	KBType string
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
// Precedence (low → high):
//  1. per-KB-type default (kbconfig.DefaultSessionRecordingMode) when kbType set,
//     else legacy default (manual, or auto for ox-initialized repos).
//  2. user config (~/.config/sageox/config.yaml).
//  3. KB-local .sageox/config.yaml (when kbID != "").
//  4. legacy project / team config (only when kbID == "" — preserves pre-KB behavior).
//  5. OX_SESSION_RECORDING env var (highest).
//
// Safety inversion: when both user and KB layers are set, kbconfig.ResolveEffectiveMode
// applies an either-side "disabled" veto regardless of standard precedence. Env still
// overrides everything (it is the pipeline/automation escape hatch).
//
// kbID/kbType are passed in by the caller to avoid an import cycle
// (internal/config cannot import internal/kb). Callers without a KB binding
// pass empty strings; behavior then matches the legacy resolver.
func ResolveSessionRecording(projectRoot string, kbID, kbType string) *ResolvedSessionRecording {
	// 0. OX_SESSION_RECORDING env var — highest priority (pipelines / automation).
	if envMode := os.Getenv(EnvSessionRecording); envMode != "" {
		return &ResolvedSessionRecording{
			Mode:   NormalizeSessionRecording(envMode),
			Source: SessionRecordingSourceEnv,
			KBID:   kbID,
			KBType: kbType,
		}
	}

	// user layer (raw — pre-normalization, empty means "unset")
	userMode := loadUserSessionRecordingMode()

	// KB-aware path: only when a KB binding was passed in.
	if kbID != "" {
		kbMode := loadKBSessionRecordingMode(kbID)

		// safety-inversion-aware combine. Empty inputs are tolerated.
		combined := kbconfig.ResolveEffectiveMode(userMode, kbMode)
		if combined != "" {
			// safety inversion: result is "disabled" AND the precedence-winning
			// layer (user when set, else kb) was NOT itself disabled. That means
			// the OTHER layer's veto flipped the result.
			precedenceLayer := userMode
			if precedenceLayer == "" {
				precedenceLayer = kbMode
			}
			safetyInv := combined == SessionRecordingDisabled && precedenceLayer != SessionRecordingDisabled

			return &ResolvedSessionRecording{
				Mode:            NormalizeSessionRecording(combined),
				Source:          pickKBChainSource(userMode, kbMode, combined),
				SafetyInversion: safetyInv,
				KBID:            kbID,
				KBType:          kbType,
			}
		}

		// neither layer set anything — fall through to per-KB-type default.
		defaultMode := defaultKBSessionRecordingMode(projectRoot, kbID, kbType)
		return &ResolvedSessionRecording{
			Mode:   NormalizeSessionRecording(defaultMode),
			Source: SessionRecordingSourceDefault,
			KBID:   kbID,
			KBType: kbType,
		}
	}

	// Legacy path (no KB binding): preserve pre-KB resolver behavior so existing
	// callers and tests are unaffected.
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

// defaultKBSessionRecordingMode returns the fallback mode for a KB-aware
// resolution when neither env/user/kb layers are set.
//
// If kbType is known, the per-type default is authoritative. When kbType is
// still unknown because local KB metadata has not synced yet, preserve the
// legacy repo-root behavior for the project's own bound KB so migration to
// YAML does not silently flip initialized repos from auto to manual.
func defaultKBSessionRecordingMode(projectRoot, kbID, kbType string) string {
	if kbType != "" {
		if mode := kbconfig.DefaultSessionRecordingMode(kbType); mode != "" {
			return mode
		}
	}
	if projectRoot != "" && kbID != "" {
		if projectCfg, err := LoadProjectConfig(projectRoot); err == nil &&
			projectCfg != nil && projectCfg.KBID == kbID {
			return SessionRecordingAuto
		}
	}
	return SessionRecordingManual
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

// loadKBSessionRecordingMode reads the KB-local .sageox/config.yaml and returns
// the configured mode, or "" if unset / unreadable. Missing files and parse
// errors are treated as "unset" — they're not fatal; defaults take over.
func loadKBSessionRecordingMode(kbID string) string {
	if kbID == "" {
		return ""
	}
	cfgPath := filepath.Join(paths.KBDir(kbID), kbconfig.ConfigYAMLPath)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return ""
	}
	env, err := kbconfig.UnmarshalConfigYAML(data)
	if err != nil || env == nil {
		return ""
	}
	// Validate before trusting the mode string — malformed config should be
	// treated as unset rather than silently coerced (which normalizes to
	// "manual" downstream, turning recording ON for an invalid KB config).
	if err := kbconfig.ValidateConfig(env.ToKBConfig()); err != nil {
		return ""
	}
	if env.Features.SessionRecording.Mode == nil {
		return ""
	}
	return *env.Features.SessionRecording.Mode
}

// pickKBChainSource determines which layer the combined value came from after
// kbconfig.ResolveEffectiveMode applied. Useful for diagnostics.
func pickKBChainSource(userMode, kbMode, combined string) SessionRecordingSource {
	// safety inversion: either-side veto → "disabled" wins. Attribute to the
	// vetoing layer so doctor / --show-origin reports the actual source.
	if combined == SessionRecordingDisabled {
		switch {
		case userMode == SessionRecordingDisabled && kbMode == SessionRecordingDisabled:
			return SessionRecordingSourceUser // tie → user (precedence)
		case userMode == SessionRecordingDisabled:
			return SessionRecordingSourceUser
		case kbMode == SessionRecordingDisabled:
			return SessionRecordingSourceKB
		}
	}
	// standard precedence: user wins when set, else kb.
	if userMode != "" {
		return SessionRecordingSourceUser
	}
	return SessionRecordingSourceKB
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
// Callers without a KB binding pass empty kbID/kbType.
func GetSessionRecording(projectRoot string) string {
	resolved := ResolveSessionRecording(projectRoot, "", "")
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
