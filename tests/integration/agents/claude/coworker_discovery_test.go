//go:build integration

package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/tests/integration/agents/common"
)

// TestCoworkerDiscovery_OSSReleaseEngineer verifies that a real Claude Code
// instance, after receiving ox agent prime output, discovers and loads the
// oss-release-engineer coworker from the REAL SageOx team context.
//
// This test uses the actual team context synced to this machine (team_jihjpfkt8b),
// not mock fixtures. It validates the full coworker discovery pipeline:
//
//	ox agent prime → coworker list in JSON → Claude recognizes expert → ox coworker load
//
// Run with:
//
//	go test -tags=integration -timeout=5m -run TestCoworkerDiscovery ./tests/integration/agents/claude/ -v
func TestCoworkerDiscovery_OSSReleaseEngineer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// Set up real team context in the isolated test environment
	setupRealTeamContext(t, env)

	// Run prime to install hooks and discover coworkers
	primeOutput := runOxPrime(t, env)

	// Verify prime discovered the oss-release-engineer coworker
	if !strings.Contains(primeOutput, "oss-release-engineer") {
		t.Fatalf("ox agent prime did not discover oss-release-engineer coworker.\nPrime output (first 2000 chars):\n%.2000s", primeOutput)
	}
	t.Log("prime discovered oss-release-engineer coworker")

	// Launch Claude with a natural prompt that should trigger coworker loading
	prompt := `use an expert oss release engineer to review our release process`

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Log("running claude CLI with coworker discovery prompt...")
	result := runClaudeWithMaxTurns(ctx, t, env, agent, prompt, 10)
	if result.Error != nil {
		t.Logf("claude stderr/error (may be ok): %v", result.Error)
	}
	t.Logf("claude completed in %v", result.Duration)

	// Analyze Claude's output for coworker discovery evidence
	output := result.RawOutput
	evidence := analyzeCoworkerDiscovery(output)

	t.Logf("Discovery evidence — tool_use: %v, name_mentioned: %v, content_match: %v",
		evidence.ToolUseDetected, evidence.NameMentioned, evidence.ContentMatch)

	if evidence.ToolUseDetected {
		t.Log("Claude ran 'ox coworker load oss-release-engineer'")
	}
	if evidence.ContentMatch {
		t.Logf("Claude's response contains release-engineer-specific content: %s", evidence.MatchedTerms)
	}

	// The test passes if ANY evidence of coworker discovery is present
	if !evidence.ToolUseDetected && !evidence.NameMentioned && !evidence.ContentMatch {
		t.Fatalf("Claude did not discover or load the oss-release-engineer coworker.\n"+
			"Expected at least one of:\n"+
			"  - 'ox coworker load oss-release-engineer' tool use\n"+
			"  - mention of 'oss-release-engineer' by name\n"+
			"  - release-engineer-specific content (GoReleaser, Homebrew tap, etc.)\n\n"+
			"Raw output (first 3000 chars):\n%.3000s", output)
	}

	t.Log("coworker discovery test passed")
}

// coworkerDiscoveryEvidence holds signals that Claude discovered a coworker.
type coworkerDiscoveryEvidence struct {
	// ToolUseDetected is true if Claude ran 'ox coworker load oss-release-engineer'
	ToolUseDetected bool

	// NameMentioned is true if Claude mentioned 'oss-release-engineer' in its response
	NameMentioned bool

	// ContentMatch is true if Claude's response contains content specific to the
	// real oss-release-engineer agent definition
	ContentMatch bool

	// MatchedTerms lists which specific terms were found
	MatchedTerms string
}

