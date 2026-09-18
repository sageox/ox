package main

import "testing"

// TestSkillTargets_SingleCanonicalRoot: amp declares exactly one write root,
// the canonical .agents/skills.
//
// Before this, amp declared no skill targets at all, so `ox init` selected it
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
