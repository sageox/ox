//go:build integration

package common

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sageox/agentx"
)

// FeatureGroup represents a testable feature area of ox.
// Each group maps to a set of integration tests that validate
// agent compatibility with that feature.
type FeatureGroup string

const (
	FeaturePrime            FeatureGroup = "prime"
	FeatureSessionRecording FeatureGroup = "session-recording"
	FeatureSessionTraces    FeatureGroup = "session-traces"
	FeatureWhispers         FeatureGroup = "whispers"
	FeatureMurmurs          FeatureGroup = "murmurs"
	FeatureDiscussions      FeatureGroup = "discussions"
	FeatureCodeDB           FeatureGroup = "codedb"
	FeatureQuery            FeatureGroup = "query"
	FeatureMCP              FeatureGroup = "mcp"
	FeatureGitHub           FeatureGroup = "github"
	FeatureHooks            FeatureGroup = "hooks"
	FeatureAntiEntropy      FeatureGroup = "anti-entropy"
)

// FeatureGroupDisplayNames maps feature groups to human-readable names
// used in reports and compatibility matrices.
var FeatureGroupDisplayNames = map[FeatureGroup]string{
	FeaturePrime:            "Context Prime",
	FeatureSessionRecording: "Session Recording",
	FeatureSessionTraces:    "Session Traces",
	FeatureWhispers:         "Whispers",
	FeatureMurmurs:          "Murmurs",
	FeatureDiscussions:      "Discussions",
	FeatureCodeDB:           "CodeDB",
	FeatureQuery:            "Query",
	FeatureMCP:              "MCP",
	FeatureGitHub:           "GitHub Integration",
	FeatureHooks:            "Hook Lifecycle",
	FeatureAntiEntropy:      "Anti-Entropy",
}

// AllFeatureGroups lists every defined feature group in display order.
var AllFeatureGroups = []FeatureGroup{
	FeaturePrime,
	FeatureSessionRecording,
	FeatureSessionTraces,
	FeatureWhispers,
	FeatureMurmurs,
	FeatureDiscussions,
	FeatureCodeDB,
	FeatureQuery,
	FeatureMCP,
	FeatureGitHub,
	FeatureHooks,
	FeatureAntiEntropy,
}

// Agent type constants for all agents under integration testing.
// These extend the existing AgentClaude/AgentOpenCode/AgentCodex
// constants from harness.go with agents added for multi-agent coverage.
const (
	AgentGemini    AgentType = "gemini"
	AgentAmp       AgentType = "amp"
	AgentPi        AgentType = "pi"
	AgentCodePuppy AgentType = "code-puppy"
)

// AllAgentTypes lists every agent type that appears in the support matrix.
var AllAgentTypes = []AgentType{
	AgentClaude,
	AgentCodex,
	AgentGemini,
	AgentAmp,
	AgentPi,
	AgentOpenCode,
	AgentCodePuppy,
}

// SupportMatrix declares which features each agent is expected to support.
// Tests for unsupported agent/feature combos are skipped via RequireFeature.
// This is the test-side declaration; the static source of truth for tiers
// and install info lives in test-results/compatibility.json.
var SupportMatrix = map[AgentType][]FeatureGroup{
	AgentClaude: {
		FeaturePrime, FeatureSessionRecording, FeatureSessionTraces,
		FeatureWhispers, FeatureMurmurs, FeatureHooks, FeatureAntiEntropy,
		FeatureCodeDB, FeatureQuery, FeatureMCP, FeatureGitHub, FeatureDiscussions,
	},
	AgentCodex: {
		FeaturePrime, FeatureHooks, FeatureSessionRecording,
	},
	AgentGemini: {
		FeaturePrime, FeatureHooks, FeatureWhispers, FeatureSessionRecording,
	},
	AgentAmp: {
		FeaturePrime,
	},
	AgentPi: {
		FeaturePrime, FeatureMCP,
	},
	AgentOpenCode: {
		FeaturePrime,
	},
	AgentCodePuppy: {
		FeaturePrime,
	},
}

