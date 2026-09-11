package config

import (
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

const teamConfigFilename = "config.toml"

// TeamConfig represents team-level configuration stored in the team context directory.
// Located at: <team_context_path>/config.toml
//
// This allows teams to set default policies that apply to all team members,
// while still allowing individual users to override via their user config.
type TeamConfig struct {
	// SessionRecording controls the default session recording mode for the team.
	// Values: "disabled", "manual", "auto"
	// Empty string means no team default (fall back to system default).
	SessionRecording string `toml:"session_recording,omitempty"`

	// SessionPublishing controls the default session publishing mode for the
	// team. Values: "auto", "manual". Empty string means no team default
	// (fall back to the repo's .sageox/config.json, then "auto"). A user's
	// own session_publishing config always overrides this — see
	// ResolveSessionPublishing (contract D13).
	SessionPublishing string `toml:"session_publishing,omitempty"`

	// SessionNotification is the message shown to users when recording starts.
	// If empty, uses the default notification message.
	SessionNotification string `toml:"session_notification,omitempty"`

	// PRVisuals supplies team defaults for rich GitHub pull-request visuals.
	PRVisuals *PRVisualsConfig `toml:"pr_visuals,omitempty"`
}

// LoadTeamConfig loads team configuration from <teamContextPath>/config.toml.
// Returns nil, nil if the config file does not exist.
// Returns an error if the file exists but cannot be parsed.
func LoadTeamConfig(teamContextPath string) (*TeamConfig, error) {
	if teamContextPath == "" {
		return nil, nil
	}

	configPath := filepath.Join(teamContextPath, teamConfigFilename)

	// return nil if file doesn't exist (not an error)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var cfg TeamConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// SaveTeamConfig saves team configuration to <teamContextPath>/config.toml.
func SaveTeamConfig(teamContextPath string, cfg *TeamConfig) error {
	if teamContextPath == "" {
		return nil
	}

	configPath := filepath.Join(teamContextPath, teamConfigFilename)

	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}
