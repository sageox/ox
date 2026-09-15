package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

// hostedLedgerSelected reports whether a --repo value names a hosted ledger by
// its canonical repo_<uuid> identity rather than a filesystem path. The two
// forms cannot collide: ParseRepoID accepts only "repo_" followed by a UUID,
// which no path a caller would pass to `ox session list --repo` can spell.
func hostedLedgerSelected(repo string) bool {
	return repotools.IsValidRepoID(repo)
}

// hostedLedgerRepoArg returns the --repo value in args, handling both the
// "--repo=value" and "--repo value" spellings. It runs before Cobra parses, so
// it cannot ask the flag set. Everything after "--" is a positional argument.
//
// The LAST occurrence wins, because that is the one Cobra binds. Returning the
// first would let `--repo /some/path --repo repo_<uuid>` run the project
// prelude — loading dotenv from the working directory — and only then select
// the hosted checkout, which is exactly the influence this path exists to deny.
func hostedLedgerRepoArg(args []string) string {
	repo := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		name, value, split := strings.Cut(arg, "=")
		if name != "--repo" {
			continue
		}
		if !split && i+1 < len(args) {
			// Step past the value. pflag hands the next token to --repo
			// whatever it is, so a "--" consumed here is this flag's value and
			// not the positional separator; treating it as the separator would
			// abandon the scan and miss a later --repo that pflag does bind.
			i++
			value = args[i]
		}
		repo = value
	}
	return repo
}

// selectHostedLedger resolves the checkout for repoID from the trusted headless
// inputs published in docs/specs/ledger-read-sync.md: SAGEOX_ENDPOINT selects
// the endpoint and XDG_DATA_HOME selects the caller's isolated data home. A
// non-empty class means the selection is unsafe and no ledger may be read; ep is
// empty until the endpoint itself is accepted, so an unsafe endpoint is never
// echoed back to the caller.
//
// Nothing here consults the working directory, a project config, or a disk
// login. Those would let whichever source checkout the process happens to sit in
// choose the identity a hosted read runs as.
func selectHostedLedger(repoID string) (path, ep, class string) {
	if !repotools.IsValidRepoID(repoID) {
		return "", "", "invalid_arguments"
	}
	ep = auth.EnvTokenEndpoint()
	u, err := url.Parse(ep)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", "", "invalid_arguments"
	}
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" && !filepath.IsAbs(dataHome) {
		return "", "", "invalid_arguments"
	}
	if os.Getenv("OX_XDG_DISABLE") != "" {
		return "", "", "invalid_arguments"
	}
	path = config.DefaultLedgerPath(repoID, ep)
	if !filepath.IsAbs(path) {
		return "", ep, "invalid_arguments"
	}
	return path, ep, ""
}

// withHostedLedger runs read while holding the same checkout lock that
// materialization takes, so a concurrent `ox sync --read-only` refresh cannot
// replace files underneath a reader. read must finish every filesystem read
// before it returns.
//
// read collects; it does not print. Rendering happens after the lock is
// released so a consumer that drains stdout slowly cannot extend how long the
// refresher is blocked.
func withHostedLedger(cmd *cobra.Command, repoID string, read func(path string) error) error {
	path, ep, class := selectHostedLedger(repoID)
	if class != "" {
		return hostedReadFailed(cmd, class, 2)
	}
	// A hosted runtime bounds the tool call by killing the process. Catching
	// the signal turns that into a sanitized failure instead of a truncated
	// stdout that a consumer would have to guess at.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var readErr error
	guardErr := ledger.WithReadCheckout(ctx, path, repoID, ep, func(ledger.ReadSyncResult) error {
		readErr = read(path)
		// The reader's own failure is returned below with its real error. Only
		// the guard's verdict travels through this return value.
		return nil
	})
	if guardErr != nil {
		var notReady *ledger.ReadNotReadyError
		if errors.As(guardErr, &notReady) {
			return hostedReadFailed(cmd, notReady.Class, 1)
		}
		// Everything else is a failure to take the lock: a wedged peer, an
		// expired budget, or a signal. CheckReadiness classifies these the
		// same way.
		return hostedReadFailed(cmd, "interrupted", 1)
	}
	// The ledger readers walk the filesystem through context-free APIs, so a
	// signal delivered after the lock was taken does not stop the traversal —
	// it runs to completion and reports success. Re-check before the caller
	// renders anything: a result gathered across a cancellation is not one the
	// consumer asked for, and the process is on its way down regardless.
	if ctx.Err() != nil {
		return hostedReadFailed(cmd, "interrupted", 1)
	}
	return readErr
}

// hostedReadFailed renders a sanitized hosted-read failure. The class
// vocabulary is the one published in docs/specs/ledger-read-sync.md. stdout
// stays empty: a consumer must never be able to mistake a refused read for a
// ledger that is genuinely empty.
func hostedReadFailed(cmd *cobra.Command, class string, code int) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "Ledger read failed: %s\n", class)
	return &commandExitError{ExitCode: code, Message: "ledger read failed"}
}
