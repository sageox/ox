package kbconfig

import "testing"

func TestDefaultSessionRecordingMode_PerType(t *testing.T) {
	cases := map[string]string{
		KBTypePersonal: SessionRecordingAuto,
		KBTypeTeam:     SessionRecordingAuto,
		KBTypeRepo:     SessionRecordingAuto,
		KBTypeProfile:  SessionRecordingManual,
		KBTypeCustom:   SessionRecordingManual,
		KBTypeChannel:  SessionRecordingManual,
	}
	for kt, want := range cases {
		t.Run(kt, func(t *testing.T) {
			if got := DefaultSessionRecordingMode(kt); got != want {
				t.Errorf("DefaultSessionRecordingMode(%q) = %q, want %q", kt, got, want)
			}
		})
	}
}

// TestDefaultSessionRecordingMode_UnknownTypeFallsBackToManual asserts the
// privacy-safe fallback for forward-compat KB types ox doesn't recognize
// yet. Manual is the conservative choice — auto-recording an unknown bubble
// type would surprise contributors.
func TestDefaultSessionRecordingMode_UnknownTypeFallsBackToManual(t *testing.T) {
	for _, unknown := range []string{"", "future-type", "garbage", "PERSONAL"} {
		if got := DefaultSessionRecordingMode(unknown); got != SessionRecordingManual {
			t.Errorf("DefaultSessionRecordingMode(%q) = %q, want %q (fallback)",
				unknown, got, SessionRecordingManual)
		}
	}
}
