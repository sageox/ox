package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/tips"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

// checkResult represents a single diagnostic check
type checkResult struct {
	name          string
	passed        bool
	warning       bool
	skipped       bool
	priority      string // "critical", "info", or "" (default warning)
	message       string
	detail        string        // action hint shown on next line with └─
	children      []checkResult // nested child checks shown with ⎿
	fixLevel      FixLevel      // how fix should behave (from DoctorCheck metadata)
	slug          string        // unique identifier for programmatic reference
	requiresAgent bool          // indicates this issue requires `ox agent doctor` to resolve
}

// PassedCheck creates a passed check result
func PassedCheck(name, message string) checkResult {
	return checkResult{name: name, passed: true, message: message}
}

// FailedCheck creates a failed check result with detail
func FailedCheck(name, message, detail string) checkResult {
	return checkResult{name: name, passed: false, message: message, detail: detail}
}

// WarningCheck creates a passed check with warning flag
func WarningCheck(name, message, detail string) checkResult {
	return checkResult{name: name, passed: true, warning: true, message: message, detail: detail}
}

// SkippedCheck creates a skipped check result
func SkippedCheck(name, message, detail string) checkResult {
	return checkResult{name: name, skipped: true, message: message, detail: detail}
}

// CriticalCheck creates a failed check with critical priority
func CriticalCheck(name, message, detail string) checkResult {
	return checkResult{name: name, passed: false, priority: "critical", message: message, detail: detail}
}

// InfoCheck creates a warning check with info priority (optional/informational)
func InfoCheck(name, message, detail string) checkResult {
	return checkResult{name: name, passed: true, warning: true, priority: "info", message: message, detail: detail}
}

// AgentRequiredCheck creates a warning check that requires `ox agent doctor` to resolve.
// These checks report issues that cannot be fixed by `ox doctor --fix` but need agent intervention.
func AgentRequiredCheck(name, message, detail string) checkResult {
	return checkResult{
		name:          name,
		passed:        true,
		warning:       true,
		message:       message,
		detail:        detail,
		requiresAgent: true,
	}
}

// WithFixInfo attaches fix metadata to a check result
func (c checkResult) WithFixInfo(slug string, level FixLevel) checkResult {
	c.slug = slug
	c.fixLevel = level
	return c
}

// WithRequiresAgent marks a check as requiring agent intervention
func (c checkResult) WithRequiresAgent() checkResult {
	c.requiresAgent = true
	return c
}

// checkCategory groups related checks under a header
type checkCategory struct {
	name   string
	checks []checkResult
}

// doctorOptions holds options for doctor command
type doctorOptions struct {
	fix      bool
	fixSlugs []string // specific check slugs to fix (empty = fix all when fix=true)
	forceYes bool
	verbose  bool
}

// shouldFix returns true if the check identified by slug should attempt a fix.
// Auto-fix checks (FixLevelAuto) always return true - they fix automatically.
// For other checks: when fixSlugs is empty, returns opts.fix (--fix applies to all).
// When fixSlugs has entries, returns true only if slug is in the list.
func (opts doctorOptions) shouldFix(slug string) bool {
	// auto-fix checks always apply their fix (they're non-destructive and always safe)
	if check, ok := DoctorCheckRegistry[slug]; ok && check.IsAutoFixable() {
		return true
	}
	if len(opts.fixSlugs) == 0 {
		return opts.fix
	}
	for _, s := range opts.fixSlugs {
		if s == slug {
			return true
		}
	}
	return false
}

// doctorState holds detected environment state for conditional check suppression
type doctorState struct {
	isAuthenticated    bool
	isDaemonRunning    bool
	isBootstrapping    bool // daemon running but first sync not completed
	projectInitialized bool // .sageox/ exists
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run diagnostics on ox installation and configuration",
	Long: `Run comprehensive diagnostics on your ox installation, project configuration,
git health, agent environment, and connected services. Use --fix to auto-repair
common issues, or --fix-slug to target specific checks.`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		gitRoot := findGitRoot()

		// trigger daemon health checks (anti-entropy, etc.) if daemon is running
		// runs in background goroutine to not block CLI
		if daemon.IsRunning() {
			go func() {
				client := daemon.NewClient()
				_, _ = client.Doctor()
			}()
		}

		// determine endpoint for this context
		projectEndpoint := endpoint.GetForProject(gitRoot)
		if projectEndpoint == "" {
			projectEndpoint = endpoint.Get()
		}
		endpointSlug := endpoint.NormalizeSlug(projectEndpoint)

		// check authentication status
		authenticated, _ := auth.IsAuthenticatedForEndpoint(projectEndpoint)

		// check if project is initialized (only relevant if in a git repo)
		projectInitialized := gitRoot != "" && config.IsInitialized(gitRoot)

		// short-circuit with setup guidance if not ready
		if !authenticated || !projectInitialized {
			w := cmd.OutOrStdout()
			fmt.Fprintln(w)
			fmt.Fprintln(w, cli.StyleWarning.Render("Setup Required"))
			fmt.Fprintln(w, cli.StyleDim.Render("──────────────"))

			if !authenticated {
				fmt.Fprintf(w, "Step 1: Run %s to authenticate with %s\n",
					cli.StyleCommand.Render("ox login"), endpointSlug)
			} else {
				fmt.Fprintf(w, "%s Logged in to %s\n",
					cli.StyleSuccess.Render("✓"), endpointSlug)
			}

			if gitRoot == "" {
				fmt.Fprintf(w, "Step 2: Run %s inside a git repo to enable it to use %s\n",
					cli.StyleCommand.Render("ox init"), cli.Wordmark())
			} else if !projectInitialized {
				fmt.Fprintf(w, "Step 2: Run %s to initialize this project\n",
					cli.StyleCommand.Render("ox init"))
				fmt.Fprintf(w, "\n%s If a coworker has already set up %s on this repo,\n"+
					"  run %s first to pull their changes.\n",
					cli.StyleDim.Render("Tip:"),
					cli.Wordmark(),
					cli.StyleCommand.Render("git pull"))
			}

			fmt.Fprintln(w)
			return nil
		}

		fix, _ := cmd.Flags().GetBool("fix")
		fixSlugs, _ := cmd.Flags().GetStringSlice("fix-slug")
		forceYes, _ := cmd.Flags().GetBool("yes")
		verbose, _ := cmd.Flags().GetBool("verbose")

		// validate fix-slug values against registered checks
		if len(fixSlugs) > 0 {
			var invalidSlugs []string
			for _, slug := range fixSlugs {
				if GetDoctorCheck(slug) == nil {
					invalidSlugs = append(invalidSlugs, slug)
				}
			}
			if len(invalidSlugs) > 0 {
				// collect available slugs for helpful error message
				availableSlugs := getAvailableSlugs()
				return fmt.Errorf("unknown check slug(s): %s\n\nAvailable slugs:\n  %s",
					strings.Join(invalidSlugs, ", "),
					strings.Join(availableSlugs, "\n  "))
			}
		}

		opts := doctorOptions{
			fix:      fix || len(fixSlugs) > 0, // --fix-slug implies fix mode
			fixSlugs: fixSlugs,
			forceYes: forceYes,
			verbose:  verbose,
		}

		categories := runDoctorChecks(opts)
		hasFailed := displayDoctorResults(cmd, categories, opts)

		// record doctor run timestamp for staleness tracking
		recordDoctorRun(opts.fix)

		// clear .needs-doctor marker if --fix ran successfully without failures
		if opts.fix && !hasFailed && gitRoot != "" {
			_ = doctor.ClearNeedsDoctorHuman(gitRoot)
		}

		// show contextual tip before returning
		userCfg, _ := config.LoadUserConfig("")
		tips.MaybeShow("doctor", tips.RandomChance, cfg.Quiet, !userCfg.AreTipsEnabled(), cfg.JSON)

		cli.PrintDisclaimer()

		if hasFailed {
			return fmt.Errorf("some checks failed")
		}
		return nil
	},
}

