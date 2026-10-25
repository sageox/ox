package main

import (
	"sort"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// TestHandleInfo_CapabilitiesPinned proves handleInfo() actually wires
// adapterprotocol.GooseCapabilities — the canonical source in
// pkg/adapterprotocol/capabilities.go, including why it carries no
// file_watcher — into the response.
//
// Every capability listed there MUST have its handler registered in the
// adapterruntime.Config literal in main.go. Declaring a capability without
// wiring its handler makes the subcommand return "not implemented" at runtime
// while every check reports the feature as present — see ox-8arr, where exactly
// that made OpenCode's hook-driven recording silently capture nothing.
func TestHandleInfo_CapabilitiesPinned(t *testing.T) {
	info, err := handleInfo()
	if err != nil {
		t.Fatalf("handleInfo() error: %v", err)
	}

	got := append([]string(nil), info.Capabilities...)
	want := append([]string(nil), adapterprotocol.GooseCapabilities...)
	sort.Strings(got)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("capabilities = %v, want %v", got, want)
		}
	}
}

func TestHandleInfo_Identity(t *testing.T) {
	info, err := handleInfo()
	if err != nil {
		t.Fatalf("handleInfo() error: %v", err)
	}

	if info.Name != "goose" {
		t.Errorf("Name = %q, want goose", info.Name)
	}
	if info.Type != adapterprotocol.TypeSession {
		t.Errorf("Type = %q, want %q", info.Type, adapterprotocol.TypeSession)
	}
	if !info.ServeMode {
		t.Error("ServeMode should be true")
	}
	if len(info.HookEnvValues) != 1 || info.HookEnvValues[0] != "goose" {
		t.Errorf("HookEnvValues = %v, want [goose]", info.HookEnvValues)
	}
	if info.ProtocolVersion != adapterprotocol.ProtocolVersion {
		t.Errorf("ProtocolVersion = %d, want %d", info.ProtocolVersion, adapterprotocol.ProtocolVersion)
	}
}

// TestSkillTargets_SingleCanonicalRoot: goose declares exactly one write root,
// the canonical .agents/skills.
//
// Before this, goose declared no skill targets at all, so `ox init` selected it
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
