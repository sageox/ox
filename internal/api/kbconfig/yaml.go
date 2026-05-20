package kbconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"

	"gopkg.in/yaml.v3"
)

// ConfigYAMLPath is the path inside a KB's git repo for the canonical config
// file. Mirrors mono's packages/kb/config_yaml.go ConfigYAMLPath.
const ConfigYAMLPath = ".sageox/config.yaml"

// ConfigYAMLEnvelope is the on-disk shape of .sageox/config.yaml in a KB
// tree (NOT the project-side binding file). Mirrors mono's envelope.
//
// ox reads this format; ox does NOT write it. Writes are server-side
// (mono reconcile) or hand-edit only. The banner header that mono prepends
// is a YAML comment — yaml.v3 ignores comments on parse so no special
// handling is needed.
//
// committed_at is tolerated on read for forward/backward compatibility with
// older mono builds that may have written the field; it is intentionally
// surfaced at the envelope level (rather than embedded inside SystemConfig)
// because mono's current schema places it at the file root. ox callers
// should ignore this value — git history is the canonical source for "when
// did this last change".
type ConfigYAMLEnvelope struct {
	// CommittedAt is tolerated on read. Treat as advisory metadata only.
	CommittedAt *time.Time `yaml:"committed_at,omitempty"`

	Version  int            `yaml:"version,omitempty"`
	Features FeaturesConfig `yaml:"features"`
	System   SystemConfig   `yaml:"system,omitempty"`
}

// UnmarshalConfigYAML parses a .sageox/config.yaml body into an envelope.
//
// Strict by default via KnownFields(true) — mirrors mono's read-side
// strictness so a typo or wrong field name surfaces immediately rather than
// silently zeroing out a value. The envelope itself is intentionally a
// superset of mono's KBConfig (it carries committed_at), so the strict
// decode still tolerates files written by mono today.
func UnmarshalConfigYAML(data []byte) (*ConfigYAMLEnvelope, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var env ConfigYAMLEnvelope
	if err := dec.Decode(&env); err != nil {
		// yaml.v3 returns io.EOF when the input has no YAML nodes (e.g.
		// banner-only file with all comments). Treat that as a zero-value
		// envelope rather than a parse error — the caller can still apply
		// defaults and validate the result.
		if errors.Is(err, io.EOF) {
			return &env, nil
		}
		return nil, fmt.Errorf("kbconfig: unmarshal config yaml: %w", err)
	}
	// Reject trailing YAML documents to avoid ambiguous/ignored config
	// payloads. KnownFields(true) only covers stray keys inside a single
	// document; a second `---`-separated doc would otherwise be silently
	// dropped on the floor.
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("kbconfig: unmarshal config yaml: trailing YAML document is not allowed")
		}
		return nil, fmt.Errorf("kbconfig: unmarshal config yaml: %w", err)
	}
	return &env, nil
}

// ToKBConfig projects an envelope onto the canonical KBConfig type, dropping
// the read-only committed_at envelope field. Callers that need the parsed
// KBConfig (for ValidateConfig, ResolveEffectiveMode inputs, etc.) should
// go through this method so committed_at semantics stay isolated to the
// envelope.
func (e *ConfigYAMLEnvelope) ToKBConfig() KBConfig {
	if e == nil {
		return KBConfig{}
	}
	return KBConfig{
		Version:  e.Version,
		Features: e.Features,
		System:   e.System,
	}
}
