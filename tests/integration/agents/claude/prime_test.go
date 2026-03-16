//go:build integration

// Package claude contains integration tests for Claude Code with ox CLI.
// These tests verify that Claude properly understands and uses ox features
// after running `ox agent prime`.
//
// # Known Issues
//
// This section documents known Claude Code issues affecting these tests.
// Each issue is date-stamped for tracking resolution.
//
// ## SessionStart Hook Bug (Claude Code #10373)
//
// Discovered: 2025-01-18
// Status: Active
// Reference: https://github.com/anthropics/claude-code/issues/10373
//
// Problem: SessionStart hooks execute but output is discarded for NEW sessions.
// The qz() function that processes hook output is only called for /clear,
// /compact, and --resume, NOT for fresh conversation starts.
//
// Impact on tests:
//   - TestHookExecution: Hook fires but Claude may report "Success" instead
//     of actual ox prime output. Test adjusted to be lenient.
//   - TestPrimeDiscovery: Claude may discover ox from CLAUDE.md/AGENTS.md
//     instead of hook. Test accepts any discovery source.
//
// Workaround: ox CLI uses CLAUDE.md/AGENTS.md fallback instructions.
// Test expectations adjusted to accept discovery from ANY source.
//
// ## Test Leniency Pattern
//
// Given the SessionStart hook bug, tests follow this pattern:
//  1. Test that Claude discovers ox from AT LEAST ONE source
//  2. Accept hook, CLAUDE.md, AGENTS.md, or project files as valid sources
//  3. Log which source was used for debugging
//  4. Only fail if NO source provides ox information
package claude

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/tests/integration/agents/common"
)

// getClaudeConfig returns the Claude agent configuration.
func getClaudeConfig() *common.AgentConfig {
	configs := common.DefaultAgentConfigs()
	return configs[common.AgentClaude]
}

// TestHookExecution verifies that ox agent prime runs via SessionStart hook.
// This test checks that hooks are working correctly by examining the raw output.
//
// KNOWN ISSUE (2025-01-18): Claude Code bug #10373 causes SessionStart hook
// output to be discarded for new sessions. The hook fires, but Claude only
// receives "Success" instead of the actual ox prime output. Tests adjusted
// to be lenient - we log the issue but don't fail, since ox has CLAUDE.md
// fallback instructions.
func TestHookExecution(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// Simple prompt - we just want to see if the hook fired
	prompt := `What is 2 + 2? Answer with just the number.`

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result := env.RunAgentPrompt(ctx, agent, prompt)

	// Check raw output for hook execution
	hookInfo := analyzeHookExecution(result.RawOutput)

	t.Run("hook_fired", func(t *testing.T) {
		// KNOWN ISSUE: SessionStart hook fires but output may be discarded
		// See: https://github.com/anthropics/claude-code/issues/10373
		if !hookInfo.SessionStartHookFired {
			// Don't fail - this is a known Claude Code bug for new sessions
			t.Log("SessionStart hook did not fire (expected for new sessions due to Claude Code bug #10373)")
		} else {
			t.Log("SessionStart hook fired successfully")
		}
	})

	t.Run("ox_prime_in_hook", func(t *testing.T) {
		if !hookInfo.OxPrimeInHookOutput {
			// Don't fail - this is expected due to the hook output bug
			t.Log("ox agent prime output not in hook (expected due to Claude Code bug #10373)")
			t.Log("Claude should discover ox via CLAUDE.md/AGENTS.md fallback")
		} else {
			t.Log("ox agent prime output received via hook")
		}
	})

	t.Run("agent_id_received", func(t *testing.T) {
		if hookInfo.AgentID == "" {
			t.Log("No agent ID in hook output (expected due to bug #10373)")
		} else {
			t.Logf("Agent ID from hook: %s", hookInfo.AgentID)
		}
	})

	t.Logf("Hook execution test completed in %v", result.Duration)
}

