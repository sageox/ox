package main

import (
	"fmt"

	"github.com/sageox/ox/internal/config"
)

// ConfigLevel represents where a config setting can be stored.
type ConfigLevel string

const (
	ConfigLevelUser    ConfigLevel = "user"
	ConfigLevelRepo    ConfigLevel = "repo"
	ConfigLevelTeam    ConfigLevel = "team"
	ConfigLevelDefault ConfigLevel = "default"
)

// ConfigSetting defines a configurable setting.
type ConfigSetting struct {
	Key             string        // setting key (e.g., "session_recording")
	Description     string        // short description for list views
	LongDescription string        // detailed explanation for help
	Category        string        // grouping category (e.g., "Sessions", "Privacy")
	ValidValues     []string      // allowed values (empty = any string)
	Default         string        // default value
	Levels          []ConfigLevel // which levels support this setting
}

// ConfigValue represents a resolved config value with its source.
type ConfigValue struct {
	Key     string      `json:"key"`
	Value   string      `json:"value"`
	Source  ConfigLevel `json:"source"`
	Default string      `json:"default,omitempty"`
	UserVal string      `json:"user_value,omitempty"`
	RepoVal string      `json:"repo_value,omitempty"`
	TeamVal string      `json:"team_value,omitempty"`
}

// AllSettings defines all configurable settings.
var AllSettings = []ConfigSetting{
	{
		Key:         "session_recording",
		Description: "Session recording mode",
		LongDescription: `Controls whether and how AI agent sessions are recorded.

  disabled - No sessions are recorded
  manual   - Recording starts only when you run 'ox session start'
  auto     - Recording starts automatically when an agent begins work

Sessions are saved to this repo's ledger (created by 'ox init'). Each
repository has its own ledger containing session history for that repo.

Team Context (shared across repos) is separate - it contains team
conventions and knowledge that apply to ALL repos your team owns.`,
		Category:    "Sessions",
		ValidValues: []string{"disabled", "manual", "auto"},
		Default:     "auto",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo, ConfigLevelTeam},
	},
	{
		Key:         "murmur_send",
		Description: "Auto work-in-progress signals",
		LongDescription: `Controls work-in-progress signals to teammates.

Murmurs are short coordination signals that let teammates know what
AI coworkers are working on before PRs or commits appear. Signals
propagate via the ledger and are delivered as whispers to active
coworkers.

  manual - Murmurs via 'ox murmur' only
  auto   - Daemon also nudges agents to murmur periodically (~15 min) (default)`,
		Category:    "Collaboration",
		ValidValues: []string{"manual", "auto"},
		Default:     "auto",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "murmur_receive",
		Description: "Receive murmurs from other coworkers",
		LongDescription: `Controls whether work-in-progress signals from other coworkers
appear in your whisper stream.

  on  - Receive murmurs as whispers (default)
  off - Suppress murmur whispers (other coworkers still receive them)

This is a personal preference — it only affects YOUR whisper
delivery, not whether murmurs are relayed for others.`,
		Category:    "Collaboration",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "telemetry",
		Description: "Anonymous usage telemetry",
		LongDescription: `Controls anonymous usage statistics collection.

When enabled, ox sends anonymous data to help improve the tool:
  - Command usage frequency (e.g., "ox session start" was run)
  - Error rates (no error details or stack traces)
  - Feature adoption metrics

What is NEVER collected:
  - Your code or file contents
  - Personal information (names, emails, etc.)
  - Session recordings or conversations
  - Repository names or paths`,
		Category:    "Privacy",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "tips",
		Description: "Show helpful tips",
		LongDescription: `Controls whether ox shows contextual tips and suggestions.

When enabled, ox displays helpful hints about features and workflows
after command output. Useful for learning ox, but can be disabled
once you're familiar with the tool.`,
		Category:    "Display",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "context_git.auto_commit",
		Description: "Auto-save session changes",
		LongDescription: `Controls automatic saving of session data to the repo's ledger.

When enabled, sessions are automatically saved to the ledger when
they end. This ensures no work is lost if you forget to save.

When disabled, you must manually save sessions.

Note: The ledger is specific to this repository. Each repo where
'ox init' was run has its own ledger for session history.`,
		Category:    "Sessions",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "context_git.auto_push",
		Description: "Auto-sync sessions to ledger",
		LongDescription: `Controls automatic syncing of sessions to the repo's ledger.

When enabled, sessions are automatically synced to the remote ledger
after being saved locally. This completes the end-to-end session
pipeline: record → commit → push → anti-entropy finalizes.

When disabled, sessions stay local until you manually sync them.`,
		Category:    "Sessions",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "view_format",
		Description: "Default session view format",
		LongDescription: `Controls the default output format for 'ox session view'.

  html - Opens session in browser with rich HTML viewer (default)
  text - Renders session as markdown in terminal
  json - Outputs structured JSON (useful for scripting)

Override per-invocation with --html, --text, or --json flags.`,
		Category:    "Display",
		ValidValues: []string{"html", "text", "json"},
		Default:     "html",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
	{
		Key:         "agent_worker",
		Description: "Daemon AI coworker for background tasks",
		LongDescription: `Selects which agent CLI the daemon uses for background tasks like
session anti-entropy (generating missing summaries for orphaned sessions).

  auto   — auto-detect from PATH (default). Prefers claude > codex.
  none   — explicitly disabled. Only lightweight cleanup runs.
  claude — use Claude Code CLI for background agent work.
  codex  — use OpenAI Codex CLI for background agent work.

When an agent is available (configured or auto-detected), the daemon
automatically detects incomplete sessions and spawns the agent to
generate summaries, then uploads them to the ledger.
Rate-limited to 60 invocations/hour, 4 concurrent.`,
		Category:    "Sessions",
		ValidValues: []string{"auto", "none", "claude", "codex"},
		Default:     "auto",
		Levels:      []ConfigLevel{ConfigLevelUser},
	},
}