// getAvailableSlugs returns a sorted list of all registered check slugs.
func getAvailableSlugs() []string {
	var slugs []string
	for slug := range DoctorCheckRegistry {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	return slugs
}

func init() {
	doctorCmd.Flags().Bool("fix", false, "automatically fix issues where possible")
	doctorCmd.Flags().StringSlice("fix-slug", nil, "fix specific issue(s) by slug (repeatable, e.g., --fix-slug=ledger-path --fix-slug=team-symlink)")
	doctorCmd.Flags().BoolP("yes", "y", false, "answer yes to all prompts (for non-interactive use)")
	doctorCmd.Flags().BoolP("verbose", "v", false, "show all checks including passed and skipped")
}

// detectDoctorState detects environment state for conditional check suppression
func detectDoctorState() doctorState {
	// use project-specific endpoint if available, otherwise default
	gitRoot := findGitRoot()
	projectEndpoint := endpoint.GetForProject(gitRoot)

	var authenticated bool
	if projectEndpoint != "" {
		authenticated, _ = auth.IsAuthenticatedForEndpoint(projectEndpoint)
	} else {
		authenticated, _ = auth.IsAuthenticated()
	}

	daemonRunning := daemon.IsRunning()

	var isBootstrapping bool
	if daemonRunning {
		client := daemon.NewClient()
		if status, err := client.Status(); err == nil {
			isBootstrapping = status.TotalSyncs == 0 &&
				status.Uptime < 3*time.Minute
		}
	}

	projectInitialized := gitRoot != "" && config.IsInitialized(gitRoot)

	return doctorState{
		isAuthenticated:    authenticated,
		isDaemonRunning:    daemonRunning,
		isBootstrapping:    isBootstrapping,
		projectInitialized: projectInitialized,
	}
}

// doctorProgress shows per-category progress during doctor checks.
// Displays timing for each category so users can see what's slow.
type doctorProgress struct {
	interactive bool
	verbose     bool
	lastStart   time.Time
	lastName    string
	lineCount   int
}

func newDoctorProgress(verbose bool) *doctorProgress {
	return &doctorProgress{
		interactive: cli.IsInteractive(),
		verbose:     verbose,
	}
}

// show prints the current category being checked.
// In verbose mode: prints timing for previous category on its own line.
// In non-verbose mode: overwrites the progress line in-place.
func (p *doctorProgress) show(category string) {
	if !p.interactive {
		return
	}

	// show timing for previous category in verbose mode
	if p.verbose && p.lastName != "" {
		elapsed := time.Since(p.lastStart)
		fmt.Fprintf(os.Stderr, "\r\033[K  %s (%dms)\n", p.lastName, elapsed.Milliseconds())
	}

	p.lastName = category
	p.lastStart = time.Now()

	// show current category
	fmt.Fprintf(os.Stderr, "\r\033[K  Checking %s...", category)
}

// clear removes the ephemeral status line and logs final timing
func (p *doctorProgress) clear() {
	if !p.interactive {
		return
	}

	// show timing for last category in verbose mode
	if p.verbose && p.lastName != "" {
		elapsed := time.Since(p.lastStart)
		fmt.Fprintf(os.Stderr, "\r\033[K  %s (%dms)\n", p.lastName, elapsed.Milliseconds())
	}

	// clear the progress line
	fmt.Fprint(os.Stderr, "\r\033[K")
}

func runDoctorChecks(opts doctorOptions) []checkCategory {
	var categories []checkCategory

	progress := newDoctorProgress(opts.verbose)
	defer progress.clear()

	// detect environment state early for conditional suppression
	state := detectDoctorState()

	// Category 0: Authentication (FIRST - most important)
	// SageOx requires authentication to function
	progress.show("Authentication")
	authCheck := checkAuthentication()
	authChecks := []checkResult{authCheck}
	// only check git credentials if authenticated (otherwise it will fail anyway)
	if authCheck.passed {
		authChecks = append(authChecks, checkGitCredentials())
	}
	categories = append(categories, checkCategory{
		name:   "Authentication",
		checks: authChecks,
	})

	// Category 1: Project Structure
	progress.show("Project Structure")
	projectChecks := []checkResult{
		checkSageoxDirectory(),
		checkSageoxGitignore(opts.shouldFix(CheckSlugSageoxGitignore)),
	}
	// only show legacy check if legacy files actually exist
	legacyCheck := checkLegacySageoxMd()
	if legacyCheck.warning {
		projectChecks = append(projectChecks, legacyCheck)
	}
	configCheck := checkConfigFile(opts.shouldFix(CheckSlugConfigJSON))
	if configCheck.passed && !configCheck.skipped {
		configCheck.children = []checkResult{checkConfigFields(opts.shouldFix(CheckSlugConfigJSON))}
	}
	projectChecks = append(projectChecks, configCheck)
	// README.md is part of project structure
	projectChecks = append(projectChecks, checkReadmeFile(opts.shouldFix(CheckSlugReadme)))
	// repo marker file
	projectChecks = append(projectChecks, checkRepoMarker())
	// multiple endpoints check (only show if multiple endpoints detected)
	multiEndpointCheck := checkMultipleEndpoints()
	if multiEndpointCheck.warning {
		projectChecks = append(projectChecks, multiEndpointCheck)
	}
	// sibling directory without init (only show if inconsistency detected)
	siblingCheck := checkSiblingWithoutInit()
	if siblingCheck.warning {
		projectChecks = append(projectChecks, siblingCheck)
	}
	categories = append(categories, checkCategory{
		name:   "Project Structure",
		checks: projectChecks,
	})

	// Category 2: Integration
	// only show checks for tools that are actually detected
	progress.show("Integration")
	integrationChecks := []checkResult{
		checkAgentFileExists(),
		checkAgentsIntegrationWithFix(opts.shouldFix(CheckSlugClaudeCodeHooks)),
	}
	if detectClaudeCode() {
		integrationChecks = append(integrationChecks, checkClaudeCodeHooks(opts.shouldFix(CheckSlugClaudeCodeHooks)))
		// validate hook commands after checking hooks exist
		hookCmdCheck := checkHookCommands()
		if !hookCmdCheck.skipped {
			integrationChecks = append(integrationChecks, hookCmdCheck)
		}
		// check project-level hook completeness (all required events present)
		completenessCheck := checkProjectHookCompleteness(opts.shouldFix(CheckSlugHookCompleteness))
		if !completenessCheck.skipped {
			integrationChecks = append(integrationChecks, completenessCheck)
		}
		// also check project-level hooks if present
		projectHookCheck := checkProjectHookCommands()
		if !projectHookCheck.skipped {
			integrationChecks = append(integrationChecks, projectHookCheck)
		}
	}
	if detectOpenCode() {
		integrationChecks = append(integrationChecks, checkOpenCodeHooks(opts.shouldFix(CheckSlugOpenCodeHooks)))
	}
	if detectGemini() {
		integrationChecks = append(integrationChecks, checkGeminiHooks(opts.shouldFix(CheckSlugGeminiHooks)))
	}
	if detectCodex() {
		integrationChecks = append(integrationChecks, checkCodexIntegration())
	}
	if detectCodePuppy() {
		integrationChecks = append(integrationChecks, checkCodePuppyHooks(opts.shouldFix(CheckSlugCodePuppyHooks)))
	}
	categories = append(categories, checkCategory{
		name:   "Integration",
		checks: integrationChecks,
	})

	// Category 3: User Configuration (optional)
	categories = append(categories, checkCategory{
		name:   "User Config",
		checks: []checkResult{checkUserLevelIntegration()},
	})

	// Category 4: Git Status (SageOx-specific git tracking)
	progress.show("Git Status")
	gitStatusChecks := []checkResult{
		checkGitStatus(),
		checkSageoxFilesTracked(opts.shouldFix(CheckSlugGitignore)),
		checkGitignore(opts.shouldFix(CheckSlugGitignore)),
		checkGitattributes(opts.shouldFix(CheckSlugGitattributes)),
	}
	// only show sageox remote check if .sageox is its own git repo
	if isSageoxGitRepo() {
		gitStatusChecks = append(gitStatusChecks, checkSageoxRemote(opts.shouldFix(CheckSlugGitRemotes)))
	}
	categories = append(categories, checkCategory{
		name:   "Git Status",
		checks: gitStatusChecks,
	})

	// Category 5: Git Repository Health (general git health)
	progress.show("Git Repository Health")
	gitRepoChecks := []checkResult{
		checkGitConfig(opts.shouldFix(CheckSlugGitConfig)),
		checkGitRemotes(),
		checkGitRepoState(),
		checkMergeConflicts(),
		checkGitLockFiles(), // check for stale lock files
	}
	// slow checks only run with --fix
	if opts.fix {
		gitRepoChecks = append(gitRepoChecks, checkGitFsck())         // git fsck ~600ms+
		gitRepoChecks = append(gitRepoChecks, checkGitConnectivity()) // git ls-remote ~200ms-5s
	} else {
		gitRepoChecks = append(gitRepoChecks, SkippedCheck("git integrity", "use --fix for deep checks", ""))
		gitRepoChecks = append(gitRepoChecks, SkippedCheck("Git connectivity", "use --fix for network checks", ""))
	}
	gitRepoChecks = append(gitRepoChecks, checkGitAuth())
	gitRepoChecks = append(gitRepoChecks, checkGitHooks())
	gitRepoChecks = append(gitRepoChecks, checkGitLFS())
	gitRepoChecks = append(gitRepoChecks, checkStashedChanges())

	// git repo paths check - suppress individual warnings when not logged in
	if state.isAuthenticated {
		gitRepoChecks = append(gitRepoChecks, checkGitRepoPaths(opts.shouldFix(CheckSlugGitRepoPaths)))
	} else {
		gitRepoChecks = append(gitRepoChecks, SkippedCheck("git repo paths", "requires login", ""))
	}

	// add remote URL checks for ledger and team contexts (SageOx is multiplayer)
	gitRoot := findGitRoot()
	if gitRoot != "" {
		localCfg, err := config.LoadLocalConfig(gitRoot)
		if err == nil && localCfg != nil {
			// check ledger remote URL
			ledgerRemoteCheck := checkLedgerRemoteURL(localCfg)
			if !ledgerRemoteCheck.skipped {
				gitRepoChecks = append(gitRepoChecks, ledgerRemoteCheck)
			}
			// check team context remote URLs
			teamContextRemoteChecks := checkTeamContextRemoteURLs(localCfg)
			for _, check := range teamContextRemoteChecks {
				if !check.skipped {
					gitRepoChecks = append(gitRepoChecks, check)
				}
			}
		}
	}
	// add ledger structure migration check
	ledgerStructureCheck := checkLedgerStructureMigration()
	if !ledgerStructureCheck.skipped {
		gitRepoChecks = append(gitRepoChecks, ledgerStructureCheck)
	}
	// add team context symlink validation
	teamSymlinkCheck := checkTeamContextSymlinks()
	if !teamSymlinkCheck.skipped {
		gitRepoChecks = append(gitRepoChecks, teamSymlinkCheck)
	}
	categories = append(categories, checkCategory{
		name:   "Git Repository Health",
		checks: gitRepoChecks,
	})

	// Category 5b: Ledger Git Health (SageOx is multiplayer - always check)
	progress.show("Ledger Git Health")
	ledgerGitChecks := checkLedgerGitHealth(opts.fix, opts.shouldFix(CheckSlugLedgerRemote), opts.shouldFix(CheckSlugGitignoreMissing))
	if len(ledgerGitChecks) > 0 {
		categories = append(categories, checkCategory{
			name:   "Ledger Git Health",
			checks: ledgerGitChecks,
		})
	}

	// Category 6: Auth Security
	authSecurityChecks := []checkResult{checkAuthFilePermissions(opts.shouldFix(CheckSlugAuthPermissions))}
	categories = append(categories, checkCategory{
		name:   "Auth Security",
		checks: authSecurityChecks,
	})

	// Category 7: Ecosystem Tools
	// SageOx is multiplayer - no offline mode checks needed
	progress.show("Ecosystem")
	ecosystemChecks := []checkResult{
		checkOxInPath(),
	}
	categories = append(categories, checkCategory{
		name:   "Ecosystem",
		checks: ecosystemChecks,
	})

	// Category 8: Agent Environment
	progress.show("Agent Environment")
	agentEnvChecks := []checkResult{
		checkAgentEnvironment(),
		checkAgentEnvValidity(),
		checkConflictingAgentEnvVars(),
	}
	// add stale instance check (always runs, does not require login)
	agentEnvChecks = append(agentEnvChecks, checkInstanceStale(opts.shouldFix(CheckSlugInstanceStale)))
	// add daemon agent instance stale check (queries daemon for stale heartbeats)
	agentEnvChecks = append(agentEnvChecks, checkDaemonInstanceStale(opts.shouldFix(CheckSlugDaemonInstanceStale)))
	categories = append(categories, checkCategory{
		name:   "Agent Environment",
		checks: agentEnvChecks,
	})

	// Category 9: Sessions
	progress.show("Sessions")
	sessionChecks := checkSessionHealth(opts)
	if len(sessionChecks) > 0 {
		categories = append(categories, checkCategory{
			name:   "Sessions",
			checks: sessionChecks,
		})
	}

	// Category 10: Daemon
	progress.show("Daemon")
	if state.isDaemonRunning {
		daemonChecks := checkDaemonHealth(opts)
		if state.isBootstrapping && len(daemonChecks) > 0 {
			// prepend bootstrap info banner
			bootstrapBanner := InfoCheck("daemon bootstrap",
				"initial sync in progress",
				"Run `ox doctor` again in a minute")
			daemonChecks = append([]checkResult{bootstrapBanner}, daemonChecks...)
		}
		if len(daemonChecks) > 0 {
			categories = append(categories, checkCategory{
				name:   "Daemon",
				checks: daemonChecks,
			})
		}
	} else if state.projectInitialized {
		// project initialized but daemon not started - softer message
		categories = append(categories, checkCategory{
			name: "Daemon",
			checks: []checkResult{
				SkippedCheck("daemon status", "not started",
					"Daemon will auto-start on next session"),
			},
		})
	} else {
		// show single "daemon not running" skip instead of individual warnings
		categories = append(categories, checkCategory{
			name: "Daemon",
			checks: []checkResult{
				SkippedCheck("daemon status", "DAEMON NOT RUNNING",
					"Run `ox daemon start` to enable background sync and heartbeats"),
			},
		})
	}

	// Category 10b: Team Context (only if team contexts configured or legacy found)
	progress.show("Team Context")
	teamContextChecks := checkTeamContextHealth(opts)
	if len(teamContextChecks) > 0 {
		categories = append(categories, checkCategory{
			name:   "Team Context",
			checks: teamContextChecks,
		})
	}

	// Category 11: Updates
	progress.show("Updates")
	categories = append(categories, checkCategory{
		name:   "Updates",
		checks: []checkResult{checkForUpdates()},
	})

	// Category 12: SageOx Configuration
	// Endpoint consistency check - always run (doesn't require authentication)
	progress.show("SageOx Configuration")
	sageoxConfigChecks := []checkResult{
		checkEndpointConsistency(opts.shouldFix(CheckSlugEndpointConsistency)),
	}
	categories = append(categories, checkCategory{
		name:   "SageOx Configuration",
		checks: sageoxConfigChecks,
	})

	// Category 13: SageOx Service
	// suppress login-dependent SageOx service checks when not logged in
	progress.show("SageOx Service")
	if state.isAuthenticated {
		categories = append(categories, checkCategory{
			name: "SageOx Service",
			checks: []checkResult{
				checkAPIConnectivity(),
				checkAPIEndpoint(opts.fix),
				checkTeamRegistrationWithOpts(opts),
			},
		})

		// Category 14: Cloud Diagnostics (optional - only shows if cloud returns issues)
		// Cloud doctor detects things the local CLI cannot:
		// - Pending merge conflicts (same repo registered twice)
		// - Team invites pending acceptance
		// - Guidance updates available
		// - Billing/quota warnings (enterprise)
		// - Team-wide health issues
		if cloudChecks := checkCloudDoctor(); len(cloudChecks) > 0 {
			categories = append(categories, checkCategory{
				name:   "Cloud Diagnostics",
				checks: cloudChecks,
			})
		}
	} else {
		// when not logged in, show grouped skip for all login-dependent checks
		categories = append(categories, checkCategory{
			name: "SageOx Service",
			checks: []checkResult{
				SkippedCheck("service checks", "NOT LOGGED IN",
					"Run `ox login` to enable SageOx API, team registration, and cloud diagnostics"),
			},
		})
	}

	// enrich check results with fix metadata from registry
	categories = enrichWithFixMetadata(categories)

	return categories
}

// enrichWithFixMetadata adds fixLevel and slug to check results from the DoctorCheckRegistry.
// Matches checks by name to their registered metadata.
func enrichWithFixMetadata(categories []checkCategory) []checkCategory {
	for catIdx := range categories {
		for checkIdx := range categories[catIdx].checks {
			check := &categories[catIdx].checks[checkIdx]
			enrichCheckResult(check)
			// also enrich children
			for childIdx := range check.children {
				enrichCheckResult(&check.children[childIdx])
			}
		}
	}
	return categories
}

// enrichCheckResult adds fix metadata to a single check result
func enrichCheckResult(check *checkResult) {
	// skip if already enriched
	if check.slug != "" {
		return
	}
	// find matching DoctorCheck by name (case-insensitive)
	for slug, dc := range DoctorCheckRegistry {
		if strings.EqualFold(dc.Name, check.name) {
			check.slug = slug
			check.fixLevel = dc.FixLevel
			return
		}
	}
}

// checkLedgerGitHealth runs all ledger git health checks.
// Returns a slice of check results, or empty slice if no ledger exists.
// Parameters:
//   - networkChecks: whether to run network checks (git ls-remote); only with --fix
//   - fixRemote: whether to fix remote URL issues
//   - fixGitignore: whether to fix .sageox/.gitignore issues in checkouts
func checkLedgerGitHealth(networkChecks bool, fixRemote bool, fixGitignore bool) []checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return nil // no ledger found, skip entire category
	}

	if !isGitRepo(ledgerPath) {
		return nil // ledger not a git repo, skip entire category
	}

	var checks []checkResult
	// network checks (git ls-remote) only run with --fix to avoid slow network I/O
	if networkChecks {
		checks = append(checks, checkLedgerRemoteReachable())
	} else {
		checks = append(checks, SkippedCheck("Ledger remote connectivity", "use --fix for network checks", ""))
	}
	checks = append(checks,
		checkLedgerBranchStatus(),
		checkLedgerCleanWorkdir(),
		checkLedgerRemoteURLMatch(fixRemote),
	)

	// add checkout .gitignore checks (protects checkout.json from being committed)
	ledgerGitignoreCheck := checkLedgerCheckoutGitignore(fixGitignore)
	if !ledgerGitignoreCheck.skipped {
		checks = append(checks, ledgerGitignoreCheck)
	}

	teamGitignoreCheck := checkTeamContextCheckoutGitignore(fixGitignore)
	if !teamGitignoreCheck.skipped {
		checks = append(checks, teamGitignoreCheck)
	}

	return checks
}

