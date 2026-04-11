package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sageox/ox/internal/paths"
	"gopkg.in/yaml.v3"
)

// MaxDisplayNameLength is the maximum allowed length (in runes) for a display name.
// Generous for real names/handles, constraining enough to prevent CLI layout breakage.
const MaxDisplayNameLength = 40

// controlCharsRe matches ASCII and Unicode control characters.
var controlCharsRe = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// SanitizeDisplayName cleans a display name for safe storage and rendering.
// Replaces control characters with spaces (preserving word boundaries),
// trims whitespace, and collapses internal whitespace runs.
// Returns "" for whitespace-only input.
func SanitizeDisplayName(name string) string {
	name = controlCharsRe.ReplaceAllString(name, " ")
	name = strings.TrimSpace(name)
	name = strings.Join(strings.Fields(name), " ")
	return name
}

// ValidateDisplayName checks a display name after sanitization.
// Returns an error if the sanitized name exceeds MaxDisplayNameLength.
// Empty string is valid (means "use auto-derivation").
func ValidateDisplayName(name string) error {
	sanitized := SanitizeDisplayName(name)
	if utf8.RuneCountInString(sanitized) > MaxDisplayNameLength {
		return fmt.Errorf("display name too long (%d chars, max %d)", utf8.RuneCountInString(sanitized), MaxDisplayNameLength)
	}
	return nil
}

// ContextGitConfig holds settings for context git repo operations.
// These control automatic commit/push behavior during session operations.
type ContextGitConfig struct {
	// AutoCommit controls whether to commit on session stop / session end.
	// Default: true
	AutoCommit *bool `yaml:"auto_commit,omitempty"`

	// AutoPush controls whether to push after commit.
	// Default: true
	AutoPush *bool `yaml:"auto_push,omitempty"`
}

// IsAutoCommitEnabled returns true if auto-commit is enabled (default: true)
func (c *ContextGitConfig) IsAutoCommitEnabled() bool {
	if c == nil || c.AutoCommit == nil {
		return true
	}
	return *c.AutoCommit
}

// IsAutoPushEnabled returns true if auto-push is enabled (default: true)
func (c *ContextGitConfig) IsAutoPushEnabled() bool {
	if c == nil || c.AutoPush == nil {
		return true
	}
	return *c.AutoPush
}

// SessionsConfig holds settings for session recording.
type SessionsConfig struct {
	// Enabled controls whether sessions are automatically recorded during agent sessions.
	// Deprecated: Use Mode instead. Kept for backward compatibility.
	// Default: false
	Enabled *bool `yaml:"enabled,omitempty"`

	// Mode controls the session recording level.
	// Values: "none", "infra", "all"
	// Default: "none" (or "all" if Enabled=true for backward compatibility)
	Mode string `yaml:"mode,omitempty"`
}

// IsEnabled returns true if session recording is enabled (default: false)
// Deprecated: Use GetMode() instead
func (c *SessionsConfig) IsEnabled() bool {
	if c == nil || c.Enabled == nil {
		return false
	}
	return *c.Enabled
}

// GetMode returns the effective session mode.
// Supports backward compatibility: if Mode is not set but Enabled=true, returns "all".
// Returns "none" if nothing is configured.
func (c *SessionsConfig) GetMode() string {
	if c == nil {
		return "none"
	}
	if c.Mode != "" {
		return c.Mode
	}
	// backward compatibility: Enabled=true maps to "all"
	if c.Enabled != nil && *c.Enabled {
		return "all"
	}
	return "none"
}

// AllowedAgentTypes is the set of valid AgentWorkerConfig.Agent values.
var AllowedAgentTypes = []string{"none", "claude", "codex"}

