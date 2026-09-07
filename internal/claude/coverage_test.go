package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgent(t *testing.T) {
	tests := []struct {
		name      string
		teamPath  string
		agentName string
		content   string // if empty, don't create file
		wantNil   bool
		wantDesc  string
		wantModel string
		wantErr   bool
	}{
		{
			name:      "empty team path returns nil",
			teamPath:  "",
			agentName: "test",
			wantNil:   true,
		},
		{
			name:      "empty agent name returns nil",
			teamPath:  "/some/path",
			agentName: "",
			wantNil:   true,
		},
		{
			name:      "nonexistent file returns error",
			teamPath:  "PLACEHOLDER", // replaced with tmpDir
			agentName: "nonexistent",
			wantErr:   true,
		},
		{
			name:      "valid agent with frontmatter",
			teamPath:  "PLACEHOLDER",
			agentName: "reviewer",
			content:   "---\ndescription: Reviews code\nmodel: opus\n---\n# Reviewer\nContent here.",
			wantDesc:  "Reviews code",
			wantModel: "opus",
		},
		{
			name:      "valid agent without frontmatter",
			teamPath:  "PLACEHOLDER",
			agentName: "simple",
			content:   "# Simple Agent\n\nJust content, no frontmatter.",
			wantDesc:  "",
			wantModel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			teamPath := tt.teamPath
			if teamPath == "PLACEHOLDER" {
				tmpDir := t.TempDir()
				teamPath = tmpDir
				if tt.content != "" {
					agentsDir := filepath.Join(tmpDir, AgentsDir)
					if err := os.MkdirAll(agentsDir, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(agentsDir, tt.agentName+".md"), []byte(tt.content), 0644); err != nil {
						t.Fatal(err)
					}
				}
			}

			agent, err := LoadAgent(teamPath, tt.agentName)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if agent != nil {
					t.Fatalf("expected nil, got %+v", agent)
				}
				return
			}

			if agent.Name != tt.agentName {
				t.Errorf("name = %q, want %q", agent.Name, tt.agentName)
			}
			if agent.Description != tt.wantDesc {
				t.Errorf("description = %q, want %q", agent.Description, tt.wantDesc)
			}
			if agent.Model != tt.wantModel {
				t.Errorf("model = %q, want %q", agent.Model, tt.wantModel)
			}
			if agent.Content != tt.content {
				t.Errorf("content mismatch: got %d bytes, want %d bytes", len(agent.Content), len(tt.content))
			}
			if agent.Path == "" {
				t.Error("expected non-empty path")
			}
		})
	}
}

func TestFindAgent(t *testing.T) {
	// set up two team paths, agent exists in second one
	team1 := t.TempDir()
	team2 := t.TempDir()

	agentsDir := filepath.Join(team2, AgentsDir)
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\ndescription: Found in team2\n---\n# my-agent\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "my-agent.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		paths     []string
		agentName string
		wantNil   bool
		wantDesc  string
	}{
		{
			name:      "empty paths returns nil",
			paths:     nil,
			agentName: "my-agent",
			wantNil:   true,
		},
		{
			name:      "agent not in any path",
			paths:     []string{team1},
			agentName: "nonexistent",
			wantNil:   true,
		},
		{
			name:      "found in second path",
			paths:     []string{team1, team2},
			agentName: "my-agent",
			wantDesc:  "Found in team2",
		},
		{
			name:      "found in first matching path",
			paths:     []string{team2, team1},
			agentName: "my-agent",
			wantDesc:  "Found in team2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, err := FindAgent(tt.paths, tt.agentName)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if agent != nil {
					t.Fatalf("expected nil, got %+v", agent)
				}
				return
			}
			if agent == nil {
				t.Fatal("expected agent, got nil")
			}
			if agent.Description != tt.wantDesc {
				t.Errorf("description = %q, want %q", agent.Description, tt.wantDesc)
			}
		})
	}
}