// displayDoctorResults renders check results
// Default: priority-first summary showing only issues
// With --verbose: full category view with all checks
// With --json: structured JSON output
// Returns true if any checks failed (not warnings)
func displayDoctorResults(cmd *cobra.Command, categories []checkCategory, opts doctorOptions) bool {
	// check for JSON output mode
	if cfg != nil && cfg.JSON {
		return displayJSONResults(cmd, categories)
	}
	if opts.verbose {
		return displayVerboseResults(cmd, categories)
	}
	return displayPrioritySummary(cmd, categories)
}

// JSONCheckResult is the JSON-serializable representation of a check result
type JSONCheckResult struct {
	Name          string            `json:"name"`
	Slug          string            `json:"slug,omitempty"`
	Status        string            `json:"status"` // "passed", "warning", "failed", "skipped"
	Priority      string            `json:"priority,omitempty"`
	FixLevel      string            `json:"fix_level,omitempty"`
	RequiresAgent bool              `json:"requires_agent,omitempty"`
	Message       string            `json:"message,omitempty"`
	Detail        string            `json:"detail,omitempty"`
	Children      []JSONCheckResult `json:"children,omitempty"`
}

// JSONCategory is the JSON-serializable representation of a category
type JSONCategory struct {
	Name   string            `json:"name"`
	Checks []JSONCheckResult `json:"checks"`
}