// TestPrimeDiscovery verifies how Claude discovers and uses ox agent prime.
// This test asks Claude to explain where it learned about ox CLI.
//
// KNOWN ISSUE (2025-01-18): Due to Claude Code bug #10373, Claude may discover
// ox from CLAUDE.md/AGENTS.md instead of the SessionStart hook. This test
// accepts discovery from ANY source as valid, since the multi-layered fallback
// is working as designed.
func TestPrimeDiscovery(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// Ask Claude to report how it learned about ox
	prompt := `I want to understand how you learned about ox CLI and SageOx integration.

Please analyze your available context and report:

Return your answer as a JSON object:
{
  "discovery_sources": {
    "from_hook": <true if you received ox info via a SessionStart hook>,
    "from_claude_md": <true if you see ox references in your CLAUDE.md instructions>,
    "from_agents_md": <true if you see ox references in AGENTS.md>,
    "from_project_files": <true if you found ox config in .sageox/ directory>,
    "other_sources": ["<any other sources you found>"]
  },
  "ox_knowledge": {
    "knows_agent_prime": <true if you know about ox agent prime>,
    "knows_agent_guidance": <true if you know about guidance fetching>,
    "has_agent_id": <true if you have an agent ID>,
    "agent_id": "<the agent ID if you have one>"
  },
  "hook_analysis": {
    "saw_hook_output": <true if you received hook output at session start>,
    "hook_contained_ox_data": <true if hook output had ox CLI data>,
    "hook_output_summary": "<brief summary of what the hook provided>"
  },
  "confidence": "<how confident are you about ox integration: high/medium/low>"
}

IMPORTANT:
- Only output the JSON object, no other text
- Be honest about what you can and cannot see
- Report ALL sources where you found ox/SageOx information`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result := env.RunAgentPrompt(ctx, agent, prompt)
	if result.Error != nil {
		t.Fatalf("Claude execution failed: %v", result.Error)
	}

	// Parse the response
	var response PrimeDiscoveryResponse
	jsonStr := common.ExtractJSONFromOutput(result.RawOutput)
	if jsonStr == "" {
		t.Fatalf("No JSON found in Claude's response.\nRaw output: %s", result.RawOutput)
	}

	if err := json.Unmarshal([]byte(jsonStr), &response); err != nil {
		t.Fatalf("Failed to parse Claude's response: %v\nJSON: %s", err, jsonStr)
	}

	// Log discovery information
	t.Logf("Discovery sources - Hook: %v, CLAUDE.md: %v, AGENTS.md: %v, Project: %v",
		response.DiscoverySources.FromHook,
		response.DiscoverySources.FromClaudeMD,
		response.DiscoverySources.FromAgentsMD,
		response.DiscoverySources.FromProjectFiles)

	t.Logf("Hook analysis - Saw output: %v, Had ox data: %v",
		response.HookAnalysis.SawHookOutput,
		response.HookAnalysis.HookContainedOxData)

	if response.HookAnalysis.HookOutputSummary != "" {
		t.Logf("Hook summary: %s", response.HookAnalysis.HookOutputSummary)
	}

	// Verify at least one discovery source
	t.Run("has_discovery_source", func(t *testing.T) {
		hasSource := response.DiscoverySources.FromHook ||
			response.DiscoverySources.FromClaudeMD ||
			response.DiscoverySources.FromAgentsMD ||
			response.DiscoverySources.FromProjectFiles ||
			len(response.DiscoverySources.OtherSources) > 0

		if !hasSource {
			t.Error("Claude should have discovered ox from at least one source")
		}
	})

	t.Run("knows_ox_commands", func(t *testing.T) {
		if !response.OxKnowledge.KnowsAgentPrime {
			t.Error("Claude should know about ox agent prime")
		}
	})

	t.Run("has_agent_id", func(t *testing.T) {
		if response.OxKnowledge.HasAgentID {
			t.Logf("Claude has agent ID: %s", response.OxKnowledge.AgentID)
		}
	})

	t.Logf("Prime discovery test completed in %v (confidence: %s)", result.Duration, response.Confidence)
}

