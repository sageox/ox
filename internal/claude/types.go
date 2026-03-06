// Package claude provides utilities for discovering and parsing team Claude customizations.
// It handles discovery of agents and commands from team context directories.
package claude

// Agent represents a Claude Code subagent definition from a team context.
type Agent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Model       string `json:"model,omitempty"` // "inherit", "opus", "sonnet", "haiku"
	Path        string `json:"path"`            // absolute file path
	FromIndex   bool   `json:"from_index"`      // true if description came from index.md
}

// Command represents a slash command defined in a team context.
type Command struct {
	Name        string `json:"name"`        // command name (without /)
	Trigger     string `json:"trigger"`     // how to invoke (e.g., "/deploy")
	Description string `json:"description"` // what it does
	Path        string `json:"path"`        // absolute file path
	FromIndex   bool   `json:"from_index"`  // true if description came from index.md
}

// TeamCustomizations holds all coworker customizations discovered from a team context.
type TeamCustomizations struct {
	TeamPath string `json:"team_path"` // base path of team context

	// Instruction files (CLAUDE.md, AGENTS.md) in coworkers/
	ClaudeMDPath string `json:"claude_md_path,omitempty"` // coworkers/CLAUDE.md
	AgentsMDPath string `json:"agents_md_path,omitempty"` // coworkers/AGENTS.md
	HasClaudeMD  bool   `json:"has_claude_md"`
	HasAgentsMD  bool   `json:"has_agents_md"`

	// Agents-level AGENTS.md (coworkers/agents/AGENTS.md)
	// Content is read (first MaxAgentsMDLines lines) and inlined into prime output.
	AgentsAgentsMDPath    string `json:"agents_agents_md_path,omitempty"` // coworkers/agents/AGENTS.md
	AgentsAgentsMDContent string `json:"agents_agents_md_content,omitempty"`
	HasAgentsAgentsMD     bool   `json:"has_agents_agents_md,omitempty"`

	// Agents index (coworkers/agents/index.md)
	HasAgentsIndex  bool   `json:"has_agents_index,omitempty"`
	AgentsIndexPath string `json:"agents_index_path,omitempty"` // path to index.md if exists

	// Discovered items
	Agents   []Agent   `json:"agents,omitempty"`
	Commands []Command `json:"commands,omitempty"`
}

// HasInstructionFiles returns true if either CLAUDE.md or AGENTS.md exists.
func (tc *TeamCustomizations) HasInstructionFiles() bool {
	return tc.HasClaudeMD || tc.HasAgentsMD
}

// HasAnyCustomizations returns true if any customizations were discovered.
func (tc *TeamCustomizations) HasAnyCustomizations() bool {
	return tc.HasClaudeMD || tc.HasAgentsMD || tc.HasAgentsAgentsMD || len(tc.Agents) > 0 || len(tc.Commands) > 0
}
