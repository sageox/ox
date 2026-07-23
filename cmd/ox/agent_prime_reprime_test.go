package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/claude"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/prime"
	"github.com/spf13/cobra"
)

// fullyLoadedPrimeFixture returns an agentPrimeOutput populated so that,
// when rendered with CompactReprime=false, every entry in
// prime.RequiredFullPrimeDirectives() is present. Mirrors a fully-loaded
// real session: team context with coworkers, ledger, code index, commit
// attribution, murmuring enabled, non-Bronze agent type (claude-code) so
// the full plan-enrichment-guidance tier (including the review loop)
// renders. Each call returns an independent value — callers mutate their
// own copy (e.g. PrimeCallCount, CompactReprime) freely.
func fullyLoadedPrimeFixture() agentPrimeOutput {
	teamCtx := &teamContextInfo{
		TeamID:               "team-1",
		TeamName:             "Test Team",
		Coworkers:            []claude.Agent{{Name: "reviewer", Description: "reviews code"}},
		ObservationGuideHint: "/team/memory/GUIDE.md",
		MemoryContent:        "Team memory: ship small PRs, prefer slog.",
		ReadCommand:          "ox agent team-ctx",
	}
	guidance := prime.BuildGuidance(prime.GuidanceParams{
		AgentID:          "test-agent",
		RepoSlug:         "org/repo",
		TeamCtx:          teamCtx,
		Ledger:           &ledgerInfo{Exists: true},
		CodeDBExists:     true,
		MemoryEnabled:    true,
		MurmuringEnabled: true,
		AgentType:        "claude-code",
	})

	return agentPrimeOutput{
		AgentID:   "test-agent",
		Status:    "fresh",
		AgentType: "claude-code",
		Guidance:  guidance,
		Attribution: config.ResolvedAttribution{
			Commit:         "Co-Authored-By: SageOx <ox@sageox.ai>",
			PR:             "Co-Authored-By: SageOx <ox@sageox.ai>",
			ScoreThreshold: 0.5,
		},
		TeamInstructions: &TeamInstructions{Content: "Follow team conventions."},
		TeamContext:      teamCtx,
		Ledger:           &ledgerInfo{Exists: true},
		Session:          &sessionStatus{Recording: true, Mode: "auto", SessionURL: "https://sageox.ai/session/999"},
		MurmurDirective:  "Murmuring is ENABLED. Proactively publish WIP to teammates. Run your first murmur NOW.",
		AgentTip:         "Use ox code search",
	}
}

// renderXML renders output through the real XML pipeline, matching the
// pattern used throughout agent_prime_xml_test.go.
func renderXML(t *testing.T, out agentPrimeOutput) string {
	t.Helper()
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	if _, err := outputAgentPrimeXML(cmd, out); err != nil {
		t.Fatalf("outputAgentPrimeXML: %v", err)
	}
	return buf.String()
}

// renderXMLBudget renders through the real XML pipeline and returns the
// SageOx-controlled context budget (the tokens ox itself injects), independent
// of how much team content a given fixture carries.
func renderXMLBudget(t *testing.T, out agentPrimeOutput) *prime.ContextBudget {
	t.Helper()
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	budget, err := outputAgentPrimeXML(cmd, out)
	if err != nil {
		t.Fatalf("outputAgentPrimeXML: %v", err)
	}
	return budget
}

