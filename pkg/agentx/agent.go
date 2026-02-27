package agentx

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"strings"
)

// AgentRole distinguishes agents from orchestrators.
// Agents are coding tools (Claude Code, Cursor); orchestrators launch and
// manage agents (OpenClaw, Conductor, Gas Town).
type AgentRole string

const (
	// RoleAgent is a coding agent that directly invokes ox commands.
	RoleAgent AgentRole = "agent"

	// RoleOrchestrator launches and manages coding agents.
	RoleOrchestrator AgentRole = "orchestrator"
)

// AgentType identifies a coding agent or orchestrator.
type AgentType string

const (
	AgentTypeUnknown    AgentType = ""
	AgentTypeClaudeCode AgentType = "claude"
	AgentTypeCursor     AgentType = "cursor"
	AgentTypeWindsurf   AgentType = "windsurf"
	AgentTypeCopilot    AgentType = "copilot"
	AgentTypeAider      AgentType = "aider"
	AgentTypeCody       AgentType = "cody"
	AgentTypeContinue   AgentType = "continue"
	AgentTypeCodePuppy  AgentType = "code-puppy"
	AgentTypeKiro       AgentType = "kiro"
	AgentTypeOpenCode   AgentType = "opencode"
	AgentTypeGoose      AgentType = "goose"
	AgentTypeAmp        AgentType = "amp"
	AgentTypeCline      AgentType = "cline"
	AgentTypeDroid      AgentType = "droid"
	AgentTypeCustom     AgentType = "custom"

	// Orchestrators
	AgentTypeOpenClaw  AgentType = "openclaw"
	AgentTypeConductor AgentType = "conductor"
)

// SupportedAgents is the canonical list of coding agents and orchestrators
// that agentx supports.
var SupportedAgents = []AgentType{
	// Agents
	AgentTypeClaudeCode,
	AgentTypeCursor,
	AgentTypeWindsurf,
	AgentTypeCopilot,
	AgentTypeAider,
	AgentTypeCody,
	AgentTypeContinue,
	AgentTypeCodePuppy,
	AgentTypeKiro,
	AgentTypeOpenCode,
	AgentTypeGoose,
	AgentTypeAmp,
	AgentTypeCline,
	AgentTypeDroid,

	// Orchestrators
	AgentTypeOpenClaw,
	AgentTypeConductor,
}

// AgentIdentity provides basic agent identification.
// Use this interface when you only need to identify an agent without
// needing detection or configuration capabilities.
type AgentIdentity interface {
	// Type returns the agent type slug
	Type() AgentType

	// Name returns the human-readable agent name
	Name() string

	// URL returns the official project URL (typically GitHub repo)
	URL() string

	// Role returns whether this is a coding agent or an orchestrator
	Role() AgentRole
}

// AgentDetector provides agent detection capabilities.
// Use this interface when you need to check if an agent is running or installed.
type AgentDetector interface {
	// Detect checks if this agent is currently running
	Detect(ctx context.Context, env Environment) (bool, error)

	// IsInstalled checks if this agent is installed on the system.
	// Detection methods vary by agent: binary in PATH, config directory exists,
	// application bundle present (macOS), etc.
	IsInstalled(ctx context.Context, env Environment) (bool, error)

	// DetectVersion attempts to determine the installed version of this agent.
	// Returns empty string if version cannot be determined (best-effort).
	// Strategies vary by agent: CLI --version, reading package.json, etc.
	DetectVersion(ctx context.Context, env Environment) string
}

// AgentConfig provides configuration path information.
// Use this interface when you need to locate agent configuration files.
type AgentConfig interface {
	// UserConfigPath returns the user-level configuration directory (e.g., ~/.claude)
	UserConfigPath(env Environment) (string, error)

	// ProjectConfigPath returns the project-level configuration directory (e.g., .claude/)
	// Returns empty string if the agent doesn't support project-level config
	ProjectConfigPath() string

	// ContextFiles returns the list of context/instruction files this agent supports
	// Examples: CLAUDE.md, AGENTS.md, .cursorrules, .windsurfrules
	ContextFiles() []string

	// SupportsXDGConfig returns true if the agent stores user config in XDG-compliant
	// locations (~/.config/app/) rather than home directory dotfiles (~/.app/).
	// XDG Base Directory Specification is the preferred standard for config locations.
	// See: https://specifications.freedesktop.org/basedir-spec/latest/
	SupportsXDGConfig() bool
}