// agentTypeToAgentx maps test AgentType slugs to agentx.AgentType constants.
// Agents without an agentx constant (e.g., gemini) are absent and fall
// through to CLI-based detection.
var agentTypeToAgentx = map[AgentType]agentx.AgentType{
	AgentClaude:    agentx.AgentTypeClaudeCode,
	AgentCodex:     agentx.AgentTypeCodex,
	AgentAmp:       agentx.AgentTypeAmp,
	AgentPi:        agentx.AgentTypePi,
	AgentOpenCode:  agentx.AgentTypeOpenCode,
	AgentCodePuppy: agentx.AgentTypeCodePuppy,
	// AgentGemini intentionally absent -- not yet in agentx
}

// RequireFeature skips the test if the agent does not support the feature.
// Call at the top of any test that targets a specific feature group.
func RequireFeature(t *testing.T, agent AgentType, feature FeatureGroup) {
	t.Helper()

	supported, ok := SupportMatrix[agent]
	if !ok {
		t.Skipf("agent %q has no entry in SupportMatrix", agent)
		return
	}

	for _, f := range supported {
		if f == feature {
			return // supported
		}
	}

	displayName := string(feature)
	if dn, ok := FeatureGroupDisplayNames[feature]; ok {
		displayName = dn
	}
	t.Skipf("agent %q does not support feature %q (%s)", agent, feature, displayName)
}

// RequireAgent skips the test if the agent CLI is not installed.
// Uses agentx.DefaultRegistry for detection when available, with
// fallback to exec.LookPath for agents not yet in the registry.
func RequireAgent(t *testing.T, agent AgentType) {
	t.Helper()

	// try agentx registry first
	if axType, ok := agentTypeToAgentx[agent]; ok {
		if ax, ok := agentx.DefaultRegistry.Get(axType); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			env := agentx.NewSystemEnvironment()
			installed, err := ax.IsInstalled(ctx, env)
			if err != nil {
				t.Skipf("agent %q: detection error: %v", agent, err)
				return
			}
			if !installed {
				t.Skipf("agent %q CLI not installed", agent)
			}
			return
		}
	}

	// fallback: use DefaultAgentConfigs or raw LookPath
	if cfg, ok := DefaultAgentConfigs()[agent]; ok {
		if !CheckAgentAvailable(cfg) {
			t.Skipf("agent %q CLI not available at %q", agent, cfg.CLIPath)
		}
		return
	}

	// last resort: binary name = agent type slug
	SkipIfAgentUnavailable(t, &AgentConfig{
		Type:    agent,
		CLIPath: string(agent),
	})
}

// AgentSupportsFeature reports whether the support matrix declares
// the given agent/feature combination as supported.
func AgentSupportsFeature(agent AgentType, feature FeatureGroup) bool {
	supported, ok := SupportMatrix[agent]
	if !ok {
		return false
	}
	for _, f := range supported {
		if f == feature {
			return true
		}
	}
	return false
}

// DetectAgentVersion returns the installed version string for an agent
// using agentx when available. Returns "unknown" on failure.
func DetectAgentVersion(agent AgentType) string {
	if axType, ok := agentTypeToAgentx[agent]; ok {
		if ax, ok := agentx.DefaultRegistry.Get(axType); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			env := agentx.NewSystemEnvironment()
			if v := ax.DetectVersion(ctx, env); v != "" {
				return v
			}
		}
	}

	// fallback: same approach as benchmark.go detectAgentVersion
	return detectAgentVersionFallback(agent)
}

// detectAgentVersionFallback uses CLI --version for agents not in agentx.
func detectAgentVersionFallback(agent AgentType) string {
	cfg, ok := DefaultAgentConfigs()[agent]
	if !ok {
		return "unknown"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	env := agentx.NewSystemEnvironment()
	out, err := env.Exec(ctx, cfg.CLIPath, "--version")
	if err != nil {
		return "unknown"
	}

	v := string(out)
	if len(v) > 64 {
		v = v[:64]
	}
	return fmt.Sprintf("%s", trimNewline(v))
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
