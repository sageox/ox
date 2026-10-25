package claude

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/constants"
)

// CoworkersDir is the base path for coworker customizations within a team context.
const CoworkersDir = "coworkers"

// AgentsDir is the LEGACY path for coworker profiles within a team context. It
// remains the write location (`ox coworker add`) so a team's profiles are never
// split across two directories mid-migration; reads accept both roots.
const AgentsDir = "coworkers/agents"

// CommandsDir is the LEGACY path for slash command definitions within a team context.
const CommandsDir = "coworkers/commands"

// ProfileDirs and CommandDirs are the read roots, canonical location first.
//
// `ox guide team-context`, the installed use-team-context rule, and both
// adapters' rule text have always advertised agents/profiles/ and
// agents/commands/ as canonical, naming coworkers/ the "legacy location ...
// still read for backward compat". Only agents/rules/ was ever wired up, so a
// profile or command authored exactly where the docs said to put it was
// invisible to every AI coworker. These mirror teamdocs.DiscoverRules: walk
// canonical first, then legacy, and dedupe by name so canonical wins.
var (
	ProfileDirs = []string{"agents/profiles", AgentsDir}
	CommandDirs = []string{"agents/commands", CommandsDir}
)

// dedupeByName keeps the first entry seen for each name. Callers walk the
// canonical root first, so the canonical copy is the one that survives.
func dedupeByName[T any](items []T, name func(T) string) []T {
	seen := make(map[string]bool, len(items))
	out := items[:0]
	for _, item := range items {
		key := name(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

// findInProfileDirs returns the first existing <teamPath>/<root>/<file> across
// ProfileDirs, canonical root first.
func findInProfileDirs(teamPath, file string) (string, bool) {
	for _, root := range ProfileDirs {
		candidate := filepath.Join(teamPath, root, file)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, true
		}
	}
	return "", false
}

// DiscoverAll finds all Claude customizations in a team context path.
// This is the main entry point for discovering team Claude customizations.
func DiscoverAll(teamPath string) (*TeamCustomizations, error) {
	if teamPath == "" {
		return nil, nil
	}

	tc := &TeamCustomizations{
		TeamPath: teamPath,
	}

	base := filepath.Join(teamPath, CoworkersDir)

	// check for instruction files
	claudeMD := filepath.Join(base, "CLAUDE.md")
	if _, err := os.Stat(claudeMD); err == nil {
		tc.ClaudeMDPath = claudeMD
		tc.HasClaudeMD = true
	}

	agentsMD := filepath.Join(base, "AGENTS.md")
	if _, err := os.Stat(agentsMD); err == nil {
		tc.AgentsMDPath = agentsMD
		tc.HasAgentsMD = true
	}

	// discover agents
	agents, _ := DiscoverAgents(teamPath)
	tc.Agents = agents

	// profile-level AGENTS.md, canonical root first
	if agentsAgentsMD, ok := findInProfileDirs(teamPath, "AGENTS.md"); ok {
		tc.HasAgentsAgentsMD = true
		tc.AgentsAgentsMDPath = agentsAgentsMD
		tc.AgentsAgentsMDContent = ReadFirstLines(agentsAgentsMD, constants.MaxInlineContextLines)
	}

	// profile index.md (catalog of available specialists), canonical root first
	if agentsIndexPath, ok := findInProfileDirs(teamPath, "index.md"); ok {
		tc.HasAgentsIndex = true
		tc.AgentsIndexPath = agentsIndexPath
	}

	// discover commands
	commands, _ := DiscoverTeamCommands(teamPath)
	tc.Commands = commands

	return tc, nil
}

// DiscoverAgents finds all coworker profiles in a team context, reading the
// canonical agents/profiles/ root and the legacy coworkers/agents/ root.
// Checks index.md first (token-optimized), falls back to individual files.
func DiscoverAgents(teamPath string) ([]Agent, error) {
	var agents []Agent
	for _, root := range ProfileDirs {
		discovered, err := discoverAgentsIn(filepath.Join(teamPath, root))
		if err != nil {
			return nil, err
		}
		agents = append(agents, discovered...)
	}
	return dedupeByName(agents, func(a Agent) string { return a.Name }), nil
}

func discoverAgentsIn(agentsDir string) ([]Agent, error) {

	// check if agents directory exists
	if _, err := os.Stat(agentsDir); os.IsNotExist(err) {
		return nil, nil
	}

	// try to load index.md for optimized descriptions
	indexDescriptions := ParseIndex(filepath.Join(agentsDir, "index.md"))

	// scan for .md files
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil, err
	}

	var agents []Agent
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if entry.Name() == "index.md" {
			continue // skip index itself
		}
		// skip non-agent files (AGENTS.md, README.md, etc.)
		if entry.Name()[0] >= 'A' && entry.Name()[0] <= 'Z' {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".md")
		agentPath := filepath.Join(agentsDir, entry.Name())

		agent := Agent{
			Name: name,
			Path: agentPath,
		}

		// prefer index.md description (token-optimized)
		if desc, ok := indexDescriptions[name]; ok {
			agent.Description = desc
			agent.FromIndex = true
		} else {
			// fallback to parsing file frontmatter
			agent.Description, agent.Model = parseAgentFrontmatter(agentPath)
		}

		agents = append(agents, agent)
	}

	return agents, nil
}