// JSONDoctorOutput is the top-level JSON output structure
type JSONDoctorOutput struct {
	Categories     []JSONCategory `json:"categories"`
	Summary        JSONSummary    `json:"summary"`
	AvailableFixes []JSONFixInfo  `json:"available_fixes,omitempty"`
}

// JSONSummary contains the summary counts
type JSONSummary struct {
	Passed    int  `json:"passed"`
	Warnings  int  `json:"warnings"`
	Failed    int  `json:"failed"`
	Skipped   int  `json:"skipped"`
	HasFailed bool `json:"has_failed"`
}

// JSONFixInfo represents a fixable check in JSON output
type JSONFixInfo struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	FixLevel string `json:"fix_level"`
}

// displayJSONResults outputs check results as JSON
func displayJSONResults(cmd *cobra.Command, categories []checkCategory) bool {
	var passCount, warnCount, failCount, skipCount int
	hasFailed := false
	var fixableSlugs []fixSlugInfo

	// convert categories to JSON format
	jsonCategories := make([]JSONCategory, 0, len(categories))
	for _, cat := range categories {
		jsonCat := JSONCategory{
			Name:   cat.name,
			Checks: make([]JSONCheckResult, 0, len(cat.checks)),
		}
		for _, check := range cat.checks {
			jsonCheck := checkResultToJSON(check)
			jsonCat.Checks = append(jsonCat.Checks, jsonCheck)

			// count stats
			countCheck(check, &passCount, &warnCount, &failCount, &skipCount)
			if !check.passed && !check.skipped {
				hasFailed = true
			}
			collectFixableSlugs(check, &fixableSlugs)

			for _, child := range check.children {
				countCheck(child, &passCount, &warnCount, &failCount, &skipCount)
				if !child.passed && !child.skipped {
					hasFailed = true
				}
				collectFixableSlugs(child, &fixableSlugs)
			}
		}
		jsonCategories = append(jsonCategories, jsonCat)
	}

	// build available fixes list
	var jsonFixes []JSONFixInfo
	for _, f := range fixableSlugs {
		jsonFixes = append(jsonFixes, JSONFixInfo{
			Slug:     f.slug,
			Name:     f.name,
			FixLevel: string(f.fixLevel),
		})
	}

	output := JSONDoctorOutput{
		Categories: jsonCategories,
		Summary: JSONSummary{
			Passed:    passCount,
			Warnings:  warnCount,
			Failed:    failCount,
			Skipped:   skipCount,
			HasFailed: hasFailed,
		},
		AvailableFixes: jsonFixes,
	}

	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(output)

	return hasFailed
}