// GetSetting returns the setting definition for a key.
func GetSetting(key string) *ConfigSetting {
	for i := range AllSettings {
		if AllSettings[i].Key == key {
			return &AllSettings[i]
		}
	}
	return nil
}

// ResolveConfigValue resolves a config value from all levels.
// Priority: User > Repo > Team > Default
func ResolveConfigValue(key string, projectRoot string) (*ConfigValue, error) {
	setting := GetSetting(key)
	if setting == nil {
		return nil, fmt.Errorf("unknown setting: %s", key)
	}

	cv := &ConfigValue{
		Key:     key,
		Default: setting.Default,
	}

	// load user config
	userCfg, _ := config.LoadUserConfig()

	// load repo config
	var repoCfg *config.ProjectConfig
	if projectRoot != "" {
		repoCfg, _ = config.LoadProjectConfig(projectRoot)
	}

	// load team config (repo's own team, not cross-team)
	var teamCfg *config.TeamConfig
	if projectRoot != "" {
		if tc := config.FindRepoTeamContext(projectRoot); tc != nil {
			teamCfg, _ = config.LoadTeamConfig(tc.Path)
		}
	}

	// resolve based on key
	switch key {
	case "session_recording":
		if userCfg != nil && userCfg.Sessions != nil {
			mode := userCfg.Sessions.GetMode()
			if mode != "" && mode != "none" {
				cv.UserVal = config.NormalizeSessionRecording(mode)
			}
		}
		if repoCfg != nil && repoCfg.SessionRecording != "" {
			cv.RepoVal = config.NormalizeSessionRecording(repoCfg.SessionRecording)
		}
		if teamCfg != nil && teamCfg.SessionRecording != "" {
			cv.TeamVal = config.NormalizeSessionRecording(teamCfg.SessionRecording)
		}

	case "murmur_send":
		if userCfg != nil && userCfg.GetMurmuring() != "" {
			cv.UserVal = config.NormalizeMurmuring(userCfg.GetMurmuring())
		}
		if repoCfg != nil && repoCfg.GetMurmuring() != "" {
			cv.RepoVal = config.NormalizeMurmuring(repoCfg.GetMurmuring())
		}

	case "murmur_receive":
		if userCfg != nil && userCfg.MurmurReceive != "" {
			cv.UserVal = config.NormalizeMurmurReceive(userCfg.MurmurReceive)
		}
		if repoCfg != nil && repoCfg.MurmurReceive != "" {
			cv.RepoVal = config.NormalizeMurmurReceive(repoCfg.MurmurReceive)
		}

	case "telemetry":
		if userCfg != nil {
			if userCfg.IsTelemetryEnabled() {
				cv.UserVal = "on"
			} else if userCfg.TelemetryEnabled != nil {
				cv.UserVal = "off"
			}
		}

	case "tips":
		if userCfg != nil {
			if userCfg.AreTipsEnabled() {
				cv.UserVal = "on"
			} else if userCfg.TipsEnabled != nil {
				cv.UserVal = "off"
			}
		}

	case "context_git.auto_commit":
		if userCfg != nil && userCfg.ContextGit != nil && userCfg.ContextGit.AutoCommit != nil {
			if *userCfg.ContextGit.AutoCommit {
				cv.UserVal = "on"
			} else {
				cv.UserVal = "off"
			}
		}

	case "context_git.auto_push":
		if userCfg != nil && userCfg.ContextGit != nil && userCfg.ContextGit.AutoPush != nil {
			if *userCfg.ContextGit.AutoPush {
				cv.UserVal = "on"
			} else {
				cv.UserVal = "off"
			}
		}

	case "view_format":
		if userCfg != nil && userCfg.ViewFormat != "" {
			cv.UserVal = userCfg.ViewFormat
		}

	case "agent_worker":
		if userCfg != nil && userCfg.AgentWorker != nil {
			agent := userCfg.AgentWorker.GetAgent()
			switch agent {
			case "":
				// not configured — auto-detect (don't set UserVal, use default)
			case "none":
				cv.UserVal = "none"
			default:
				cv.UserVal = agent
			}
		}
	}

	// determine effective value and source (User > Repo > Team > Default)
	if cv.UserVal != "" {
		cv.Value = cv.UserVal
		cv.Source = ConfigLevelUser
	} else if cv.RepoVal != "" {
		cv.Value = cv.RepoVal
		cv.Source = ConfigLevelRepo
	} else if cv.TeamVal != "" {
		cv.Value = cv.TeamVal
		cv.Source = ConfigLevelTeam
	} else {
		cv.Value = cv.Default
		cv.Source = ConfigLevelDefault
	}

	return cv, nil
}

