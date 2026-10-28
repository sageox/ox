package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
)

// setupCoworkerProject creates a project dir with .sageox, project config, and local config.
// Returns (projectDir, teamDir).
func setupCoworkerProject(t *testing.T, teamID string, teams []config.TeamContext) (string, string) {
	t.Helper()
	dir := t.TempDir()
	teamDir := ""
	for _, tc := range teams {
		if tc.TeamID == teamID {
			teamDir = tc.Path
		}
		agentsDir := filepath.Join(tc.Path, "coworkers", "agents")
		if err := os.MkdirAll(agentsDir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if teamDir == "" && len(teams) > 0 {
		teamDir = teams[0].Path
	}
	requireSageoxDir(t, dir)
	if err := config.SaveProjectConfig(dir, &config.ProjectConfig{TeamID: teamID}); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveLocalConfig(dir, &config.LocalConfig{TeamContexts: teams}); err != nil {
		t.Fatal(err)
	}
	return dir, teamDir
}

// newCoworkerCmd creates a cobra.Command with standard coworker flags.
func newCoworkerCmd(flags map[string]string) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.Flags().String("team", "", "")
	cmd.Flags().String("model", "", "")
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("force", false, "")
	for k, v := range flags {
		if v == "true" {
			cmd.Flags().Set(k, "true")
		} else if v != "" {
			cmd.Flags().Set(k, v)
		}
	}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	return cmd, &buf
}

func TestCoworkerListCommand(t *testing.T) {
	os.Setenv("CLAUDE_CODE_SESSION_ID", "test-session")
	defer os.Unsetenv("CLAUDE_CODE_SESSION_ID")

	tests := []struct {
		name       string
		setupFn    func(t *testing.T) (string, func())
		flags      map[string]string
		wantErr    bool
		wantOutput string
		wantAbsent string
	}{
		{
			name: "no coworkers found shows team name",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				teamDir := filepath.Join(dir, "team-context")
				projectDir, _ := setupCoworkerProject(t, "test-team", []config.TeamContext{
					{TeamID: "test-team", TeamName: "Test Team", Path: teamDir},
				})
				return projectDir, func() {}
			},
			wantOutput: "No SageOx coworkers found in Test Team team.",
		},
		{
			name: "no team context configured json mode",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				requireSageoxDir(t, dir)
				return dir, func() {}
			},
			flags: map[string]string{"json": "true"},
		},
		{
			name: "team context with agents shows team name in header",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				teamDir := filepath.Join(dir, "team-context")
				projectDir, _ := setupCoworkerProject(t, "test-team", []config.TeamContext{
					{TeamID: "test-team", TeamName: "Test Team", Path: teamDir},
				})
				agentsDir := filepath.Join(teamDir, "coworkers", "agents")
				os.WriteFile(filepath.Join(agentsDir, "code-reviewer.md"), []byte("---\ndescription: Test code reviewer specialist\nmodel: sonnet\n---\n\n# code-reviewer\n\nYou are an expert code reviewer...\n"), 0644)
				return projectDir, func() {}
			},
			wantOutput: "Expert Coworkers (Test Team)",
		},
		{
			name: "only shows repo team coworkers",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				team1Dir := filepath.Join(dir, "team1-context")
				team2Dir := filepath.Join(dir, "team2-context")
				projectDir, _ := setupCoworkerProject(t, "team-1", []config.TeamContext{
					{TeamID: "team-1", TeamName: "Team One", Path: team1Dir},
					{TeamID: "team-2", TeamName: "Team Two", Path: team2Dir},
				})
				os.WriteFile(filepath.Join(team1Dir, "coworkers", "agents", "code-reviewer.md"), []byte("---\ndescription: Code reviewer\n---\n# code-reviewer\n"), 0644)
				os.WriteFile(filepath.Join(team2Dir, "coworkers", "agents", "style-expert.md"), []byte("---\ndescription: Style expert\n---\n# style-expert\n"), 0644)
				return projectDir, func() {}
			},
			wantOutput: "code-reviewer",
			wantAbsent: "style-expert",
		},
		{
			name: "team flag overrides repo team",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				team1Dir := filepath.Join(dir, "team1-context")
				team2Dir := filepath.Join(dir, "team2-context")
				projectDir, _ := setupCoworkerProject(t, "team-1", []config.TeamContext{
					{TeamID: "team-1", TeamName: "Team One", Path: team1Dir},
					{TeamID: "team-2", TeamName: "Team Two", Path: team2Dir},
				})
				os.WriteFile(filepath.Join(team1Dir, "coworkers", "agents", "code-reviewer.md"), []byte("---\ndescription: Code reviewer\n---\n# code-reviewer\n"), 0644)
				os.WriteFile(filepath.Join(team2Dir, "coworkers", "agents", "style-expert.md"), []byte("---\ndescription: Style expert\n---\n# style-expert\n"), 0644)
				return projectDir, func() {}
			},
			flags:      map[string]string{"team": "team-2"},
			wantOutput: "style-expert",
			wantAbsent: "code-reviewer",
		},
		{
			name: "team flag with unknown team ID",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				teamDir := filepath.Join(dir, "team-context")
				projectDir, _ := setupCoworkerProject(t, "test-team", []config.TeamContext{
					{TeamID: "test-team", Path: teamDir},
				})
				return projectDir, func() {}
			},
			flags:      map[string]string{"team": "nonexistent-team"},
			wantOutput: "No team context configured", // graceful fallback
		},
		{
			name: "list json mode with coworkers validates structure",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				teamDir := filepath.Join(dir, "team-context")
				projectDir, _ := setupCoworkerProject(t, "test-team", []config.TeamContext{
					{TeamID: "test-team", TeamName: "Test Team", Path: teamDir},
				})
				os.WriteFile(filepath.Join(teamDir, "coworkers", "agents", "reviewer.md"), []byte("---\ndescription: Reviews code\nmodel: opus\n---\n# Reviewer\n"), 0644)
				return projectDir, func() {}
			},
			flags: map[string]string{"json": "true"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot, cleanup := tt.setupFn(t)
			defer cleanup()

			oldWd, _ := os.Getwd()
			if err := os.Chdir(projectRoot); err != nil {
				t.Fatal(err)
			}
			defer os.Chdir(oldWd)

			cmd, buf := newCoworkerCmd(tt.flags)
			err := runCoworkerList(cmd, nil)

			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			output := buf.String()
			if tt.wantOutput != "" && !strings.Contains(output, tt.wantOutput) {
				t.Errorf("output = %q, want to contain %q", output, tt.wantOutput)
			}
			if tt.wantAbsent != "" && strings.Contains(output, tt.wantAbsent) {
				t.Errorf("output = %q, must NOT contain %q", output, tt.wantAbsent)
			}

			// validate JSON structure when in json mode
			jsonMode, _ := cmd.Flags().GetBool("json")
			if jsonMode && err == nil {
				var jsonOutput coworkerListOutput
				if err := json.Unmarshal(buf.Bytes(), &jsonOutput); err != nil {
					t.Errorf("invalid JSON output: %v", err)
				}
				if jsonOutput.Coworkers == nil {
					t.Error("expected coworkers array (even if empty)")
				}
			}
		})
	}
}