// checkResultToJSON converts a checkResult to its JSON representation
func checkResultToJSON(check checkResult) JSONCheckResult {
	status := "passed"
	if check.skipped {
		status = "skipped"
	} else if !check.passed {
		status = "failed"
	} else if check.warning {
		status = "warning"
	}

	jsonCheck := JSONCheckResult{
		Name:          check.name,
		Slug:          check.slug,
		Status:        status,
		Priority:      check.priority,
		FixLevel:      string(check.fixLevel),
		RequiresAgent: check.requiresAgent,
		Message:       check.message,
		Detail:        check.detail,
	}

	// convert children
	if len(check.children) > 0 {
		jsonCheck.Children = make([]JSONCheckResult, 0, len(check.children))
		for _, child := range check.children {
			jsonCheck.Children = append(jsonCheck.Children, checkResultToJSON(child))
		}
	}

	return jsonCheck
}

// fixSlugInfo tracks a fixable check's slug and level
type fixSlugInfo struct {
	slug     string
	fixLevel FixLevel
	name     string
}

// collectFixableSlugs gathers slugs from failed/warning checks that have fixes
func collectFixableSlugs(check checkResult, slugs *[]fixSlugInfo) {
	// only collect for non-passed checks that have fix info
	if check.passed && !check.warning {
		return
	}
	if check.skipped {
		return
	}
	if check.slug == "" || check.fixLevel == "" || check.fixLevel == FixLevelCheckOnly {
		return
	}
	*slugs = append(*slugs, fixSlugInfo{
		slug:     check.slug,
		fixLevel: check.fixLevel,
		name:     check.name,
	})
}

