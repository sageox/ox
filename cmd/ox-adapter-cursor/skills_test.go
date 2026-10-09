package main

import (
	"reflect"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func TestCursorSkillTargetsAdvertiseSharedProjectProjection(t *testing.T) {
	want := []adapterprotocol.SkillTarget{{
		Key:        "agents-project",
		Root:       ".agents/skills",
		Format:     adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}}
	got := cursorSkillTargets()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cursorSkillTargets() = %#v, want %#v", got, want)
	}

	// Callers may append to the returned inventory without mutating future
	// responses. The host owns cross-adapter deduplication by the stable key.
	got[0].Root = "changed"
	if again := cursorSkillTargets(); !reflect.DeepEqual(again, want) {
		t.Fatalf("cursorSkillTargets returned shared mutable state: %#v", again)
	}
}
