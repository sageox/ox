package gitutil

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNewNetworkCmd_DisablesPrompt locks in the single invariant the chokepoint
// exists for: every network git command runs non-interactively.
// Failure prevented: a network git exec omits GIT_TERMINAL_PROMPT=0 and a
// credential gap prompts for a username on a TTY-less daemon, EOFing into a
// confusing "could not read Username ... Input/output error" (the original
// team-context clone bug).
func TestNewNetworkCmd_DisablesPrompt(t *testing.T) {
	cmd := NewNetworkCmd(context.Background(), "ls-remote", "origin")

	assert.True(t, slices.Contains(cmd.Env, "GIT_TERMINAL_PROMPT=0"),
		"network git commands must disable the interactive credential prompt")
	// the args are passed through verbatim after the git binary
	assert.Equal(t, []string{"git", "ls-remote", "origin"}, cmd.Args)
}