// renderFixLevelBadge returns a styled badge for the fix level
func renderFixLevelBadge(level FixLevel) string {
	switch level {
	case FixLevelAuto:
		return ui.PassStyle.Render("[auto-fix]")
	case FixLevelSuggested:
		return ui.AccentStyle.Render("[--fix]")
	case FixLevelConfirm:
		return ui.WarnStyle.Render("[confirm]")
	case FixLevelCheckOnly:
		return ui.MutedStyle.Render("[manual]")
	default:
		return ""
	}
}

// renderAgentRequiredBadge returns a styled badge for agent-required checks
func renderAgentRequiredBadge() string {
	return ui.AccentStyle.Render("[agent]")
}

// renderFixableSlugs displays available fix slugs grouped by fix level
func renderFixableSlugs(cmd *cobra.Command, slugs []fixSlugInfo) {
	if len(slugs) == 0 {
		return
	}

	// group by fix level
	autoFixes := []string{}
	suggestedFixes := []string{}
	confirmFixes := []string{}

	for _, s := range slugs {
		switch s.fixLevel {
		case FixLevelAuto:
			autoFixes = append(autoFixes, s.slug)
		case FixLevelSuggested:
			suggestedFixes = append(suggestedFixes, s.slug)
		case FixLevelConfirm:
			confirmFixes = append(confirmFixes, s.slug)
		}
	}

	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), ui.MutedStyle.Render("Available fixes:"))

	if len(autoFixes) > 0 {
		sort.Strings(autoFixes)
		fmt.Fprintf(cmd.OutOrStdout(), "  %s %s\n",
			renderFixLevelBadge(FixLevelAuto),
			ui.MutedStyle.Render(strings.Join(autoFixes, ", ")))
	}
	if len(suggestedFixes) > 0 {
		sort.Strings(suggestedFixes)
		fmt.Fprintf(cmd.OutOrStdout(), "  %s %s\n",
			renderFixLevelBadge(FixLevelSuggested),
			ui.MutedStyle.Render(strings.Join(suggestedFixes, ", ")))
	}
	if len(confirmFixes) > 0 {
		sort.Strings(confirmFixes)
		fmt.Fprintf(cmd.OutOrStdout(), "  %s %s\n",
			renderFixLevelBadge(FixLevelConfirm),
			ui.MutedStyle.Render(strings.Join(confirmFixes, ", ")))
	}
}

// displayPrioritySummary shows issues grouped by priority (default view)
func displayPrioritySummary(cmd *cobra.Command, categories []checkCategory) bool {
	fmt.Fprintln(cmd.OutOrStdout(), "\nox doctor")
	fmt.Fprintln(cmd.OutOrStdout())

	var passCount, warnCount, failCount, skipCount int
	var critical, attention, optional, agentRequired []checkResult
	hasFailed := false

	// collect fixable slugs for summary
	var fixableSlugs []fixSlugInfo

	// collect and categorize all checks
	for _, cat := range categories {
		for _, check := range cat.checks {
			countCheck(check, &passCount, &warnCount, &failCount, &skipCount)
			categorizeCheck(check, &critical, &attention, &optional, &agentRequired, &hasFailed)
			collectFixableSlugs(check, &fixableSlugs)

			for _, child := range check.children {
				countCheck(child, &passCount, &warnCount, &failCount, &skipCount)
				categorizeCheck(child, &critical, &attention, &optional, &agentRequired, &hasFailed)
				collectFixableSlugs(child, &fixableSlugs)
			}
		}
	}

	// render priority sections (grouped by remediation action)
	if len(critical) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), ui.RenderFail(ui.IconFail+" CRITICAL"))
		renderGroupedChecks(cmd, critical)
		fmt.Fprintln(cmd.OutOrStdout())
	}

	if len(attention) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), ui.RenderWarn(ui.IconWarn+" NEEDS ATTENTION"))
		renderGroupedChecks(cmd, attention)
		fmt.Fprintln(cmd.OutOrStdout())
	}

	// agent-required section: issues that need `ox agent doctor` to resolve
	if len(agentRequired) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), ui.RenderAccent(ui.IconAgent+" REQUIRES AGENT"))
		renderGroupedChecks(cmd, agentRequired)
		fmt.Fprintln(cmd.OutOrStdout())
	}

	if len(optional) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), ui.MutedStyle.Render("ℹ OPTIONAL"))
		renderGroupedChecks(cmd, optional)
		fmt.Fprintln(cmd.OutOrStdout())
	}

	// summary line
	fmt.Fprintln(cmd.OutOrStdout(), ui.RenderSeparator())
	summary := fmt.Sprintf("Summary: %s %d passed  %s %d warnings  %s %d failed",
		ui.RenderPassIcon(), passCount,
		ui.RenderWarnIcon(), warnCount,
		ui.RenderFailIcon(), failCount,
	)
	// add skipped count if any
	if skipCount > 0 {
		summary += fmt.Sprintf("  %s %d skipped", ui.MutedStyle.Render(ui.IconSkip), skipCount)
	}
	fmt.Fprintln(cmd.OutOrStdout(), summary)

	// when everything is healthy, show a reassuring confirmation
	if failCount == 0 && warnCount == 0 {
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), ui.RenderPass("All checks passed — setup is healthy"))
	}

	// show fixable slugs if any failed or warning checks have fixes
	renderFixableSlugs(cmd, fixableSlugs)

	if passCount > 0 && len(fixableSlugs) == 0 {
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), ui.MutedStyle.Render("Run ox doctor -v for full details"))
	}

	return hasFailed
}

