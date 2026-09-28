package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/proc"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/selfexec"
	"github.com/sageox/ox/internal/teamconverge"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
)

const syncResultSchemaVersion = 1

var (
	ensureDaemonForSync    = ensureDaemonRunning
	syncLedgerForSync      = syncViaDaemon
	syncTeamForSync        = syncTeamContext
	syncAllTeamsForSync    = syncAllTeamContexts
	convergeRepositorySync = runSyncConvergence
)

// SyncResult is the versioned JSON output for ordinary sync operations.
// Transport and convergence are separate because a successful pull does not
// mean Team Context artifacts reached the current repository.
type SyncResult struct {
	SchemaVersion int                   `json:"schema_version"`
	Success       bool                  `json:"success"`
	Mode          string                `json:"mode"` // "daemon" or "direct"
	Transport     SyncTransportResult   `json:"transport"`
	Convergence   SyncConvergenceResult `json:"convergence"`
	Error         string                `json:"error,omitempty"`
}

type SyncTransportResult struct {
	Status       string                  `json:"status"` // "synced" or "failed"
	Ledger       *SyncLedgerResult       `json:"ledger,omitempty"`
	TeamContexts []TeamContextSyncResult `json:"team_contexts,omitempty"`
	Error        string                  `json:"error,omitempty"`
}

type SyncConvergenceResult struct {
	Status       string                            `json:"status"` // "converged", "pending", "failed", or "skipped"
	Repositories []RepositoryConvergenceSyncResult `json:"repositories,omitempty"`
	Detail       string                            `json:"detail,omitempty"`
}

type RepositoryConvergenceSyncResult struct {
	Repository string               `json:"repository,omitempty"`
	TeamID     string               `json:"team_id"`
	TeamName   string               `json:"team_name,omitempty"`
	TeamPath   string               `json:"team_path"`
	Status     string               `json:"status"` // "converged", "pending", "failed", or "skipped"
	Report     *teamconverge.Report `json:"report,omitempty"`
	Error      string               `json:"error,omitempty"`
}

// SyncLedgerResult represents sync result for the ledger.
type SyncLedgerResult struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "synced", "skipped", "error", "not_found"
	Error  string `json:"error,omitempty"`
}

// TeamContextSyncResult represents sync result for a single team context.
type TeamContextSyncResult struct {
	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name"`
	TeamSlug string `json:"team_slug,omitempty"`
	Path     string `json:"path"`
	// "synced", "skipped", "cloning", "error", "not_found", "ambiguous", or
	// "unknown" ("unknown" = a legacy daemon that didn't report per-team results;
	// "ambiguous" = a slug/name selector that matched more than one team).
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Args:  cobra.NoArgs,
	Short: "Manually sync ledger/team contexts (rarely needed)",
	Long:  syncLong(),
	RunE:  runSync,
}

// `ox sync --help` is assembled from two halves so the Add-on Catalog paragraph
// between them can be gated. Splitting it is what makes the gate a compile-time
// guarantee: a hand-spliced anchor string would silently stop matching the day
// someone reworded the surrounding help.
const syncLongHead = `Manually synchronize your Ledger and Team Context, then converge the
current repository with applicable Team Context artifacts.

NOTE: You should RARELY need this command. The background daemon automatically
keeps transport and local convergence synchronized. This command exists only for:
  - Troubleshooting sync issues
  - Triggering an immediate sync (this command itself forces sync)
  - Diagnostic purposes`

// syncAddonsHelp states the boundary between convergence and the Add-on Catalog:
// sync delivers Add-on-managed content but never checks for new catalog releases.
//
// It is printed ONLY when the addons gate is on. Unconditionally, it would name an
// Add-on Catalog this build cannot reach (#1028, #1029); the rule stays recorded in
// ADR-032 either way.
//
// It names `ox addons update`, which is now safe to name: the same gate that
// prints this paragraph registers that command (syncFeatureGatedCommands in
// root.go), so the two cannot disagree. The pointer was deliberately absent
// while the command was still follow-up work — help that sent a user to
// "unknown command" in exactly the state the flag exists to make safe.
// TestAddonsHelpNamesNoUnregisteredCommand enforces that pairing in both
// directions.
const syncAddonsHelp = `Convergence includes Add-on-managed and hand-authored Team Context content through
the same delivery path. It does not check the Add-on Catalog for newer releases;
use 'ox addons update' for catalog updates.`

