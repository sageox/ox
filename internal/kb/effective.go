package kb

// effective.go — precedence + safety-inversion resolver for `ox kb config get/list`.
//
// Layer values are pre-resolved by the caller (CLI reads env, KB config file,
// user config; this function only applies the layering rules so the safety
// inversion invariant on session_recording stays load-bearing in one place).
//
// Precedence for non-recording keys: env > kb > user > default.
// For session_recording.mode the user/kb layers are combined via
// kbconfig.ResolveEffectiveMode, which OR-vetoes "disabled" on either side
// before applying precedence. Env still wins when set; default backstops.

import "github.com/sageox/ox/internal/api/kbconfig"

// KeySessionRecordingMode is the dotted key for the session recording mode.
const (
	KeySessionRecordingMode = "features.session_recording.mode"
	KeyMurmursEnabled       = "features.murmurs.enabled"
	KeyVisibility           = "features.visibility"
)

// Layer scope labels — short, stable, used by --show-origin and JSON output.
const (
	LayerScopeEnv     = "env"
	LayerScopeKB      = "kb"
	LayerScopeUser    = "user"
	LayerScopeDefault = "default"
)

// LayerValue is one layer in the precedence chain.
type LayerValue struct {
	Scope   string // "env" | "kb" | "user" | "default"
	Source  string // human label e.g. "OX_SESSION_RECORDING", ".sageox/config.yaml"
	Value   string // empty if unset at this layer
	Trigger bool   // true when safety-inversion fired *because of* this layer
}

// EffectiveValue is the computed result for one key.
type EffectiveValue struct {
	Key             string
	Effective       string
	Layers          []LayerValue
	SafetyInversion bool
}

// ResolveEffective computes the effective value for a key given pre-resolved
// per-layer string values. The caller is responsible for extracting each
// layer's string from its underlying config; this function applies precedence
// and safety inversion only.
//
// For session_recording.mode: kb/user layers combine via
// kbconfig.ResolveEffectiveMode so a "disabled" on either side vetoes the
// other. Env wins outright when set; default backstops when everything else
// is empty.
//
// For all other keys: env > kb > user > default precedence.
func ResolveEffective(key string, envVal, kbVal, userVal, defaultVal string) EffectiveValue {
	ev := EffectiveValue{Key: key}

	// Record raw layer values first; Trigger/SafetyInversion get stamped below.
	ev.Layers = []LayerValue{
		{Scope: LayerScopeEnv, Value: envVal},
		{Scope: LayerScopeKB, Value: kbVal},
		{Scope: LayerScopeUser, Value: userVal},
		{Scope: LayerScopeDefault, Value: defaultVal},
	}

	if key == KeySessionRecordingMode {
		// Env wins when set (pipelines/automation).
		if envVal != "" {
			ev.Effective = envVal
			return ev
		}
		// Combine kb + user via the safety-inversion-aware resolver.
		combined := kbconfig.ResolveEffectiveMode(userVal, kbVal)
		// Safety inversion fired when one side carries "disabled" AND the
		// other side carries a different non-empty value (i.e. the disabled
		// side actually vetoed an opposing preference). When both sides agree,
		// or only one side is set, there's no inversion to surface.
		oneDisabled := kbVal == kbconfig.SessionRecordingDisabled || userVal == kbconfig.SessionRecordingDisabled
		opposingNonEmpty := (kbVal != "" && userVal != "" && kbVal != userVal)
		if oneDisabled && opposingNonEmpty {
			ev.SafetyInversion = true
			for i := range ev.Layers {
				if (ev.Layers[i].Scope == LayerScopeKB || ev.Layers[i].Scope == LayerScopeUser) &&
					ev.Layers[i].Value == kbconfig.SessionRecordingDisabled {
					ev.Layers[i].Trigger = true
				}
			}
		}
		if combined != "" {
			ev.Effective = combined
			return ev
		}
		ev.Effective = defaultVal
		return ev
	}

	// Standard precedence for all other keys.
	switch {
	case envVal != "":
		ev.Effective = envVal
	case kbVal != "":
		ev.Effective = kbVal
	case userVal != "":
		ev.Effective = userVal
	default:
		ev.Effective = defaultVal
	}
	return ev
}

// KnownKeys is the whitelist of keys `ox kb config get/list` accepts in v1.
var KnownKeys = []string{
	KeySessionRecordingMode,
	KeyMurmursEnabled,
	KeyVisibility,
}

// IsKnownKey reports whether key is in the v1 whitelist.
func IsKnownKey(key string) bool {
	for _, k := range KnownKeys {
		if k == key {
			return true
		}
	}
	return false
}