// AgentExtensions provides hook and command management capabilities.
// Use this interface when you need to work with agent extensions.
type AgentExtensions interface {
	// Capabilities returns what features this agent supports
	Capabilities() Capabilities

	// HookManager returns the hook manager for this agent, or nil if hooks not supported
	HookManager() HookManager

	// CommandManager returns the command manager for this agent, or nil if custom commands not supported
	CommandManager() CommandManager
}

// LifecycleEventMapper is implemented by agents that support lifecycle hooks.
// It maps the agent's native hook events to canonical lifecycle phases,
// enabling ox to handle events generically across different coding agents.
type LifecycleEventMapper interface {
	// EventPhases returns the mapping from this agent's native events to canonical phases.
	EventPhases() EventPhaseMap

	// AgentENVAliases returns the AGENT_ENV values that identify this agent in hook commands.
	// The first value is the canonical/preferred alias used in hook installation.
	// Example: Claude Code returns ["claude-code", "claudecode", "claude"].
	AgentENVAliases() []string
}

// Agent represents a coding agent with full detection and configuration capabilities.
// It embeds all focused interfaces for complete agent functionality.
//
// When defining functions that work with agents, prefer using the narrowest
// interface that meets your needs:
//   - AgentIdentity: when you only need type, name, or URL
//   - AgentDetector: when you need to check if agent is running/installed
//   - AgentConfig: when you need configuration paths
//   - AgentExtensions: when you need hooks
//   - Agent: when you need full functionality or storage/registry
type Agent interface {
	AgentIdentity
	AgentDetector
	AgentConfig
	AgentExtensions
}

// Capabilities describes what features a coding agent supports.
// This allows tools to adapt their behavior based on agent capabilities.
type Capabilities struct {
	// Hooks indicates the agent supports hook installation
	Hooks bool

	// MCPServers indicates the agent supports MCP server configuration
	MCPServers bool

	// SystemPrompt indicates the agent supports custom system instructions
	SystemPrompt bool

	// ProjectContext indicates the agent reads project context files (CLAUDE.md, etc.)
	ProjectContext bool

	// CustomCommands indicates the agent supports custom slash commands
	CustomCommands bool

	// MinVersion is the minimum agent version required for full feature support (future)
	MinVersion string
}

// Detector identifies which coding agent(s) and orchestrator(s) are currently active.
type Detector interface {
	// Detect identifies the active coding agent (RoleAgent), returning nil if none detected.
	// Orchestrators are excluded — use DetectOrchestrator for those.
	Detect(ctx context.Context) (Agent, error)

	// DetectOrchestrator returns the active orchestrator (RoleOrchestrator), or nil.
	// This is independent of Detect() which returns the coding agent.
	DetectOrchestrator(ctx context.Context) (Agent, error)

	// DetectAll returns all detected agents and orchestrators
	DetectAll(ctx context.Context) ([]Agent, error)

	// DetectByType checks if a specific agent type is active
	DetectByType(ctx context.Context, agentType AgentType) (bool, error)
}

// HookManager handles hook installation and management for an agent.
type HookManager interface {
	// Install installs hooks for the agent
	Install(ctx context.Context, config HookConfig) error

	// Uninstall removes installed hooks
	Uninstall(ctx context.Context) error

	// IsInstalled checks if hooks are installed
	IsInstalled(ctx context.Context) (bool, error)

	// Validate validates the current hook configuration
	Validate(ctx context.Context) error
}