// SetConfigValue sets a config value at a specific level.
func SetConfigValue(key, value string, level ConfigLevel, projectRoot string) error {
	setting := GetSetting(key)
	if setting == nil {
		return fmt.Errorf("unknown setting: %s", key)
	}

	// validate value if setting has valid values
	if len(setting.ValidValues) > 0 {
		valid := false
		for _, v := range setting.ValidValues {
			if v == value {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid value %q for %s: valid values are %v", value, key, setting.ValidValues)
		}
	}

	// check if level is supported for this setting
	levelSupported := false
	for _, l := range setting.Levels {
		if l == level {
			levelSupported = true
			break
		}
	}
	if !levelSupported {
		return fmt.Errorf("setting %s cannot be set at %s level", key, level)
	}

	switch level {
	case ConfigLevelUser:
		return setUserConfig(key, value)
	case ConfigLevelRepo:
		return setRepoConfig(key, value, projectRoot)
	case ConfigLevelTeam:
		return setTeamConfig(key, value, projectRoot)
	default:
		return fmt.Errorf("cannot set config at %s level", level)
	}
}

func setUserConfig(key, value string) error {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		cfg = &config.UserConfig{}
	}

	switch key {
	case "session_recording":
		if cfg.Sessions == nil {
			cfg.Sessions = &config.SessionsConfig{}
		}
		cfg.Sessions.Mode = value

	case "telemetry":
		enabled := value == "on"
		cfg.TelemetryEnabled = &enabled

	case "tips":
		enabled := value == "on"
		cfg.TipsEnabled = &enabled

	case "context_git.auto_commit":
		if cfg.ContextGit == nil {
			cfg.ContextGit = &config.ContextGitConfig{}
		}
		enabled := value == "on"
		cfg.ContextGit.AutoCommit = &enabled

	case "context_git.auto_push":
		if cfg.ContextGit == nil {
			cfg.ContextGit = &config.ContextGitConfig{}
		}
		enabled := value == "on"
		cfg.ContextGit.AutoPush = &enabled

	case "view_format":
		cfg.ViewFormat = value

	case "murmur_send":
		cfg.SetMurmuring(value)

	case "murmur_receive":
		cfg.SetMurmurReceive(value)

	case "agent_worker":
		if value == "auto" {
			cfg.SetAgentWorkerAgent("") // empty = auto-detect
		} else {
			cfg.SetAgentWorkerAgent(value)
		}

	default:
		return fmt.Errorf("unknown user setting: %s", key)
	}

	return config.SaveUserConfig(cfg)
}

func setRepoConfig(key, value, projectRoot string) error {
	if projectRoot == "" {
		return fmt.Errorf("not in a SageOx project")
	}

	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load project config: %w", err)
	}

	switch key {
	case "session_recording":
		cfg.SessionRecording = value

	case "murmur_send":
		cfg.SetMurmuring(value)

	case "murmur_receive":
		cfg.MurmurReceive = value

	default:
		return fmt.Errorf("setting %s not supported at repo level", key)
	}

	return config.SaveProjectConfig(projectRoot, cfg)
}

func setTeamConfig(key, value, projectRoot string) error {
	if projectRoot == "" {
		return fmt.Errorf("not in a SageOx project")
	}

	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return fmt.Errorf("no team context configured")
	}

	teamPath := tc.Path
	cfg, err := config.LoadTeamConfig(teamPath)
	if err != nil {
		cfg = &config.TeamConfig{}
	}

	switch key {
	case "session_recording":
		cfg.SessionRecording = value

	default:
		return fmt.Errorf("setting %s not supported at team level", key)
	}

	return config.SaveTeamConfig(teamPath, cfg)
}