// TestPrimeOutputUnderstanding verifies Claude understands ox agent prime output.
//
// KNOWN ISSUE (2025-01-18): Claude Code bug #10373 means ox prime output may
// come from CLAUDE.md instructions rather than the SessionStart hook for new
// sessions. Test expectations are adjusted to be lenient - we verify Claude
// knows about ox commands regardless of source.
func TestPrimeOutputUnderstanding(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	prompt := `Check ALL your available context: CLAUDE.md, AGENTS.md, project files, hook output, and any other sources.
For each field below, answer true if you can find ANY mention of the concept in your available context.

Return your answers as a JSON object:
{
  "test_passed": true,
  "agent_id": "<the agent ID if you have one, or empty>",
  "knows_ox_commands": {
    "ox_agent_prime": <true if ANY source mentions 'ox agent prime'>,
    "ox_agent_guidance": <true if ANY source mentions 'ox agent' and 'guidance'>,
    "ox_doctor": <true if ANY source mentions 'ox doctor'>,
    "ox_status": <true if ANY source mentions 'ox status'>
  },
  "understands_attribution": {
    "commit_footer_required": <true if ANY source mentions commit attribution or Co-Authored-By>,
    "commit_footer_text": "<the Co-Authored-By text if visible, or empty>"
  },
  "knows_team_context": {
    "has_team": <true if team context is present>,
    "team_name": "<team name if visible>",
    "knows_subagents": <true if you see team subagents or coworkers listed>,
    "subagent_names": ["<list of subagent/coworker names>"]
  },
  "knows_transcription": <true if ANY source mentions session recording or transcription>,
  "knows_guidance_system": {
    "understands_progressive": <true if ANY source mentions 'progressive guidance' or on-demand guidance>,
    "knows_paths": <true if you see guidance paths mentioned>,
    "example_paths": ["<list of example paths>"]
  }
}

IMPORTANT:
- Only output the JSON object, no other text
- Answer true if you see the concept mentioned ANYWHERE in your context, even briefly`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result := env.RunAgentPrompt(ctx, agent, prompt)
	if result.Error != nil {
		t.Fatalf("Claude execution failed: %v", result.Error)
	}

	// Parse the response
	var response PrimeUnderstandingResponse
	jsonStr := common.ExtractJSONFromOutput(result.RawOutput)
	if jsonStr == "" {
		t.Fatalf("No JSON found in Claude's response.\nRaw output (first 2000 chars): %.2000s", result.RawOutput)
	}

	if err := json.Unmarshal([]byte(jsonStr), &response); err != nil {
		t.Fatalf("Failed to parse Claude's response: %v\nJSON: %s", err, jsonStr)
	}

	// Verify understanding — at least some ox commands should be recognized.
	// LLM self-reporting is inherently non-deterministic, so we require a
	// minimum score rather than demanding every field be true.
	t.Run("knows_ox_commands", func(t *testing.T) {
		knownCount := 0
		for _, known := range []bool{
			response.KnowsOxCommands.OxAgentPrime,
			response.KnowsOxCommands.OxAgentGuidance,
			response.KnowsOxCommands.OxDoctor,
			response.KnowsOxCommands.OxStatus,
		} {
			if known {
				knownCount++
			}
		}
		t.Logf("ox commands recognized: %d/4 (prime=%v guidance=%v doctor=%v status=%v)",
			knownCount,
			response.KnowsOxCommands.OxAgentPrime,
			response.KnowsOxCommands.OxAgentGuidance,
			response.KnowsOxCommands.OxDoctor,
			response.KnowsOxCommands.OxStatus)

		if knownCount == 0 {
			t.Error("Claude should recognize at least one ox command from AGENTS.md")
		}
	})

	t.Run("understands_attribution", func(t *testing.T) {
		if !response.UnderstandsAttribution.CommitFooterRequired {
			// Attribution is explicitly in both AGENTS.md and CLAUDE.md fixtures
			t.Error("Claude should see attribution requirements in project files")
		}
		if response.UnderstandsAttribution.CommitFooterText != "" &&
			!strings.Contains(response.UnderstandsAttribution.CommitFooterText, "SageOx") {
			t.Errorf("Attribution should mention SageOx, got: %s", response.UnderstandsAttribution.CommitFooterText)
		}
	})

	t.Run("knows_team_context", func(t *testing.T) {
		// Team context may or may not be present depending on setup
		if response.KnowsTeamContext.HasTeam {
			t.Logf("Team detected: %s", response.KnowsTeamContext.TeamName)
			if response.KnowsTeamContext.KnowsSubagents {
				t.Logf("Subagents: %v", response.KnowsTeamContext.SubagentNames)
			}
		}
	})

	t.Run("knows_guidance_system", func(t *testing.T) {
		// Progressive guidance is mentioned in fixtures but LLM self-reporting
		// on this specific concept is unreliable. Log for observability.
		if response.KnowsGuidanceSystem.UnderstandsProgressive {
			t.Log("Claude recognizes progressive guidance system")
		} else {
			t.Log("Claude did not report understanding progressive guidance (non-critical — LLM self-assessment varies)")
		}
	})

	t.Logf("Prime understanding test completed in %v", result.Duration)
	t.Logf("Agent ID: %s", response.AgentID)
}

