package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverTeamCommands_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	commands, err := DiscoverTeamCommands(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commands) != 0 {
		t.Errorf("expected 0 commands, got %d", len(commands))
	}
}

func TestDiscoverTeamCommands_WithCommands(t *testing.T) {
	tmpDir := t.TempDir()

	// create commands directory
	commandsDir := filepath.Join(tmpDir, CommandsDir)
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// create a command file with frontmatter
	cmdContent := `---
name: deploy
description: "Deploy to production environment"
trigger: /deploy
---

# Deploy Command

Deploys the current branch to production.
`
	if err := os.WriteFile(filepath.Join(commandsDir, "deploy.md"), []byte(cmdContent), 0644); err != nil {
		t.Fatal(err)
	}

	commands, err := DiscoverTeamCommands(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(commands))
	}

	cmd := commands[0]
	if cmd.Name != "deploy" {
		t.Errorf("expected name 'deploy', got %q", cmd.Name)
	}
	if cmd.Trigger != "/deploy" {
		t.Errorf("expected trigger '/deploy', got %q", cmd.Trigger)
	}
	if cmd.Description != "Deploy to production environment" {
		t.Errorf("expected description 'Deploy to production environment', got %q", cmd.Description)
	}
}

func TestDiscoverTeamCommands_WithIndex(t *testing.T) {
	tmpDir := t.TempDir()

	commandsDir := filepath.Join(tmpDir, CommandsDir)
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// create index.md with token-optimized descriptions
	indexContent := `# Commands

- **review-pr**: Review pull request changes
`
	if err := os.WriteFile(filepath.Join(commandsDir, "index.md"), []byte(indexContent), 0644); err != nil {
		t.Fatal(err)
	}

	// create command file (description should be overridden by index)
	cmdContent := `---
description: "Long description in file"
---

# review-pr

Some content.
`
	if err := os.WriteFile(filepath.Join(commandsDir, "review-pr.md"), []byte(cmdContent), 0644); err != nil {
		t.Fatal(err)
	}

	commands, err := DiscoverTeamCommands(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(commands))
	}

	cmd := commands[0]
	if cmd.Description != "Review pull request changes" {
		t.Errorf("expected index description, got %q", cmd.Description)
	}
	if !cmd.FromIndex {
		t.Error("expected FromIndex to be true")
	}
	if cmd.Trigger != "/review-pr" {
		t.Errorf("expected trigger '/review-pr', got %q", cmd.Trigger)
	}
}

func TestDiscoverTeamCommands_FallbackToFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()

	commandsDir := filepath.Join(tmpDir, CommandsDir)
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// create command file with frontmatter (no index.md)
	cmdContent := `---
name: custom-name
description: "Custom command description"
trigger: /my-trigger
---

# Custom Command

Does something custom.
`
	if err := os.WriteFile(filepath.Join(commandsDir, "custom.md"), []byte(cmdContent), 0644); err != nil {
		t.Fatal(err)
	}

	commands, err := DiscoverTeamCommands(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(commands))
	}

	cmd := commands[0]
	if cmd.Name != "custom-name" {
		t.Errorf("expected name 'custom-name', got %q", cmd.Name)
	}
	if cmd.Trigger != "/my-trigger" {
		t.Errorf("expected trigger '/my-trigger', got %q", cmd.Trigger)
	}
	if cmd.Description != "Custom command description" {
		t.Errorf("expected description from frontmatter, got %q", cmd.Description)
	}
	if cmd.FromIndex {
		t.Error("expected FromIndex to be false")
	}
}

func TestDiscoverTeamCommands_MultipleCommands(t *testing.T) {
	tmpDir := t.TempDir()

	commandsDir := filepath.Join(tmpDir, CommandsDir)
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// create multiple command files
	files := []struct {
		name    string
		content string
	}{
		{"deploy.md", "---\ndescription: Deploy command\n---\n# Deploy\n"},
		{"test.md", "---\ndescription: Run tests\n---\n# Test\n"},
		{"rollback.md", "---\ndescription: Rollback deployment\n---\n# Rollback\n"},
	}

	for _, f := range files {
		if err := os.WriteFile(filepath.Join(commandsDir, f.name), []byte(f.content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	commands, err := DiscoverTeamCommands(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commands) != 3 {
		t.Errorf("expected 3 commands, got %d", len(commands))
	}
}

func TestParseCommandFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name        string
		content     string
		wantName    string
		wantDesc    string
		wantTrigger string
	}{
		{
			name: "all fields",
			content: `---
name: my-command
description: "Does something"
trigger: /my-cmd
---
# Content`,
			wantName:    "my-command",
			wantDesc:    "Does something",
			wantTrigger: "/my-cmd",
		},
		{
			name: "partial fields",
			content: `---
description: Just a description
---
# Content`,
			wantName:    "",
			wantDesc:    "Just a description",
			wantTrigger: "",
		},
		{
			name:        "no frontmatter",
			content:     "# No Frontmatter\n\nJust content.",
			wantName:    "",
			wantDesc:    "",
			wantTrigger: "",
		},
		{
			name: "single quotes",
			content: `---
name: 'quoted-name'
description: 'quoted desc'
trigger: '/quoted-trigger'
---`,
			wantName:    "quoted-name",
			wantDesc:    "quoted desc",
			wantTrigger: "/quoted-trigger",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(tmpDir, tt.name+".md")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}

			fm := parseCommandFrontmatter(path)
			if fm.name != tt.wantName {
				t.Errorf("name: got %q, want %q", fm.name, tt.wantName)
			}
			if fm.description != tt.wantDesc {
				t.Errorf("description: got %q, want %q", fm.description, tt.wantDesc)
			}
			if fm.trigger != tt.wantTrigger {
				t.Errorf("trigger: got %q, want %q", fm.trigger, tt.wantTrigger)
			}
		})
	}
}