func TestCoworkerLoadCommand(t *testing.T) {
	os.Setenv("CLAUDE_CODE_SESSION_ID", "test-session")
	defer os.Unsetenv("CLAUDE_CODE_SESSION_ID")

	tests := []struct {
		name       string
		agentName  string
		setupFn    func(t *testing.T) (string, func())
		flags      map[string]string
		wantErr    bool
		wantOutput string
	}{
		{
			name:      "coworker not found",
			agentName: "nonexistent",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				teamDir := filepath.Join(dir, "team-context")
				projectDir, _ := setupCoworkerProject(t, "test-team", []config.TeamContext{
					{TeamID: "test-team", TeamName: "Test Team", Path: teamDir},
				})
				return projectDir, func() {}
			},
			wantErr: true,
		},
		{
			name:      "load coworker successfully",
			agentName: "code-reviewer",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				teamDir := filepath.Join(dir, "team-context")
				projectDir, _ := setupCoworkerProject(t, "test-team", []config.TeamContext{
					{TeamID: "test-team", TeamName: "Test Team", Path: teamDir},
				})
				os.WriteFile(filepath.Join(teamDir, "coworkers", "agents", "code-reviewer.md"), []byte("---\ndescription: Test code reviewer specialist\nmodel: sonnet\n---\n\n# code-reviewer\n\nYou are an expert code reviewer...\n"), 0644)
				return projectDir, func() {}
			},
			wantOutput: "expert code reviewer",
		},
		{
			name:      "load json mode includes team info",
			agentName: "code-reviewer",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				teamDir := filepath.Join(dir, "team-context")
				projectDir, _ := setupCoworkerProject(t, "test-team", []config.TeamContext{
					{TeamID: "test-team", TeamName: "Test Team", Path: teamDir},
				})
				os.WriteFile(filepath.Join(teamDir, "coworkers", "agents", "code-reviewer.md"), []byte("---\ndescription: Test code reviewer specialist\nmodel: sonnet\n---\n\n# code-reviewer\n\nYou are an expert code reviewer...\n"), 0644)
				return projectDir, func() {}
			},
			flags: map[string]string{"json": "true"},
		},
		{
			name:      "model override applied",
			agentName: "code-reviewer",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				teamDir := filepath.Join(dir, "team-context")
				projectDir, _ := setupCoworkerProject(t, "test-team", []config.TeamContext{
					{TeamID: "test-team", TeamName: "Test Team", Path: teamDir},
				})
				os.WriteFile(filepath.Join(teamDir, "coworkers", "agents", "code-reviewer.md"), []byte("---\ndescription: reviewer\nmodel: sonnet\n---\n# code-reviewer\n"), 0644)
				return projectDir, func() {}
			},
			flags:      map[string]string{"model": "opus"},
			wantOutput: "model: opus",
		},
		{
			name:      "team flag loads from other team",
			agentName: "style-expert",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				team1Dir := filepath.Join(dir, "team1-context")
				team2Dir := filepath.Join(dir, "team2-context")
				projectDir, _ := setupCoworkerProject(t, "team-1", []config.TeamContext{
					{TeamID: "team-1", TeamName: "Team One", Path: team1Dir},
					{TeamID: "team-2", TeamName: "Team Two", Path: team2Dir},
				})
				os.WriteFile(filepath.Join(team2Dir, "coworkers", "agents", "style-expert.md"), []byte("---\ndescription: Style expert\n---\n# style-expert\nYou are a style expert.\n"), 0644)
				return projectDir, func() {}
			},
			flags:      map[string]string{"team": "team-2"},
			wantOutput: "style expert",
		},
		{
			name:      "team flag with unknown team errors",
			agentName: "anything",
			setupFn: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				teamDir := filepath.Join(dir, "team-context")
				projectDir, _ := setupCoworkerProject(t, "test-team", []config.TeamContext{
					{TeamID: "test-team", Path: teamDir},
				})
				return projectDir, func() {}
			},
			flags:   map[string]string{"team": "nonexistent"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot, cleanup := tt.setupFn(t)
			defer cleanup()

			oldWd, _ := os.Getwd()
			if err := os.Chdir(projectRoot); err != nil {
				t.Fatal(err)
			}
			defer os.Chdir(oldWd)

			cmd, buf := newCoworkerCmd(tt.flags)
			err := runCoworkerLoad(cmd, []string{tt.agentName})

			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			output := buf.String()
			if tt.wantOutput != "" && !strings.Contains(output, tt.wantOutput) {
				t.Errorf("output = %q, want to contain %q", output, tt.wantOutput)
			}

			// validate JSON structure
			jsonMode, _ := cmd.Flags().GetBool("json")
			if jsonMode && err == nil {
				var jsonOutput coworkerLoadOutput
				if err := json.Unmarshal(buf.Bytes(), &jsonOutput); err != nil {
					t.Errorf("invalid JSON output: %v", err)
				}
				if !jsonOutput.Loaded {
					t.Error("expected loaded=true")
				}
				if jsonOutput.TeamID == "" {
					t.Error("expected team_id")
				}
			}
		})
	}
}

