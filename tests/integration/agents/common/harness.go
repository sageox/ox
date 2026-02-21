//go:build integration

// Package common provides a shared test harness for agent integration tests.
// This harness is designed to be agent-agnostic, supporting Claude Code, OpenCode,
// Codex, and other legitimate CLI coding agents that integrate with ox CLI.
package common

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// AgentType represents a coding agent that can be tested.
type AgentType string

const (
	AgentClaude   AgentType = "claude"
	AgentOpenCode AgentType = "opencode"
	AgentCodex    AgentType = "codex"
)

// AgentConfig holds configuration for running a specific agent.
type AgentConfig struct {
	// Type identifies the agent
	Type AgentType

	// CLIPath is the path to the agent's CLI executable
	CLIPath string

	// PromptFlag is the CLI flag for passing a prompt (e.g., "-p" for claude)
	PromptFlag string

	// NonInteractiveFlags are flags to run in non-interactive mode
	NonInteractiveFlags []string

	// OutputFormat specifies how the agent returns structured output
	OutputFormat string // "json", "text"

	// Timeout for a single prompt execution
	Timeout time.Duration
}

// DefaultAgentConfigs returns default configurations for known agents.
func DefaultAgentConfigs() map[AgentType]*AgentConfig {
	return map[AgentType]*AgentConfig{
		AgentClaude: {
			Type:                AgentClaude,
			CLIPath:             "claude",
			PromptFlag:          "-p",
			NonInteractiveFlags: []string{"--output-format", "json", "--verbose"},
			OutputFormat:        "json",
			Timeout:             2 * time.Minute,
		},
		AgentOpenCode: {
			Type:                AgentOpenCode,
			CLIPath:             "opencode",
			PromptFlag:          "-p",
			NonInteractiveFlags: []string{"--non-interactive"},
			OutputFormat:        "text",
			Timeout:             2 * time.Minute,
		},
		AgentCodex: {
			Type:                AgentCodex,
			CLIPath:             "codex",
			PromptFlag:          "-p",
			NonInteractiveFlags: []string{"--quiet"},
			OutputFormat:        "text",
			Timeout:             2 * time.Minute,
		},
	}
}

// TestEnvironment holds the isolated test environment.
type TestEnvironment struct {
	// RootDir is the temporary root directory for the test
	RootDir string

	// ProjectDir is the mock project directory
	ProjectDir string

	// OxBinaryPath is the path to the built ox binary
	OxBinaryPath string

	// EnvVars holds the isolated environment variables for subprocesses
	EnvVars []string

	// t is the test context
	t *testing.T
}

// SetupTestEnvironment creates an isolated test environment.
// It copies fixtures to a temp directory and builds the ox CLI.
func SetupTestEnvironment(t *testing.T) *TestEnvironment {
	t.Helper()

	// Create temp root directory
	rootDir, err := os.MkdirTemp("", "ox-agent-integration-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	env := &TestEnvironment{
		RootDir: rootDir,
		t:       t,
	}

	// Register cleanup with t.Cleanup for panic-safe cleanup
	t.Cleanup(func() {
		if rootDir != "" {
			os.RemoveAll(rootDir)
		}
	})

	// Copy fixtures to temp directory
	fixturesDir := findFixturesDir(t)
	env.ProjectDir = filepath.Join(rootDir, "project")
	if err := copyDir(filepath.Join(fixturesDir, "mock-project"), env.ProjectDir); err != nil {
		t.Fatalf("failed to copy fixtures: %v", err)
	}

	// Copy team context fixtures
	teamContextDir := filepath.Join(rootDir, "team-context")
	if err := copyDir(filepath.Join(fixturesDir, "mock-team-context"), teamContextDir); err != nil {
		t.Fatalf("failed to copy team context fixtures: %v", err)
	}

	// Create ledger directory
	ledgerDir := filepath.Join(rootDir, "ledger")
	os.MkdirAll(ledgerDir, 0755)

	// Initialize git repos in fixtures
	initGitRepo(t, env.ProjectDir)
	initGitRepo(t, ledgerDir)
	initGitRepo(t, teamContextDir)

	// Fix placeholder paths in local.json to be absolute
	fixLocalConfigPaths(t, env, teamContextDir, ledgerDir)

	// Build ox CLI
	env.OxBinaryPath = buildOxCLI(t, rootDir)

	// Set up isolated environment variables (stored for subprocess use)
	env.setupEnvironment()

	return env
}

// Cleanup removes the test environment.
// Note: This is now handled by t.Cleanup() registered in SetupTestEnvironment.
// This method is kept for backward compatibility but is a no-op.
func (e *TestEnvironment) Cleanup() {
	// Cleanup is now handled by t.Cleanup() for panic safety
}