// AgentWorkerConfig controls daemon-spawned AI coworker invocations.
type AgentWorkerConfig struct {
	// Agent selects which agent CLI the daemon uses for background work.
	// "none" disables agent spawning, "claude" or "codex" selects that CLI.
	// Default: "none"
	Agent string `yaml:"agent,omitempty"`

	// Deprecated: Use Agent instead. Kept for backward compatibility.
	Enabled *bool `yaml:"enabled,omitempty"`

	// Deprecated: Use Agent instead. Kept for backward compatibility.
	AgentType string `yaml:"agent_type,omitempty"`

	// MaxInvocationsPerHour rate-limits agent spawning per daemon.
	// Default: 60
	MaxInvocationsPerHour *int `yaml:"max_invocations_per_hour,omitempty"`

	// MaxConcurrent limits parallel agent processes per daemon.
	// Default: 4
	MaxConcurrent *int `yaml:"max_concurrent,omitempty"`

	// SessionFinalize enables automatic session anti-entropy (generating
	// missing summaries, HTML, and markdown for incomplete sessions).
	// Default: true (runs automatically when daemon is enabled)
	SessionFinalize *bool `yaml:"session_finalize,omitempty"`

	// QualityUploadThreshold is the minimum quality score (0.0-1.0) for a
	// session to be uploaded to the team ledger. Sessions below this score
	// are kept locally but not shared. Default: 0.3 (capture more initially)
	QualityUploadThreshold *float64 `yaml:"quality_upload_threshold,omitempty"`

	// QualityDiscardThreshold is the quality score (0.0-1.0) below which a
	// session is deleted entirely. Only truly empty/abandoned sessions should
	// score this low. Default: 0.1
	QualityDiscardThreshold *float64 `yaml:"quality_discard_threshold,omitempty"`
}

// GetAgent returns the raw configured agent selector.
// Returns "" when nothing is configured (caller should auto-detect),
// "none" (explicitly disabled), "claude", or "codex".
// Handles backward compat: Enabled=true with no Agent field maps to "claude".
// For a fully resolved value (with auto-detection), use agentwork.ResolveAgent().
func (c *AgentWorkerConfig) GetAgent() string {
	if c == nil {
		return ""
	}
	// new field takes precedence
	if c.Agent != "" {
		return c.Agent
	}
	// backward compat: Enabled bool + AgentType string
	if c.Enabled != nil {
		if *c.Enabled {
			if c.AgentType != "" {
				return c.AgentType
			}
			return "claude"
		}
		return "none" // explicitly disabled via legacy field
	}
	return "" // not configured — auto-detect
}

// IsEnabled returns true if agent worker spawning is enabled.
// Returns false for "none" and "" (unconfigured). Callers that want
// auto-detection should use agentwork.ResolveAgent() instead.
func (c *AgentWorkerConfig) IsEnabled() bool {
	agent := c.GetAgent()
	return agent != "none" && agent != ""
}

// GetAgentType returns the configured agent type (default: "claude").
// Returns "claude" for unset or disabled states. For callers that need
// auto-detection, use agentwork.ResolveAgent() instead.
func (c *AgentWorkerConfig) GetAgentType() string {
	agent := c.GetAgent()
	if agent == "" || agent == "none" {
		return "claude"
	}
	return agent
}

// GetMaxInvocationsPerHour returns the rate limit (default: 60)
func (c *AgentWorkerConfig) GetMaxInvocationsPerHour() int {
	if c == nil || c.MaxInvocationsPerHour == nil {
		return 60
	}
	return *c.MaxInvocationsPerHour
}

// GetMaxConcurrent returns the concurrency limit (default: 4)
func (c *AgentWorkerConfig) GetMaxConcurrent() int {
	if c == nil || c.MaxConcurrent == nil {
		return 4
	}
	return *c.MaxConcurrent
}

// IsSessionFinalizeEnabled returns true if session anti-entropy is enabled (default: true).
// Also requires the master Enabled switch to be true.
func (c *AgentWorkerConfig) IsSessionFinalizeEnabled() bool {
	if c == nil {
		return false
	}
	if c.SessionFinalize == nil {
		return c.IsEnabled() // default: enabled when daemon is enabled
	}
	return *c.SessionFinalize && c.IsEnabled()
}