// HookConfig represents hook configuration for installation.
type HookConfig struct {
	// SourcePath is the path to hook source files (templates, commands)
	SourcePath string

	// MCPServers are MCP server configurations to add
	MCPServers map[string]MCPServerConfig

	// SystemInstructions are custom instructions for the agent
	SystemInstructions string

	// EventHooks are lifecycle event hooks to install (PreToolUse, PostToolUse, etc.)
	// See HookEvent constants for available events.
	EventHooks EventHooks

	// Merge indicates whether to merge with existing config (true) or replace (false)
	Merge bool
}

// MCPServerConfig represents an MCP server configuration.
type MCPServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Phase represents a canonical agent lifecycle moment.
// Phases are agent-agnostic — each agent maps its native events to these phases.
type Phase string

const (
	PhaseStart      Phase = "start"       // session began
	PhaseEnd        Phase = "end"         // session ending
	PhaseBeforeTool Phase = "before_tool" // tool about to execute
	PhaseAfterTool  Phase = "after_tool"  // tool just completed
	PhasePrompt     Phase = "prompt"      // user submitted a prompt
	PhaseStop       Phase = "stop"        // agent finished responding
	PhaseCompact    Phase = "compact"     // context about to be compacted
)

// HookEvent represents a coding agent lifecycle event that can trigger hooks.
// Each agent has its own native event names (e.g., Claude's "SessionStart" vs
// Cursor's "sessionStart" vs Windsurf's "pre_read_code"). Use the agent-specific
// constants below, or the canonical constants for events shared across agents.
type HookEvent string

// Canonical hook events shared across multiple agents.
// Agents may use these exact strings or their own native names.
const (
	HookEventSessionStart      HookEvent = "SessionStart"
	HookEventSessionEnd        HookEvent = "SessionEnd"
	HookEventPreToolUse        HookEvent = "PreToolUse"
	HookEventPostToolUse       HookEvent = "PostToolUse"
	HookEventUserPromptSubmit  HookEvent = "UserPromptSubmit"
	HookEventPermissionRequest HookEvent = "PermissionRequest"
	HookEventStop              HookEvent = "Stop"
	HookEventSubagentStop      HookEvent = "SubagentStop"
	HookEventPreCompact        HookEvent = "PreCompact"
)

// Cursor-specific hook events.
// Reference: https://cursor.com/docs/agent/hooks
const (
	CursorEventSessionStart         HookEvent = "sessionStart"
	CursorEventSessionEnd           HookEvent = "sessionEnd"
	CursorEventPreToolUse           HookEvent = "preToolUse"
	CursorEventPostToolUse          HookEvent = "postToolUse"
	CursorEventPostToolUseFailure   HookEvent = "postToolUseFailure"
	CursorEventSubagentStart        HookEvent = "subagentStart"
	CursorEventSubagentStop         HookEvent = "subagentStop"
	CursorEventBeforeShellExecution HookEvent = "beforeShellExecution"
	CursorEventAfterShellExecution  HookEvent = "afterShellExecution"
	CursorEventBeforeMCPExecution   HookEvent = "beforeMCPExecution"
	CursorEventAfterMCPExecution    HookEvent = "afterMCPExecution"
	CursorEventBeforeReadFile       HookEvent = "beforeReadFile"
	CursorEventAfterFileEdit        HookEvent = "afterFileEdit"
	CursorEventBeforeSubmitPrompt   HookEvent = "beforeSubmitPrompt"
	CursorEventPreCompact           HookEvent = "preCompact"
	CursorEventStop                 HookEvent = "stop"
	CursorEventAfterAgentResponse   HookEvent = "afterAgentResponse"
	CursorEventAfterAgentThought    HookEvent = "afterAgentThought"
)

