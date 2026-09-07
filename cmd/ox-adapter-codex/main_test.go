package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// TestHandleInfo_CapabilitiesPinned pins this binary's declared capabilities to
// the set the cross-agent conformance fixture mirrors
// (internal/prime/conformance_test.go). Codex declares NEITHER a commands nor a
// rules installer. Skills are a portable Layer-2 surface installed into
// .agents/skills. If handleInfo() drifts, this
// fails so the conformance fixture cannot silently fall out of sync.
//
// KEEP IN SYNC: the want set below must match the "codex" entry in adapterCaps
// in internal/prime/conformance_test.go. Adding/removing a capability requires
// updating BOTH places. Comparison is order-insensitive (a set) so the two
// fixtures need not list caps in the same order.
func TestHandleInfo_CapabilitiesPinned(t *testing.T) {
	want := []string{
		adapterprotocol.CapSessionReader,
		adapterprotocol.CapHookInstaller,
		adapterprotocol.CapSkillsInstaller,
		adapterprotocol.CapIncrementalReader,
		adapterprotocol.CapFileWatcher,
		adapterprotocol.CapServeMode,
		adapterprotocol.CapSessionImporter,
	}

	info, err := handleInfo()
	if err != nil {
		t.Fatalf("handleInfo() error: %v", err)
	}
	assertCapabilitySetsEqual(t, "codex", info.Capabilities, want)
	if len(info.SkillTargets) != 1 || info.SkillTargets[0].Key != "agents-project" || info.SkillTargets[0].Root != ".agents/skills" {
		t.Fatalf("codex skill targets = %#v, want shared agents-project target", info.SkillTargets)
	}
}

func TestInstallSkills_WritesCanonicalAgentSkills(t *testing.T) {
	repo := t.TempDir()
	var out bytes.Buffer
	if err := adapterruntime.RunWithArgs(adapterConfig, []string{"install-skills", "--repo-root", repo, "--version", "1.0.0", "--skill", "ox-cli-attest-goal"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".agents", "skills", "ox-cli-attest-goal", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "---\nname: ox-cli-attest-goal") {
		t.Fatal("installed skill did not retain canonical frontmatter")
	}
}

// TestReadFromOffset_WiredInOneShotMode drives read-from-offset through the
// real CLI dispatch path (adapterruntime.RunWithArgs against adapterConfig,
// exactly what os.Args[1:] does in main), not the handler function directly.
// A binary declaring adapterprotocol.CapIncrementalReader answered every
// one-shot read-from-offset call with {"error":"read-from-offset not
// implemented"} because Config.ReadFromOffset was never set — Codex's
// PostToolUse hook shells out to `ox agent hook`, a fresh subprocess per
// call, and the daemon's catch-up read on restart
// (internal/daemon/agentwork/session_watcher.go) hits the same one-shot
// path, so both silently dropped every turn written since the last
// persisted offset.
func TestReadFromOffset_WiredInOneShotMode(t *testing.T) {
	var buf bytes.Buffer
	args := []string{"read-from-offset", "--session-file", "testdata/session-real.jsonl", "--offset", "0"}
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
