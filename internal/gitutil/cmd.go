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
//
// LC_ALL=C / LANG=C: matches RunGit's env — several callers substring-match
// git's output to classify failures (non-fast-forward, LFS, auth), which
// breaks silently on a host whose locale renders git's messages translated.
// See RunGit's comment in run.go for the full rationale.
//
// commit.gpgsign=false / tag.gpgsign=false: also matches RunGit (run.go).
// Most network commands never commit, so this reads like a no-op — but
// `git pull --rebase` does (internal/daemon/sync_managed.go), and a signing
// prompt on the daemon's TTY-less environment kills that commit. The rebase
// then sits halted with a CLEAN, conflict-free index: byte-identical to the
// upstream-equivalent-commit halt that ResolveRebaseAcceptTheirs skips.
// Skipping it would silently discard content that was resolved but never
// recorded. Applied here rather than at the one committing call site so a
// future network command that commits inherits the hardening by default.
func NewNetworkCmd(ctx context.Context, args ...string) *exec.Cmd {
	gitArgs := append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")
	return cmd
}
