package main

import (
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// TestHandleInfo_CapabilitiesPinned proves handleInfo() actually wires
// adapterprotocol.ClaudeCodeCapabilities — the canonical source in
// pkg/adapterprotocol/capabilities.go — into the response, rather than a
// stale or hand-edited literal. The capability set itself lives in exactly
// one place now; this test only guards the wiring.
func TestHandleInfo_CapabilitiesPinned(t *testing.T) {
	info, err := handleInfo()
	if err != nil {
		t.Fatalf("handleInfo() error: %v", err)
	}
	assertCapabilitySetsEqual(t, "claude-code", info.Capabilities, adapterprotocol.ClaudeCodeCapabilities)
	if len(info.SkillTargets) != 1 || info.SkillTargets[0].Key != "claude-project" || info.SkillTargets[0].Root != ".claude/skills" {
		t.Fatalf("claude skill targets = %#v, want claude-project target", info.SkillTargets)
	}
	if len(info.RuleTargets) != 1 || info.RuleTargets[0].Key != "claude-rules" || info.RuleTargets[0].Root != ".claude/rules" {
		t.Fatalf("claude rule targets = %#v, want claude-rules target", info.RuleTargets)
	}
}

// assertCapabilitySetsEqual compares two capability lists as sets, so the pin
// test does not impose an ordering the conformance fixture (which uses an
// order-insensitive hasCap lookup) is not held to.
func assertCapabilitySetsEqual(t *testing.T, adapter string, got, want []string) {
	t.Helper()
	gotSet := make(map[string]bool, len(got))
	for _, c := range got {
		gotSet[c] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, c := range want {
		wantSet[c] = true
	}
	for c := range wantSet {
		if !gotSet[c] {
			t.Errorf("%s capabilities missing %q (drifted from conformance fixture)\n got: %v\nwant: %v",
				adapter, c, got, want)
		}
	}
	for c := range gotSet {
		if !wantSet[c] {
			t.Errorf("%s capabilities include unexpected %q (drifted from conformance fixture)\n got: %v\nwant: %v",
				adapter, c, got, want)
		}
	}
}
