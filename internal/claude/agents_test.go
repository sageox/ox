package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverAgents_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	agents, err := DiscoverAgents(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

func TestDiscoverAgents_WithAgents(t *testing.T) {
	tmpDir := t.TempDir()

	// create agents directory
	agentsDir := filepath.Join(tmpDir, AgentsDir)
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// create an agent file with frontmatter
	agentContent := `---
description: "Test agent for code review"
model: "opus"
---

# test-agent

A test agent for reviewing code.
`
	if err := os.WriteFile(filepath.Join(agentsDir, "test-agent.md"), []byte(agentContent), 0644); err != nil {
		t.Fatal(err)
	}

	agents, err := DiscoverAgents(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}

	agent := agents[0]
	if agent.Name != "test-agent" {
		t.Errorf("expected name 'test-agent', got %q", agent.Name)
	}
	if agent.Description != "Test agent for code review" {
		t.Errorf("expected description 'Test agent for code review', got %q", agent.Description)
	}
	if agent.Model != "opus" {
		t.Errorf("expected model 'opus', got %q", agent.Model)
	}
}

func TestDiscoverAgents_WithIndex(t *testing.T) {
	tmpDir := t.TempDir()

	agentsDir := filepath.Join(tmpDir, AgentsDir)
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// create index.md with token-optimized descriptions
	indexContent := `# Agents

- **my-agent**: Optimized description from index
`
	if err := os.WriteFile(filepath.Join(agentsDir, "index.md"), []byte(indexContent), 0644); err != nil {
		t.Fatal(err)
	}

	// create agent file (description should be overridden by index)
	agentContent := `---
description: "Long description in file"
---

# my-agent

Some content.
`
	if err := os.WriteFile(filepath.Join(agentsDir, "my-agent.md"), []byte(agentContent), 0644); err != nil {
		t.Fatal(err)
	}

	agents, err := DiscoverAgents(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}

	agent := agents[0]
	if agent.Description != "Optimized description from index" {
		t.Errorf("expected index description, got %q", agent.Description)
	}
	if !agent.FromIndex {
		t.Error("expected FromIndex to be true")
	}
}

func TestDiscoverAgents_SkipsUppercaseFiles(t *testing.T) {
	tmpDir := t.TempDir()

	agentsDir := filepath.Join(tmpDir, AgentsDir)
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// uppercase documentation files should be excluded
	os.WriteFile(filepath.Join(agentsDir, "AGENTS.md"), []byte("# Agents Guide"), 0644)
	os.WriteFile(filepath.Join(agentsDir, "README.md"), []byte("# README"), 0644)

	// lowercase agent file should be included
	os.WriteFile(filepath.Join(agentsDir, "reviewer.md"), []byte("---\ndescription: Reviews code\n---\n# Reviewer\n"), 0644)

	agents, err := DiscoverAgents(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (uppercase files excluded), got %d", len(agents))
	}
	if agents[0].Name != "reviewer" {
		t.Errorf("expected 'reviewer', got %q", agents[0].Name)
	}
}

func TestDiscoverAll(t *testing.T) {
	tmpDir := t.TempDir()

	coworkersDir := filepath.Join(tmpDir, CoworkersDir)
	if err := os.MkdirAll(coworkersDir, 0755); err != nil {
		t.Fatal(err)
	}

	// create CLAUDE.md
	if err := os.WriteFile(filepath.Join(coworkersDir, "CLAUDE.md"), []byte("# Team Claude Config"), 0644); err != nil {
		t.Fatal(err)
	}

	// create AGENTS.md
	if err := os.WriteFile(filepath.Join(coworkersDir, "AGENTS.md"), []byte("# Team Agents"), 0644); err != nil {
		t.Fatal(err)
	}

	tc, err := DiscoverAll(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !tc.HasClaudeMD {
		t.Error("expected HasClaudeMD to be true")
	}
	if !tc.HasAgentsMD {
		t.Error("expected HasAgentsMD to be true")
	}
	if tc.ClaudeMDPath != filepath.Join(coworkersDir, "CLAUDE.md") {
		t.Errorf("unexpected ClaudeMDPath: %s", tc.ClaudeMDPath)
	}
	if !tc.HasInstructionFiles() {
		t.Error("expected HasInstructionFiles to return true")
	}
}

func TestParseIndex(t *testing.T) {
	tmpDir := t.TempDir()

	indexContent := `# Index

- **agent-one**: First agent description
- **agent-two**: Second agent description
* **agent-three** - Third with dash separator
`
	indexPath := filepath.Join(tmpDir, "index.md")
	if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
		t.Fatal(err)
	}

	result := ParseIndex(indexPath)

	if result["agent-one"] != "First agent description" {
		t.Errorf("agent-one: got %q", result["agent-one"])
	}
	if result["agent-two"] != "Second agent description" {
		t.Errorf("agent-two: got %q", result["agent-two"])
	}
	if result["agent-three"] != "Third with dash separator" {
		t.Errorf("agent-three: got %q", result["agent-three"])
	}
}

func TestValidateAgentFile(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantDesc    string
		wantModel   string
		wantErr     bool
		errContains string
	}{
		{
			name:      "valid with description and model",
			content:   "---\ndescription: Expert reviewer\nmodel: opus\n---\n# Reviewer\n",
			wantDesc:  "Expert reviewer",
			wantModel: "opus",
		},
		{
			name:     "valid with description only",
			content:  "---\ndescription: Security auditor\n---\n# Security\n",
			wantDesc: "Security auditor",
		},
		{
			name:        "missing frontmatter",
			content:     "# Just markdown\nNo frontmatter here.\n",
			wantErr:     true,
			errContains: "missing YAML frontmatter",
		},
		{
			name:        "missing description",
			content:     "---\nmodel: sonnet\n---\n# Agent\n",
			wantErr:     true,
			errContains: "missing required 'description'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := filepath.Join(t.TempDir(), "agent.md")
			if err := os.WriteFile(tmpFile, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}

			desc, model, err := ValidateAgentFile(tmpFile)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateAgentFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errContains != "" {
				if err == nil || !contains(err.Error(), tt.errContains) {
					t.Errorf("error = %v, want to contain %q", err, tt.errContains)
				}
				return
			}
			if desc != tt.wantDesc {
				t.Errorf("description = %q, want %q", desc, tt.wantDesc)
			}
			if model != tt.wantModel {
				t.Errorf("model = %q, want %q", model, tt.wantModel)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