func TestCoworkerLoadEntry(t *testing.T) {
	entry := newCoworkerLoadEntryForTest("code-reviewer", "sonnet")

	if entry.CoworkerName != "code-reviewer" {
		t.Errorf("expected coworker_name=code-reviewer, got %s", entry.CoworkerName)
	}
	if entry.CoworkerModel != "sonnet" {
		t.Errorf("expected coworker_model=sonnet, got %s", entry.CoworkerModel)
	}
	if entry.Type != "system" {
		t.Errorf("expected type=system, got %s", entry.Type)
	}
	if entry.Content == "" {
		t.Error("expected non-empty content")
	}
}

// initTeamGitRepo initializes a git repo in the team context dir for add/remove tests.
func initTeamGitRepo(t *testing.T, dir string) {
	t.Helper()
	gitEnv := append(os.Environ(), // safe: git subprocess in temp dir, not ox
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.name", "Test User"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "commit.gpgsign", "false"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git setup %v failed: %v\n%s", args, err, out)
		}
	}
	// initial commit so git rm works
	readmePath := filepath.Join(dir, "README.md")
	os.WriteFile(readmePath, []byte("# team context\n"), 0644)
	for _, args := range [][]string{
		{"git", "add", "README.md"},
		{"git", "commit", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init commit %v failed: %v\n%s", args, err, out)
		}
	}
}