// Windsurf-specific hook events.
// Reference: https://docs.windsurf.com/windsurf/cascade/hooks
const (
	WindsurfEventPreReadCode                       HookEvent = "pre_read_code"
	WindsurfEventPostReadCode                      HookEvent = "post_read_code"
	WindsurfEventPreWriteCode                      HookEvent = "pre_write_code"
	WindsurfEventPostWriteCode                     HookEvent = "post_write_code"
	WindsurfEventPreRunCommand                     HookEvent = "pre_run_command"
	WindsurfEventPostRunCommand                    HookEvent = "post_run_command"
	WindsurfEventPreMCPToolUse                     HookEvent = "pre_mcp_tool_use"
	WindsurfEventPostMCPToolUse                    HookEvent = "post_mcp_tool_use"
	WindsurfEventPreUserPrompt                     HookEvent = "pre_user_prompt"
	WindsurfEventPostCascadeResponse               HookEvent = "post_cascade_response"
	WindsurfEventPostCascadeResponseWithTranscript HookEvent = "post_cascade_response_with_transcript"
	WindsurfEventPostSetupWorktree                 HookEvent = "post_setup_worktree"
)

// Kiro-specific hook events.
// Reference: https://kiro.dev/docs/hooks/types/
const (
	KiroEventPromptSubmit      HookEvent = "PromptSubmit"
	KiroEventAgentStop         HookEvent = "AgentStop"
	KiroEventPreToolUse        HookEvent = "PreToolUse"  // same as canonical
	KiroEventPostToolUse       HookEvent = "PostToolUse" // same as canonical
	KiroEventFileCreate        HookEvent = "FileCreate"
	KiroEventFileSave          HookEvent = "FileSave"
	KiroEventFileDelete        HookEvent = "FileDelete"
	KiroEventPreTaskExecution  HookEvent = "PreTaskExecution"
	KiroEventPostTaskExecution HookEvent = "PostTaskExecution"
	KiroEventManualTrigger     HookEvent = "ManualTrigger"
)

// Droid-specific hook events (Factory).
// Reference: https://docs.factory.ai/reference/hooks-reference
const (
	DroidEventNotification HookEvent = "Notification"
	// Droid also supports: SessionStart, SessionEnd, PreToolUse, PostToolUse,
	// UserPromptSubmit, Stop, SubagentStop, PreCompact (same as canonical).
)

// Cline-specific hook events.
// Reference: https://docs.cline.bot/features/hooks
const (
	ClineEventTaskStart        HookEvent = "TaskStart"
	ClineEventTaskResume       HookEvent = "TaskResume"
	ClineEventTaskCancel       HookEvent = "TaskCancel"
	ClineEventTaskComplete     HookEvent = "TaskComplete"
	ClineEventPreToolUse       HookEvent = "PreToolUse"  // same as canonical
	ClineEventPostToolUse      HookEvent = "PostToolUse" // same as canonical
	ClineEventUserPromptSubmit HookEvent = "UserPromptSubmit"
	ClineEventPreCompact       HookEvent = "PreCompact"
)

// GitHub Copilot hook events.
// Reference: https://docs.github.com/en/copilot/reference/hooks-configuration
const (
	CopilotEventSessionStart        HookEvent = "sessionStart"
	CopilotEventSessionEnd          HookEvent = "sessionEnd"
	CopilotEventUserPromptSubmitted HookEvent = "userPromptSubmitted"
	CopilotEventPreToolUse          HookEvent = "preToolUse"
	CopilotEventPostToolUse         HookEvent = "postToolUse"
	CopilotEventErrorOccurred       HookEvent = "errorOccurred"
)

// Amp-specific hook events.
// Reference: https://ampcode.com/news/hooks
const (
	AmpEventToolPreExecute  HookEvent = "tool:pre-execute"
	AmpEventToolPostExecute HookEvent = "tool:post-execute"
)

// OpenCode-specific hook events (plugin system).
// Reference: https://opencode.ai/docs/plugins/
const (
	OpenCodeEventSessionCreated    HookEvent = "session.created"
	OpenCodeEventSessionCompacted  HookEvent = "session.compacted"
	OpenCodeEventSessionDeleted    HookEvent = "session.deleted"
	OpenCodeEventSessionError      HookEvent = "session.error"
	OpenCodeEventSessionIdle       HookEvent = "session.idle"
	OpenCodeEventSessionStatus     HookEvent = "session.status"
	OpenCodeEventSessionUpdated    HookEvent = "session.updated"
	OpenCodeEventToolExecuteBefore HookEvent = "tool.execute.before"
	OpenCodeEventToolExecuteAfter  HookEvent = "tool.execute.after"
	OpenCodeEventFileEdited        HookEvent = "file.edited"
	OpenCodeEventPermissionAsked   HookEvent = "permission.asked"
	OpenCodeEventPermissionReplied HookEvent = "permission.replied"
	OpenCodeEventMessageUpdated    HookEvent = "message.updated"
	OpenCodeEventCommandExecuted   HookEvent = "command.executed"
)