// GetQualityUploadThreshold returns the minimum quality score for ledger upload (default: 0.3).
func (c *AgentWorkerConfig) GetQualityUploadThreshold() float64 {
	if c == nil || c.QualityUploadThreshold == nil {
		return 0.3
	}
	return *c.QualityUploadThreshold
}

// GetQualityDiscardThreshold returns the quality score below which sessions are deleted (default: 0.1).
func (c *AgentWorkerConfig) GetQualityDiscardThreshold() float64 {
	if c == nil || c.QualityDiscardThreshold == nil {
		return 0.1
	}
	return *c.QualityDiscardThreshold
}

// Validate checks the config for invalid values.
// Returns an error if agent is not in AllowedAgentTypes or limits are non-positive.
func (c *AgentWorkerConfig) Validate() error {
	if c == nil {
		return nil
	}

	if c.Agent != "" && !slices.Contains(AllowedAgentTypes, c.Agent) {
		return fmt.Errorf("unknown agent %q, allowed: %v", c.Agent, AllowedAgentTypes)
	}

	// backward compat: validate legacy AgentType field too
	if c.Agent == "" && c.AgentType != "" {
		// legacy field only allowed non-"none" agent types
		validLegacy := []string{"claude", "codex"}
		if !slices.Contains(validLegacy, c.AgentType) {
			return fmt.Errorf("unknown agent_type %q, allowed: %v", c.AgentType, validLegacy)
		}
	}

	if c.MaxInvocationsPerHour != nil && *c.MaxInvocationsPerHour < 1 {
		return fmt.Errorf("max_invocations_per_hour must be >= 1, got %d", *c.MaxInvocationsPerHour)
	}

	if c.MaxConcurrent != nil && *c.MaxConcurrent < 1 {
		return fmt.Errorf("max_concurrent must be >= 1, got %d", *c.MaxConcurrent)
	}

	return nil
}

// WithDefaults returns a copy of the config with zero-value fields set to defaults.
// Does not modify the receiver.
func (c *AgentWorkerConfig) WithDefaults() *AgentWorkerConfig {
	out := &AgentWorkerConfig{}
	if c != nil {
		*out = *c
	}

	// migrate legacy fields to new Agent field (only if legacy fields are set)
	if out.Agent == "" && out.Enabled != nil {
		if *out.Enabled {
			if out.AgentType != "" {
				out.Agent = out.AgentType
			} else {
				out.Agent = "claude"
			}
		} else {
			out.Agent = "none"
		}
	}
	// Agent="" with no legacy fields means "auto-detect" — don't override
	if out.MaxInvocationsPerHour == nil {
		v := 60
		out.MaxInvocationsPerHour = &v
	}
	if out.MaxConcurrent == nil {
		v := 4
		out.MaxConcurrent = &v
	}
	if out.SessionFinalize == nil {
		t := true
		out.SessionFinalize = &t
	}
	if out.QualityUploadThreshold == nil {
		v := 0.3
		out.QualityUploadThreshold = &v
	}
	if out.QualityDiscardThreshold == nil {
		v := 0.1
		out.QualityDiscardThreshold = &v
	}

	return out
}

// UserConfig holds user-level configuration from config.yaml
type UserConfig struct {
	DisplayName       string             `yaml:"display_name,omitempty"`
	TipsEnabled       *bool              `yaml:"tips_enabled,omitempty"`
	TelemetryEnabled  *bool              `yaml:"telemetry_enabled,omitempty"`
	SessionTermsShown *bool              `yaml:"session_terms_shown,omitempty"`
	Attribution       *Attribution       `yaml:"attribution,omitempty"`
	Badge             *BadgeConfig       `yaml:"badge,omitempty"`
	ContextGit        *ContextGitConfig  `yaml:"context_git,omitempty"`
	Sessions          *SessionsConfig    `yaml:"sessions,omitempty"`
	AgentWorker       *AgentWorkerConfig `yaml:"agent_worker,omitempty"`
	ViewFormat        string             `yaml:"view_format,omitempty"`     // "web", "text", "json" (default: "web")
	Murmuring         string             `yaml:"murmur_send,omitempty"`     // "auto", "manual"
	LegacyMurmuring   string             `yaml:"murmuring,omitempty"`       // deprecated: read old key on upgrade
	MurmurReceive     string             `yaml:"murmur_receive,omitempty"`  // "on", "off"

}

