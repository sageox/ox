package kbconfig

// Session recording modes. Text slugs per project convention (no enums).
// These mirror sageox-mono's packages/kb/config.go constants. Drift checked
// by scripts/check-kbconfig-drift.sh.
const (
	SessionRecordingAuto     = "auto"
	SessionRecordingManual   = "manual"
	SessionRecordingDisabled = "disabled"
)

// ValidSessionRecordingModes is the closed set of allowed values.
var ValidSessionRecordingModes = []string{
	SessionRecordingAuto,
	SessionRecordingManual,
	SessionRecordingDisabled,
}

// Visibility values. Text slugs per project convention (no enums).
const (
	VisibilityPrivate = "private"
	VisibilityTeam    = "team"
)

// ValidVisibilities is the closed set of allowed visibility slugs.
var ValidVisibilities = []string{VisibilityPrivate, VisibilityTeam}

// KB type constants vendored from mono. The canonical ox KBType definition
// lives in internal/api/kb.go (typed as api.KBType); this package keeps a
// parallel set of untyped string constants so kbconfig-internal logic
// (defaults, validation, drift) doesn't pull in the api package and create
// an import cycle.
//
// KBTypeChannel is pre-shipped here ahead of mono's channel rollout per
// /grill-me Q12: defaulting unknown-type-to-manual is the privacy-safe
// behavior, but having the constant lets us hard-code the expected default
// and exercise it in tests.
const (
	KBTypePersonal = "personal"
	KBTypeProfile  = "profile"
	KBTypeTeam     = "team"
	KBTypeRepo     = "repo"
	KBTypeCustom   = "custom"
	KBTypeChannel  = "channel" // pre-shipped before mono rollout
)