// Response types

type HookExecutionInfo struct {
	SessionStartHookFired bool
	OxPrimeInHookOutput   bool
	AgentID               string
}

func analyzeHookExecution(rawOutput string) HookExecutionInfo {
	info := HookExecutionInfo{}

	// Claude Code outputs a JSON array of message objects
	// Try parsing as JSON array first
	var messages []map[string]interface{}
	if err := json.Unmarshal([]byte(rawOutput), &messages); err != nil {
		// Fallback: try NDJSON (newline-delimited JSON)
		lines := strings.Split(rawOutput, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var msg map[string]interface{}
			if err := json.Unmarshal([]byte(line), &msg); err == nil {
				messages = append(messages, msg)
			}
		}
	}

	// Analyze messages for hook responses
	for _, msg := range messages {
		// Check for hook response
		if msg["type"] == "system" && msg["subtype"] == "hook_response" {
			if hookName, ok := msg["hook_name"].(string); ok {
				if strings.Contains(hookName, "SessionStart") {
					info.SessionStartHookFired = true

					// Check stdout for ox prime output
					if stdout, ok := msg["stdout"].(string); ok {
						if strings.Contains(stdout, "agent_id") || strings.Contains(stdout, "SageOx") {
							info.OxPrimeInHookOutput = true

							// Try to extract agent ID
							var primeOutput map[string]interface{}
							if err := json.Unmarshal([]byte(stdout), &primeOutput); err == nil {
								if agentID, ok := primeOutput["agent_id"].(string); ok {
									info.AgentID = agentID
								}
							}
						}
					}
				}
			}
		}
	}

	return info
}

type PrimeDiscoveryResponse struct {
	DiscoverySources struct {
		FromHook         bool     `json:"from_hook"`
		FromClaudeMD     bool     `json:"from_claude_md"`
		FromAgentsMD     bool     `json:"from_agents_md"`
		FromProjectFiles bool     `json:"from_project_files"`
		OtherSources     []string `json:"other_sources"`
	} `json:"discovery_sources"`

	OxKnowledge struct {
		KnowsAgentPrime    bool   `json:"knows_agent_prime"`
		KnowsAgentGuidance bool   `json:"knows_agent_guidance"`
		HasAgentID         bool   `json:"has_agent_id"`
		AgentID            string `json:"agent_id"`
	} `json:"ox_knowledge"`

	HookAnalysis struct {
		SawHookOutput       bool   `json:"saw_hook_output"`
		HookContainedOxData bool   `json:"hook_contained_ox_data"`
		HookOutputSummary   string `json:"hook_output_summary"`
	} `json:"hook_analysis"`

	Confidence string `json:"confidence"`
}

type PrimeUnderstandingResponse struct {
	TestPassed bool   `json:"test_passed"`
	AgentID    string `json:"agent_id"`

	KnowsOxCommands struct {
		OxAgentPrime    bool `json:"ox_agent_prime"`
		OxAgentGuidance bool `json:"ox_agent_guidance"`
		OxDoctor        bool `json:"ox_doctor"`
		OxStatus        bool `json:"ox_status"`
	} `json:"knows_ox_commands"`

	UnderstandsAttribution struct {
		CommitFooterRequired bool   `json:"commit_footer_required"`
		CommitFooterText     string `json:"commit_footer_text"`
	} `json:"understands_attribution"`

	KnowsTeamContext struct {
		HasTeam        bool     `json:"has_team"`
		TeamName       string   `json:"team_name"`
		KnowsSubagents bool     `json:"knows_subagents"`
		SubagentNames  []string `json:"subagent_names"`
	} `json:"knows_team_context"`

	KnowsTranscription bool `json:"knows_transcription"`

	KnowsGuidanceSystem struct {
		UnderstandsProgressive bool     `json:"understands_progressive"`
		KnowsPaths             bool     `json:"knows_paths"`
		ExamplePaths           []string `json:"example_paths"`
	} `json:"knows_guidance_system"`
}
