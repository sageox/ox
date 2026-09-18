package agentwork

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AgentUsability describes whether an agent CLI is installed and authenticated.
type AgentUsability struct {
	Installed     bool
	Authenticated bool
	AuthDetail    string // human-readable auth status (e.g. "OAuth", "API key", "not logged in")
}

// CheckAgentUsability probes whether the given agent CLI is installed and authenticated.
// Designed to be fast (< 2s) for use in doctor checks and auto-detection.
func CheckAgentUsability(agent string) AgentUsability {
	switch agent {
	case "claude":
		return checkClaudeUsability()
	case "codex":
		return checkCodexUsability()
	case "gemini":
		return checkGeminiUsability()
	default:
		return AgentUsability{}
	}
}

// checkClaudeUsability checks if claude CLI is installed and has valid auth.
// Checks: ANTHROPIC_API_KEY env var, then ~/.claude.json for oauthAccount.
func checkClaudeUsability() AgentUsability {
	result := AgentUsability{}

	if _, err := exec.LookPath("claude"); err != nil {
		return result
	}
	result.Installed = true

	// check API key env var first (fastest)
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		result.Authenticated = true
		result.AuthDetail = "API key"
		return result
	}

	// check OAuth in ~/.claude.json
	home, err := os.UserHomeDir()
	if err != nil {
		result.AuthDetail = "unable to check"
		return result
	}

	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		result.AuthDetail = "not logged in"
		return result
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		result.AuthDetail = "config unreadable"
		return result
	}

	if _, ok := cfg["oauthAccount"]; ok {
		result.Authenticated = true
		result.AuthDetail = "OAuth"
		return result
	}

	result.AuthDetail = "not logged in"
	return result
}

// checkCodexUsability checks if codex CLI is installed and has valid auth.
// Runs `codex login status` which is designed to be fast.
func checkCodexUsability() AgentUsability {
	result := AgentUsability{}

	codexPath, err := exec.LookPath("codex")
	if err != nil {
		return result
	}
	result.Installed = true

	// check API key env var first
	if os.Getenv("OPENAI_API_KEY") != "" {
		result.Authenticated = true
		result.AuthDetail = "API key"
		return result
	}

	// run `codex login status` with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, codexPath, "login", "status")
	// Current Codex prints successful login status to stderr; older builds
	// used stdout. Keep both bounded, and never accept text from a failed probe.
	outputBuffer := &boundedCodexOutput{limit: 64 * 1024}
	cmd.Stdout, cmd.Stderr = outputBuffer, outputBuffer
	cmd.WaitDelay = time.Second
	setProcAttr(cmd)
	err = cmd.Run()
	if ctx.Err() != nil {
		result.AuthDetail = "authentication check timed out"
		return result
	}
	if err != nil {
		result.AuthDetail = "not logged in"
		return result
	}
	if !outputBuffer.overflow {
		// A host warning may precede the status on either stream. Match a
		// complete status line after verifying the command's successful exit.
		for _, line := range strings.Split(outputBuffer.buf.String(), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(strings.ToLower(line), "logged in using ") {
				result.Authenticated = true
				result.AuthDetail = line
				return result
			}
		}
	}

	result.AuthDetail = "not logged in"
	return result
}

// checkGeminiUsability checks if gemini CLI is installed and has valid auth.
// Checks: GEMINI_API_KEY or GOOGLE_API_KEY env var, then CLI in PATH.
func checkGeminiUsability() AgentUsability {
	result := AgentUsability{}

	if _, err := exec.LookPath("gemini"); err != nil {
		return result
	}
	result.Installed = true

	if os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_API_KEY") != "" {
		result.Authenticated = true
		result.AuthDetail = "API key"
		return result
	}

	result.AuthDetail = "no API key"
	return result
}
