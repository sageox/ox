package kbconfig

import "testing"

// TestResolveEffectiveMode_SafetyInversionMatrix exercises the full 4x4
// matrix of (userMode, kbMode) combinations across {"", auto, manual,
// disabled}. The invariant under test is the safety-inversion property:
// if EITHER side is "disabled", the result is "disabled". Otherwise the
// user layer wins when non-empty, falling back to the KB layer.
//
// This is the privacy-critical test in this package. Any change to
// ResolveEffectiveMode that breaks a case here is, by construction, a
// privacy regression — the corresponding test in mono's
// packages/kb/config_test.go covers the same surface.
func TestResolveEffectiveMode_SafetyInversionMatrix(t *testing.T) {
	const empty = ""
	cases := []struct {
		userMode string
		kbMode   string
		want     string
	}{
		// user = ""
		{empty, empty, empty},
		{empty, SessionRecordingAuto, SessionRecordingAuto},
		{empty, SessionRecordingManual, SessionRecordingManual},
		{empty, SessionRecordingDisabled, SessionRecordingDisabled},
		// user = auto
		{SessionRecordingAuto, empty, SessionRecordingAuto},
		{SessionRecordingAuto, SessionRecordingAuto, SessionRecordingAuto},
		{SessionRecordingAuto, SessionRecordingManual, SessionRecordingAuto},
		{SessionRecordingAuto, SessionRecordingDisabled, SessionRecordingDisabled},
		// user = manual
		{SessionRecordingManual, empty, SessionRecordingManual},
		{SessionRecordingManual, SessionRecordingAuto, SessionRecordingManual},
		{SessionRecordingManual, SessionRecordingManual, SessionRecordingManual},
		{SessionRecordingManual, SessionRecordingDisabled, SessionRecordingDisabled},
		// user = disabled (veto in all columns)
		{SessionRecordingDisabled, empty, SessionRecordingDisabled},
		{SessionRecordingDisabled, SessionRecordingAuto, SessionRecordingDisabled},
		{SessionRecordingDisabled, SessionRecordingManual, SessionRecordingDisabled},
		{SessionRecordingDisabled, SessionRecordingDisabled, SessionRecordingDisabled},
	}
	if len(cases) != 16 {
		t.Fatalf("matrix coverage incomplete: have %d cases, want 16", len(cases))
	}
	for _, c := range cases {
		name := "user=" + c.userMode + "/kb=" + c.kbMode
		if c.userMode == "" {
			name = "user=empty/kb=" + c.kbMode
		}
		if c.kbMode == "" {
			name = "user=" + c.userMode + "/kb=empty"
		}
		if c.userMode == "" && c.kbMode == "" {
			name = "user=empty/kb=empty"
		}
		t.Run(name, func(t *testing.T) {
			got := ResolveEffectiveMode(c.userMode, c.kbMode)
			if got != c.want {
				t.Errorf("ResolveEffectiveMode(%q, %q) = %q, want %q",
					c.userMode, c.kbMode, got, c.want)
			}
		})
	}
}

// TestResolveEffectiveMode_DisabledVetoIsCommutative pins down the
// "either side wins" property symmetrically: swapping the arguments must
// not change the disabled outcome.
func TestResolveEffectiveMode_DisabledVetoIsCommutative(t *testing.T) {
	pairs := [][2]string{
		{SessionRecordingDisabled, SessionRecordingAuto},
		{SessionRecordingDisabled, SessionRecordingManual},
		{SessionRecordingDisabled, ""},
	}
	for _, p := range pairs {
		if ResolveEffectiveMode(p[0], p[1]) != SessionRecordingDisabled {
			t.Errorf("ResolveEffectiveMode(%q, %q) lost veto", p[0], p[1])
		}
		if ResolveEffectiveMode(p[1], p[0]) != SessionRecordingDisabled {
			t.Errorf("ResolveEffectiveMode(%q, %q) lost veto (swapped)", p[1], p[0])
		}
	}
}