// BadgeConfig tracks badge suggestion state across all projects.
type BadgeConfig struct {
	// SuggestionStatus tracks user response: "not_asked", "added", "declined"
	SuggestionStatus string `yaml:"suggestion_status,omitempty"`

	// LastDeclined timestamp if user chose "never" - we respect this permanently
	LastDeclined *string `yaml:"last_declined,omitempty"`
}

// GetDisplayName returns the user's configured display name, or "" if not set.
func (c *UserConfig) GetDisplayName() string {
	return c.DisplayName
}

// SetDisplayName sets the user's display name for privacy-aware rendering.
// Silently sanitizes the input (strips control chars, trims whitespace).
func (c *UserConfig) SetDisplayName(name string) {
	c.DisplayName = SanitizeDisplayName(name)
}

// AreTipsEnabled returns true if tips are enabled (default: true)
func (c *UserConfig) AreTipsEnabled() bool {
	if c.TipsEnabled == nil {
		return true
	}
	return *c.TipsEnabled
}

// HasSeenSessionTerms returns true if the user has seen the session recording notice.
func (c *UserConfig) HasSeenSessionTerms() bool {
	if c.SessionTermsShown == nil {
		return false
	}
	return *c.SessionTermsShown
}

// SetSessionTermsShown records whether the user has seen the session recording notice.
func (c *UserConfig) SetSessionTermsShown(shown bool) {
	c.SessionTermsShown = &shown
}

// IsTelemetryEnabled returns true if telemetry is enabled (default: true)
func (c *UserConfig) IsTelemetryEnabled() bool {
	if c.TelemetryEnabled == nil {
		return true
	}
	return *c.TelemetryEnabled
}

// SetTelemetryEnabled sets the telemetry preference
func (c *UserConfig) SetTelemetryEnabled(enabled bool) {
	c.TelemetryEnabled = &enabled
}

// GetContextGitAutoCommit returns whether auto-commit is enabled for context git.
// Default: true
func (c *UserConfig) GetContextGitAutoCommit() bool {
	if c.ContextGit == nil {
		return true
	}
	return c.ContextGit.IsAutoCommitEnabled()
}

// GetContextGitAutoPush returns whether auto-push is enabled for context git.
// Default: true
func (c *UserConfig) GetContextGitAutoPush() bool {
	if c.ContextGit == nil {
		return true
	}
	return c.ContextGit.IsAutoPushEnabled()
}

// SetContextGitAutoCommit sets the auto-commit preference for context git.
func (c *UserConfig) SetContextGitAutoCommit(enabled bool) {
	if c.ContextGit == nil {
		c.ContextGit = &ContextGitConfig{}
	}
	c.ContextGit.AutoCommit = &enabled
}

// SetContextGitAutoPush sets the auto-push preference for context git.
func (c *UserConfig) SetContextGitAutoPush(enabled bool) {
	if c.ContextGit == nil {
		c.ContextGit = &ContextGitConfig{}
	}
	c.ContextGit.AutoPush = &enabled
}

// AreSessionsEnabled returns whether session recording is enabled.
// Default: false
func (c *UserConfig) AreSessionsEnabled() bool {
	if c.Sessions == nil {
		return false
	}
	return c.Sessions.IsEnabled()
}

// SetSessionsEnabled sets the session recording preference.
func (c *UserConfig) SetSessionsEnabled(enabled bool) {
	if c.Sessions == nil {
		c.Sessions = &SessionsConfig{}
	}
	c.Sessions.Enabled = &enabled
}

// GetAgentWorkerConfig returns the agent worker config, or nil if not set.
func (c *UserConfig) GetAgentWorkerConfig() *AgentWorkerConfig {
	return c.AgentWorker
}

