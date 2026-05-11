package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/spf13/cobra"
)

// git-credential-helper is the standard git helper protocol implemented as
// an ox subcommand. Git invokes it as `ox git-credential-helper <op>` where
// <op> is `get`, `store`, or `erase` (we only implement `get`).
//
// Per ox-eeqi, this is the migration target away from embedding the GitLab
// PAT directly into every Ledger's `.git/config` origin URL. Forensic scan
// 2026-05-10 found the embedded PAT in 9 local Ledgers, leaking via Time
// Machine, iCloud, `git remote -v`, and GIT_TRACE.
//
// Protocol reference: https://git-scm.com/docs/gitcredentials
//
// Input on stdin (one key=value per line, terminated by a blank line):
//
//	protocol=https
//	host=git.sageox.ai
//	path=team/repo.git    [optional]
//	username=oauth2       [optional, git may pre-populate]
//
// Output on stdout (same format) on success:
//
//	username=oauth2
//	password=<PAT>
//
// On failure or no credentials: emit nothing, exit 0. Git treats empty
// output as "helper has nothing for this request" and falls back to its
// own prompts (or the next helper). Exit non-zero ONLY for real errors —
// not for "I don't have a credential for that host."

var gitCredentialHelperCmd = &cobra.Command{
	Use:    "git-credential-helper [get|store|erase]",
	Short:  "Git credential helper backed by ox credential store",
	Long:   gitCredentialHelperLong,
	Hidden: true, // not user-facing; invoked by git
	Args:   cobra.MaximumNArgs(1),
	RunE:   runGitCredentialHelper,
	// Disable cobra's built-in error/usage printing for this command.
	// Any non-zero exit must be silent to stdout/stderr to avoid
	// interfering with git's protocol parsing.
	SilenceErrors: true,
	SilenceUsage:  true,
}

const gitCredentialHelperLong = `Implements the git credential helper protocol against the ox credential store.

Not intended for direct invocation. ox configures this helper inside each
Ledger's .git/config:

  [credential "https://git.sageox.ai"]
      helper = !ox git-credential-helper

Git then calls "ox git-credential-helper get" on every authenticated fetch
or push, eliminating the need to embed the PAT in the origin URL.`

func init() {
	rootCmd.AddCommand(gitCredentialHelperCmd)
}

func runGitCredentialHelper(cmd *cobra.Command, args []string) error {
	op := "get"
	if len(args) > 0 {
		op = args[0]
	}
	switch op {
	case "get":
		return helperGet(cmd.InOrStdin(), cmd.OutOrStdout())
	case "store", "erase":
		// ox's credential store is driven by the ox CLI's login/logout flows,
		// not by git's helper protocol. Silently drain stdin and exit 0 so
		// git doesn't see a failure when chaining helpers.
		return drainStdin(cmd.InOrStdin())
	default:
		// Unknown op: silently exit 0 per the protocol.
		return drainStdin(cmd.InOrStdin())
	}
}

// helperGet parses the credential request on stdin and emits a matching
// credential (if any) on stdout. Errors during the lookup are logged via
// slog but not surfaced to git — git would treat a non-zero exit as a
// fatal helper error and abort the operation, which is the wrong UX
// when the user is just trying to authenticate against an unrelated host.
func helperGet(stdin io.Reader, stdout io.Writer) error {
	req, err := parseGitCredentialRequest(stdin)
	if err != nil {
		slog.Debug("git-credential-helper: parse request failed", "error", err)
		return nil // silent on parse error
	}
	if req["protocol"] == "" || req["host"] == "" {
		slog.Debug("git-credential-helper: missing protocol/host in request",
			"protocol", req["protocol"], "host", req["host"])
		return nil
	}

	// Resolve which ox endpoint owns this git host. The credential store is
	// keyed by SageOx endpoint URL (e.g. "https://sageox.ai"), but git asks
	// for credentials keyed by git host (e.g. "git.sageox.ai"). The mapping
	// is: strip the "git." prefix, prepend the protocol.
	host := strings.ToLower(req["host"])
	gitHost := host
	endpointHost := strings.TrimPrefix(host, "git.")
	endpointURL := req["protocol"] + "://" + endpointHost

	creds, err := gitserver.LoadCredentialsForEndpoint(endpointURL)
	if err != nil {
		slog.Debug("git-credential-helper: load credentials failed",
			"endpoint", endpointURL, "error", err)
		return nil
	}
	if creds == nil || creds.Token == "" {
		// No credentials stored for this endpoint — let git fall through to
		// its own prompts or the next helper.
		slog.Debug("git-credential-helper: no credentials for endpoint",
			"endpoint", endpointURL, "git_host", gitHost)
		return nil
	}

	// Emit the credential. Git expects username and password each on their
	// own line, then a blank line.
	username := creds.Username
	if username == "" {
		// GitLab convention: oauth2:<PAT> for PAT-based auth.
		username = "oauth2"
	}
	if _, err := fmt.Fprintf(stdout, "username=%s\n", username); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "password=%s\n", creds.Token); err != nil {
		return err
	}
	if _, err := fmt.Fprint(stdout, "\n"); err != nil {
		return err
	}
	return nil
}

// parseGitCredentialRequest reads the git credential helper protocol's
// key=value lines until a blank line or EOF. Lines with no "=" are
// ignored (per protocol).
func parseGitCredentialRequest(stdin io.Reader) (map[string]string, error) {
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	out := map[string]string{}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break // blank line terminates the request
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		out[line[:idx]] = line[idx+1:]
	}
	return out, scanner.Err()
}

// drainStdin reads and discards stdin until EOF. Used for unsupported ops
// so git doesn't block waiting for the helper to consume its input.
func drainStdin(stdin io.Reader) error {
	_, _ = io.Copy(io.Discard, stdin)
	return nil
}

// HelperCommandString returns the value to put in `credential.<host>.helper`
// for ox-managed credentials. The leading "!" tells git to interpret the
// value as a shell command rather than `git-credential-<name>`. We resolve
// the ox binary path via os.Executable so the helper still works if `ox`
// isn't on $PATH (common for IDE-integrated workflows).
//
// Falls back to plain `ox git-credential-helper` if os.Executable returns
// an error or a value that doesn't look like an absolute path — in that
// case we rely on `ox` being on $PATH, which is the install default.
func HelperCommandString() string {
	exe, err := os.Executable()
	if err == nil && strings.HasPrefix(exe, "/") {
		return "!" + shellQuote(exe) + " git-credential-helper"
	}
	return "!ox git-credential-helper"
}

// shellQuote returns s wrapped in single quotes with any inner single
// quotes escaped. Sufficient for the limited use here (absolute paths
// that may contain spaces but won't contain shell metacharacters in any
// supported install location).
func shellQuote(s string) string {
	if !strings.ContainsAny(s, " '\t\"$`\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// EndpointURLForHost returns the credential-store endpoint URL for a git
// host. Exposed for callers that need to align their endpoint key with
// the helper's lookup behavior. The mapping mirrors helperGet().
func EndpointURLForHost(scheme, host string) string {
	if scheme == "" {
		scheme = "https"
	}
	host = strings.ToLower(host)
	host = strings.TrimPrefix(host, "git.")
	return scheme + "://" + host
}

