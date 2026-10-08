package main

import (
	"strings"
	"testing"
)

// TestIntegrateInstallHint_ResolvesToRealCommand guards #1257. Doctor hints are
// free text, so a moved command or a display-name flag ("--gemini cli") turns
// them into dead ends that only a user ever finds. Every flag a hook check or
// proactive tip passes must resolve to a real `ox integrate install` flag.
func TestIntegrateInstallHint_ResolvesToRealCommand(t *testing.T) {
	// the flags passed by check{OpenCode,Gemini,Codex,Amp}Hooks and doctor_proactive.go
	for _, flag := range []string{"opencode", "gemini", "codex", "amp"} {
		t.Run(flag, func(t *testing.T) {
			hint := integrateInstallHint(flag)
			args := strings.Fields(hint)
			if len(args) < 2 || args[0] != "ox" {
				t.Fatalf("hint must be an ox command, got %q", hint)
			}

			cmd, rest, err := rootCmd.Find(args[1:])
			if err != nil {
				t.Fatalf("hint %q does not resolve to a command: %v", hint, err)
			}
			if got := cmd.CommandPath(); got != "ox integrate install" {
				t.Fatalf("hint %q resolved to %q, want \"ox integrate install\"", hint, got)
			}
			if len(rest) != 1 || !strings.HasPrefix(rest[0], "--") {
				t.Fatalf("hint %q should carry exactly one --flag, got %q", hint, rest)
			}
			if cmd.Flags().Lookup(strings.TrimPrefix(rest[0], "--")) == nil {
				t.Errorf("hint %q names a flag `ox integrate install` does not have", hint)
			}
		})
	}
}