const syncLongTail = `The daemon syncs automatically on:
  - File changes in your project
  - Periodic intervals (configurable)
  - Session start/end events

Ordinary sync requires the daemon. Pull operations are handled by the daemon
to ensure consistent sync behavior and proper locking.

Examples:
  ox sync              # sync Ledger + Team Context, then converge this repo
  ox sync --team acme  # sync specific team context
  ox sync --all-teams  # sync all team contexts

Headless read-only mode runs a bounded ledger refresh without the daemon:
  ox sync --read-only --repo repo_<uuid> --timeout 30m --json
  ox sync --read-only --repo repo_<uuid> --check --json

Read-only mode uses SAGEOX_TOKEN and SAGEOX_ENDPOINT, independent of the
current project. See docs/specs/ledger-read-sync.md for the reader contract.`

// syncLong assembles `ox sync --help`. The Add-on Catalog paragraph is
// unconditional: `ox addons` is an ordinary command now, so naming it is
// always correct.
func syncLong() string {
	return syncLongHead + "\n\n" + syncAddonsHelp + "\n\n" + syncLongTail
}

func init() {
	syncCmd.Flags().String("team", "", "sync a specific team context by ID")
	syncCmd.Flags().Bool("all-teams", false, "sync all configured team contexts")
	syncCmd.Flags().String("remove-team", "", "remove a team context (clears config and optionally deletes repo)")
	syncCmd.Flags().Bool("read-only", false, "refresh one ledger using the selected team token, without the daemon")
	syncCmd.Flags().String("repo", "", "repository ID for --read-only (repo_<uuid>)")
	// The default has to cover a cold clone of a real ledger — tens of thousands
	// of LFS objects through internal/ledger's bounded-concurrency hydration —
	// not the seconds a warm refresh takes. A consumer scheduling background
	// refreshes sets its own; docs/specs/ledger-read-sync.md carries the contract.
	syncCmd.Flags().Duration("timeout", 30*time.Minute, "maximum duration of a read-only operation, including lock wait and hydration")
	syncCmd.Flags().Bool("check", false, "check local read-only readiness without contacting the server")

	// add to root command
	rootCmd.AddCommand(syncCmd)
	syncCmd.GroupID = "auth" // group with ledger and other auth-related commands
}

// collectTransportProblem folds one transport step's result into the running
// problem list. An interactive interrupt must abort the whole command
// immediately, so it is returned unmodified rather than collected; every
// other error is appended to problems and swallowed here, so the remaining
// transport steps still run.
func collectTransportProblem(err error, problems *[]string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, tea.ErrInterrupted) {
		return err
	}
	*problems = append(*problems, err.Error())
	return nil
}

