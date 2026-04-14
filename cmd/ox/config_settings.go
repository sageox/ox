package main

import (
	"fmt"
	"strconv"

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
	{
		Key:         "recording_reminder",
		Description: "Periodic recording status reminders",
		LongDescription: `Controls whether AI coworkers receive periodic reminders
that their session is being recorded.

  on  - Whisper recording status (turn count, duration) periodically (default)
  off - No periodic reminders (startup banner still appears)

Useful for confirming sessions are actively being captured, especially
during long-running sessions. Reminders appear roughly once per hour
as whispers in the agent's context.`,
		Category:    "Sessions",
		ValidValues: []string{"on", "off"},
		Default:     "on",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	// NOTE: attribution.plan and attribution.session are intentionally not exposed
	// in ox config — they are always-on transparency requirements, not user preferences.
	{
		Key:         "attribution.commit",
		Description: "Git commit trailer for AI coworker attribution",
		LongDescription: `Controls the git trailer added to commits made with AI coworker assistance.

Any coding agent that touches your code and is deeply involved in the plan
for execution is as much responsible for the resulting code as the human is.
Attribution makes this shared responsibility visible in your git history.

  Set to any string to customize the commit trailer.
  Set to "" (empty) to disable commit attribution entirely.
  Unset to restore the default.

The default format is recognized by GitHub for contributor profile links.

Default: "Co-Authored-By: SageOx <ox@sageox.ai>"`,
		Category:    "Attribution",
		ValidValues: []string{}, // free-text: any string allowed, "" disables
		Default:     "Co-Authored-By: SageOx <ox@sageox.ai>",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "attribution.pr",
		Description: "PR body attribution for AI coworker contribution",
		LongDescription: `Controls the attribution line added to pull request descriptions.

When team context shapes the code in a PR — architecture decisions,
conventions, prior session learnings — attribution makes that influence
visible to reviewers. It signals that this work was informed by more
than one contributor's knowledge.

  Set to any string to customize the PR attribution.
  Set to "" (empty) to disable PR attribution entirely.
  Unset to restore the default.

Default: "Co-Authored-By: [SageOx](https://github.com/SageOx)"`,
		Category:    "Attribution",
		ValidValues: []string{}, // free-text: any string allowed, "" disables
		Default:     "Co-Authored-By: [SageOx](https://github.com/SageOx)",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
	},
	{
		Key:         "attribution.score_threshold",
		Description: "Minimum SageOx contribution score for commit attribution",
		LongDescription: `Controls the threshold at which SageOx earns commit attribution.

AI coworkers self-report how much SageOx team context influenced their work
on a 0.0-1.0 scale. When the score meets or exceeds this threshold, the
commit hook adds the Co-Authored-By trailer.

  Set to a value between 0.0 and 1.0.
  Set to 0.0 to always attribute (when a score is reported).
  Set to 1.0 to only attribute when SageOx was critically influential.
  Unset to restore the default.

Default: 0.5`,
		Category:    "Attribution",
		ValidValues: []string{},
		Default:     "0.5",
		Levels:      []ConfigLevel{ConfigLevelUser, ConfigLevelRepo},
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

	case "recording_reminder":
		if userCfg != nil && userCfg.RecordingReminder != "" {
			cv.UserVal = config.NormalizeRecordingReminder(userCfg.RecordingReminder)
		}
		if repoCfg != nil && repoCfg.RecordingReminder != "" {
			cv.RepoVal = config.NormalizeRecordingReminder(repoCfg.RecordingReminder)
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

	case "attribution.commit":
		if userCfg != nil && userCfg.Attribution != nil && userCfg.Attribution.IsCommitSet() {
			if v := userCfg.Attribution.GetCommit(); v == "" {
				cv.UserVal = "(disabled)"
			} else {
				cv.UserVal = v
			}
		}
		if repoCfg != nil && repoCfg.Attribution != nil && repoCfg.Attribution.IsCommitSet() {
			if v := repoCfg.Attribution.GetCommit(); v == "" {
				cv.RepoVal = "(disabled)"
			} else {
				cv.RepoVal = v
			}
		}

	case "attribution.pr":
		if userCfg != nil && userCfg.Attribution != nil && userCfg.Attribution.IsPRSet() {
			if v := userCfg.Attribution.GetPR(); v == "" {
				cv.UserVal = "(disabled)"
			} else {
				cv.UserVal = v
			}
		}
		if repoCfg != nil && repoCfg.Attribution != nil && repoCfg.Attribution.IsPRSet() {
			if v := repoCfg.Attribution.GetPR(); v == "" {
				cv.RepoVal = "(disabled)"
			} else {
				cv.RepoVal = v
			}
		}

	case "attribution.score_threshold":
		if userCfg != nil && userCfg.Attribution != nil && userCfg.Attribution.IsScoreThresholdSet() {
			cv.UserVal = strconv.FormatFloat(userCfg.Attribution.GetScoreThreshold(), 'f', -1, 64)
		}
		if repoCfg != nil && repoCfg.Attribution != nil && repoCfg.Attribution.IsScoreThresholdSet() {
			cv.RepoVal = strconv.FormatFloat(repoCfg.Attribution.GetScoreThreshold(), 'f', -1, 64)
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

// UnsetConfigValue clears a config value at a specific level, causing it to
// fall back to the next level in the precedence chain (user > repo > team > default).
func UnsetConfigValue(key string, level ConfigLevel, projectRoot string) error {
	setting := GetSetting(key)
	if setting == nil {
		return fmt.Errorf("unknown setting: %s", key)
	}

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
		return unsetUserConfig(key)
	case ConfigLevelRepo:
		return unsetRepoConfig(key, projectRoot)
	case ConfigLevelTeam:
		return unsetTeamConfig(key, projectRoot)
	default:
		return fmt.Errorf("cannot unset config at %s level", level)
	}
}

// SetConfigValue sets a config value at a specific level.
func SetConfigValue(key, value string, level ConfigLevel, projectRoot string) error {
	setting := GetSetting(key)
	if setting == nil {
		return fmt.Errorf("unknown setting: %s", key)
	}

	// validate value — score_threshold uses float validation, others use ValidValues list
	if key == "attribution.score_threshold" {
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0.0 || f > 1.0 {
			return fmt.Errorf("invalid value %q for %s: must be a number between 0.0 and 1.0", value, key)
		}
	} else if len(setting.ValidValues) > 0 {
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

	case "recording_reminder":
		cfg.RecordingReminder = value

	case "agent_worker":
		if value == "auto" {
			cfg.SetAgentWorkerAgent("") // empty = auto-detect
		} else {
			cfg.SetAgentWorkerAgent(value)
		}

	case "attribution.commit":
		if cfg.Attribution == nil {
			cfg.Attribution = &config.Attribution{}
		}
		if value == "(disabled)" || value == "" {
			cfg.Attribution.Commit = config.StringPtr("")
		} else {
			cfg.Attribution.Commit = config.StringPtr(value)
		}

	case "attribution.pr":
		if cfg.Attribution == nil {
			cfg.Attribution = &config.Attribution{}
		}
		if value == "(disabled)" || value == "" {
			cfg.Attribution.PR = config.StringPtr("")
		} else {
			cfg.Attribution.PR = config.StringPtr(value)
		}

	case "attribution.score_threshold":
		if cfg.Attribution == nil {
			cfg.Attribution = &config.Attribution{}
		}
		f, _ := strconv.ParseFloat(value, 64) // already validated
		cfg.Attribution.ScoreThreshold = config.Float64Ptr(f)

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

	case "recording_reminder":
		cfg.RecordingReminder = value

	case "attribution.commit":
		if cfg.Attribution == nil {
			cfg.Attribution = &config.Attribution{}
		}
		if value == "(disabled)" || value == "" {
			cfg.Attribution.Commit = config.StringPtr("")
		} else {
			cfg.Attribution.Commit = config.StringPtr(value)
		}

	case "attribution.pr":
		if cfg.Attribution == nil {
			cfg.Attribution = &config.Attribution{}
		}
		if value == "(disabled)" || value == "" {
			cfg.Attribution.PR = config.StringPtr("")
		} else {
			cfg.Attribution.PR = config.StringPtr(value)
		}

	case "attribution.score_threshold":
		if cfg.Attribution == nil {
			cfg.Attribution = &config.Attribution{}
		}
		f, _ := strconv.ParseFloat(value, 64) // already validated
		cfg.Attribution.ScoreThreshold = config.Float64Ptr(f)

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
		return fmt.Errorf("failed to load team config: %w", err)
	}
	if cfg == nil {
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

func unsetUserConfig(key string) error {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		cfg = &config.UserConfig{}
	}

	switch key {
	case "session_recording":
		if cfg.Sessions != nil {
			cfg.Sessions.Mode = ""
			// nil the struct if nothing else is set
			if cfg.Sessions.GetMode() == "" {
				cfg.Sessions = nil
			}
		}

	case "telemetry":
		cfg.TelemetryEnabled = nil

	case "tips":
		cfg.TipsEnabled = nil

	case "context_git.auto_commit":
		if cfg.ContextGit != nil {
			cfg.ContextGit.AutoCommit = nil
			if cfg.ContextGit.AutoPush == nil {
				cfg.ContextGit = nil
			}
		}

	case "context_git.auto_push":
		if cfg.ContextGit != nil {
			cfg.ContextGit.AutoPush = nil
			if cfg.ContextGit.AutoCommit == nil {
				cfg.ContextGit = nil
			}
		}

	case "view_format":
		cfg.ViewFormat = ""

	case "murmur_send":
		cfg.SetMurmuring("")

	case "murmur_receive":
		cfg.SetMurmurReceive("")

	case "recording_reminder":
		cfg.RecordingReminder = ""

	case "agent_worker":
		cfg.AgentWorker = nil

	case "attribution.commit":
		if cfg.Attribution != nil {
			cfg.Attribution.Commit = nil
			if !cfg.Attribution.IsPlanSet() && !cfg.Attribution.IsPRSet() && !cfg.Attribution.IsSessionSet() && !cfg.Attribution.IsScoreThresholdSet() {
				cfg.Attribution = nil
			}
		}

	case "attribution.pr":
		if cfg.Attribution != nil {
			cfg.Attribution.PR = nil
			if !cfg.Attribution.IsPlanSet() && !cfg.Attribution.IsCommitSet() && !cfg.Attribution.IsSessionSet() && !cfg.Attribution.IsScoreThresholdSet() {
				cfg.Attribution = nil
			}
		}

	case "attribution.score_threshold":
		if cfg.Attribution != nil {
			cfg.Attribution.ScoreThreshold = nil
			if !cfg.Attribution.IsPlanSet() && !cfg.Attribution.IsCommitSet() && !cfg.Attribution.IsPRSet() && !cfg.Attribution.IsSessionSet() {
				cfg.Attribution = nil
			}
		}

	default:
		return fmt.Errorf("unknown user setting: %s", key)
	}

	return config.SaveUserConfig(cfg)
}

func unsetRepoConfig(key, projectRoot string) error {
	if projectRoot == "" {
		return fmt.Errorf("not in a SageOx project")
	}

	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load project config: %w", err)
	}

	switch key {
	case "session_recording":
		cfg.SessionRecording = ""

	case "murmur_send":
		cfg.SetMurmuring("")

	case "murmur_receive":
		cfg.MurmurReceive = ""

	case "recording_reminder":
		cfg.RecordingReminder = ""

	case "attribution.commit":
		if cfg.Attribution != nil {
			cfg.Attribution.Commit = nil
			if !cfg.Attribution.IsPlanSet() && !cfg.Attribution.IsPRSet() && !cfg.Attribution.IsSessionSet() && !cfg.Attribution.IsScoreThresholdSet() {
				cfg.Attribution = nil
			}
		}

	case "attribution.pr":
		if cfg.Attribution != nil {
			cfg.Attribution.PR = nil
			if !cfg.Attribution.IsPlanSet() && !cfg.Attribution.IsCommitSet() && !cfg.Attribution.IsSessionSet() && !cfg.Attribution.IsScoreThresholdSet() {
				cfg.Attribution = nil
			}
		}

	case "attribution.score_threshold":
		if cfg.Attribution != nil {
			cfg.Attribution.ScoreThreshold = nil
			if !cfg.Attribution.IsPlanSet() && !cfg.Attribution.IsCommitSet() && !cfg.Attribution.IsPRSet() && !cfg.Attribution.IsSessionSet() {
				cfg.Attribution = nil
			}
		}

	default:
		return fmt.Errorf("setting %s not supported at repo level", key)
	}

	return config.SaveProjectConfig(projectRoot, cfg)
}

func unsetTeamConfig(key, projectRoot string) error {
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
		cfg.SessionRecording = ""

	default:
		return fmt.Errorf("setting %s not supported at team level", key)
	}

	return config.SaveTeamConfig(teamPath, cfg)
}
