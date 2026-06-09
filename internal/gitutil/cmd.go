package gitutil

import (
	"context"
	"os"
	"os/exec"
)

// NewNetworkCmd builds an *exec.Cmd for a git operation that talks to a remote
// (clone, fetch, ls-remote, push). It is the single chokepoint that guarantees
// every network git invocation runs non-interactively.
//
// GIT_TERMINAL_PROMPT=0: ox resolves credentials via the ox-managed credential
// helper, never an interactive prompt. Without this, a credential gap makes git
// prompt for a username on a TTY that the daemon (and doctor fallbacks) don't
// have — the prompt EOFs into a confusing "could not read Username ...
// Input/output error" instead of a clear auth failure.
//
// Use this for every direct exec.Command("git", ...) network call so the env
// hardening can't be forgotten in one path while present in another — the exact
// drift that let the team-context clone prompt non-interactively while the
// ledger clone did not. RunGit applies the same env for calls that route
// through it; this covers the call sites that build their own *exec.Cmd
// (because they need to set Env, capture output differently, etc.).
//
// The caller still sets Dir and appends any credential/protocol/timeout flags.
func NewNetworkCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}
