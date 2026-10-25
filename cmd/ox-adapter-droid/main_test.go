package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// TestHandleInfo_CapabilitiesPinned proves handleInfo() actually wires
// adapterprotocol.DroidCapabilities — the canonical source in
// pkg/adapterprotocol/capabilities.go — into the response. Rule projection is
// declared separately through RuleTargets, not as an imperative capability.
// The capability set itself lives in exactly one place now; this test only
// guards the wiring.
func TestHandleInfo_CapabilitiesPinned(t *testing.T) {
	info, err := handleInfo()
	if err != nil {
		t.Fatalf("handleInfo() error: %v", err)
	}
	assertCapabilitySetsEqual(t, "droid", info.Capabilities, adapterprotocol.DroidCapabilities)
}

func TestRuleTargets_SingleCanonicalRoot(t *testing.T) {
	info, err := handleInfo()
	if err != nil {
		t.Fatalf("handleInfo: %v", err)
	}
	if len(info.RuleTargets) != 1 || info.RuleTargets[0].Key != "droid-rules" || info.RuleTargets[0].Root != ".factory/rules" {
		t.Fatalf("RuleTargets = %#v, want droid-rules at .factory/rules", info.RuleTargets)
	}
}

// TestReadFromOffset_WiredInOneShotMode drives read-from-offset through the
// real CLI dispatch path (adapterruntime.RunWithArgs against adapterConfig,
// exactly what os.Args[1:] does in main), not the handler function directly.
// A binary declaring adapterprotocol.CapIncrementalReader answered every
// one-shot read-from-offset call with {"error":"read-from-offset not
// implemented"} because Config.ReadFromOffset was never set — the daemon's
// catch-up read on restart (internal/daemon/agentwork/session_watcher.go)
// hit exactly this path and silently dropped every turn written since the
// last persisted offset.
func TestReadFromOffset_WiredInOneShotMode(t *testing.T) {
	var buf bytes.Buffer
	args := []string{"read-from-offset", "--session-file", realFixture, "--offset", "0"}
	if err := adapterruntime.RunWithArgs(adapterConfig, args, nil, &buf); err != nil {
		t.Fatalf("read-from-offset one-shot dispatch failed: %v (output: %s)", err, buf.String())
	}
	if strings.Contains(buf.String(), "not implemented") {
		t.Fatalf("read-from-offset returned %q — Config.ReadFromOffset is not wired in main.go", buf.String())
	}

	var result adapterprotocol.ReadFromOffsetResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("decode read-from-offset result: %v (raw: %s)", err, buf.String())
	}
	if len(result.Entries) == 0 {
		t.Fatal("read-from-offset returned zero entries from a real transcript")
	}
	if result.NewOffset <= 0 {
		t.Fatalf("new_offset = %d, want > 0", result.NewOffset)
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

// TestSkillTargets_SingleCanonicalRoot: droid declares exactly one write root,
// the canonical .agents/skills.
//
// Before this, droid declared no skill targets at all, so `ox init` selected it
// and installed ZERO skills — silently, because an adapter with no targets is
// indistinguishable from one whose skills are already current.
//
// Exactly one target is the assertion, not "at least one": a skill copied into
// several of an agent's discovery paths is several files to keep in sync and
// several answers when they drift.
func TestSkillTargets_SingleCanonicalRoot(t *testing.T) {
	info, err := handleInfo()
	if err != nil {
		t.Fatalf("handleInfo: {%v}", err)
	}
	if len(info.SkillTargets) != 1 {
		t.Fatalf("SkillTargets = %#v, want exactly one canonical root", info.SkillTargets)
	}
	target := info.SkillTargets[0]
	if target.Key != "agents-project" || target.Root != ".agents/skills" {
		t.Errorf("target = %+v, want key agents-project at .agents/skills", target)
	}
	var declared bool
	for _, c := range info.Capabilities {
		if c == "skills_installer" {
			declared = true
		}
	}
	if !declared {
		t.Error("SkillTargets are declared but CapSkillsInstaller is not, so ox init routes to the legacy RPC and installs nothing")
	}
}