func TestReadFirstLines(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		maxLines int
		want     string
	}{
		{
			name:     "reads all lines when under limit",
			content:  "line1\nline2\nline3",
			maxLines: 10,
			want:     "line1\nline2\nline3",
		},
		{
			name:     "truncates at limit",
			content:  "line1\nline2\nline3\nline4\nline5",
			maxLines: 3,
			want:     "line1\nline2\nline3",
		},
		{
			name:     "single line",
			content:  "only line",
			maxLines: 5,
			want:     "only line",
		},
		{
			name:     "empty file",
			content:  "",
			maxLines: 5,
			want:     "",
		},
		{
			name:     "nonexistent file returns empty",
			content:  "", // special case handled below
			maxLines: 5,
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "nonexistent file returns empty" {
				got := ReadFirstLines("/nonexistent/path/file.md", tt.maxLines)
				if got != "" {
					t.Errorf("got %q, want empty string", got)
				}
				return
			}

			tmpFile := filepath.Join(t.TempDir(), "test.md")
			if err := os.WriteFile(tmpFile, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			got := ReadFirstLines(tmpFile, tt.maxLines)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseFirstDescription(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "from frontmatter",
			content: "---\ndescription: Frontmatter desc\n---\n# Title\nBody text.",
			want:    "Frontmatter desc",
		},
		{
			name:    "from body after title",
			content: "# My Title\n\nThis is the first paragraph after the title.",
			want:    "This is the first paragraph after the title.",
		},
		{
			name:    "skips empty lines between title and description",
			content: "# Title\n\n\n\nActual description here.",
			want:    "Actual description here.",
		},
		{
			name:    "truncates long descriptions",
			content: "# Title\n\n" + strings.Repeat("x", 150),
			want:    strings.Repeat("x", 117) + "...",
		},
		{
			name:    "quoted frontmatter description",
			content: "---\ndescription: 'Quoted value'\n---\n# Title\n",
			want:    "Quoted value",
		},
		{
			name:    "empty file",
			content: "",
			want:    "",
		},
		{
			name:    "no title no frontmatter",
			content: "Just some text without a title.",
			want:    "", // passedTitle is false, so no description returned
		},
		{
			name:    "nonexistent file returns empty",
			content: "", // special case
			want:    "",
		},
		{
			name:    "frontmatter without description field",
			content: "---\nmodel: opus\n---\n# Title\nBody after frontmatter.",
			want:    "Body after frontmatter.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "nonexistent file returns empty" {
				got := parseFirstDescription("/nonexistent/path.md")
				if got != "" {
					t.Errorf("got %q, want empty", got)
				}
				return
			}

			tmpFile := filepath.Join(t.TempDir(), "test.md")
			if err := os.WriteFile(tmpFile, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			got := parseFirstDescription(tmpFile)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseIndexWithTable(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string]string
	}{
		{
			name: "standard table",
			content: `# Agents

| Name | Description |
|------|-------------|
| reviewer | Reviews pull requests |
| deployer | Handles deployments |
`,
			want: map[string]string{
				"reviewer": "Reviews pull requests",
				"deployer": "Handles deployments",
			},
		},
		{
			name: "backtick-wrapped names",
			content: `| Name | Description |
|------|-------------|
| ` + "`code-review`" + ` | Reviews code quality |
`,
			want: map[string]string{
				"code-review": "Reviews code quality",
			},
		},
		{
			name:    "empty file",
			content: "",
			want:    map[string]string{},
		},
		{
			name: "non-table content ignored",
			content: `# Just a heading

Some paragraph text.

- A list item
`,
			want: map[string]string{},
		},
		{
			name:    "nonexistent file returns empty map",
			content: "", // special case
			want:    map[string]string{},
		},
		{
			name: "table ends at empty line then restarts",
			content: `| Name | Description |
|------|-------------|
| first | First entry |

| Name | Description |
|------|-------------|
| second | Second entry |
`,
			want: map[string]string{
				"first":  "First entry",
				"second": "Second entry",
			},
		},
		{
			name: "row with insufficient columns ignored",
			content: `| Name | Description |
|------|-------------|
| only-name |
| good | Good description |
`,
			want: map[string]string{
				"good": "Good description",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "nonexistent file returns empty map" {
				got := ParseIndexWithTable("/nonexistent/path.md")
				if len(got) != 0 {
					t.Errorf("expected empty map, got %v", got)
				}
				return
			}

			tmpFile := filepath.Join(t.TempDir(), "index.md")
			if err := os.WriteFile(tmpFile, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			got := ParseIndexWithTable(tmpFile)

			if len(got) != len(tt.want) {
				t.Fatalf("got %d entries, want %d: %v", len(got), len(tt.want), got)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestHasAnyCustomizations(t *testing.T) {
	tests := []struct {
		name string
		tc   TeamCustomizations
		want bool
	}{
		{
			name: "empty",
			tc:   TeamCustomizations{},
			want: false,
		},
		{
			name: "has claude md",
			tc:   TeamCustomizations{HasClaudeMD: true},
			want: true,
		},
		{
			name: "has agents md",
			tc:   TeamCustomizations{HasAgentsMD: true},
			want: true,
		},
		{
			name: "has agents agents md",
			tc:   TeamCustomizations{HasAgentsAgentsMD: true},
			want: true,
		},
		{
			name: "has agents",
			tc:   TeamCustomizations{Agents: []Agent{{Name: "test"}}},
			want: true,
		},
		{
			name: "has commands",
			tc:   TeamCustomizations{Commands: []Command{{Name: "deploy"}}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.tc.HasAnyCustomizations()
			if got != tt.want {
				t.Errorf("HasAnyCustomizations() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiscoverAll_EmptyPath(t *testing.T) {
	tc, err := DiscoverAll("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tc != nil {
		t.Fatalf("expected nil for empty path, got %+v", tc)
	}
}

func TestDiscoverAll_WithAgentsSubdir(t *testing.T) {
	tmpDir := t.TempDir()

	// set up coworkers/ directory structure
	coworkersDir := filepath.Join(tmpDir, CoworkersDir)
	agentsDir := filepath.Join(tmpDir, AgentsDir)
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// CLAUDE.md
	if err := os.WriteFile(filepath.Join(coworkersDir, "CLAUDE.md"), []byte("# Config"), 0644); err != nil {
		t.Fatal(err)
	}

	// coworkers/agents/AGENTS.md (agents-level AGENTS.md)
	agentsContent := "# Agents Guide\n\nUse these agents for code review."
	if err := os.WriteFile(filepath.Join(agentsDir, "AGENTS.md"), []byte(agentsContent), 0644); err != nil {
		t.Fatal(err)
	}

	// coworkers/agents/index.md
	indexContent := "# Agents\n\n- **reviewer**: Reviews code\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "index.md"), []byte(indexContent), 0644); err != nil {
		t.Fatal(err)
	}

	// an actual agent file
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"), []byte("---\ndescription: Reviews code\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tc, err := DiscoverAll(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !tc.HasClaudeMD {
		t.Error("expected HasClaudeMD = true")
	}
	if !tc.HasAgentsAgentsMD {
		t.Error("expected HasAgentsAgentsMD = true")
	}
	if tc.AgentsAgentsMDPath != filepath.Join(agentsDir, "AGENTS.md") {
		t.Errorf("unexpected AgentsAgentsMDPath: %s", tc.AgentsAgentsMDPath)
	}
	if tc.AgentsAgentsMDContent == "" {
		t.Error("expected non-empty AgentsAgentsMDContent")
	}
	if !tc.HasAgentsIndex {
		t.Error("expected HasAgentsIndex = true")
	}
	if tc.AgentsIndexPath != filepath.Join(agentsDir, "index.md") {
		t.Errorf("unexpected AgentsIndexPath: %s", tc.AgentsIndexPath)
	}
	if len(tc.Agents) != 1 {
		t.Errorf("expected 1 agent, got %d", len(tc.Agents))
	}
	if !tc.HasAnyCustomizations() {
		t.Error("expected HasAnyCustomizations = true")
	}
}

func TestDiscoverAgents_SkipsNonMdFiles(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, AgentsDir)
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// non-.md files and directories should be skipped
	_ = os.WriteFile(filepath.Join(agentsDir, "notes.txt"), []byte("not an agent"), 0644)
	_ = os.WriteFile(filepath.Join(agentsDir, "config.json"), []byte("{}"), 0644)
	_ = os.Mkdir(filepath.Join(agentsDir, "subdir"), 0755)

	// one valid agent
	_ = os.WriteFile(filepath.Join(agentsDir, "valid.md"), []byte("---\ndescription: Valid\n---\n"), 0644)

	agents, err := DiscoverAgents(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 1 {
		t.Errorf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "valid" {
		t.Errorf("expected name 'valid', got %q", agents[0].Name)
	}
}

func TestParseAgentFrontmatter_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantDesc string
		wantMod  string
	}{
		{
			name:     "no frontmatter delimiter on line 1",
			content:  "not frontmatter\n---\ndescription: ignored\n---\n",
			wantDesc: "",
			wantMod:  "",
		},
		{
			name:     "frontmatter with double-quoted values",
			content:  "---\ndescription: \"Double quoted\"\nmodel: \"sonnet\"\n---\n",
			wantDesc: "Double quoted",
			wantMod:  "sonnet",
		},
		{
			name:     "nonexistent file",
			content:  "", // special case
			wantDesc: "",
			wantMod:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "nonexistent file" {
				desc, model := parseAgentFrontmatter("/nonexistent/path.md")
				if desc != "" || model != "" {
					t.Errorf("got desc=%q model=%q, want empty", desc, model)
				}
				return
			}

			tmpFile := filepath.Join(t.TempDir(), "agent.md")
			if err := os.WriteFile(tmpFile, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			desc, model := parseAgentFrontmatter(tmpFile)
			if desc != tt.wantDesc {
				t.Errorf("description = %q, want %q", desc, tt.wantDesc)
			}
			if model != tt.wantMod {
				t.Errorf("model = %q, want %q", model, tt.wantMod)
			}
		})
	}
}

func TestValidateAgentFile_NonexistentFile(t *testing.T) {
	_, _, err := ValidateAgentFile("/nonexistent/path/agent.md")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "cannot read file") {
		t.Errorf("expected 'cannot read file' error, got: %v", err)
	}
}

func TestDiscoverTeamCommands_SkipsNonMdAndIndex(t *testing.T) {
	tmpDir := t.TempDir()
	commandsDir := filepath.Join(tmpDir, CommandsDir)
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// these should all be skipped
	_ = os.WriteFile(filepath.Join(commandsDir, "index.md"), []byte("# Index"), 0644)
	_ = os.WriteFile(filepath.Join(commandsDir, "notes.txt"), []byte("not a command"), 0644)
	_ = os.Mkdir(filepath.Join(commandsDir, "subdir"), 0755)

	// one valid command
	_ = os.WriteFile(filepath.Join(commandsDir, "deploy.md"), []byte("---\ndescription: Deploy\n---\n"), 0644)

	commands, err := DiscoverTeamCommands(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commands) != 1 {
		t.Errorf("expected 1 command, got %d", len(commands))
	}
}

func TestParseIndex_NonexistentFile(t *testing.T) {
	result := ParseIndex("/nonexistent/path.md")
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestParseIndex_EmptyNames(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "index.md")
	// empty name or empty description should be skipped
	content := "- ****: description without name\n- **name**: \n- **good**: valid entry\n"
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	result := ParseIndex(tmpFile)
	if len(result) != 1 {
		t.Errorf("expected 1 entry, got %d: %v", len(result), result)
	}
	if result["good"] != "valid entry" {
		t.Errorf("got %q for 'good'", result["good"])
	}
}

func TestExtractFrontmatterValue(t *testing.T) {
	tests := []struct {
		line   string
		prefix string
		want   string
	}{
		{"description: hello world", "description:", "hello world"},
		{"name: 'quoted'", "name:", "quoted"},
		{"trigger: \"/cmd\"", "trigger:", "/cmd"},
		{"description:   extra spaces  ", "description:", "extra spaces"},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := extractFrontmatterValue(tt.line, tt.prefix)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDiscoverAll_WithCommands(t *testing.T) {
	tmpDir := t.TempDir()

	commandsDir := filepath.Join(tmpDir, CommandsDir)
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}

	_ = os.WriteFile(filepath.Join(commandsDir, "deploy.md"), []byte("---\ndescription: Deploy\n---\n"), 0644)

	tc, err := DiscoverAll(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(tc.Commands) != 1 {
		t.Errorf("expected 1 command, got %d", len(tc.Commands))
	}
	if !tc.HasAnyCustomizations() {
		t.Error("expected HasAnyCustomizations = true with commands")
	}
}
