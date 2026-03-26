package claude

import (
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
