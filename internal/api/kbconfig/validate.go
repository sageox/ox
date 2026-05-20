package kbconfig

import "fmt"

// ValidateConfig is the semantic gate for a parsed KBConfig. Every code path
// that reads a config from disk or the wire and intends to act on it should
// route through here. ox does not write configs, but ValidateConfig still
// runs on every read so an upstream-malformed value surfaces before
// downstream code makes a decision based on it.
//
// Intentionally LENIENT on unknown fields — forward compat for fields a
// newer mono build wrote that this ox binary doesn't know about. The YAML /
// JSON decoder enforces structure; ValidateConfig enforces semantics of
// known fields.
//
// Returns a wrapped error naming the offending field so the message threads
// through to UI / log without ambiguity.
func ValidateConfig(c KBConfig) error {
	if c.Features.SessionRecording.Mode != nil {
		if err := ValidateSessionRecordingMode(*c.Features.SessionRecording.Mode); err != nil {
			return fmt.Errorf("features.session_recording: %w", err)
		}
	}
	if c.Features.Visibility != "" {
		if err := ValidateVisibility(c.Features.Visibility); err != nil {
			return fmt.Errorf("features.visibility: %w", err)
		}
	}
	// Negative versions or absurdly-future versions almost certainly
	// indicate a malformed file or wire-format attack — reject. Zero is
	// tolerated (treated as "pre-versioned row" by readers).
	if c.Version < 0 || c.Version > ConfigVersion+10 {
		return fmt.Errorf("version: implausible value %d (current=%d)", c.Version, ConfigVersion)
	}
	return nil
}

// ValidateSessionRecordingMode returns nil if m is one of the known mode
// slugs. Empty string is rejected — callers that want "not set" should not
// invoke validation in the first place (Mode is a *string for that reason).
func ValidateSessionRecordingMode(m string) error {
	for _, v := range ValidSessionRecordingModes {
		if m == v {
			return nil
		}
	}
	return fmt.Errorf("invalid session_recording mode %q (allowed: %v)", m, ValidSessionRecordingModes)
}

// ValidateVisibility returns nil if v is a known visibility slug. Empty
// string is rejected — Visibility uses omitempty on its tag, so an unset
// value never reaches validation.
func ValidateVisibility(v string) error {
	switch v {
	case VisibilityPrivate, VisibilityTeam:
		return nil
	default:
		return fmt.Errorf("invalid visibility %q (allowed: %q, %q)", v, VisibilityPrivate, VisibilityTeam)
	}
}