// TestPrimeTokenBudget_FullAndCompactStayUnderCeiling is the durable regression
// guard for the compaction win: without an ABSOLUTE ceiling, the static
// preamble can silently regrow over future PRs and the large re-prime saving
// erodes with nothing to catch it. The relative check in TestRePrimeSafety_Matrix
// (compact >=4x smaller than full) would still pass even if BOTH full and
// compact doubled — only an absolute cap catches proportional bloat. Ceilings
// are regression guards, NOT specs: if one trips, prime bloated — trim it, or
// raise the ceiling deliberately with justification in the PR.
func TestPrimeTokenBudget_FullAndCompactStayUnderCeiling(t *testing.T) {
	// Measured sageox budget on the fully-loaded fixture (full ~2669, compact
	// ~263) plus ~20-50% headroom — enough to absorb legitimate small growth,
	// tight enough to catch a preamble regrowth.
	const (
		fullCeiling    = 3200
		compactCeiling = 400
	)

	full := fullyLoadedPrimeFixture()
	full.CompactReprime = false
	fullBudget := renderXMLBudget(t, full).Get("sageox")
	t.Logf("full-prime sageox budget: %d (ceiling %d)", fullBudget, fullCeiling)
	if fullBudget > fullCeiling {
		t.Errorf("FULL prime sageox budget %d exceeds ceiling %d — prime bloated; trim or raise the ceiling deliberately", fullBudget, fullCeiling)
	}

	compact := fullyLoadedPrimeFixture()
	compact.PrimeCallCount = 3
	compact.CompactReprime = true
	compactBudget := renderXMLBudget(t, compact).Get("sageox")
	t.Logf("compact-reprime sageox budget: %d (ceiling %d)", compactBudget, compactCeiling)
	if compactBudget > compactCeiling {
		t.Errorf("COMPACT re-prime sageox budget %d exceeds ceiling %d — the delta bloated; trim or raise the ceiling deliberately", compactBudget, compactCeiling)
	}
}

// TestRequiredFullPrimeDirectives_PresentInFullOutput is the enforcement
// half of the manifest defined in internal/prime/directives.go. The
// structural half (manifest well-formed, entries reachable from pure
// prime-package functions) lives in internal/prime/conformance_test.go —
// but only cmd/ox can render XML, so this is the one test that literally
// proves "every required directive is present in full-prime output," and
// the one that fails if a future edit to agent_prime_xml.go's compaction
// logic accidentally drops a directive from the FULL path.
func TestRequiredFullPrimeDirectives_PresentInFullOutput(t *testing.T) {
	out := fullyLoadedPrimeFixture()
	out.CompactReprime = false
	xml := renderXML(t, out)

	for _, d := range prime.RequiredFullPrimeDirectives() {
		if !strings.Contains(xml, d.Marker) {
			t.Errorf("FULL prime output missing required directive %q (marker %q)", d.Name, d.Marker)
		}
	}
}

// TestRePrimeSafety_Matrix is the belts-and-suspenders proof for bd ox-32f6.
// Each case computes CompactReprime via the REAL prime.ShouldCompactReprime
// decision function (not a hand-set bool) from a (primeCallCount, hookSource)
// pair, then renders — so this test exercises the same decision logic
// runAgentPrime wires into output.CompactReprime, not just the renderer's
// reaction to a pre-decided flag. Three scenarios, matching bd ox-32f6:
//
//	(a) the agent's first prime this window is always FULL.
//	(b) a routine re-prime (PrimeCallCount>1, source NOT clear/compact) is a
//	    compact DELTA: the static+slow-changing tier is entirely absent,
//	    dynamic per-session content survives, output is materially smaller.
//	(c) a clear/compact-triggered re-prime is FULL again, even with a high
//	    PrimeCallCount inherited from prior redundant re-primes — the
//	    safety-critical case where PrimeCallCount alone would wrongly say
//	    "compact" and the source override must win.
func TestRePrimeSafety_Matrix(t *testing.T) {
	tests := []struct {
		name           string
		primeCallCount int
		hookSource     string
		wantCompact    bool
	}{
		{"a) first prime this window", 1, "", false},
		{"b) routine re-prime, direct re-invocation (no hook stdin, e.g. CLAUDE.md BLOCKING)", 2, "", true},
		{"b) routine re-prime, resume source", 3, "resume", true},
		{"c) clear-triggered re-prime, even with a high call count", 6, "clear", false},
		{"c) compact-triggered re-prime, even with a high call count", 9, "compact", false},
	}

	fullFixture := fullyLoadedPrimeFixture()
	fullFixture.CompactReprime = false
	fullFixture.PrimeCallCount = 1
	fullXML := renderXML(t, fullFixture)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCompact := prime.ShouldCompactReprime(tt.primeCallCount, tt.hookSource)
			if gotCompact != tt.wantCompact {
				t.Fatalf("prime.ShouldCompactReprime(%d, %q) = %v, want %v — the decision gate itself disagrees with the scenario this test documents",
					tt.primeCallCount, tt.hookSource, gotCompact, tt.wantCompact)
			}

			out := fullyLoadedPrimeFixture()
			out.PrimeCallCount = tt.primeCallCount
			out.CompactReprime = gotCompact
			xml := renderXML(t, out)

			if gotCompact {
				assertCompactRePrime(t, xml, fullXML)
			} else {
				assertFullRePrime(t, xml)
			}
		})
	}
}