// setupEnvironment configures isolated environment variables for subprocesses.
// Instead of modifying the process environment (which causes race conditions in
// parallel tests), we store env vars to pass to subprocesses via cmd.Env.
func (e *TestEnvironment) setupEnvironment() {
	// Start with current environment
	e.EnvVars = os.Environ()

	// Add/override our isolated environment variables
	testEnvVars := map[string]string{
		// XDG paths pointing to our test fixtures
		"XDG_CONFIG_HOME": filepath.Join(e.RootDir, "config"),
		"XDG_DATA_HOME":   filepath.Join(e.RootDir, "data"),
		"XDG_CACHE_HOME":  filepath.Join(e.RootDir, "cache"),
		"XDG_STATE_HOME":  filepath.Join(e.RootDir, "state"),

		// Force offline mode to avoid cloud API calls
		"OX_OFFLINE": "1",

		// Disable XDG mode to use project .sageox/ directory
		"OX_XDG_DISABLE": "1",

		// Add ox binary to PATH
		"PATH": filepath.Dir(e.OxBinaryPath) + string(os.PathListSeparator) + os.Getenv("PATH"),
	}

	// Override values in EnvVars
	for key, value := range testEnvVars {
		e.EnvVars = setEnvVar(e.EnvVars, key, value)
	}

	// Remove CLAUDECODE env var so spawned Claude Code sessions don't refuse
	// to start with "cannot be launched inside another Claude Code session"
	e.EnvVars = removeEnvVar(e.EnvVars, "CLAUDECODE")

	// Create required directories
	for _, dir := range []string{"config", "data", "cache", "state"} {
		os.MkdirAll(filepath.Join(e.RootDir, dir, "sageox"), 0755)
	}
}

// setEnvVar sets or updates an environment variable in a slice of env vars.
func setEnvVar(envVars []string, key, value string) []string {
	prefix := key + "="
	for i, v := range envVars {
		if strings.HasPrefix(v, prefix) {
			envVars[i] = prefix + value
			return envVars
		}
	}
	return append(envVars, prefix+value)
}

// removeEnvVar removes an environment variable from a slice of env vars.
func removeEnvVar(envVars []string, key string) []string {
	prefix := key + "="
	result := envVars[:0]
	for _, v := range envVars {
		if !strings.HasPrefix(v, prefix) {
			result = append(result, v)
		}
	}
	return result
}

// AgentTestResult holds the result of running a prompt through an agent.
type AgentTestResult struct {
	// RawOutput is the complete output from the agent
	RawOutput string

	// JSONResponse is the parsed JSON response (if agent outputs JSON)
	JSONResponse map[string]interface{}

	// ExitCode is the agent's exit code
	ExitCode int

	// Duration is how long the agent took to respond
	Duration time.Duration

	// Error is any error that occurred
	Error error
}

// RunAgentPrompt executes a prompt through the specified agent.
func (e *TestEnvironment) RunAgentPrompt(ctx context.Context, agent *AgentConfig, prompt string) *AgentTestResult {
	e.t.Helper()

	result := &AgentTestResult{}
	start := time.Now()

	// Build command arguments
	args := []string{agent.PromptFlag, prompt}
	args = append(args, agent.NonInteractiveFlags...)

	// Create command with timeout
	cmdCtx, cancel := context.WithTimeout(ctx, agent.Timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, agent.CLIPath, args...)
	cmd.Dir = e.ProjectDir
	cmd.Env = e.EnvVars

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run command
	err := cmd.Run()
	result.Duration = time.Since(start)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
		result.Error = fmt.Errorf("agent execution failed: %w\nstderr: %s", err, stderr.String())
	}

	result.RawOutput = stdout.String()

	// Debug output if requested
	if os.Getenv("CLAUDE_TEST_DEBUG") == "1" || os.Getenv("AGENT_TEST_DEBUG") == "1" {
		e.t.Logf("Agent output:\n%s", result.RawOutput)
		if stderr.Len() > 0 {
			e.t.Logf("Agent stderr:\n%s", stderr.String())
		}
	}

	// Parse JSON if expected
	if agent.OutputFormat == "json" && result.RawOutput != "" {
		// Try to extract JSON from output (may be wrapped in other text)
		if jsonStr := extractJSON(result.RawOutput); jsonStr != "" {
			if err := json.Unmarshal([]byte(jsonStr), &result.JSONResponse); err != nil {
				e.t.Logf("Warning: failed to parse JSON response: %v", err)
			}
		}
	}

	return result
}

// CheckAgentAvailable returns true if the agent CLI is available.
func CheckAgentAvailable(agent *AgentConfig) bool {
	_, err := exec.LookPath(agent.CLIPath)
	return err == nil
}

