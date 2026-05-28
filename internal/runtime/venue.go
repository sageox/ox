package runtime

// venue.go — raw venue / platform detection. These helpers answer "what
// kind of machine am I on?" with no derived interpretation. The
// capability probe in caps.go composes them; the ephemeral package
// re-exports Reason() for operator-facing output paths so existing
// callers don't have to chase a new package.
//
// Venue detection lives here (not in internal/ephemeral) so the
// capability layer can consume it without forming an import cycle —
// ephemeral now derives from runtime, not the other way around.

import (
	"os"
	"strings"
	"sync/atomic"
)

// EnvEphemeral is the explicit override env var. Setting it to a truthy
// value ("1", "true", "yes", "on" — case-insensitive) forces the runtime
// to treat the environment as ephemeral, downgrading PersistDisk and
// LongLivedHelper regardless of what the venue probes say.
const EnvEphemeral = "OX_EPHEMERAL"

// ReasonUserConfig is the synthetic Reason() value returned when ephemeral
// mode was triggered solely by the user-config layer (no env var, no
// venue marker). Exported so callers that emit structured logs / doctor
// output can match against it without a magic string.
const ReasonUserConfig = "user-config"

// userConfigEphemeral is overridden by the config layer at process start
// to wire in the user's persisted `ephemeral: true|false` preference
// without forming an import cycle. The value is a pointer so we can
// distinguish "unset" (nil) from explicit false. nil = no opinion.
//
// The pointer is atomic because config lookups can happen from concurrent
// goroutines during tests and daemon hot paths; copy the bool on Store so
// callers can pass stack-local pointers safely.
var userConfigEphemeral atomic.Pointer[bool]

// SetUserConfigEphemeralPreference is called by the config-loading code
// to publish the user's persisted `ephemeral` preference. Passing nil
// clears any prior setting.
//
// Lives in runtime (not config) so the capability probe can read it
// without a config → runtime cycle. The ephemeral package re-exports
// this under its historical name SetUserConfigPreference for callers
// that haven't migrated yet.
func SetUserConfigEphemeralPreference(value *bool) {
	if value == nil {
		userConfigEphemeral.Store(nil)
		return
	}
	copy := new(bool)
	*copy = *value
	userConfigEphemeral.Store(copy)
}

// Reason returns the highest-precedence venue / explicit-override signal
// that suggests this is a constrained / ephemeral environment, or "" if
// none. Precedence (highest to lowest):
//
//	OX_EPHEMERAL  >  CLAUDE_CODE_REMOTE  >  DEVIN_TASK_ID  >  user-config
//
// Notably absent: CODESPACES (persistent disk; treat as slow laptop) and
// generic CI markers (writable FS within a job; non-interactive UX is
// driven by internal/config.IsCI(), not by us).
func Reason() string {
	if explicitEphemeral() {
		return EnvEphemeral
	}
	if isClaudeCloud() {
		return "CLAUDE_CODE_REMOTE"
	}
	if isDevin() {
		return "DEVIN_TASK_ID"
	}
	if userConfigSaysOn() {
		return ReasonUserConfig
	}
	return ""
}

// explicitEphemeral returns true when OX_EPHEMERAL is set to a truthy
// value. Empty / "0" / "false" / "no" / "off" all leave the flag off.
func explicitEphemeral() bool {
	v := strings.TrimSpace(os.Getenv(EnvEphemeral))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func userConfigSaysOn() bool {
	value := userConfigEphemeral.Load()
	return value != nil && *value
}

// isClaudeCloud detects Claude Code Cloud (remote sandbox) by the
// presence of CLAUDE_CODE_REMOTE in the environment.
func isClaudeCloud() bool {
	return os.Getenv("CLAUDE_CODE_REMOTE") != ""
}

// isDevin detects a Devin task runner by the presence of DEVIN_TASK_ID.
func isDevin() bool {
	return os.Getenv("DEVIN_TASK_ID") != ""
}

// isCodespaces detects a GitHub Codespace by the CODESPACES=true signal
// the platform injects into every workspace. Kept as a venue helper
// (used by probeLifetime to set LifetimePersistent) — it deliberately
// does NOT contribute to Reason(); see the package doc.
func isCodespaces() bool {
	return os.Getenv("CODESPACES") == "true"
}
