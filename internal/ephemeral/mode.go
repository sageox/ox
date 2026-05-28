// Package ephemeral provides a single source of truth for detecting when ox
// is running inside an ephemeral environment (Claude Code Cloud, Devin,
// GitHub Codespaces, user-config opt-in, or explicit env override).
// Subsystems should consult IsEphemeral() instead of growing their own
// ad-hoc env-var checks.
//
// The mode is named "ephemeral" because the *behavior* is ephemeral (no
// persistent daemon, no local ledger clone, no on-disk caches). The venue
// may still be a cloud agent — that's just one of several triggers.
//
// IMPORTANT: generic CI signals (CI, GITHUB_ACTIONS, GITLAB_CI, ...) are NOT
// considered ephemeral here. CI runners have writable filesystems and within
// a job their state persists. They do trigger non-interactive UX, but that's
// driven separately by internal/config.IsCI() — keep the two concepts
// distinct or kb merger / codedb / etc. break in CI test environments.
//
// Consumers today:
//   - internal/config: NoInteractive auto-fires when ephemeral OR isCI
//   - internal/daemon: short-circuits daemon spawn in ephemeral mode
//   - internal/kb:     skips the kb merger's network fan-out in ephemeral mode
//   - (future) internal/codedb: skips Bleve indexer startup
//
// Precedence for Reason() (highest to lowest):
//
//	OX_EPHEMERAL  >  CLAUDE_CODE_REMOTE  >  DEVIN_TASK_ID  >  CODESPACES  >  user-config
//
// Setting OX_EPHEMERAL=0 (or "false"/"no") does NOT force-disable ephemeral
// mode — other signals (Codespaces, user-config opt-in) still take effect.
// To force non-ephemeral, unset every signal and set the user-config value
// to false. The "explicit override" is one-way: on.
package ephemeral

import (
	"os"
	"strings"
	"sync/atomic"
)

// EnvEphemeral is the explicit override env var. Setting it to a truthy
// value ("1", "true", "yes" — case-insensitive) forces ephemeral mode on,
// even if no other signal is present.
const EnvEphemeral = "OX_EPHEMERAL"

// reasonUserConfig is the synthetic reason name returned when ephemeral
// mode was triggered solely by the user-config layer. Not an env var.
const reasonUserConfig = "user-config"

// userConfigEphemeral is overridden by the config layer at process start
// to wire in the user's persisted `ephemeral: true|false` preference
// without forming an import cycle. The value is a pointer so we can
// distinguish "unset" (nil) from explicit false. nil = no opinion.
//
// The pointer is atomic because config lookups can happen from concurrent
// goroutines during tests and daemon hot paths; copy the bool on Store so
// callers can pass stack-local pointers safely.
var userConfigEphemeral atomic.Pointer[bool]

// SetUserConfigPreference is called by the config-loading code (which
// imports this package, so the dep arrow stays one-way) to publish the
// user-config value. Passing nil clears any prior setting.
func SetUserConfigPreference(value *bool) {
	if value == nil {
		userConfigEphemeral.Store(nil)
		return
	}
	copy := new(bool)
	*copy = *value
	userConfigEphemeral.Store(copy)
}

// IsEphemeral returns true when ox is running in a short-lived cloud agent
// or CI environment and should disable interactive UI, background daemons,
// and unnecessary network fan-out.
func IsEphemeral() bool {
	return Reason() != ""
}

// Reason returns the source name that triggered ephemeral mode, or "" if
// not ephemeral. Useful for structured logging (key=ephemeral_reason).
// The returned value is either an env var name, a venue marker, or the
// synthetic "user-config" string.
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
	if isCodespaces() {
		return "CODESPACES"
	}
	if userConfigSaysOn() {
		return reasonUserConfig
	}
	return ""
}

// explicitEphemeral returns true when OX_EPHEMERAL is set to a truthy value.
// Empty / "0" / "false" / "no" / "off" all leave the flag off so the value
// behaves like a standard boolean toggle.
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

// userConfigSaysOn returns true when the user-config layer has explicitly
// opted into ephemeral mode. nil and explicit false both return false.
// It is the lowest-precedence signal: any higher source (env var, venue
// marker, CI) overrides it. An explicit `ephemeral: false` in user config
// simply does not contribute — it neither forces on nor forces off.
func userConfigSaysOn() bool {
	value := userConfigEphemeral.Load()
	return value != nil && *value
}

// isClaudeCloud detects Claude Code Cloud (remote sandbox) by the presence
// of CLAUDE_CODE_REMOTE in the environment.
func isClaudeCloud() bool {
	return os.Getenv("CLAUDE_CODE_REMOTE") != ""
}

// isDevin detects a Devin task runner by the presence of DEVIN_TASK_ID.
func isDevin() bool {
	return os.Getenv("DEVIN_TASK_ID") != ""
}

// isCodespaces detects a GitHub Codespace by the CODESPACES=true signal
// that the platform injects into every workspace.
func isCodespaces() bool {
	return os.Getenv("CODESPACES") == "true"
}