// countCheck updates pass/warn/fail/skip counts for a check
func countCheck(check checkResult, passCount, warnCount, failCount, skipCount *int) {
	if check.skipped {
		*skipCount++
		return
	}
	if check.passed {
		if check.warning {
			*warnCount++
		} else {
			*passCount++
		}
	} else {
		*failCount++
	}
}

// categorizeCheck places a check into the appropriate priority bucket
func categorizeCheck(check checkResult, critical, attention, optional, agentRequired *[]checkResult, hasFailed *bool) {
	if check.skipped {
		return // skip neutral items
	}

	// agent-required checks go to their own bucket
	if check.requiresAgent && (check.warning || !check.passed) {
		*agentRequired = append(*agentRequired, check)
		if !check.passed {
			*hasFailed = true
		}
		return
	}

	if !check.passed {
		*hasFailed = true
		if check.priority == "critical" {
			*critical = append(*critical, check)
		} else {
			*attention = append(*attention, check)
		}
	} else if check.warning {
		if check.priority == "info" {
			*optional = append(*optional, check)
		} else {
			*attention = append(*attention, check)
		}
	}
	// passed checks without warning are not shown in priority view
}

// checkGroup represents checks grouped by remediation action
type checkGroup struct {
	detail string
	checks []checkResult
}

// renderGroupedChecks groups checks by remediation and renders them
func renderGroupedChecks(cmd *cobra.Command, checks []checkResult) {
	// group checks by detail (remediation action)
	groups := groupChecksByDetail(checks)

	for _, group := range groups {
		if len(group.checks) == 1 {
			// single check - render normally
			check := group.checks[0]
			line := fmt.Sprintf("  %s", check.name)
			// add agent-required badge if applicable
			if check.requiresAgent {
				line += " " + renderAgentRequiredBadge()
			} else if badge := renderFixLevelBadge(check.fixLevel); badge != "" {
				// add fix level badge if available (only if not agent-required)
				line += " " + badge
			}
			if check.message != "" {
				line += ui.MutedStyle.Render(" (" + check.message + ")")
			}
			fmt.Fprintln(cmd.OutOrStdout(), line)
			if check.detail != "" {
				formattedDetail := cli.FormatTipText(check.detail)
				fmt.Fprintf(cmd.OutOrStdout(), "  %s%s\n", ui.MutedStyle.Render(ui.TreeLast), formattedDetail)
			}
		} else {
			// multiple checks with same remediation - group them
			action := extractActionHeader(group.detail)
			header := fmt.Sprintf("  %s%s", action, ui.MutedStyle.Render(fmt.Sprintf(" (%d items)", len(group.checks))))
			fmt.Fprintln(cmd.OutOrStdout(), header)

			for _, check := range group.checks {
				name := check.name
				// add agent-required badge if applicable
				if check.requiresAgent {
					name += " " + renderAgentRequiredBadge()
				} else if badge := renderFixLevelBadge(check.fixLevel); badge != "" {
					// add fix level badge if available (only if not agent-required)
					name += " " + badge
				}
				if check.message != "" {
					name += ui.MutedStyle.Render(" (" + check.message + ")")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "    %s %s\n", ui.MutedStyle.Render("•"), name)
			}
		}
	}
}

// groupChecksByDetail groups checks by their primary remediation command
func groupChecksByDetail(checks []checkResult) []checkGroup {
	// use ordered map to preserve insertion order
	seen := make(map[string]int) // key -> index in groups
	var groups []checkGroup

	for _, check := range checks {
		// extract the command from detail to use as grouping key
		key := extractGroupingKey(check.detail)

		if idx, ok := seen[key]; ok {
			groups[idx].checks = append(groups[idx].checks, check)
		} else {
			seen[key] = len(groups)
			groups = append(groups, checkGroup{
				detail: check.detail,
				checks: []checkResult{check},
			})
		}
	}

	return groups
}

// extractGroupingKey extracts the primary command from a detail string for grouping
// e.g., "Run `ox init` to create it" -> "ox init"
// e.g., "Run `ox doctor --fix` to add them" -> "ox doctor --fix"
func extractGroupingKey(detail string) string {
	if detail == "" {
		return "__no_detail__"
	}

	// extract command from backticks
	start := strings.Index(detail, "`")
	if start != -1 {
		end := strings.Index(detail[start+1:], "`")
		if end != -1 {
			return detail[start+1 : start+1+end]
		}
	}

	// no backticks - use full detail as key
	return detail
}

// extractActionHeader extracts a short header from a detail string
// e.g., "Run `ox init` to create it" -> "Run ox init"
func extractActionHeader(detail string) string {
	if detail == "" {
		return "Other issues"
	}

	// take first line only
	if idx := strings.Index(detail, "\n"); idx != -1 {
		detail = detail[:idx]
	}

	// extract command from backticks if present
	start := strings.Index(detail, "`")
	if start != -1 {
		end := strings.Index(detail[start+1:], "`")
		if end != -1 {
			cmd := detail[start+1 : start+1+end]
			// return "Run <cmd>" format
			if strings.HasPrefix(strings.ToLower(detail), "run") {
				return "Run " + cmd
			}
			return cmd
		}
	}

	// fallback: truncate at reasonable length
	if len(detail) > 40 {
		return detail[:37] + "..."
	}
	return detail
}