// EventPhaseMap maps an agent's native hook events to canonical lifecycle phases.
type EventPhaseMap map[HookEvent]Phase

// AllPhases is the ordered list of canonical lifecycle phases for matrix display.
var AllPhases = []Phase{
	PhaseStart, PhaseEnd, PhaseBeforeTool, PhaseAfterTool,
	PhasePrompt, PhaseStop, PhaseCompact,
}

// HookRule defines when and how a hook triggers.
// Rules use matchers to filter which tools activate the hook.
type HookRule struct {
	// Matcher is a tool name pattern (e.g., "Bash", "Edit|Write", "*" for all).
	// Only applies to PreToolUse, PostToolUse, and PermissionRequest events.
	Matcher string `json:"matcher,omitempty"`

	// Hooks are the actions to execute when this rule matches.
	Hooks []HookAction `json:"hooks"`
}

// HookAction defines what happens when a hook triggers.
type HookAction struct {
	// Type is the action type. Currently only "command" is supported.
	Type string `json:"type"`

	// Command is the shell command to execute. Receives tool context via stdin as JSON.
	// Exit code 2 from PreToolUse/PermissionRequest hooks denies the action.
	Command string `json:"command"`
}

// EventHooks maps lifecycle events to their hook rules.
// This is the structure used in settings.json under the "hooks" key.
type EventHooks map[HookEvent][]HookRule

// CommandHashPrefix is the marker used to stamp command files with a content hash
// and CLI version. Format: <!-- ox-hash: <hash> ver: <version> -->
// Files are only rewritten when content changes AND the writing version is >= installed.
const CommandHashPrefix = "<!-- ox-hash: "

// CommandFile represents a custom slash command to install.
type CommandFile struct {
	// Name is the filename (e.g., "ox-status.md")
	Name string

	// Content is the file content (without stamp; stamp is added on write)
	Content []byte

	// Version is the ox version that ships this command.
	// Used as a downgrade guard: an older binary won't overwrite commands
	// installed by a newer binary.
	Version string
}

// CommandManager handles custom slash command installation for agents.
type CommandManager interface {
	// Install writes command files to the agent's command directory.
	// When overwrite is false, existing files are skipped entirely (safe for init).
	// When overwrite is true, existing files are replaced only if content differs (safe for doctor).
	// Returns the list of filenames that were written.
	Install(ctx context.Context, projectRoot string, commands []CommandFile, overwrite bool) ([]string, error)

	// Uninstall removes command files matching the prefix from the command directory.
	// Returns the list of filenames that were removed.
	Uninstall(ctx context.Context, projectRoot string, prefix string) ([]string, error)

	// Validate checks which expected files are missing or stale in the command directory.
	// Returns missing filenames and stale filenames (content differs from expected).
	Validate(ctx context.Context, projectRoot string, commands []CommandFile) (missing []string, stale []string, err error)

	// CommandDir returns the path to the command directory for a project.
	CommandDir(projectRoot string) string
}

// ReadCommandFiles reads .md files from an fs.FS directory into CommandFile slices.
// Useful for converting go:embed filesystems into CommandFile arrays for installation.
func ReadCommandFiles(fsys fs.FS, dir string) ([]CommandFile, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read command directory %s: %w", dir, err)
	}

	var commands []CommandFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		content, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, fmt.Errorf("read command file %s: %w", name, err)
		}
		commands = append(commands, CommandFile{
			Name:    name,
			Content: content,
		})
	}
	return commands, nil
}

// ContentHash returns the first 12 characters of the SHA-256 hex digest of content.
func ContentHash(content []byte) string {
	h := sha256.Sum256(content)
	return fmt.Sprintf("%x", h)[:12]
}

