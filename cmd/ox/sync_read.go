package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

// headlessLedgerReadRequested runs before flag parsing and dotenv loading. A malformed
// read invocation also skips project configuration, daemon IPC, and friction retries.
func headlessLedgerReadRequested(args []string) bool {
	// Reuse Cobra's command lookup so positional arguments and flag values
	// cannot select the read path for an unrelated command.
	cmd, _, _ := rootCmd.Find(args)
	syncRequested := cmd == syncCmd
	helperRequested := cmd == gitCredentialHelperCmd
	if !syncRequested && !helperRequested {
		return false
	}
	readOnly := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		name, value, _ := strings.Cut(arg, "=")
		if helperRequested && (name == "--read-endpoint" || name == "--read-repo" || name == "--read-url") {
			return true
		}
		if !syncRequested {
			continue
		}
		switch name {
		case "--read-only":
			parsed, err := strconv.ParseBool(value)
			readOnly = err != nil || parsed
		case "--repo", "--timeout", "--check":
			return true
		}
	}
	return readOnly
}

func isHeadlessLedgerRead(cmd *cobra.Command) bool {
	if cmd.Name() == "git-credential-helper" {
		return cmd.Flags().Changed("read-endpoint") || cmd.Flags().Changed("read-repo") || cmd.Flags().Changed("read-url")
	}
	if cmd.Name() != "sync" {
		return false
	}
	readOnly, _ := cmd.Flags().GetBool("read-only")
	return readOnly || cmd.Flags().Changed("repo") || cmd.Flags().Changed("timeout") || cmd.Flags().Changed("check")
}

func runReadSync(cmd *cobra.Command, args []string) error {
	result := ledger.ReadSyncResult{
		SchemaVersion: 1,
		History:       "unknown",
		Coverage:      ledger.ReadCoverage{Paths: []string{}},
		Hydration:     ledger.ReadHydration{State: "unknown"},
	}
	readOnly, _ := cmd.Flags().GetBool("read-only")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	repoID, _ := cmd.Flags().GetString("repo")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	check, _ := cmd.Flags().GetBool("check")
	if !readOnly || len(args) != 0 || !repotools.IsValidRepoID(repoID) || timeout <= 0 {
		result.ErrorClass = "invalid_arguments"
		return finishReadSync(cmd, result, jsonOutput, 2)
	}
	for _, name := range []string{"team", "all-teams", "remove-team", "config", "profile"} {
		if cmd.Flags().Changed(name) {
			result.ErrorClass = "invalid_arguments"
			return finishReadSync(cmd, result, jsonOutput, 2)
		}
	}
	// Selection is explicitly independent of the source repository and disk
	// logins. This is the same endpoint binding used for SAGEOX_TOKEN itself.
	ep := auth.EnvTokenEndpoint()
	u, err := url.Parse(ep)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		result.ErrorClass = "invalid_arguments"
		return finishReadSync(cmd, result, jsonOutput, 2)
	}
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" && !filepath.IsAbs(dataHome) {
		result.ErrorClass = "invalid_arguments"
		return finishReadSync(cmd, result, jsonOutput, 2)
	}
	if os.Getenv("OX_XDG_DISABLE") != "" {
		result.ErrorClass = "invalid_arguments"
		return finishReadSync(cmd, result, jsonOutput, 2)
	}
	result.RepoID, result.Endpoint = repoID, ep
	result.Path = config.DefaultLedgerPath(repoID, ep)
	if !filepath.IsAbs(result.Path) {
		result.Path, result.ErrorClass = "", "invalid_arguments"
		return finishReadSync(cmd, result, jsonOutput, 2)
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if check {
		result = ledger.CheckReadiness(ctx, result.Path, repoID, ep)
	} else {
		token, err := auth.CurrentReadToken(ep)
		var readURL string
		if err == nil {
			readURL, err = api.NewRepoClientWithEndpoint(ep).GetLedgerReadURL(ctx, repoID, token)
		}
		if err != nil {
			switch {
			case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(ctx.Err(), context.Canceled):
				result.ErrorClass = "interrupted"
			case errors.Is(err, api.ErrUnauthorized), errors.Is(err, api.ErrForbidden):
				result.ErrorClass = "denied"
			case errors.Is(err, auth.ErrReadTokenUnavailable):
				result.ErrorClass = "denied"
			case errors.Is(err, api.ErrLedgerReadMissing):
				result.ErrorClass = "missing_ledger"
			case errors.Is(err, api.ErrLedgerReadUnavailable):
				result.ErrorClass = "unavailable"
			default:
				result.ErrorClass = "unavailable"
			}
		} else {
			result = ledger.ReadSync(ctx, ledger.ReadSyncOptions{RepoID: repoID, Endpoint: ep, Path: result.Path, ReadURL: readURL})
		}
	}
	code := 0
	if !result.Ready || result.ErrorClass != "" {
		code = 1
	}
	return finishReadSync(cmd, result, jsonOutput, code)
}

func finishReadSync(cmd *cobra.Command, result ledger.ReadSyncResult, jsonOutput bool, code int) error {
	if jsonOutput {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
			return &distillHistoryExitError{ExitCode: 1}
		}
	} else if code == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Ledger ready: %s (HEAD %s)\n", result.Path, result.Head)
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "Ledger read failed: %s\n", result.ErrorClass)
		if code == 2 {
			fmt.Fprintln(cmd.ErrOrStderr(), "Use: ox sync --read-only --repo repo_<uuid> [--timeout 5m] [--check] [--json]")
		}
	}
	if code != 0 {
		return &distillHistoryExitError{ExitCode: code, Envelope: distillHistoryEnvelope{Error: &distillHistoryEnvelopeError{Code: result.ErrorClass, Message: "ledger read failed"}}}
	}
	return nil
}

func writeReadSyncUsageError(cmd *cobra.Command, args []string) int {
	jsonOutput := slices.Contains(args, "--json") || slices.Contains(args, "--json=true")
	_ = finishReadSync(cmd, ledger.ReadSyncResult{
		SchemaVersion: 1,
		History:       "unknown",
		Coverage:      ledger.ReadCoverage{Paths: []string{}},
		Hydration:     ledger.ReadHydration{State: "unknown"},
		ErrorClass:    "invalid_arguments",
	}, jsonOutput, 2)
	return 2
}