// analyzeCoworkerDiscovery checks Claude's output for evidence of coworker discovery.
func analyzeCoworkerDiscovery(rawOutput string) coworkerDiscoveryEvidence {
	evidence := coworkerDiscoveryEvidence{}
	lower := strings.ToLower(rawOutput)

	// Check for tool use: Claude running 'ox coworker load oss-release-engineer'
	if strings.Contains(lower, "ox coworker load oss-release-engineer") {
		evidence.ToolUseDetected = true
	}

	// Check for name mention
	if strings.Contains(lower, "oss-release-engineer") {
		evidence.NameMentioned = true
	}

	// Check for content specific to the real oss-release-engineer definition.
	// These terms appear in the agent's body, not in generic release discussions.
	specificTerms := []string{
		"goreleaser",
		"homebrew tap",
		"sageox/homebrew-tap",
		"cosign",
		"sbom",
		"multi-arch binaries",
	}

	var matched []string
	for _, term := range specificTerms {
		if strings.Contains(lower, term) {
			matched = append(matched, term)
		}
	}
	if len(matched) >= 2 {
		evidence.ContentMatch = true
		evidence.MatchedTerms = strings.Join(matched, ", ")
	}

	return evidence
}

// setupRealTeamContext copies the real SageOx team context into the test's
// isolated XDG data directory and patches the project config to reference it.
func setupRealTeamContext(t *testing.T, env *common.TestEnvironment) {
	t.Helper()

	realTeamPath := findRealTeamContextPath(t)

	// Destination: $XDG_DATA_HOME/sageox/sageox.ai/teams/team_jihjpfkt8b/
	// The test env sets XDG_DATA_HOME to rootDir/data/
	destTeamPath := filepath.Join(env.RootDir, "data", "sageox", "sageox.ai", "teams", "team_jihjpfkt8b")
	if err := os.MkdirAll(filepath.Dir(destTeamPath), 0755); err != nil {
		t.Fatalf("failed to create teams directory: %v", err)
	}

	// Copy real team context
	if err := copyDirRecursive(realTeamPath, destTeamPath); err != nil {
		t.Fatalf("failed to copy real team context: %v", err)
	}
	t.Logf("copied real team context to %s", destTeamPath)

	// Init git repo in copied team context (prime requires it)
	if _, err := os.Stat(filepath.Join(destTeamPath, ".git")); os.IsNotExist(err) {
		cmd := exec.Command("git", "init")
		cmd.Dir = destTeamPath
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to init git in team context: %v\n%s", err, output)
		}
		cmd = exec.Command("git", "add", "-A")
		cmd.Dir = destTeamPath
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to stage team context files: %v\n%s", err, output)
		}
		cmd = exec.Command("git", "commit", "--allow-empty", "-m", "init")
		cmd.Dir = destTeamPath
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to create initial commit in team context: %v\n%s", err, output)
		}
	}

	// Patch project config.json to include the real team_id
	configPath := filepath.Join(env.ProjectDir, ".sageox", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config.json: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to parse config.json: %v", err)
	}

	cfg["team_id"] = "team_jihjpfkt8b"
	cfg["team_name"] = "SageOx"

	patched, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal config.json: %v", err)
	}

	if err := os.WriteFile(configPath, patched, 0644); err != nil {
		t.Fatalf("failed to write config.json: %v", err)
	}
	t.Log("patched config.json with real team_id")
}

// findRealTeamContextPath locates the real SageOx team context on disk.
// Skips the test if not found (requires ox to be synced).
func findRealTeamContextPath(t *testing.T) string {
	t.Helper()

	// Resolve via XDG_DATA_HOME (respects custom XDG config), fallback to ~/.local/share
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("cannot determine home dir: %v", err)
		}
		dataHome = filepath.Join(home, ".local", "share")
	}

	teamPath := filepath.Join(dataHome, "sageox", "sageox.ai", "teams", "team_jihjpfkt8b")

	if _, err := os.Stat(teamPath); os.IsNotExist(err) {
		t.Skipf("real team context not found at %s — requires ox to be synced on this machine", teamPath)
	}

	// Verify the oss-release-engineer agent exists
	agentPath := filepath.Join(teamPath, "coworkers", "agents", "oss-release-engineer.md")
	if _, err := os.Stat(agentPath); os.IsNotExist(err) {
		t.Skipf("oss-release-engineer agent not found at %s", agentPath)
	}

	t.Logf("found real team context at %s", teamPath)
	return teamPath
}

// copyDirRecursive copies a directory tree from src to dst.
func copyDirRecursive(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip .git directories to avoid copying large git history
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
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
			return fmt.Errorf("read %s: %w", path, err)
		}

		return os.WriteFile(dstPath, data, info.Mode())
	})
}