// StampedContent returns the command content with a hash+version stamp prepended.
// Format: <!-- ox-hash: <12-char-hash> ver: <version> -->
func StampedContent(content []byte, version string) []byte {
	hash := ContentHash(content)
	stamp := fmt.Sprintf("%s%s ver: %s -->\n", CommandHashPrefix, hash, version)
	return append([]byte(stamp), content...)
}

// ExtractCommandHash extracts the content hash from a stamped command file.
// Returns empty string if no hash stamp is found.
func ExtractCommandHash(content []byte) string {
	line := firstLine(content)
	if !strings.HasPrefix(line, CommandHashPrefix) {
		return ""
	}
	rest := strings.TrimPrefix(line, CommandHashPrefix)
	// hash is the first 12 hex characters
	if len(rest) < 12 {
		return ""
	}
	return rest[:12]
}

// ExtractStampVersion extracts the CLI version from a stamped command file.
// Returns empty string if no version is found in the stamp.
func ExtractStampVersion(content []byte) string {
	line := firstLine(content)
	if !strings.HasPrefix(line, CommandHashPrefix) {
		return ""
	}
	const marker = " ver: "
	idx := strings.Index(line, marker)
	if idx < 0 {
		return ""
	}
	rest := line[idx+len(marker):]
	rest = strings.TrimSuffix(rest, " -->")
	return strings.TrimSpace(rest)
}

// CompareVersions returns true if version a is strictly older than version b.
// Uses simple semver comparison (major.minor.patch).
func CompareVersions(a, b string) bool {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")

	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}

	for i := 0; i < maxLen; i++ {
		var numA, numB int
		if i < len(partsA) {
			fmt.Sscanf(partsA[i], "%d", &numA)
		}
		if i < len(partsB) {
			fmt.Sscanf(partsB[i], "%d", &numB)
		}
		if numA < numB {
			return true
		}
		if numA > numB {
			return false
		}
	}
	return false
}

// ShouldWriteCommand determines whether a command file should be written.
// existing is the current file content on disk (nil if the file doesn't exist).
// Returns true if the file should be written, false if it should be skipped.
//
// Decision logic:
//   - File doesn't exist → write
//   - File exists + overwrite=false → skip
//   - File exists + no hash stamp → skip (user-managed)
//   - Hash matches → skip (content identical)
//   - Hash differs + installed version is newer → skip (downgrade guard)
//   - Otherwise → write
func ShouldWriteCommand(existing []byte, cmd CommandFile, overwrite bool) bool {
	if existing == nil {
		return true
	}
	if !overwrite {
		return false
	}
	installedHash := ExtractCommandHash(existing)
	if installedHash == "" {
		return false
	}
	if installedHash == ContentHash(cmd.Content) {
		return false
	}
	installedVer := ExtractStampVersion(existing)
	if installedVer != "" && cmd.Version != "" && CompareVersions(cmd.Version, installedVer) {
		return false
	}
	return true
}

// IsCommandStale determines whether an installed command file is outdated
// compared to expected content.
// Returns false for user-managed files (no stamp) and files installed by a
// newer version (downgrade guard).
func IsCommandStale(existing []byte, cmd CommandFile) bool {
	installedHash := ExtractCommandHash(existing)
	if installedHash == "" {
		return false
	}
	if installedHash == ContentHash(cmd.Content) {
		return false
	}
	installedVer := ExtractStampVersion(existing)
	if installedVer != "" && cmd.Version != "" && CompareVersions(cmd.Version, installedVer) {
		return false
	}
	return true
}

// firstLine returns the first line of content (without newline).
func firstLine(content []byte) string {
	s := string(content)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

// Registry manages available agents and provides detection.
type Registry interface {
	// Register adds an agent to the registry
	Register(agent Agent) error

	// Get retrieves an agent by type
	Get(agentType AgentType) (Agent, bool)

	// List returns all registered agents
	List() []Agent

	// Detector returns a detector for registered agents
	Detector() Detector
}
