package gitserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os/exec"
	"strings"
	"sync"
)

// helperCommand is the shell string git invokes for credential resolution.
// Set once at process startup via SetHelperCommand from the main binary, where
// we know the path to the running ox executable. Internal callers query it
// via DefaultHelperCommand(); when unset, returns a fallback that assumes
// `ox` is on $PATH.
var (
	helperCmdMu sync.RWMutex
	helperCmd   string
)

// SetHelperCommand records the credential helper invocation string for use
// by MigrateLedgerCredentials and other gitserver callers. Idempotent.
func SetHelperCommand(cmd string) {
	helperCmdMu.Lock()
	defer helperCmdMu.Unlock()
	helperCmd = cmd
}

// DefaultHelperCommand returns the current helper command, or a sensible
// fallback ("!ox git-credential-helper") if the binary location wasn't
// registered. The fallback still works as long as `ox` is on $PATH, which
// is the install default.
func DefaultHelperCommand() string {
	helperCmdMu.RLock()
	defer helperCmdMu.RUnlock()
	if helperCmd != "" {
		return helperCmd
	}
	return "!ox git-credential-helper"
}

// CredentialHelperArgs returns the `-c` flags that install the ox-managed
// credential helper for a single git invocation, without touching the repo's
// persisted .git/config. The leading empty `credential.helper=` clears any
// inherited helpers so ours is authoritative for that one command; the second
// installs the ox helper.
//
// Use this on network git operations that run before the helper has been
// written into .git/config — notably the initial clone (the repo doesn't
// exist yet, so InstallCredentialHelper can't be called first). Both the
// ledger full-clone and the team-context two-phase clone build their clone
// argv from this so the two paths cannot drift.
func CredentialHelperArgs() []string {
	return []string{
		"-c", "credential.helper=",
		"-c", "credential.helper=" + DefaultHelperCommand(),
	}
}

// HelperConfig holds the parameters needed to install an ox-managed git
// credential helper into a single repo's .git/config. The shell command
// the helper invokes is produced by the cmd/ox layer (see HelperCommandString)
// and threaded down here so internal/gitserver doesn't have to know the path
// to the running ox binary.
type HelperConfig struct {
	// Scheme is the credential scope scheme: "https" (default when empty) or
	// "http" for loopback remotes. Git matches the scope against the remote's
	// scheme, so an https scope never fires for an http remote.
	Scheme string

	// Host is the git server host (e.g. "git.sageox.ai"). The credential
	// scope is set to "https://<host>" so the helper only fires for that
	// exact host — never for third-party remotes that may live in the
	// same repo (forks, upstream pointers).
	Host string

	// Command is the shell command git invokes for credential resolution.
	// Conventionally prefixed with "!" (per git-credential helper docs)
	// and includes the absolute path to the ox binary when available.
	// See cmd/ox/git_credential_helper.go:HelperCommandString.
	Command string
}

