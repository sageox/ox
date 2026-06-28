package gitserver

import (
	"errors"
	"fmt"
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
	scope := "credential.https://" + cfg.Host + ".helper"

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
// Idempotent: skips the write when both keys already read "false".
func DisableCommitSigning(repoPath string) (changed bool, err error) {
	for _, key := range []string{"commit.gpgsign", "tag.gpgsign"} {
		// readGitConfig walks all config levels, so an inherited global
		// "true" is what we compare against — a local "false" already in
		// place reads back as "false" and we skip.
		if current, rerr := readGitConfig(repoPath, key); rerr == nil && current == "false" {
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

// MigrateLedgerCredentials performs the one-shot migration for a single
// ledger: disable commit signing, strip any embedded oauth2:TOKEN from the
// origin URL, then install the ox credential helper for the resulting bare
// host. Idempotent — safe to invoke on every daemon startup.
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

	// Only migrate https:// remotes with the ox-managed oauth2 prefix.
	parsed, err := url.Parse(remoteURL)
	if err != nil || parsed.Scheme != "https" {
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
	host := parsed.Hostname()
	if host == "" {
		return changed, nil
	}
	if err := InstallCredentialHelper(repoPath, HelperConfig{
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
