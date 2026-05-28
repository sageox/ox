// Package ephemeral is the legacy operator-facing facade for the
// capability-based runtime in internal/runtime. New code should query
// the specific capability it needs (runtime.Caps().PersistDisk,
// DaemonViable, Browser, ...) directly. This package remains because:
//
//   - operators grep for `ephemeral_reason=` in logs;
//   - `ox doctor` still surfaces a single "ephemeral: ACTIVE / INACTIVE" line;
//   - the EphemeralHint emitted by `ox agent prime` carries a Reason field;
//   - several config / auth-warning sites genuinely want the coarse
//     "this environment is constrained" predicate.
//
// IsEphemeral() is now derived from runtime capabilities; Reason() and
// SetUserConfigPreference are re-exported from runtime so the original
// call sites keep working without changes. The raw venue helpers
// (isClaudeCloud, isDevin, isCodespaces, the OX_EPHEMERAL env constant)
// live in internal/runtime/venue.go.
//
// IMPORTANT: generic CI signals (CI, GITHUB_ACTIONS, GITLAB_CI, ...) are
// NOT considered ephemeral here. CI runners have writable filesystems and
// within a job their state persists. They trigger non-interactive UX
// (internal/config.IsCI), but keep filesystem persistence — kb merge,
// codedb, etc. continue to work.
package ephemeral

import (
	"github.com/sageox/ox/internal/runtime"
)

// EnvEphemeral re-exports the canonical override env var name so callers
// that built `os.Setenv(ephemeral.EnvEphemeral, "1")` keep working. The
// authoritative declaration is in internal/runtime.
const EnvEphemeral = runtime.EnvEphemeral

// SetUserConfigPreference is the legacy entry point for the config layer
// to publish the user's persisted `ephemeral` preference. New code should
// call runtime.SetUserConfigEphemeralPreference directly.
func SetUserConfigPreference(value *bool) {
	runtime.SetUserConfigEphemeralPreference(value)
}

// IsEphemeral is the derived predicate. It returns true when the runtime
// has either lost persistent disk OR cannot run a daemon — the composite
// "this environment is constrained" answer the operator surfaces
// (`ox doctor`, the EphemeralHint, the auth-expiry warning) are actually
// asking about.
//
// CI runners *do* fire this predicate today, because PersistDisk=true
// but DaemonViable=false (minutes-shaped lifetime). Subsystems that want
// the older "FS doesn't persist" meaning should gate on PersistDisk
// directly — see internal/kb/merge.go.
//
// New subsystem code should query the specific capability it needs
// rather than this composite. If you're tempted to gate a feature on
// IsEphemeral(), ask "which capability does this feature actually
// require?" and gate on that — daemon spawn → DaemonViable, KB local
// cache → PersistDisk, browser auth flow → Browser, etc.
func IsEphemeral() bool {
	c := runtime.Caps()
	return !c.PersistDisk || !c.DaemonViable
}

// Reason returns the venue / override marker that triggered the
// constrained environment, or "" if none. The value space matches the
// historical contract:
//
//	OX_EPHEMERAL  >  CLAUDE_CODE_REMOTE  >  DEVIN_TASK_ID  >  user-config
//
// Useful for structured logging (key=ephemeral_reason). When
// IsEphemeral() is true but Reason() is empty, the capability probe
// inferred the constraint from something other than a known venue marker
// (e.g. OX_NO_DAEMON=1 alone collapses DaemonViable without raising a
// venue Reason — IsEphemeral() returns true; Reason() returns "").
func Reason() string {
	return runtime.Reason()
}