// InstallCredentialHelper writes the ox-managed credential helper config
// into a repo's .git/config under a host-scoped section. Idempotent: if
// the same helper is already configured, the function is a no-op.
//
// Rewrites (not appends) the per-host helper value so a repo whose
// .git/config previously had an embedded oauth2:TOKEN URL gets a clean
// single entry instead of stacked helpers. Other unrelated [credential]
// sections (e.g. for github.com, third-party hosts) are preserved.
//
// Per ox-eeqi: this is the migration target. After this is called and
// StripRemoteCredentials runs against the same repo, future git fetch /
// push operations resolve credentials via the helper instead of reading
// them out of the embedded origin URL.
func InstallCredentialHelper(repoPath string, cfg HelperConfig) error {
	if cfg.Host == "" {
		return fmt.Errorf("helper config: host is empty")
	}
	if cfg.Command == "" {
		return fmt.Errorf("helper config: command is empty")
	}
	scheme := cfg.Scheme
	if scheme == "" {
		scheme = "https"
	}
	if scheme != "https" && scheme != "http" {
		return fmt.Errorf("helper config: unsupported scheme %q", scheme)
	}
	scope := "credential." + scheme + "://" + cfg.Host + ".helper"

	// Read current value so we can skip the write if it already matches.
	if current, err := readGitConfig(repoPath, scope); err == nil && current == cfg.Command {
		return nil
	}

	// Use --replace-all so a previously-stacked helper entry (left from an
	// earlier ox version, or hand-edited by the user) gets reset to a
	// single canonical value. Without --replace-all, git ERRORS if the key
	// already has multiple values.
	cmd := exec.Command("git", "-C", repoPath, "config",
		"--replace-all", scope, cfg.Command)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config %s: %s: %w", scope, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// readGitConfigLocal reads a single config value from the repo's LOCAL
// .git/config only (ignoring global/system scopes); returns ("", nil) when the
// key has no local override. Use this when the persistence of a repo-local
// override matters and an inherited value must NOT count as "already set".
func readGitConfigLocal(repoPath, key string) (string, error) {
	cmd := exec.Command("git", "-C", repoPath, "config", "--local", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		// exit 1 = key absent locally (or no local config file yet); treat as
		// a clean "no local value" so callers persist the override.
		if asErr(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// readGitConfig reads a single config value; returns ("", nil) when the
// key is absent (git exits 1 with empty output in that case).
func readGitConfig(repoPath, key string) (string, error) {
	cmd := exec.Command("git", "-C", repoPath, "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		// "git config --get" returns 1 when the key is missing — treat that
		// as a clean "no value" rather than an error so callers don't need
		// to special-case it.
		if asErr(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// DisableCommitSigning forces commit and tag signing OFF in a single repo's
// .git/config. ox-managed repos (ledgers, team contexts) are committed
// non-interactively by the daemon and CLI; if they inherit a user's global
// commit.gpgsign=true with an SSH/GPG signing key, every commit blocks on a
// passphrase prompt that has no TTY to answer it and dies with
// "fatal: failed to write commit object". The result is silent: sessions
// stage but never commit, never push, never sync.
//
// Writing the override into the repo's LOCAL config (not --global) keeps the
// user's own repos free to sign while guaranteeing ox-managed repos never do.
// Idempotent: skips the write when the key is already "false" in the repo's
// LOCAL config specifically.
func DisableCommitSigning(repoPath string) (changed bool, err error) {
	for _, key := range []string{"commit.gpgsign", "tag.gpgsign"} {
		// Read the repo-LOCAL value only, not the merged config. A merged
		// "false" can come from a global/system scope while the repo has no
		// local override at all — skipping on that would leave the managed
		// repo unprotected, so a later global flip to "true" re-wedges it.
		// Only a persisted local "false" means the repair is already in place.
		if current, rerr := readGitConfigLocal(repoPath, key); rerr == nil && current == "false" {
			continue
		}
		cmd := exec.Command("git", "-C", repoPath, "config", "--local", key, "false")
		if output, cerr := cmd.CombinedOutput(); cerr != nil {
			return changed, fmt.Errorf("git config --local %s false: %s: %w",
				key, strings.TrimSpace(string(output)), cerr)
		}
		changed = true
	}
	return changed, nil
}

// TeamCoworkerGetter reports the endpoint SAGEOX_TOKEN is bound to and, when
// that token is a team token acting as an AI coworker, the coworker's name and
// agt_ id; id is "" otherwise. Set by the auth package to avoid circular
// imports. Nil reads as no coworker.
var TeamCoworkerGetter func() (ep, name, id string)

// SignedInUserGetter reports the commit identity of the SageOx account signed
// in on the server at ep: the name teammates see and the account's email. Both
// are "" when nobody is signed in there. Set by cmd/ox, which can reach auth,
// identity and user config. Nil reads as nobody signed in.
var SignedInUserGetter func(ep string) (name, email string)

// coworkerEmailDomain ends the user.email stampLedgerAuthor writes for an AI
// coworker. ".invalid" (RFC 2606) never resolves, so the address belongs to no
// one. Stamps written before authorStampKey existed are recognized by it.
const coworkerEmailDomain = "@ai-coworker.invalid"

// authorStampKey marks a clone whose user.name and user.email stampLedgerAuthor
// wrote, so it removes only its own identity and never one a person set.
const authorStampKey = "sageox.authorstamp"

// stampLedgerAuthor sets who authors the commits ox makes in the clone at
// repoPath. ox's Ledger writers commit through several git paths, and all of
// them take author and committer from user.name and user.email, which git reads
// from the clone's own config ahead of the machine's global one (GIT_AUTHOR_*
// and GIT_COMMITTER_* still override both).
//
// The identity is, in order: the AI coworker a team token acts as, then the
// SageOx account signed in on the clone's server — so a machine with no git
// identity can still save sessions — then nothing, which leaves git's own
// identity in charge. Each applies only when the clone's origin is on the
// server it belongs to. With nothing to stamp, it removes an identity it
// stamped earlier, such as one a team token or a signed-out account left on the
// machine, and leaves any other alone.
func stampLedgerAuthor(repoPath, remoteURL string) error {
	repoEp := endpointFromRemoteURL(remoteURL)
	var name, email string
	if TeamCoworkerGetter != nil {
		if ep, n, id := TeamCoworkerGetter(); id != "" && endpointHostsEqual(ep, repoEp) {
			name, email = n, id+coworkerEmailDomain
		}
	}
	if email == "" && SignedInUserGetter != nil && repoEp != "" {
		name, email = SignedInUserGetter(repoEp)
	}
	// A failed read counts as unset: removal waits for the next call, and a
	// write reports its own failure.
	current, _ := readGitConfigLocal(repoPath, "user.email")
	stamped, _ := readGitConfigLocal(repoPath, authorStampKey)

	if email == "" {
		if stamped == "" && !strings.HasSuffix(current, coworkerEmailDomain) {
			return nil
		}
		// The marker goes last: if removing user.name or user.email fails,
		// the next call still finds the stamp and finishes.
		for _, key := range []string{"user.name", "user.email", authorStampKey} {
			out, err := exec.Command("git", "-C", repoPath, "config", "--local", "--unset-all", key).CombinedOutput()
			var exitErr *exec.ExitError
			if asErr(err, &exitErr) && exitErr.ExitCode() == 5 {
				continue // already unset
			}
			if err != nil {
				return fmt.Errorf("git config --unset-all %s: %s: %w", key, strings.TrimSpace(string(out)), err)
			}
		}
		return nil
	}

	if currentName, _ := readGitConfigLocal(repoPath, "user.name"); current == email && currentName == name && stamped != "" {
		return nil
	}
	// The marker goes first, so a stamp interrupted partway is still ox's to remove.
	for _, kv := range [][2]string{{authorStampKey, "true"}, {"user.name", name}, {"user.email", email}} {
		out, err := exec.Command("git", "-C", repoPath, "config", "--local", "--replace-all", kv[0], kv[1]).CombinedOutput()
		if err != nil {
			return fmt.Errorf("git config %s: %s: %w", kv[0], strings.TrimSpace(string(out)), err)
		}
	}
	return nil
}

// MigrateLedgerCredentials performs the one-shot migration for a single
// ledger: disable commit signing, strip any embedded oauth2:TOKEN from the
// origin URL, then install the ox credential helper for the resulting bare
// host. It also sets who authors the clone's commits (stampLedgerAuthor).
// Idempotent — safe to invoke on every daemon startup.
//
// Returns ok=true if the migration ran (signing disabled, stripped, or
// installed); ok=false if there was nothing to do. The error result is
// reserved for genuine failures (git command errors, malformed origin URLs).
func MigrateLedgerCredentials(repoPath string, helperCmd string) (changed bool, err error) {
	// 0. Disable commit/tag signing FIRST, unconditionally. This must run
	// even for repos with no/SSH/file origin (which return early below),
	// because a wedged signing config blocks local commits regardless of
	// the remote. Self-heals existing repos on the next daemon sweep.
	if signChanged, serr := DisableCommitSigning(repoPath); serr != nil {
		return false, fmt.Errorf("disable commit signing: %w", serr)
	} else if signChanged {
		changed = true
	}

	// 1. Read origin URL; bail cleanly for SSH / file:// / missing remotes.
	pat, remoteURL, err := extractPATFromRemote(repoPath)
	if err != nil {
		// No origin or unreadable config — nothing to migrate, but not a
		// hard error. Callers (daemon startup) shouldn't fail because of one
		// weird ledger. Preserve any signing change made above.
		return changed, nil
	}
	if remoteURL == "" {
		return changed, nil
	}

	// Logged, not returned: RefreshRemoteCredentials runs this before Ledger
	// pushes, and a push must not fail over who authored it.
	if err := stampLedgerAuthor(repoPath, remoteURL); err != nil {
		slog.Warn("set Ledger commit author failed", "repo", repoPath, "error", err)
	}

	// Only migrate https:// remotes with the ox-managed oauth2 prefix. A
	// malformed origin URL is genuine misconfig worth surfacing (per the
	// "genuine failures" contract above) — don't fold it into the non-HTTPS
	// no-op, or callers like doctor never learn the remote is broken.
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return changed, fmt.Errorf("parse origin URL %q: %w", remoteURL, err)
	}
	scheme, host := helperTarget(parsed)
	if host == "" {
		return changed, nil
	}

	// 2. Strip embedded PAT if present (no-op if origin is already bare).
	if pat != "" {
		if err := StripRemoteCredentials(repoPath); err != nil {
			return false, fmt.Errorf("strip credentials: %w", err)
		}
		changed = true
	}

	// 3. Install (or refresh) the credential helper for this host. We do
	// this even when no PAT was embedded so brand-new ledgers also get
	// the helper configured during normal sync.
	if err := InstallCredentialHelper(repoPath, HelperConfig{
		Scheme:  scheme,
		Host:    host,
		Command: helperCmd,
	}); err != nil {
		return changed, fmt.Errorf("install helper: %w", err)
	}
	// InstallCredentialHelper is itself idempotent; "changed" only reflects
	// the strip (which is the user-observable migration).
	return changed, nil
}

// asErr is a tiny shim around errors.As to keep call sites readable.
func asErr(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}

// IsLoopbackHost reports whether h (a bare host, no port) names the local
// machine: an IP loopback literal or the well-known name "localhost". It is the
// single definition of "loopback" shared by the LFS client and the credential
// helper, so plain http is allowed in exactly the same places.
func IsLoopbackHost(h string) bool {
	if h == "" {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(h, "localhost")
}

// helperTarget returns the credential scope scheme and host for a remote URL,
// or ("", "") when ox installs no helper for it. https remotes scope to the
// bare hostname (any port). Plain http is accepted only for loopback hosts and
// scopes to host:port, since git matches the port for non-default ports.
// Non-loopback http, file://, and ssh remotes get no helper.
func helperTarget(u *url.URL) (scheme, host string) {
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "https", u.Hostname()
	case "http":
		if IsLoopbackHost(u.Hostname()) {
			return "http", u.Host
		}
	}
	return "", ""
}
