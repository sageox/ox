package kbconfig

// ResolveEffectiveMode combines the user-layer and KB-layer recording modes
// into the single mode that governs a session. THE only function call site
// for combining recording layers — every consumer (ox CLI session start,
// daemon recording gate, future MCP handlers) MUST route through here so
// the safety-inversion invariant cannot be sidestepped.
//
// INVARIANT (load-bearing — privacy guarantee):
//
//	If EITHER userMode == "disabled" OR kbMode == "disabled", the result is
//	"disabled". This is a logical OR, NOT a precedence rule. A user opting
//	out of recording cannot be overridden by a KB admin; a KB admin
//	disabling recording cannot be overridden by an individual user. Both
//	sides have veto power. Standard precedence (user > kb) only applies
//	when neither side is "disabled".
//
// Empty string for either layer means "use the other layer's value";
// callers that want a default should resolve it before calling this
// function (see DefaultSessionRecordingMode).
//
// This function MUST stay byte-identical to mono's
// packages/kb/config.go ResolveEffectiveMode. Drift here is a security bug;
// scripts/check-kbconfig-drift.sh runs in CI to catch it.
func ResolveEffectiveMode(userMode, kbMode string) string {
	// Safety inversion first — either veto wins regardless of precedence.
	// Do NOT move this below the precedence logic; the order IS the
	// semantics.
	if userMode == SessionRecordingDisabled || kbMode == SessionRecordingDisabled {
		return SessionRecordingDisabled
	}
	// Standard precedence: user-layer wins when set. If user didn't set
	// anything, fall back to KB-layer. If neither, return empty (caller
	// should have resolved a default first).
	if userMode != "" {
		return userMode
	}
	return kbMode
}
