package kbconfig

import (
	"strings"
	"testing"
)

func TestValidateSessionRecordingMode(t *testing.T) {
	for _, ok := range []string{SessionRecordingAuto, SessionRecordingManual, SessionRecordingDisabled} {
		if err := ValidateSessionRecordingMode(ok); err != nil {
			t.Errorf("ValidateSessionRecordingMode(%q) returned error: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ON", "off", "enabled", "always", "  auto  ", "AUTO"} {
		if err := ValidateSessionRecordingMode(bad); err == nil {
			t.Errorf("ValidateSessionRecordingMode(%q) returned nil, want error", bad)
		}
	}
}

func TestValidateVisibility(t *testing.T) {
	for _, ok := range []string{VisibilityPrivate, VisibilityTeam} {
		if err := ValidateVisibility(ok); err != nil {
			t.Errorf("ValidateVisibility(%q) returned error: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Public", "PRIVATE", "team-internal", " private "} {
		if err := ValidateVisibility(bad); err == nil {
			t.Errorf("ValidateVisibility(%q) returned nil, want error", bad)
		}
	}
}

func TestValidateConfig_AcceptsCanonicalValues(t *testing.T) {
	mode := SessionRecordingAuto
	cfg := KBConfig{
		Version: ConfigVersion,
		Features: FeaturesConfig{
			Visibility:       VisibilityPrivate,
			SessionRecording: SessionRecordingConfig{Mode: &mode},
		},
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("ValidateConfig on canonical input returned: %v", err)
	}
}

func TestValidateConfig_ZeroAndOmittedAreLenient(t *testing.T) {
	// Version=0 (pre-versioned row), missing Mode pointer, empty
	// Visibility all need to pass — ValidateConfig is intentionally
	// lenient on "not set" values.
	if err := ValidateConfig(KBConfig{}); err != nil {
		t.Errorf("ValidateConfig on zero value returned: %v", err)
	}
}

func TestValidateConfig_RejectsBadFields(t *testing.T) {
	t.Run("bad session_recording", func(t *testing.T) {
		bad := "always"
		cfg := KBConfig{Features: FeaturesConfig{SessionRecording: SessionRecordingConfig{Mode: &bad}}}
		err := ValidateConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "session_recording") {
			t.Errorf("expected session_recording error, got %v", err)
		}
	})
	t.Run("bad visibility", func(t *testing.T) {
		cfg := KBConfig{Features: FeaturesConfig{Visibility: "world-readable"}}
		err := ValidateConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "visibility") {
			t.Errorf("expected visibility error, got %v", err)
		}
	})
	t.Run("implausible version", func(t *testing.T) {
		cfg := KBConfig{Version: 9999}
		err := ValidateConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "version") {
			t.Errorf("expected version error, got %v", err)
		}
	})
	t.Run("negative version", func(t *testing.T) {
		cfg := KBConfig{Version: -1}
		err := ValidateConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "version") {
			t.Errorf("expected version error, got %v", err)
		}
	})
}
