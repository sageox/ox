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
	// caller args are passed through verbatim, after ox's own -c hardening
	assert.Equal(t, []string{"ls-remote", "origin"}, cmd.Args[len(cmd.Args)-2:])
}

// TestNewNetworkCmd_DisablesCommitSigning covers the OTHER half of running
// non-interactively: a git command that COMMITS must not stop for a signing
// passphrase. `git pull --rebase` (internal/daemon/sync_managed.go) is the
// network command that commits.
//
// Failure prevented: on a host whose git config enables passphrase-protected
// commit signing, the daemon's pull-rebase commit dies for want of a TTY and
// leaves the rebase halted with a CLEAN, conflict-free index — which is
// byte-identical to the upstream-equivalent-commit halt that
// ResolveRebaseAcceptTheirs skips. Skipping it would silently discard content
// that was resolved but never recorded.
func TestNewNetworkCmd_DisablesCommitSigning(t *testing.T) {
	// asserted across the whole surface, not just the pull path: the point of
	// the chokepoint is that a future network command that commits inherits
	// the hardening rather than having to remember it.
	// Asserts the COMPLETE argv, not just membership: `-c` is positional and
	// must immediately precede its key=value, so a slices.Contains check would
	// still pass on an unpaired or misordered flag that git silently rejects.
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{
			args: []string{"pull", "--rebase", "--autostash"},
			want: []string{"git", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "pull", "--rebase", "--autostash"},
		},
		{
			args: []string{"-C", "/tmp/example", "fetch", "--quiet"},
			want: []string{"git", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-C", "/tmp/example", "fetch", "--quiet"},
		},
		{
			args: []string{"ls-remote", "origin"},
			want: []string{"git", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "ls-remote", "origin"},
		},
	} {
		cmd := NewNetworkCmd(context.Background(), tc.args...)
		assert.Equal(t, tc.want, cmd.Args,
			"signing must be disabled ahead of the caller's verbatim args for %v", tc.args)
	}
}
