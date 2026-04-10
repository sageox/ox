//go:build slow

// Slow tests for ox agent <id> session capture-prior that exercise the
// REAL ox binary. They build ox from source and shell out to it, which is
// the only way to verify cobra's flag parsing accepts the flags consumed
// by the dispatcher's manual parsers (parseTitle, parseCapturePriorFile,
// parseSessionID, parseAdapter).
//
// Why slow: previous unit tests only exercised the parse* helpers as pure
// functions, never the cobra path. That left the original bug — cobra
// rejecting --session-id, --title, --file, --adapter as "unknown flag" —
// invisible to CI for as long as it shipped. These tests are the regression
// gate for that class of failure.
//
// Run with: go test -tags=slow -run TestCobraFlagsAcceptedByCapturePrior ./cmd/ox/

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestCobraFlagsAcceptedByCapturePrior verifies that the real ox binary
// does NOT reject --title, --file, --session-id, or --adapter as unknown
// flags when invoked under `ox agent <id> session capture-prior`.
//
// Failure prevented: a regression that re-removes the agentCmd flag
// registration (or the dispatcher reinjection) would silently break every
// session subcommand that depends on these flags. The original bug only
// surfaced when a user actually tried to use --session-id in production.
//
// We don't care about the command's success — only that cobra accepts the
// flag tokens. We assert the binary fails for some OTHER reason (no agent,
// no recording, etc.) but never with "unknown flag".
func TestCobraFlagsAcceptedByCapturePrior(t *testing.T) {
	oxBin := buildOxBinary(t)

	cases := []struct {
		name string
		args []string
	}{
		{
			name: "session capture-prior --title",
			args: []string{"agent", "Oxslow1", "session", "capture-prior", "--title", "regression test"},
		},
		{
			name: "session capture-prior --file",
			args: []string{"agent", "Oxslow1", "session", "capture-prior", "--file", "/tmp/nonexistent.jsonl"},
		},
		{
			name: "session capture-prior --session-id",
			args: []string{"agent", "Oxslow1", "session", "capture-prior", "--session-id", "00000000-0000-0000-0000-000000000000"},
		},
		{
			name: "session capture-prior --adapter",
			args: []string{"agent", "Oxslow1", "session", "capture-prior", "--adapter", "claude"},
		},
		{
			name: "session capture-prior all flags combined",
			args: []string{
				"agent", "Oxslow1", "session", "capture-prior",
				"--title", "combined",
				"--adapter", "claude-code",
				"--session-id", "00000000-0000-0000-0000-000000000000",
			},
		},
		{
			name: "session capture-prior equals form",
			args: []string{"agent", "Oxslow1", "session", "capture-prior", "--adapter=claude", "--session-id=abc"},
		},
		{
			name: "session start --title",
			args: []string{"agent", "Oxslow1", "session", "start", "--title", "regression test"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(oxBin, tc.args...)
			out, _ := cmd.CombinedOutput()
			combined := string(out)
			if strings.Contains(combined, "unknown flag") {
				t.Errorf("cobra rejected a flag for args %v\noutput:\n%s", tc.args, combined)
			}
		})
	}
}
