package claude

import (
	"regexp"
	"strings"

	"github.com/sageox/ox/internal/constants"
)

// IsOxPrimeCommand checks if a command is any variant of ox agent prime.
// Recognizes both legacy commands (without AGENT_ENV) and new commands (with AGENT_ENV prefix).
func IsOxPrimeCommand(cmd string) bool {
	return cmd == constants.OxPrimeCommandClaudeCode ||
		cmd == constants.OxPrimeCommand ||
		strings.Contains(cmd, "ox agent prime")
}

// IsOxHookCommand checks if a command is any variant of ox agent hook.
func IsOxHookCommand(cmd string) bool {
	return strings.Contains(cmd, "ox agent hook")
}

// IsAnyOxCommand checks if a command is any ox hook command (prime or lifecycle hook).
func IsAnyOxCommand(cmd string) bool {
	return IsOxPrimeCommand(cmd) || IsOxHookCommand(cmd)
}

// oxStatuslineRe matches "ox statusline" only in command position: at the
// start of the command, after a shell operator, or after a keyword that
// introduces a command ("if …; then ox statusline"), optionally behind a path
// ("/usr/local/bin/ox statusline", "cd /repo && ox statusline"). An argument
// ("echo ox statusline") or another binary ("fox statusline",
// "~/bin/box statusline") is foreign.
var oxStatuslineRe = regexp.MustCompile(`(^|[;&|({\n]|\b(then|else|do|exec)\s)\s*(\S*/)?ox\s+statusline(\s|$)`)

// IsOxStatuslineCommand reports whether a statusLine command runs ox's status
// line (any version of the installed command, or a hand-written one). A
// foreign command that merely ends in "ox statusline" is not ox's.
func IsOxStatuslineCommand(cmd string) bool {
	return oxStatuslineRe.MatchString(cmd)
}
