package kbconfig

import (
	"strings"
	"testing"
)

// TestUnmarshalConfigYAML_FullEnvelope reads a config payload shaped exactly
// like what mono's MarshalConfigYAML produces (banner + KBConfig body) and
// verifies all known fields decode.
func TestUnmarshalConfigYAML_FullEnvelope(t *testing.T) {
	body := `# THIS FILE IS THE SOURCE OF TRUTH FOR THIS KNOWLEDGE BUBBLE'S CONFIG.
# (banner comments are ignored on parse)
version: 1
features:
  murmurs:
    enabled: true
  visibility: private
  session_recording:
    mode: auto
system: {}
`
	env, err := UnmarshalConfigYAML([]byte(body))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Version != 1 {
		t.Errorf("Version = %d, want 1", env.Version)
	}
	if env.Features.Murmurs.Enabled == nil || !*env.Features.Murmurs.Enabled {
		t.Errorf("Murmurs.Enabled = %v, want true", env.Features.Murmurs.Enabled)
	}
	if env.Features.Visibility != VisibilityPrivate {
		t.Errorf("Visibility = %q, want %q", env.Features.Visibility, VisibilityPrivate)
	}
	if env.Features.SessionRecording.Mode == nil || *env.Features.SessionRecording.Mode != SessionRecordingAuto {
		t.Errorf("SessionRecording.Mode = %v, want %q", env.Features.SessionRecording.Mode, SessionRecordingAuto)
	}
}

// TestUnmarshalConfigYAML_BannerOnlyParsesEmpty verifies that yaml.v3
// ignores leading comments and that an otherwise-empty file decodes to a
// zero-value envelope without erroring.
func TestUnmarshalConfigYAML_BannerOnlyParsesEmpty(t *testing.T) {
	body := `# banner only
# nothing else here
`
	env, err := UnmarshalConfigYAML([]byte(body))
	if err != nil {
		t.Fatalf("unmarshal banner-only: %v", err)
	}
	if env.Version != 0 {
		t.Errorf("expected zero envelope, got Version=%d", env.Version)
	}
}

// TestUnmarshalConfigYAML_ToleratesCommittedAt confirms ox reads (but
// does not require) the legacy committed_at field. Older mono builds
// wrote this; current mono does not.
func TestUnmarshalConfigYAML_ToleratesCommittedAt(t *testing.T) {
	body := `committed_at: 2026-05-19T12:34:56Z
version: 1
features:
  murmurs:
    enabled: true
  visibility: team
  session_recording:
    mode: manual
system: {}
`
	env, err := UnmarshalConfigYAML([]byte(body))
	if err != nil {
		t.Fatalf("unmarshal with committed_at: %v", err)
	}
	if env.CommittedAt == nil {
		t.Error("expected CommittedAt to be parsed, got nil")
	}
	if env.Features.Visibility != VisibilityTeam {
		t.Errorf("Visibility = %q, want %q", env.Features.Visibility, VisibilityTeam)
	}
}

// TestUnmarshalConfigYAML_MalformedReturnsError covers the must-fail case
// for genuinely broken YAML.
func TestUnmarshalConfigYAML_MalformedReturnsError(t *testing.T) {
	body := `features:
  murmurs:
    enabled: [this is, not, valid: at all
`
	if _, err := UnmarshalConfigYAML([]byte(body)); err == nil {
		t.Error("expected error for malformed YAML, got nil")
	}
}

// TestUnmarshalConfigYAML_UnknownFieldsRejected pins the strict-decode
// behavior. mono's MarshalConfigYAML is also strict on read; we match the
// posture so an upstream typo doesn't silently zero out a field.
func TestUnmarshalConfigYAML_UnknownFieldsRejected(t *testing.T) {
	body := `version: 1
features:
  murmurs:
    enabled: true
  visibility: private
  session_recording:
    mode: auto
  totally_made_up_field: 42
`
	_, err := UnmarshalConfigYAML([]byte(body))
	if err == nil {
		t.Error("expected strict decode to reject unknown field, got nil")
		return
	}
	if !strings.Contains(err.Error(), "totally_made_up_field") {
		t.Errorf("error should name the unknown field, got: %v", err)
	}
}

// TestEnvelopeToKBConfig_DropsCommittedAt confirms the projection helper
// strips the read-only envelope field so downstream KBConfig consumers
// don't accidentally depend on it.
func TestEnvelopeToKBConfig_DropsCommittedAt(t *testing.T) {
	body := `committed_at: 2026-05-19T12:34:56Z
version: 1
features:
  murmurs:
    enabled: true
  visibility: private
  session_recording:
    mode: auto
system: {}
`
	env, err := UnmarshalConfigYAML([]byte(body))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cfg := env.ToKBConfig()
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1", cfg.Version)
	}
	if cfg.Features.Visibility != VisibilityPrivate {
		t.Errorf("Visibility = %q, want %q", cfg.Features.Visibility, VisibilityPrivate)
	}
	if cfg.System.ConfigCommittedAt != nil {
		// envelope-level committed_at must NOT leak into SystemConfig
		t.Errorf("ToKBConfig leaked committed_at into System: %v", cfg.System.ConfigCommittedAt)
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("projected config failed validation: %v", err)
	}
}

// TestEnvelopeToKBConfig_NilSafe ensures the helper handles a nil receiver
// gracefully — callers may forward a nil parse result.
func TestEnvelopeToKBConfig_NilSafe(t *testing.T) {
	var env *ConfigYAMLEnvelope
	cfg := env.ToKBConfig()
	if cfg.Version != 0 {
		t.Errorf("nil envelope projection should be zero, got Version=%d", cfg.Version)
	}
}