// IsAgentWorkerEnabled returns whether daemon agent spawning is enabled.
// Default: false
func (c *UserConfig) IsAgentWorkerEnabled() bool {
	if c.AgentWorker == nil {
		return false
	}
	return c.AgentWorker.IsEnabled()
}

// GetAgentWorkerAgent returns the raw configured agent selector.
// Returns "" (unconfigured/auto-detect), "none", "claude", or "codex".
func (c *UserConfig) GetAgentWorkerAgent() string {
	if c.AgentWorker == nil {
		return ""
	}
	return c.AgentWorker.GetAgent()
}

// SetAgentWorkerAgent sets the agent worker agent selector and clears deprecated fields.
func (c *UserConfig) SetAgentWorkerAgent(agent string) {
	if c.AgentWorker == nil {
		c.AgentWorker = &AgentWorkerConfig{}
	}
	c.AgentWorker.Agent = agent
	c.AgentWorker.Enabled = nil
	c.AgentWorker.AgentType = ""
}

// Deprecated: Use SetAgentWorkerAgent instead.
func (c *UserConfig) SetAgentWorkerEnabled(enabled bool) {
	if enabled {
		c.SetAgentWorkerAgent("claude")
	} else {
		c.SetAgentWorkerAgent("none")
	}
}

// GetViewFormat returns the preferred session view format.
// Default: "web"
func (c *UserConfig) GetViewFormat() string {
	if c.ViewFormat == "" {
		return "web"
	}
	return c.ViewFormat
}

// GetMurmuring returns the user's configured murmuring mode, or "" if not set.
// Falls back to the legacy "murmuring" key for upgrade compatibility.
func (c *UserConfig) GetMurmuring() string {
	if c.Murmuring != "" {
		return c.Murmuring
	}
	return c.LegacyMurmuring
}

// SetMurmuring sets the user's murmuring mode preference and clears the legacy key.
func (c *UserConfig) SetMurmuring(mode string) {
	c.Murmuring = mode
	c.LegacyMurmuring = ""
}

// GetMurmurReceive returns the user's configured murmur receive mode, or "" if not set.
func (c *UserConfig) GetMurmurReceive() string {
	return c.MurmurReceive
}

// SetMurmurReceive sets the user's murmur receive preference.
func (c *UserConfig) SetMurmurReceive(mode string) {
	c.MurmurReceive = mode
}

// LoadUserConfig loads user configuration using standard path discovery.
// Checks OX_USER_CONFIG env var first, then XDG/default paths.
//
// For tests that need to load from an explicit directory, use LoadUserConfigFrom.
func LoadUserConfig() (*UserConfig, error) {
	// OX_USER_CONFIG overrides all path discovery — for CI/ephemeral environments
	if envPath := os.Getenv(EnvUserConfig); envPath != "" {
		return loadUserConfigFromFile(envPath)
	}

	return LoadUserConfigFrom("")
}

// LoadUserConfigFrom loads user configuration from the specified config directory.
// If configDir is empty, uses GetUserConfigDir() which respects XDG_CONFIG_HOME.
// This is primarily for tests that need to point at a temp directory.
func LoadUserConfigFrom(configDir string) (*UserConfig, error) {
	if configDir == "" {
		configDir = GetUserConfigDir()
		if configDir == "" {
			return &UserConfig{}, nil
		}
	}

	configPath := filepath.Join(configDir, "config.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &UserConfig{}, nil
		}
		return &UserConfig{}, fmt.Errorf("reading config: %w", err)
	}

	var cfg UserConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return &UserConfig{}, fmt.Errorf("parsing config: %w", err)
	}

	return &cfg, nil
}

// loadUserConfigFromFile loads user config from an explicit file path.
func loadUserConfigFromFile(path string) (*UserConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &UserConfig{}, nil
		}
		return &UserConfig{}, fmt.Errorf("reading config from OX_USER_CONFIG=%s: %w", path, err)
	}

	var cfg UserConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return &UserConfig{}, fmt.Errorf("parsing config from OX_USER_CONFIG=%s: %w", path, err)
	}

	return &cfg, nil
}