// assertFullRePrime asserts every manifest directive is present and the
// output is not tagged compact — the (a) and (c) cases.
func assertFullRePrime(t *testing.T, xml string) {
	t.Helper()
	if strings.Contains(xml, `mode="compact"`) {
		t.Error("expected FULL prime output, but found mode=\"compact\"")
	}
	for _, d := range prime.RequiredFullPrimeDirectives() {
		if !strings.Contains(xml, d.Marker) {
			t.Errorf("FULL prime output missing required directive %q (marker %q)", d.Name, d.Marker)
		}
	}
}

// staticTierTags are XML tags that only ever render inside the static +
// slow-changing tier (the `if !compact` block in outputAgentPrimeXML).
// A compact re-prime must carry NONE of them.
var staticTierTags = []string{
	"<instructions>",
	"<consult-first>",
	"<rule-promotion-guidance>",
	"<plan-enrichment-guidance>",
	"<decision-record-guidance>",
	"<code-search",
	"<commands",
	"<attribution>",
	"<team-knowledge>",
	"<team-instructions>",
	"<coworkers>",
	"<team-commands>",
	"<memory>",
	"<ledger>",
	"<other-teams>",
	"<capture-prior",
}

// assertCompactRePrime asserts the (b) case: the static tier is entirely
// absent, dynamic per-session content survives ("new murmurs/sessions
// present"), and the output is materially smaller than a full prime of the
// identical fixture. Deliberately does NOT require any
// prime.RequiredFullPrimeDirectives() entry — the manifest is explicitly
// full-prime-only (see its doc comment); the agent already received those
// directives on its earlier prime call this window.
func assertCompactRePrime(t *testing.T, xml, fullXML string) {
	t.Helper()

	if !strings.Contains(xml, `mode="compact"`) {
		t.Error(`expected compact re-prime output to carry mode="compact"`)
	}

	for _, tag := range staticTierTags {
		if strings.Contains(xml, tag) {
			t.Errorf("compact re-prime must omit static-tier tag %s (agent already has it from its earlier prime this window), but found it", tag)
		}
	}

	// dynamic per-session content MUST survive — the "new murmurs/sessions"
	// signal the compact delta exists to deliver: session recording status
	// and URL, the murmur directive, and the session-context/budget
	// scaffolding that carries them.
	dynamicMarkers := []string{
		"<session-context",
		`agent_id="test-agent"`,
		"https://sageox.ai/session/999", // session URL — the "new session" signal
		"Murmuring is ENABLED",          // the murmur directive
		"<context-budget",
	}
	for _, marker := range dynamicMarkers {
		if !strings.Contains(xml, marker) {
			t.Errorf("compact re-prime dropped dynamic content %q — this is exactly what the delta must preserve", marker)
		}
	}

	// materially smaller: the whole point of this tier is a large token
	// reduction on redundant re-primes. Require at least 4x — measured
	// savings on this fixture are ~8x; 4x leaves headroom before this test
	// itself becomes the bottleneck on legitimate small content growth.
	const minReductionFactor = 4
	if len(xml)*minReductionFactor > len(fullXML) {
		t.Errorf("compact re-prime (%d chars) is not materially smaller than full (%d chars) — expected at least %dx reduction",
			len(xml), len(fullXML), minReductionFactor)
	}
}
