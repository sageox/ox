package kb

import (
	"testing"

	"github.com/sageox/ox/internal/api/kbconfig"
)

func TestResolveEffective_Precedence_NonRecording(t *testing.T) {
	tests := []struct {
		name        string
		env, kb, u  string
		def         string
		wantEffectv string
	}{
		{"env wins", "team", "private", "", "private", "team"},
		{"kb beats user", "", "team", "private", "private", "team"},
		{"user beats default", "", "", "team", "private", "team"},
		{"default fallback", "", "", "", "private", "private"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := ResolveEffective(KeyVisibility, tt.env, tt.kb, tt.u, tt.def)
			if ev.Effective != tt.wantEffectv {
				t.Errorf("Effective=%q want=%q", ev.Effective, tt.wantEffectv)
			}
			if ev.SafetyInversion {
				t.Errorf("non-recording key should never set SafetyInversion")
			}
		})
	}
}

func TestResolveEffective_SessionRecording_SafetyInversion_KBVetoes(t *testing.T) {
	// user wants auto, but kb says disabled — kb vetoes.
	ev := ResolveEffective(KeySessionRecordingMode, "", "disabled", "auto", "manual")
	if ev.Effective != kbconfig.SessionRecordingDisabled {
		t.Fatalf("Effective=%q want disabled", ev.Effective)
	}
	if !ev.SafetyInversion {
		t.Fatalf("expected SafetyInversion=true")
	}
	var kbLayer *LayerValue
	for i := range ev.Layers {
		if ev.Layers[i].Scope == LayerScopeKB {
			kbLayer = &ev.Layers[i]
		}
	}
	if kbLayer == nil || !kbLayer.Trigger {
		t.Errorf("expected kb layer to be Trigger=true")
	}
}

func TestResolveEffective_SessionRecording_SafetyInversion_UserVetoes(t *testing.T) {
	// kb wants auto, but user says disabled — user vetoes.
	ev := ResolveEffective(KeySessionRecordingMode, "", "auto", "disabled", "manual")
	if ev.Effective != kbconfig.SessionRecordingDisabled {
		t.Fatalf("Effective=%q want disabled", ev.Effective)
	}
	if !ev.SafetyInversion {
		t.Fatalf("expected SafetyInversion=true")
	}
}

func TestResolveEffective_SessionRecording_EnvWins(t *testing.T) {
	// env is set — wins outright, no safety inversion (env is trusted as
	// pipeline override).
	ev := ResolveEffective(KeySessionRecordingMode, "auto", "disabled", "disabled", "manual")
	if ev.Effective != "auto" {
		t.Fatalf("Effective=%q want auto", ev.Effective)
	}
	if ev.SafetyInversion {
		t.Fatalf("expected SafetyInversion=false when env overrides")
	}
}

func TestResolveEffective_SessionRecording_UserBeatsKBWhenNeitherDisabled(t *testing.T) {
	ev := ResolveEffective(KeySessionRecordingMode, "", "manual", "auto", "manual")
	if ev.Effective != "auto" {
		t.Fatalf("Effective=%q want auto", ev.Effective)
	}
	if ev.SafetyInversion {
		t.Errorf("no safety inversion when neither side is disabled")
	}
}

func TestResolveEffective_SessionRecording_BothEmpty_UseDefault(t *testing.T) {
	ev := ResolveEffective(KeySessionRecordingMode, "", "", "", "manual")
	if ev.Effective != "manual" {
		t.Fatalf("Effective=%q want manual (default)", ev.Effective)
	}
}

func TestIsKnownKey(t *testing.T) {
	if !IsKnownKey(KeySessionRecordingMode) {
		t.Errorf("session_recording.mode should be known")
	}
	if !IsKnownKey(KeyMurmursEnabled) {
		t.Errorf("murmurs.enabled should be known")
	}
	if !IsKnownKey(KeyVisibility) {
		t.Errorf("visibility should be known")
	}
	if IsKnownKey("features.bogus") {
		t.Errorf("bogus key should not be known")
	}
}
