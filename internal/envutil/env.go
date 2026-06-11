// Package envutil sanitizes subprocess environments so the daemon and CLI never
// leak secrets (SAGEOX_TOKEN, GITHUB_TOKEN, AWS_*, …) into untrusted or
// long-lived child processes (third-party adapters, user-defined hooks).
//
// It is a leaf package: it imports only the standard library plus
// pkg/adapterprotocol (for the OX_PROTOCOL_VERSION default), so it can be safely
// imported by internal/session/adapters, internal/daemon, and internal/daemon/hooks
// without creating an import cycle.
//
// See ADR-022 §6 (docs/adr/ADR-022-adapter-security-posture.md) for the
// first-party-vs-third-party trust distinction that motivates default-deny
// sanitization.
package envutil

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// allowlistedEnvVars are exact-match variable names always passed to child processes.
var allowlistedEnvVars = map[string]bool{
	"HOME":   true,
	"PATH":   true,
	"TMPDIR": true,
}

// allowlistedEnvPrefixes are prefix patterns; any variable starting with these is passed through.
var allowlistedEnvPrefixes = []string{
	"XDG_",
}

// denylistedEnvPatterns are substrings that block child-declared required env vars.
// Prevents callers from requesting sensitive credentials via self-declared required_env.
var denylistedEnvPatterns = []string{
	"SECRET",
	"TOKEN",
	"KEY",
	"PASSWORD",
	"CREDENTIAL",
	"PRIVATE",
}

// SanitizedEnv builds a sanitized environment for subprocess execution.
// It filters environ (typically os.Environ()) to only include:
//   - Exact-match allowlisted vars: HOME, PATH, TMPDIR
//   - Prefix-match allowlisted vars: XDG_*
//   - OX_* protocol vars (OX_PROTOCOL_VERSION, OX_REPO_ROOT, OX_REPO_ID, OX_TEAM_ID)
//   - Any additional vars declared in requiredEnv (e.g. an adapter's required_env list)
//
// All other variables (API keys, tokens, secrets) are stripped. The denylist
// always wins: a requiredEnv name matching a denylisted pattern is never passed
// through, so a malicious adapter cannot exfiltrate credentials by declaring them.
func SanitizedEnv(environ []string, requiredEnv []string) []string {
	// build a set of caller-declared required env var names, filtering out
	// names that match sensitive patterns to prevent credential exfiltration
	required := make(map[string]bool, len(requiredEnv))
	for _, name := range requiredEnv {
		if isDenylisted(name) {
			continue
		}
		required[name] = true
	}

	env := make([]string, 0, len(allowlistedEnvVars)+len(requiredEnv)+4)

	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}

		// OX_PROTOCOL_VERSION is owned by ox, not inherited. A stale value in the
		// daemon's own environment (e.g. exported by the parent shell) must never
		// reach a child: drop any inherited copy so the compiled value injected
		// below is the single, authoritative entry. On Linux/glibc getenv returns
		// the FIRST matching envp entry, so a passed-through stale copy would
		// shadow a later-appended fresh one — the subtle bug this guards against.
		if name == "OX_PROTOCOL_VERSION" {
			continue
		}

		if isAllowlisted(name) || required[name] {
			env = append(env, entry)
		}
	}

	// Inject the compiled OX_PROTOCOL_VERSION unconditionally (inherited copies
	// were dropped above, so this is always the authoritative value).
	env = append(env, fmt.Sprintf("OX_PROTOCOL_VERSION=%d", adapterprotocol.ProtocolVersion))

	return env
}

// SafeCommand returns an exec.Cmd whose environment is pre-sanitized via
// SanitizedEnv(os.Environ(), nil) — a default-deny convenience for spawning
// untrusted children. Callers needing to pass through a declared required_env
// allowlist should build the command manually and set cmd.Env = SanitizedEnv(...).
func SafeCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = SanitizedEnv(os.Environ(), nil)
	return cmd
}

// isDenylisted returns true if the variable name contains a sensitive pattern.
// Used to prevent caller-declared required env from requesting credentials.
func isDenylisted(name string) bool {
	upper := strings.ToUpper(name)
	for _, pattern := range denylistedEnvPatterns {
		if strings.Contains(upper, pattern) {
			return true
		}
	}
	return false
}

// isAllowlisted returns true if the variable name matches the allowlist.
func isAllowlisted(name string) bool {
	if allowlistedEnvVars[name] {
		return true
	}
	// OX_* protocol vars are always passed through — EXCEPT names matching
	// the credential denylist, which must never leak to child subprocesses
	// regardless of prefix.
	if strings.HasPrefix(name, "OX_") {
		return !isDenylisted(name)
	}
	for _, prefix := range allowlistedEnvPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