// SkipIfAgentUnavailable skips the test if the agent is not available.
func SkipIfAgentUnavailable(t *testing.T, agent *AgentConfig) {
	t.Helper()
	if !CheckAgentAvailable(agent) {
		t.Skipf("agent %q CLI not available at %q", agent.Type, agent.CLIPath)
	}
}

// Helper functions

func findFixturesDir(t *testing.T) string {
	t.Helper()

	// Get the directory of this source file
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to get current file path")
	}

	fixturesDir := filepath.Join(filepath.Dir(filename), "fixtures")
	if _, err := os.Stat(fixturesDir); os.IsNotExist(err) {
		t.Fatalf("fixtures directory not found at %s", fixturesDir)
	}

	return fixturesDir
}

func buildOxCLI(t *testing.T, outputDir string) string {
	t.Helper()

	// Find the repo root (where go.mod is)
	repoRoot := findRepoRoot(t)

	// Build ox CLI
	binaryName := "ox"
	if runtime.GOOS == "windows" {
		binaryName = "ox.exe"
	}
	outputPath := filepath.Join(outputDir, "bin", binaryName)
	os.MkdirAll(filepath.Dir(outputPath), 0755)

	cmd := exec.Command("go", "build", "-o", outputPath, "./cmd/ox")
	cmd.Dir = repoRoot

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build ox CLI: %v\n%s", err, output)
	}

	return outputPath
}

func findRepoRoot(t *testing.T) string {
	t.Helper()

	// Start from current file and walk up to find go.mod
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to get current file path")
	}

	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod)")
		}
		dir = parent
	}
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(dstPath, data, info.Mode())
	})
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()

	os.MkdirAll(dir, 0755)

	// Only init if not already a git repo
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		cmd := exec.Command("git", "init")
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Logf("Warning: failed to init git repo at %s: %v\n%s", dir, err, output)
		}

		// Create initial commit
		cmd = exec.Command("git", "commit", "--allow-empty", "-m", "Initial commit")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		cmd.CombinedOutput() // Ignore errors
	}
}

func fixLocalConfigPaths(t *testing.T, env *TestEnvironment, teamContextDir, ledgerDir string) {
	t.Helper()

	// Read local.json
	localConfigPath := filepath.Join(env.ProjectDir, ".sageox", "local.json")
	data, err := os.ReadFile(localConfigPath)
	if err != nil {
		return // No local.json, that's OK
	}

	// Replace placeholder paths with absolute paths
	content := string(data)
	content = strings.ReplaceAll(content, "__TEAM_CONTEXT_PATH__", teamContextDir)
	content = strings.ReplaceAll(content, "__LEDGER_PATH__", ledgerDir)

	os.WriteFile(localConfigPath, []byte(content), 0644)
}

// ExtractJSONFromOutput extracts Claude's response content from NDJSON output.
// This is the main entry point for test files to extract JSON responses.
func ExtractJSONFromOutput(s string) string {
	return extractJSON(s)
}

// extractJSON extracts Claude's response content from Claude Code output.
// Claude Code with --output-format json outputs a JSON array of message objects.
// We look for the "result" type message and extract the content field.
func extractJSON(s string) string {
	// Try parsing as JSON array first (Claude Code format)
	var messages []map[string]interface{}
	if err := json.Unmarshal([]byte(s), &messages); err == nil {
		// Look for result message with content
		for i := len(messages) - 1; i >= 0; i-- {
			msg := messages[i]
			if msg["type"] == "result" {
				if result, ok := msg["result"].(string); ok && result != "" {
					return extractJSONFromText(result)
				}
			}
			// Also check assistant messages for JSON content
			if msg["type"] == "assistant" {
				if msgContent, ok := msg["message"].(map[string]interface{}); ok {
					if content, ok := msgContent["content"].([]interface{}); ok {
						for _, c := range content {
							if textBlock, ok := c.(map[string]interface{}); ok {
								if text, ok := textBlock["text"].(string); ok {
									if jsonStr := extractJSONFromText(text); jsonStr != "" {
										return jsonStr
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Fallback: try NDJSON (newline-delimited JSON)
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		var msg struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Result  string `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		// Look for result message type
		if msg.Type == "result" && msg.Result != "" {
			return extractJSONFromText(msg.Result)
		}
	}

	// Final fallback: try to find JSON object in the raw text
	return extractJSONFromText(s)
}

// extractJSONFromText finds a JSON object in text content.
func extractJSONFromText(s string) string {
	// Find the first { that starts a JSON object
	start := strings.Index(s, "{")
	if start == -1 {
		return ""
	}

	// Find matching closing brace
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}

	return ""
}