// parseAgentFrontmatter extracts description and model from agent file frontmatter.
// Returns empty strings if frontmatter is not found or invalid.
func parseAgentFrontmatter(path string) (description, model string) {
	file, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	inFrontmatter := false
	lineCount := 0

	for scanner.Scan() {
		lineCount++
		line := scanner.Text()

		// frontmatter starts with ---
		if lineCount == 1 && line == "---" {
			inFrontmatter = true
			continue
		}

		// frontmatter ends with ---
		if inFrontmatter && line == "---" {
			break
		}

		if inFrontmatter {
			if strings.HasPrefix(line, "description:") {
				description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
				description = strings.Trim(description, `"'`)
			}
			if strings.HasPrefix(line, "model:") {
				model = strings.TrimSpace(strings.TrimPrefix(line, "model:"))
				model = strings.Trim(model, `"'`)
			}
		}

		// stop after 20 lines to avoid scanning entire file
		if lineCount > 20 {
			break
		}
	}

	return description, model
}

// AgentContent holds the full content and metadata of an agent file.
type AgentContent struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
	Path        string `json:"path"`
	Content     string `json:"content"` // full markdown content
}

// LoadAgent loads the full content of a coworker profile by name, searching the
// same roots DiscoverAgents walks. Discovery and load MUST agree: a profile that
// prime lists but `ox coworker load` cannot open is worse than one never listed.
// Returns the full file content along with parsed frontmatter metadata.
func LoadAgent(teamPath, name string) (*AgentContent, error) {
	if teamPath == "" || name == "" {
		return nil, nil
	}

	agentPath, ok := findInProfileDirs(teamPath, name+".md")
	if !ok {
		// preserve the legacy not-found error shape for callers that inspect it
		agentPath = filepath.Join(teamPath, AgentsDir, name+".md")
	}

	data, err := os.ReadFile(agentPath)
	if err != nil {
		return nil, err
	}

	description, model := parseAgentFrontmatter(agentPath)

	return &AgentContent{
		Name:        name,
		Description: description,
		Model:       model,
		Path:        agentPath,
		Content:     string(data),
	}, nil
}

// ValidateAgentFile validates that a file is a valid coworker agent definition.
// Returns the parsed description and model, or an error if validation fails.
// Requires: YAML frontmatter with a description field.
func ValidateAgentFile(path string) (description, model string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("cannot read file: %w", err)
	}

	content := string(data)
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return "", "", fmt.Errorf("missing YAML frontmatter (file must start with ---)")
	}

	description, model = parseAgentFrontmatter(path)
	if description == "" {
		return "", "", fmt.Errorf("frontmatter missing required 'description' field")
	}

	return description, model, nil
}

// FindAgent looks up an agent by name across all configured team contexts.
// Returns nil if the agent is not found.
func FindAgent(teamPaths []string, name string) (*AgentContent, error) {
	for _, teamPath := range teamPaths {
		agent, err := LoadAgent(teamPath, name)
		if err == nil && agent != nil {
			return agent, nil
		}
	}
	return nil, nil
}

// parseFirstDescription extracts the first meaningful description from a file.
// Looks for frontmatter description or first paragraph after title.
func parseFirstDescription(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	inFrontmatter := false
	lineCount := 0
	passedTitle := false

	for scanner.Scan() {
		lineCount++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// handle frontmatter
		if lineCount == 1 && trimmed == "---" {
			inFrontmatter = true
			continue
		}
		if inFrontmatter {
			if trimmed == "---" {
				inFrontmatter = false
				continue
			}
			if strings.HasPrefix(line, "description:") {
				desc := strings.TrimSpace(strings.TrimPrefix(line, "description:"))
				return strings.Trim(desc, `"'`)
			}
			continue
		}

		// skip empty lines and titles
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			passedTitle = true
			continue
		}

		// return first non-empty, non-title line after title
		if passedTitle && trimmed != "" {
			// truncate long descriptions
			if len(trimmed) > 120 {
				return trimmed[:117] + "..."
			}
			return trimmed
		}

		// stop after 30 lines
		if lineCount > 30 {
			break
		}
	}

	return ""
}

// ReadFirstLines reads up to maxLines lines from a file.
// Returns the content as a string, truncated at the line limit.
// Exported for reuse in prime output (e.g., MEMORY.md, AGENTS.md).
func ReadFirstLines(path string, maxLines int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) >= maxLines {
			break
		}
	}
	return strings.Join(lines, "\n")
}