// displayVerboseResults shows full category-based output (ox doctor -v)
func displayVerboseResults(cmd *cobra.Command, categories []checkCategory) bool {
	fmt.Fprintln(cmd.OutOrStdout(), "\nox doctor -v")
	fmt.Fprintln(cmd.OutOrStdout())

	var passCount, warnCount, failCount, skipCount int
	hasFailed := false

	// collect fixable slugs for summary
	var fixableSlugs []fixSlugInfo

	for _, cat := range categories {
		// category header
		fmt.Fprintln(cmd.OutOrStdout(), ui.RenderCategory(cat.name))

		for _, check := range cat.checks {
			renderCheck(cmd, check, 1, &passCount, &warnCount, &failCount, &skipCount)
			if !check.passed && !check.warning && !check.skipped {
				hasFailed = true
			}
			collectFixableSlugs(check, &fixableSlugs)

			for _, child := range check.children {
				if !child.passed && !child.warning && !child.skipped {
					hasFailed = true
				}
				collectFixableSlugs(child, &fixableSlugs)
			}
		}
		fmt.Fprintln(cmd.OutOrStdout())
	}

	// summary line
	fmt.Fprintln(cmd.OutOrStdout(), ui.RenderSeparator())
	summary := fmt.Sprintf("Summary: %s %d passed  %s %d warnings  %s %d failed",
		ui.RenderPassIcon(), passCount,
		ui.RenderWarnIcon(), warnCount,
		ui.RenderFailIcon(), failCount,
	)
	// add skipped count if any
	if skipCount > 0 {
		summary += fmt.Sprintf("  %s %d skipped", ui.MutedStyle.Render(ui.IconSkip), skipCount)
	}
	fmt.Fprintln(cmd.OutOrStdout(), summary)

	// show fixable slugs if any failed or warning checks have fixes
	renderFixableSlugs(cmd, fixableSlugs)

	return hasFailed
}

func renderCheck(cmd *cobra.Command, check checkResult, depth int, passCount, warnCount, failCount, skipCount *int) {
	indent := strings.Repeat(ui.TreeIndent, depth)

	// determine status icon and style
	var statusIcon string
	if check.skipped {
		statusIcon = ui.MutedStyle.Render(ui.IconSkip)
		*skipCount++
	} else if check.passed {
		if check.warning {
			// use agent icon for agent-required warnings
			if check.requiresAgent {
				statusIcon = ui.AccentStyle.Render(ui.IconAgent)
			} else {
				statusIcon = ui.WarnStyle.Render(ui.IconWarn)
			}
			*warnCount++
		} else {
			statusIcon = ui.PassStyle.Render(ui.IconPass)
			*passCount++
		}
	} else {
		// use agent icon for agent-required failures
		if check.requiresAgent {
			statusIcon = ui.AccentStyle.Render(ui.IconAgent)
		} else {
			statusIcon = ui.FailStyle.Render(ui.IconFail)
		}
		*failCount++
	}

	// build the check line
	line := fmt.Sprintf("%s%s %s", indent, statusIcon, check.name)
	// add badge for non-passed checks
	if (!check.passed || check.warning) && !check.skipped {
		if check.requiresAgent {
			line += " " + renderAgentRequiredBadge()
		} else if badge := renderFixLevelBadge(check.fixLevel); badge != "" {
			line += " " + badge
		}
	}
	if check.message != "" {
		line += ui.MutedStyle.Render(" (" + check.message + ")")
	}
	fmt.Fprintln(cmd.OutOrStdout(), line)

	// render detail line if present (with command highlighting)
	if check.detail != "" {
		detailIndent := strings.Repeat(ui.TreeIndent, depth+1)
		// format backtick-wrapped commands with highlighting, then apply muted style to the rest
		formattedDetail := cli.FormatTipText(check.detail)
		fmt.Fprintln(cmd.OutOrStdout(), ui.MutedStyle.Render(detailIndent+ui.TreeLast)+formattedDetail)
	}

	// render children with tree indicators
	for _, child := range check.children {
		childIndent := strings.Repeat(ui.TreeIndent, depth)

		// determine child status icon
		var childIcon string
		if child.skipped {
			childIcon = ui.MutedStyle.Render(ui.IconSkip)
			*skipCount++
		} else if child.passed {
			if child.warning {
				// use agent icon for agent-required warnings
				if child.requiresAgent {
					childIcon = ui.AccentStyle.Render(ui.IconAgent)
				} else {
					childIcon = ui.WarnStyle.Render(ui.IconWarn)
				}
				*warnCount++
			} else {
				childIcon = ui.PassStyle.Render(ui.IconPass)
				*passCount++
			}
		} else {
			// use agent icon for agent-required failures
			if child.requiresAgent {
				childIcon = ui.AccentStyle.Render(ui.IconAgent)
			} else {
				childIcon = ui.FailStyle.Render(ui.IconFail)
			}
			*failCount++
		}

		childLine := fmt.Sprintf("%s%s%s %s", childIndent, ui.MutedStyle.Render(ui.TreeChild), childIcon, child.name)
		// add badge for non-passed children
		if (!child.passed || child.warning) && !child.skipped {
			if child.requiresAgent {
				childLine += " " + renderAgentRequiredBadge()
			} else if badge := renderFixLevelBadge(child.fixLevel); badge != "" {
				childLine += " " + badge
			}
		}
		if child.message != "" {
			childLine += ui.MutedStyle.Render(" (" + child.message + ")")
		}
		fmt.Fprintln(cmd.OutOrStdout(), childLine)

		// render child detail (with command highlighting)
		if child.detail != "" {
			detailIndent := childIndent + ui.TreeIndent
			formattedDetail := cli.FormatTipText(child.detail)
			fmt.Fprintln(cmd.OutOrStdout(), ui.MutedStyle.Render(detailIndent+ui.TreeLast)+formattedDetail)
		}
	}
}

// recordDoctorRun saves the doctor run timestamp to .sageox/health.json.
// Runs silently - errors are ignored since this is non-critical telemetry.
func recordDoctorRun(fix bool) {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return
	}

	health, err := config.LoadHealth(gitRoot)
	if err != nil {
		return
	}

	health.RecordDoctorRun()
	if fix {
		health.RecordDoctorFixRun()
	}

	_ = config.SaveHealth(gitRoot, health)
}