func TestCoworkerAddCommand(t *testing.T) {
	tests := []struct {
		name       string
		srcFile    string // filename (not content)
		srcContent string
		flags      map[string]string
		wantErr    bool
		wantOutput string
		wantName   string // expected coworker name derived from filename
	}{
		{
			name:       "valid coworker file",
			srcFile:    "test-agent.md",
			srcContent: "---\ndescription: Expert code reviewer\nmodel: opus\n---\n# Code Reviewer\nYou review code.\n",
			wantOutput: `Added coworker "test-agent"`,
			wantName:   "test-agent",
		},
		{
			name:       "missing frontmatter",
			srcFile:    "bad-agent.md",
			srcContent: "# No Frontmatter\nJust markdown.\n",
			wantErr:    true,
		},
		{
			name:       "missing description",
			srcFile:    "no-desc.md",
			srcContent: "---\nmodel: sonnet\n---\n# Agent\n",
			wantErr:    true,
		},
		{
			name:       "non-md extension stripped correctly",
			srcFile:    "my-helper.md",
			srcContent: "---\ndescription: Helper agent\n---\n# Helper\n",
			wantOutput: `Added coworker "my-helper"`,
			wantName:   "my-helper",
		},
		{
			name:       "team flag targets other team",
			srcFile:    "team2-agent.md",
			srcContent: "---\ndescription: Team 2 agent\nmodel: haiku\n---\n# Agent\n",
			flags:      map[string]string{"team": "team-2"},
			wantOutput: `Added coworker "team2-agent"`,
			wantName:   "team2-agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			team1Dir := filepath.Join(dir, "team1-context")
			team2Dir := filepath.Join(dir, "team2-context")
			teams := []config.TeamContext{
				{TeamID: "test-team", TeamName: "Test Team", Path: team1Dir},
				{TeamID: "team-2", TeamName: "Team Two", Path: team2Dir},
			}
			for _, tc := range teams {
				agentsDir := filepath.Join(tc.Path, "coworkers", "agents")
				os.MkdirAll(agentsDir, 0755)
				initTeamGitRepo(t, tc.Path)
			}

			requireSageoxDir(t, dir)
			config.SaveProjectConfig(dir, &config.ProjectConfig{TeamID: "test-team"})
			config.SaveLocalConfig(dir, &config.LocalConfig{TeamContexts: teams})

			srcFile := filepath.Join(dir, tt.srcFile)
			os.WriteFile(srcFile, []byte(tt.srcContent), 0644)

			oldWd, _ := os.Getwd()
			os.Chdir(dir)
			defer os.Chdir(oldWd)

			cmd, buf := newCoworkerCmd(tt.flags)
			err := runCoworkerAdd(cmd, []string{srcFile})

			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantOutput != "" && !strings.Contains(buf.String(), tt.wantOutput) {
				t.Errorf("output = %q, want to contain %q", buf.String(), tt.wantOutput)
			}
			// verify file was copied to the correct team
			if !tt.wantErr && tt.wantName != "" {
				targetTeam := team1Dir
				teamFlag, _ := cmd.Flags().GetString("team")
				if teamFlag == "team-2" {
					targetTeam = team2Dir
				}
				destPath := filepath.Join(targetTeam, "coworkers", "agents", tt.wantName+".md")
				if _, err := os.Stat(destPath); os.IsNotExist(err) {
					t.Errorf("expected coworker file at %s", destPath)
				}
			}
		})
	}
}

func TestCoworkerAddDuplicate(t *testing.T) {
	dir := t.TempDir()
	teamDir := filepath.Join(dir, "team-context")
	agentsDir := filepath.Join(teamDir, "coworkers", "agents")
	os.MkdirAll(agentsDir, 0755)
	initTeamGitRepo(t, teamDir)

	os.WriteFile(filepath.Join(agentsDir, "reviewer.md"), []byte("---\ndescription: existing\n---\n"), 0644)

	requireSageoxDir(t, dir)
	config.SaveProjectConfig(dir, &config.ProjectConfig{TeamID: "test-team"})
	config.SaveLocalConfig(dir, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{TeamID: "test-team", Path: teamDir}},
	})

	srcFile := filepath.Join(dir, "reviewer.md")
	os.WriteFile(srcFile, []byte("---\ndescription: new version\n---\n"), 0644)

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cmd, _ := newCoworkerCmd(nil)
	err := runCoworkerAdd(cmd, []string{srcFile})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}
}