func runSync(cmd *cobra.Command, args []string) error {
	readOnly, _ := cmd.Flags().GetBool("read-only")
	if readOnly || cmd.Flags().Changed("repo") || cmd.Flags().Changed("timeout") || cmd.Flags().Changed("check") {
		return runReadSync(cmd, args)
	}

	teamID, _ := cmd.Flags().GetString("team")
	allTeams, _ := cmd.Flags().GetBool("all-teams")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	removeTeamID, _ := cmd.Flags().GetString("remove-team")

	// handle team removal (separate flow)
	if removeTeamID != "" {
		return removeTeamContext(removeTeamID, jsonOutput)
	}

	result := SyncResult{
		SchemaVersion: syncResultSchemaVersion,
		Mode:          "daemon",
		Transport:     SyncTransportResult{Status: "failed"},
		Convergence:   SyncConvergenceResult{Status: "skipped"},
	}
	if err := ensureDaemonForSync(jsonOutput); err != nil {
		result.Transport.Error = err.Error()
		result.Convergence.Detail = "transport unavailable"
		result.Error = err.Error()
		return finishSync(cmd, result, jsonOutput, err)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
	defer cancel()

	var transportProblems []string

	if teamID != "" {
		if err := collectTransportProblem(syncTeamForSync(ctx, teamID, jsonOutput, &result), &transportProblems); err != nil {
			return err
		}
	} else if allTeams {
		if err := collectTransportProblem(syncAllTeamsForSync(ctx, jsonOutput, &result), &transportProblems); err != nil {
			return err
		}
	} else {
		// The default promise is the whole current-repository path: Ledger and
		// Team Context transport, followed by local convergence.
		if err := collectTransportProblem(syncLedgerForSync(ctx, jsonOutput, &result), &transportProblems); err != nil {
			return err
		}
		if err := collectTransportProblem(syncAllTeamsForSync(ctx, jsonOutput, &result), &transportProblems); err != nil {
			return err
		}
	}

	var transportErr error
	if len(transportProblems) > 0 {
		result.Transport.Status = "failed"
		result.Transport.Error = strings.Join(transportProblems, "; ")
		transportErr = errors.New(result.Transport.Error)
	} else {
		result.Transport.Status = "synced"
	}

	convergenceErr := convergeRepositorySync(ctx, teamID, &result)
	problems := append([]string(nil), transportProblems...)
	if convergenceErr != nil {
		problems = append(problems, convergenceErr.Error())
	}
	result.Success = len(problems) == 0
	if len(problems) > 0 {
		result.Error = strings.Join(problems, "; ")
	}
	operationErr := errors.Join(transportErr, convergenceErr)

	// surface daemon health issues (e.g. a wedged session conflict) beyond
	// just `ox agent <id>` — same warnings, same severity thresholds, now
	// visible from a plain `ox sync` too. Suppressed in --json mode to keep
	// programmatic output clean (matches ox status).
	if !jsonOutput {
		emitDaemonIssueWarnings()
	}

	return finishSync(cmd, result, jsonOutput, operationErr)
}

func finishSync(cmd *cobra.Command, result SyncResult, jsonOutput bool, operationErr error) error {
	if jsonOutput {
		cli.PrintJSON(result)
		if operationErr != nil {
			return cli.ErrSilent
		}
		return nil
	}
	if err := writeSyncResultText(cmd.OutOrStdout(), result); err != nil && operationErr == nil {
		return err
	}
	return operationErr
}

func writeSyncResultText(w io.Writer, result SyncResult) error {
	if _, err := fmt.Fprintf(w, "Transport: %s\n", result.Transport.Status); err != nil {
		return err
	}
	if result.Transport.Ledger != nil {
		if _, err := fmt.Fprintf(w, "  Ledger: %s\n", result.Transport.Ledger.Status); err != nil {
			return err
		}
	}
	for _, team := range result.Transport.TeamContexts {
		name := team.TeamName
		if name == "" {
			name = team.TeamID
		}
		if _, err := fmt.Fprintf(w, "  Team Context %s: %s\n", name, team.Status); err != nil {
			return err
		}
	}
	if result.Transport.Error != "" {
		if _, err := fmt.Fprintf(w, "  Error: %s\n", result.Transport.Error); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintf(w, "Convergence: %s\n", result.Convergence.Status); err != nil {
		return err
	}
	for _, repository := range result.Convergence.Repositories {
		label := repository.Repository
		if label == "" {
			label = repository.TeamID
		}
		if _, err := fmt.Fprintf(w, "  %s: %s\n", label, repository.Status); err != nil {
			return err
		}
		if repository.Report != nil {
			if err := teamconverge.WriteText(w, *repository.Report); err != nil {
				return err
			}
		}
		if repository.Error != "" {
			if _, err := fmt.Fprintf(w, "  Error: %s\n", repository.Error); err != nil {
				return err
			}
		}
	}
	if result.Convergence.Detail != "" {
		_, err := fmt.Fprintf(w, "  %s\n", result.Convergence.Detail)
		return err
	}
	return nil
}

// syncViaDaemon triggers a sync via the daemon.
// The daemon handles pull operations.
func syncViaDaemon(_ context.Context, jsonOutput bool, result *SyncResult) error {
	var err error
	if !jsonOutput {
		err = cli.WithSpinnerNoResult("Syncing via daemon...", func() error {
			client := daemon.NewClientForCurrentRepoWithTimeout(30 * time.Second)
			return client.SyncWithProgress(func(stage string, percent *int, message string) {
				// progress updates are shown by spinner
			})
		})
	} else {
		client := daemon.NewClientForCurrentRepoWithTimeout(30 * time.Second)
		err = client.SyncWithProgress(nil)
	}
	if errors.Is(err, tea.ErrInterrupted) {
		return err
	}

	if err != nil {
		result.Transport.Ledger = &SyncLedgerResult{Status: "error", Error: err.Error()}
		return fmt.Errorf("daemon sync: %w", err)
	}

	result.Transport.Ledger = &SyncLedgerResult{Status: "synced"}
	return nil
}

// syncTeamContext syncs a specific team context by ID via daemon.
// CLI delegates pull operations to daemon. The daemon syncs all configured
// teams at once and returns per-team results; we report the status of the
// requested team specifically (not the bare success of the IPC round-trip).
func syncTeamContext(_ context.Context, teamID string, jsonOutput bool, result *SyncResult) error {
	run := func() ([]daemon.TeamSyncResult, error) {
		client := daemon.NewClientForCurrentRepoWithTimeout(60 * time.Second)
		return client.TeamSyncWithProgress(nil)
	}

	var results []daemon.TeamSyncResult
	var syncErr error
	if !jsonOutput {
		results, syncErr = cli.WithSpinner(fmt.Sprintf("Syncing team %s via daemon...", teamID), run)
	} else {
		results, syncErr = run()
	}
	if errors.Is(syncErr, tea.ErrInterrupted) {
		return syncErr
	}

	tcResult := resolveTeamSyncResult(teamID, results, syncErr)
	// for the legacy-daemon ("unknown") case, replace the placeholder error with
	// a version-aware message (queries the daemon's version) before it's reported.
	if tcResult.Status == "unknown" {
		tcResult.Error = legacyDaemonError().Error()
	}
	result.Transport.TeamContexts = append(result.Transport.TeamContexts, tcResult)

	// The command succeeds only when the requested team context is locally
	// usable: "synced" (pulled) or "skipped" (already up to date and present).
	// Everything else is a non-success that exits non-zero:
	//   - "cloning": clone in progress, not yet usable — fail so scripts don't
	//     proceed against missing context (re-run once the clone finishes)
	//   - "error" / "not_found" / "ambiguous": a real failure, typo/stale selector,
	//     or a selector matching multiple teams
	//   - "unknown": a legacy daemon that returned no per-team data. We fail CLOSED:
	//     its "success" is untrustworthy (old daemons swallowed failures), so we
	//     surface it with a restart hint instead of implying the team synced.
	// A whole-op error caused by some *other* team never reaches here as the
	// requested team's status, so it can't fail this targeted request.
	switch tcResult.Status {
	case "synced":
		return nil
	case "skipped":
		return nil
	case "cloning":
		return fmt.Errorf("team %s clone in progress (not yet available)", teamID)
	default: // error, not_found, ambiguous, unknown
		return errors.New(tcResult.Error)
	}
}

// resolveTeamSyncResult derives the reported result for a targeted --team sync
// from the daemon's per-team results and the IPC error. It is pure (no I/O) so
// the status-derivation logic can be unit-tested directly.
//
// The selector may be the team ID, its kebab-case slug, or its name (the --help
// example uses a slug-like value), so it is matched against all three.
//
// Selector resolution: an exact team-ID match wins immediately (IDs are unique).
// Otherwise the selector is matched against slug and name, which are NOT
// guaranteed unique — if more than one team matches, the result is "ambiguous"
// rather than silently picking the first (which could report the wrong team).
//
// Status outcomes:
//   - match found      → that team's reported status (synced/skipped/cloning/error)
//   - slug/name matches >1 team → "ambiguous": caller must disambiguate by ID
//   - results == nil, no err → "unknown": a legacy pre-change daemon that returned
//     success without per-team data (current daemons always send an array). The
//     caller fails CLOSED on this — a legacy daemon also swallowed sync failures,
//     so its "success" can't be trusted; the user should restart the daemon and
//     retry rather than proceed on possibly stale/missing context.
//   - empty results + err → "error": transport/setup failure with nothing to attribute
//   - results present, no match → "not_found": a genuine miss (typo/stale config)
func resolveTeamSyncResult(teamID string, results []daemon.TeamSyncResult, syncErr error) TeamContextSyncResult {
	tcResult := TeamContextSyncResult{TeamID: teamID}

	// exact ID match wins (IDs are unique)
	var match *daemon.TeamSyncResult
	for i := range results {
		if teamID == results[i].TeamID {
			match = &results[i]
			break
		}
	}
	// otherwise resolve via slug/name, which may collide across teams
	if match == nil {
		var candidates []*daemon.TeamSyncResult
		for i := range results {
			r := &results[i]
			if (r.TeamSlug != "" && teamID == r.TeamSlug) || (r.TeamName != "" && teamID == r.TeamName) {
				candidates = append(candidates, r)
			}
		}
		switch len(candidates) {
		case 1:
			match = candidates[0]
		case 0:
			// fall through to not_found / legacy / transport handling below
		default:
			ids := make([]string, len(candidates))
			for i, c := range candidates {
				ids[i] = c.TeamID
			}
			tcResult.Status = "ambiguous"
			tcResult.Error = fmt.Sprintf("selector %q matches multiple teams (%s); use the team ID", teamID, strings.Join(ids, ", "))
			return tcResult
		}
	}

	switch {
	case match != nil:
		tcResult.TeamName = match.TeamName
		tcResult.TeamSlug = match.TeamSlug
		tcResult.Path = match.Path
		tcResult.Status = match.Status
		tcResult.Error = match.Error
	case results == nil && syncErr == nil:
		tcResult.Status = "unknown"
		tcResult.Error = "daemon did not report per-team results (restart the daemon: ox daemon restart)"
	case len(results) == 0 && syncErr != nil:
		tcResult.Status = "error"
		tcResult.Error = syncErr.Error()
	default:
		tcResult.Status = "not_found"
		tcResult.Error = fmt.Sprintf("team %q not found among synced team contexts", teamID)
	}

	return tcResult
}

// legacyDaemonError builds the error surfaced when a TeamSync returned success
// with no per-team data — i.e. the daemon predates the per-team-results protocol
// (a new CLI talking to an old, still-running daemon after an upgrade). It reads
// the daemon's reported version to tell the user it's running an older build. It
// does NOT restart the daemon: the daemon restarts itself on its next heartbeat
// (version-mismatch detection), so a re-run resolves it.
func legacyDaemonError() error {
	daemonVer := ""
	client := daemon.NewClientForCurrentRepoWithTimeout(5 * time.Second)
	if st, err := client.Status(); err == nil && st != nil {
		daemonVer = st.Version
	}
	return legacyDaemonErrorMsg(daemonVer)
}

// legacyDaemonErrorMsg formats the legacy-daemon error from the daemon's reported
// version. Split out (pure) so the message logic is unit-testable. daemonVersion
// may carry a "+builddate" suffix, which is stripped before comparison.
func legacyDaemonErrorMsg(daemonVersion string) error {
	const base = "daemon did not report per-team results"
	if daemonVersion == "" {
		return fmt.Errorf("%s; restart the daemon and retry (ox daemon restart)", base)
	}
	daemonSemver, _, _ := strings.Cut(daemonVersion, "+")
	if daemonSemver != version.Version {
		return fmt.Errorf("%s: the daemon is running an older version (%s) than this CLI (%s). "+
			"The daemon restarts itself on its next heartbeat — re-run shortly, or run 'ox daemon restart'",
			base, daemonSemver, version.Version)
	}
	// versions match but still no per-team data — unexpected; surface for diagnosis.
	return fmt.Errorf("%s (daemon version %s); restart the daemon and retry (ox daemon restart)", base, daemonSemver)
}

// syncAllTeamContexts syncs all team contexts via daemon.
// CLI delegates pull operations to daemon and reports the per-team results
// returned by the daemon in the JSON output.
func syncAllTeamContexts(_ context.Context, jsonOutput bool, result *SyncResult) error {
	run := func() ([]daemon.TeamSyncResult, error) {
		client := daemon.NewClientForCurrentRepoWithTimeout(60 * time.Second)
		return client.TeamSyncWithProgress(nil)
	}

	var results []daemon.TeamSyncResult
	var err error
	if !jsonOutput {
		results, err = cli.WithSpinner("Syncing team contexts via daemon...", run)
	} else {
		results, err = run()
	}
	if errors.Is(err, tea.ErrInterrupted) {
		return err
	}

	for _, r := range results {
		result.Transport.TeamContexts = append(result.Transport.TeamContexts, TeamContextSyncResult{
			TeamID:   r.TeamID,
			TeamName: r.TeamName,
			TeamSlug: r.TeamSlug,
			Path:     r.Path,
			Status:   r.Status,
			Error:    r.Error,
		})
	}

	// Any error from the daemon must fail the command — never print blanket
	// success on a non-nil error. This covers setup failures (e.g. unreadable
	// config) and aggregated per-team failures, regardless of whether the daemon
	// also sent (possibly empty) per-team data.
	if err != nil {
		return err
	}

	// Success with no per-team data at all (results is nil, not an empty array)
	// means a legacy pre-change daemon — we can't confirm anything synced, so
	// surface the version skew rather than reporting success. (A current daemon
	// with zero teams sends `[]`, which is non-nil and falls through to success.)
	if results == nil {
		retErr := legacyDaemonError()
		return retErr
	}

	// The sync succeeds only if every team context ended up locally usable
	// (synced or already up to date). Teams that errored or are still cloning are
	// not usable, so report them as a failure rather than printing blanket success
	// — otherwise downstream commands could proceed against missing/stale context.
	if notReady := notReadyTeams(results); len(notReady) > 0 {
		msg := fmt.Sprintf("team context(s) not ready: %s", strings.Join(notReady, ", "))
		return errors.New(msg)
	}

	return nil
}

// notReadyTeams returns "<name> (<status>)" labels for every team context that
// did not end up locally usable — i.e. anything other than "synced" or
// "skipped" (already up to date). Used to fail an all-teams sync when some team
// is still cloning or errored, instead of reporting blanket success.
func notReadyTeams(results []daemon.TeamSyncResult) []string {
	var notReady []string
	for _, r := range results {
		if r.Status == "synced" || r.Status == "skipped" {
			continue
		}
		name := r.TeamName
		if name == "" {
			name = r.TeamID
		}
		notReady = append(notReady, fmt.Sprintf("%s (%s)", name, r.Status))
	}
	return notReady
}

// syncPathExists checks if a path exists.
// named differently to avoid conflict with other pathExists in package
func syncPathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// autoStartDaemon starts the daemon in background for sync operations.
// This mirrors the logic in daemon.go:startDaemonBackground but is self-contained
// to avoid circular dependencies.
func autoStartDaemon() error {
	// OX_NO_DAEMON=1 prevents daemon start (integration tests)
	if os.Getenv("OX_NO_DAEMON") == "1" {
		return fmt.Errorf("daemon start disabled: OX_NO_DAEMON=1")
	}

	// get the path to the current executable
	exe, err := selfexec.Path()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	// start daemon process in background
	cmd := exec.Command(exe, "daemon", "start")
	proc.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	// detach - don't wait for the process
	return nil
}

// removeTeamContext removes a team context from all locations:
// 1. Project config (config.local.toml)
// 2. XDG data directory (~/.local/share/sageox/<endpoint>/teams/<team_id>)
func removeTeamContext(teamID string, jsonOutput bool) error {
	// find project root (optional - we can still remove XDG path without it)
	projectRoot, projectErr := repotools.FindRepoRoot(repotools.VCSGit)

	var localCfg *config.LocalConfig
	var projectCfg *config.ProjectConfig
	var tc *config.TeamContext
	var endpoint string

	// try to load project config for endpoint
	if projectErr == nil {
		projectCfg, _ = config.LoadProjectConfig(projectRoot)
		if projectCfg != nil {
			endpoint = projectCfg.GetEndpoint()
		}

		// try to load local config for team context
		localCfg, _ = config.LoadLocalConfig(projectRoot)
		if localCfg != nil {
			tc = localCfg.GetTeamContext(teamID)
		}
	}

	teamName := teamID
	var configPath string
	if tc != nil {
		if tc.TeamName != "" {
			teamName = tc.TeamName
		}
		configPath = tc.Path
	}

	// determine XDG path (centralized team storage)
	var xdgPath string
	if endpoint != "" {
		xdgPath = paths.TeamContextDir(teamID, endpoint)
	}

	// collect all paths to potentially delete
	var pathsToDelete []string
	if configPath != "" && syncPathExists(configPath) {
		pathsToDelete = append(pathsToDelete, configPath)
	}
	if xdgPath != "" && syncPathExists(xdgPath) && xdgPath != configPath {
		pathsToDelete = append(pathsToDelete, xdgPath)
	}

	// check if we found anything to remove
	if tc == nil && len(pathsToDelete) == 0 {
		if !jsonOutput {
			cli.PrintError(fmt.Sprintf("Team context not found: %s", teamID))
			if endpoint != "" {
				cli.PrintHint(fmt.Sprintf("Checked: project config and %s", xdgPath))
			}
		}
		return fmt.Errorf("team context not found: %s", teamID)
	}

	repoDeleted := false

	// delete repos
	for _, path := range pathsToDelete {
		if !jsonOutput {
			hasChanges, statusErr := checkGitHasUncommittedChanges(path)
			if statusErr != nil {
				cli.PrintWarning(fmt.Sprintf("Could not check git status: %v", statusErr))
			}

			var prompt string
			if hasChanges {
				prompt = fmt.Sprintf("Delete repo at %s? (has uncommitted changes)", path)
			} else {
				prompt = fmt.Sprintf("Delete repo at %s? (no uncommitted changes, safe to delete)", path)
			}

			if cli.ConfirmYesNo(prompt, !hasChanges) {
				if err := os.RemoveAll(path); err != nil {
					cli.PrintWarning(fmt.Sprintf("Failed to delete repo: %v", err))
				} else {
					cli.PrintSuccess(fmt.Sprintf("Deleted %s", path))
					repoDeleted = true
				}
			} else {
				cli.PrintInfo(fmt.Sprintf("Repo left at %s", path))
			}
		} else {
			// json mode: delete without prompting
			if err := os.RemoveAll(path); err == nil {
				repoDeleted = true
			}
		}
	}

	// remove from project config if present, under MutateLocalConfig
	// so concurrent daemon writes to config.local.toml don't lose the
	// removal (ox-dfy4).
	configRemoved := false
	if localCfg != nil && tc != nil {
		err := config.MutateLocalConfig(context.Background(), projectRoot, func(cfg *config.LocalConfig) error {
			cfg.RemoveTeamContext(teamID)
			return nil
		})
		if err != nil {
			if !jsonOutput {
				cli.PrintWarning(fmt.Sprintf("Failed to save config: %v", err))
			}
		} else {
			configRemoved = true
		}
	}

	if !jsonOutput {
		if configRemoved {
			cli.PrintSuccess(fmt.Sprintf("Removed team %s from configuration", teamName))
		} else if repoDeleted {
			cli.PrintSuccess(fmt.Sprintf("Removed team %s data", teamName))
		}
	}

	if jsonOutput {
		result := map[string]any{
			"success":        true,
			"team_id":        teamID,
			"config_removed": configRemoved,
			"repo_deleted":   repoDeleted,
			"paths_checked":  pathsToDelete,
		}
		cli.PrintJSON(result)
	}

	return nil
}

// checkGitHasUncommittedChanges checks if a git repo has uncommitted changes.
func checkGitHasUncommittedChanges(repoPath string) (bool, error) {
	cmd := exec.Command("git", "-C", repoPath, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(output) > 0, nil
}