// GetUserConfigDir returns the user config directory path.
//
// Path Resolution (via internal/paths package):
//
//	Default:           ~/.sageox/config/
//	With OX_XDG_ENABLE: $XDG_CONFIG_HOME/sageox/ (default: ~/.config/sageox/)
//
// The consolidated ~/.sageox/ structure provides a single discoverable location
// for all SageOx data. Users who prefer XDG standard locations can set
// OX_XDG_ENABLE=1 to use traditional XDG paths.
//
// See internal/paths/doc.go for architecture rationale.
func GetUserConfigDir() string {
	return paths.ConfigDir()
}

// SaveUserConfig saves user configuration to the config directory.
// Uses atomic write (temp file + rename) to prevent corruption from
// concurrent writes or crashes mid-write.
func SaveUserConfig(cfg *UserConfig) error {
	configDir := GetUserConfigDir()
	if configDir == "" {
		return os.ErrNotExist
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	configPath := filepath.Join(configDir, "config.yaml")
	tempPath := configPath + ".tmp"

	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("writing temp config: %w", err)
	}

	if err := os.Rename(tempPath, configPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("renaming temp config: %w", err)
	}

	return nil
}

// GetContextGitAutoCommit loads user config and returns the auto-commit setting.
// This is a convenience function for use without loading the full config.
// Default: true
func GetContextGitAutoCommit() bool {
	cfg, err := LoadUserConfig()
	if err != nil {
		return true
	}
	return cfg.GetContextGitAutoCommit()
}

// GetContextGitAutoPush loads user config and returns the auto-push setting.
// This is a convenience function for use without loading the full config.
// Default: false
func GetContextGitAutoPush() bool {
	cfg, err := LoadUserConfig()
	if err != nil {
		return false
	}
	return cfg.GetContextGitAutoPush()
}

// SetContextGitAutoCommit loads user config, sets auto-commit, and saves.
// This is a convenience function for setting a single value.
func SetContextGitAutoCommit(value bool) error {
	cfg, err := LoadUserConfig()
	if err != nil {
		cfg = &UserConfig{}
	}
	cfg.SetContextGitAutoCommit(value)
	return SaveUserConfig(cfg)
}

// SetContextGitAutoPush loads user config, sets auto-push, and saves.
// This is a convenience function for setting a single value.
func SetContextGitAutoPush(value bool) error {
	cfg, err := LoadUserConfig()
	if err != nil {
		cfg = &UserConfig{}
	}
	cfg.SetContextGitAutoPush(value)
	return SaveUserConfig(cfg)
}

// AreSessionsEnabled loads user config and returns the sessions.enabled setting.
// This is a convenience function for use without loading the full config.
// Default: false
func AreSessionsEnabled() bool {
	cfg, err := LoadUserConfig()
	if err != nil {
		return false
	}
	return cfg.AreSessionsEnabled()
}

// SetSessionsEnabled loads user config, sets sessions.enabled, and saves.
// This is a convenience function for setting a single value.
func SetSessionsEnabled(value bool) error {
	cfg, err := LoadUserConfig()
	if err != nil {
		cfg = &UserConfig{}
	}
	cfg.SetSessionsEnabled(value)
	return SaveUserConfig(cfg)
}

// GetDisplayName loads user config and returns the display_name setting.
// Returns "" if not set.
func GetDisplayName() string {
	cfg, err := LoadUserConfig()
	if err != nil {
		return ""
	}
	return cfg.GetDisplayName()
}

// SetDisplayName loads user config, validates, sets display_name, and saves.
// Returns an error if the name exceeds MaxDisplayNameLength after sanitization.
func SetDisplayName(name string) error {
	if err := ValidateDisplayName(name); err != nil {
		return err
	}
	cfg, err := LoadUserConfig()
	if err != nil {
		cfg = &UserConfig{}
	}
	cfg.SetDisplayName(name)
	return SaveUserConfig(cfg)
}