func TestCoworkerRemoveCommand(t *testing.T) {
	dir := t.TempDir()
	teamDir := filepath.Join(dir, "team-context")
	agentsDir := filepath.Join(teamDir, "coworkers", "agents")
	os.MkdirAll(agentsDir, 0755)
	initTeamGitRepo(t, teamDir)

	// add a coworker via git so git rm works
	agentFile := filepath.Join(agentsDir, "code-reviewer.md")
	os.WriteFile(agentFile, []byte("---\ndescription: reviewer\nmodel: sonnet\n---\n# Reviewer\n"), 0644)
	gitEnv := append(os.Environ(), // safe: git identity for temp dir only
		"GIT_AUTHOR_NAME=Test User", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	for _, args := range [][]string{
		{"git", "add", "coworkers/agents/code-reviewer.md"},
		{"git", "commit", "-m", "add coworker"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = teamDir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	requireSageoxDir(t, dir)
	config.SaveProjectConfig(dir, &config.ProjectConfig{TeamID: "test-team"})
	config.SaveLocalConfig(dir, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{TeamID: "test-team", Path: teamDir}},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	t.Run("remove with force", func(t *testing.T) {
		cmd, buf := newCoworkerCmd(map[string]string{"force": "true"})
		err := runCoworkerRemove(cmd, []string{"code-reviewer"})
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if !strings.Contains(buf.String(), "Removed") {
			t.Errorf("output = %q, want 'Removed'", buf.String())
		}
		if _, err := os.Stat(agentFile); !os.IsNotExist(err) {
			t.Error("expected coworker file to be removed")
		}
	})

	t.Run("remove nonexistent", func(t *testing.T) {
		cmd, _ := newCoworkerCmd(map[string]string{"force": "true"})
		err := runCoworkerRemove(cmd, []string{"nonexistent"})
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' error, got: %v", err)
		}
	})
}

func TestCoworkerRemoveCancellation(t *testing.T) {
	dir := t.TempDir()
	teamDir := filepath.Join(dir, "team-context")
	agentsDir := filepath.Join(teamDir, "coworkers", "agents")
	os.MkdirAll(agentsDir, 0755)
	initTeamGitRepo(t, teamDir)

	agentFile := filepath.Join(agentsDir, "keep-me.md")
	os.WriteFile(agentFile, []byte("---\ndescription: keeper\n---\n# Keeper\n"), 0644)
	gitEnv := append(os.Environ(), // safe: git identity for temp dir only
		"GIT_AUTHOR_NAME=Test User", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	for _, args := range [][]string{
		{"git", "add", "coworkers/agents/keep-me.md"},
		{"git", "commit", "-m", "add coworker"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = teamDir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	requireSageoxDir(t, dir)
	config.SaveProjectConfig(dir, &config.ProjectConfig{TeamID: "test-team"})
	config.SaveLocalConfig(dir, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{TeamID: "test-team", Path: teamDir}},
	})

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	// simulate stdin "n" to cancel
	cmd, buf := newCoworkerCmd(nil) // force=false (default)
	cmd.SetIn(strings.NewReader("n\n"))

	err := runCoworkerRemove(cmd, []string{"keep-me"})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(buf.String(), "Canceled") {
		t.Errorf("output = %q, want 'Canceled'", buf.String())
	}
	// file should still exist
	if _, err := os.Stat(agentFile); os.IsNotExist(err) {
		t.Error("coworker file should NOT have been removed after cancellation")
	}
}

// newCoworkerLoadEntryForTest creates a test entry without importing session package
func newCoworkerLoadEntryForTest(name, model string) struct {
	Type          string `json:"type"`
	Content       string `json:"content"`
	CoworkerName  string `json:"coworker_name,omitempty"`
	CoworkerModel string `json:"coworker_model,omitempty"`
} {
	content := "Loaded coworker: " + name
	if model != "" {
		content += " (model: " + model + ")"
	}
	return struct {
		Type          string `json:"type"`
		Content       string `json:"content"`
		CoworkerName  string `json:"coworker_name,omitempty"`
		CoworkerModel string `json:"coworker_model,omitempty"`
	}{
		Type:          "system",
		Content:       content,
		CoworkerName:  name,
		CoworkerModel: model,
	}
}
