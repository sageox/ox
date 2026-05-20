// Package kbconfig vendors the canonical KBConfig Go types from sageox-mono's
// packages/kb/config.go into the ox CLI. The mono package is the source of
// truth — ox imports nothing from mono (mono is not a Go module dependency),
// so this is a hand-maintained vendored copy.
//
// Drift between this file and packages/kb/config.go in mono is a security
// concern: ResolveEffectiveMode encodes a privacy invariant (safety
// inversion on session_recording) and field names participate in the wire
// format read by ox. Run scripts/check-kbconfig-drift.sh on every PR that
// touches this directory.
//
// ox is a READER of the KB config envelope. The mono service writes
// .sageox/config.yaml during reconcile; the public PATCH /config endpoint
// validates inputs. ox does NOT write the envelope and does NOT carry the
// banner-comment generation or the committed_at field on writes.
package kbconfig

import "time"

// ConfigVersion mirrors mono's packages/kb/config.go ConfigVersion. Bump only
// when the schema semantics change (additive fields are backward-compatible).
const ConfigVersion = 1

// KBConfig is the per-bubble dynamic configuration. Two namespaces with
// different write paths in mono:
//
//   - Features (user-facing) — owners toggle these from the KB settings page.
//   - System (internal)      — set by mono workflows / watchman only.
//
// ox reads both namespaces; ox never writes either.
type KBConfig struct {
	// Version tracks the schema shape so future migrations can detect old
	// payloads. Reads tolerate Version==0 (treat as current).
	Version int `json:"version" yaml:"version"`

	// Features is the user-controllable namespace.
	Features FeaturesConfig `json:"features" yaml:"features"`

	// System is the internal namespace. Mono mutates it via internal code
	// paths only; ox treats it as read-only and tolerates new fields.
	System SystemConfig `json:"system" yaml:"system"`
}

// FeaturesConfig is the user-facing toggle set. Mirrors mono.
type FeaturesConfig struct {
	// Murmurs controls the murmur feature on this bubble.
	Murmurs MurmursConfig `json:"murmurs" yaml:"murmurs"`

	// Visibility controls bubble discoverability. See VisibilityPrivate /
	// VisibilityTeam. Empty string is treated as VisibilityPrivate by
	// downstream consumers.
	Visibility string `json:"visibility,omitempty" yaml:"visibility,omitempty"`

	// SessionRecording controls AI-coworker session recording for the
	// bubble. Tri-state per ox CLI's existing vocabulary so the two
	// surfaces stay in lockstep. Defaults vary by KB type — see
	// DefaultSessionRecordingMode.
	SessionRecording SessionRecordingConfig `json:"session_recording" yaml:"session_recording"`
}

// SessionRecordingConfig holds the per-bubble recording policy.
//
// Mode is *string so a patch can distinguish "field omitted, keep current
// value" from "explicitly set". After defaults are applied the pointer is
// non-nil.
type SessionRecordingConfig struct {
	Mode *string `json:"mode,omitempty" yaml:"mode,omitempty"`
}

// MurmursConfig holds murmur-feature toggles. Enabled is a *bool for the same
// "omitted vs explicit false" reason as SessionRecordingConfig.Mode.
type MurmursConfig struct {
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// SystemConfig is the internal namespace.
//
// Mono's current SystemConfig is an empty struct — the earlier
// ConfigCommittedAt field was removed in favor of relying on git history.
// ox keeps a tolerated, read-only ConfigCommittedAt pointer here so that
// envelopes written by older mono builds (or by hand-edited test files)
// don't trip strict-unmarshal errors. ox never writes this field; it is
// preserved on read for forward/backward compatibility and ignored by all
// downstream consumers. See bead ox-jzlm and the [HUMAN] pushback bead.
type SystemConfig struct {
	// ConfigCommittedAt is tolerated on read. ox does not populate it on
	// any write path. Treat as advisory metadata only — git history is the
	// canonical "when did this last change" source.
	ConfigCommittedAt *time.Time `json:"config_committed_at,omitempty" yaml:"config_committed_at,omitempty"`
}
