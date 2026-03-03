package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/pprof"
	"runtime/trace"
	"strings"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	cfg *config.Config

	// CLI context for dependency injection
	cliCtx *cli.Context

	// profiling state
	profileEnabled bool
	profileFile    *os.File
	traceFile      *os.File
)

var rootCmd = &cobra.Command{
	Use:   "ox",
	Short: "Shared team context that makes agentic engineering multiplayer",
	Long:  `Shared team context between your AI and human coworkers. Sessions, ledgers, and team knowledge that make agentic engineering multiplayer.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// initialize CLI context (centralizes config, logger, telemetry)
		var err error
		cliCtx, err = cli.NewContext(cmd, args)
		if err != nil {
			return err
		}

		// store config in global for backward compatibility
		cfg = cliCtx.Config

		// Daemon is started by `ox agent prime` as a child process of the coding agent.
		// No auto-start here — when the agent exits, the daemon gets cleaned up automatically.
		// Users can still start the daemon manually with `ox daemon start`.

		// fire-and-forget heartbeat so daemon always knows current CLI version
		// (triggers version mismatch restart when CLI is upgraded)
		if shouldHeartbeat(cmd) && config.IsInitializedInCwd() && daemon.IsRunning() {
			if gitRoot := findGitRoot(); gitRoot != "" {
				Heartbeat(gitRoot, nil, "")
			}
		}

		// performance profiling setup
		// creates CPU profile (.prof) and execution trace (.out) files
		if profileEnabled {
			timestamp := cliCtx.CommandStartTime.Format("20060102-150405")
			if f, err := os.Create(fmt.Sprintf("ox-profile-%s-%s.prof", cmd.Name(), timestamp)); err == nil {
				profileFile = f
				_ = pprof.StartCPUProfile(f)
			}
			if f, err := os.Create(fmt.Sprintf("ox-trace-%s-%s.out", cmd.Name(), timestamp)); err == nil {
				traceFile = f
				_ = trace.Start(f)
			}
		}

		return nil
	},
	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		// track command completion via telemetry (non-blocking)
		if cliCtx != nil {
			cliCtx.TrackCommandCompletion(cmd)
			cliCtx.Shutdown()
		}

		// stop profiling and close files
		if profileFile != nil {
			pprof.StopCPUProfile()
			_ = profileFile.Close()
		}
		if traceFile != nil {
			trace.Stop()
			_ = traceFile.Close()
		}

		return nil
	},
	Version: version.Version,
}

// registerPersistentFlags registers all global persistent flags on rootCmd.
// This is called both during init() and after ResetFlags() in friction recovery.
func registerPersistentFlags() {
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "enable verbose output (default: false)")
	rootCmd.PersistentFlags().BoolP("quiet", "q", false, "suppress non-error output (default: false)")
	rootCmd.PersistentFlags().Bool("json", false, "output in JSON format (default: false)")
	rootCmd.PersistentFlags().StringP("config", "c", "", "config file path (default: .sageox/config.yaml)")
	rootCmd.PersistentFlags().BoolVar(&profileEnabled, "profile", false, "generate CPU profile and execution trace for performance analysis (default: false)")
	rootCmd.PersistentFlags().Bool("no-interactive", false, "disable spinners and TUI elements (auto-enabled when CI=true)")
}

func init() {
	registerPersistentFlags()

	// custom version template - include build timestamp for dev builds
	versionStr := version.Version
	if version.BuildDate != "unknown" && version.BuildDate != "" {
		// parse ISO8601 and format as compact timestamp (yymmdd-hhmmss)
		if t, err := time.Parse(time.RFC3339, version.BuildDate); err == nil {
			versionStr = fmt.Sprintf("%s (%s)", version.Version, t.Format("060102-150405"))
		}
	}
	rootCmd.SetVersionTemplate(fmt.Sprintf("ox version %s\n", versionStr))

	// custom help with brand colors
	rootCmd.SetHelpFunc(brandedHelp)

	// define command groups
	rootCmd.AddGroup(&cobra.Group{ID: "dev", Title: "Software Development:"})
	rootCmd.AddGroup(&cobra.Group{ID: "auth", Title: "Authentication:"})
	rootCmd.AddGroup(&cobra.Group{ID: "diagnostics", Title: "Diagnostics:"})
	rootCmd.AddGroup(&cobra.Group{ID: "agent-interface", Title: "Agent Integration:"})
	agentCmd.GroupID = "agent-interface"

	// software development commands
	initCmd.GroupID = "dev"
	importCmd.GroupID = "dev"

	// auth commands
	loginCmd.GroupID = "auth"
	logoutCmd.GroupID = "auth"
	statusCmd.GroupID = "auth"

	// diagnostics commands
	doctorCmd.GroupID = "diagnostics"
	versionCmd.GroupID = "diagnostics"
	releaseNotesCmd.GroupID = "diagnostics"
	daemonCmd.GroupID = "diagnostics"

	// register commands
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(importCmd)
	// agentCmd is registered in agent.go

	// auth commands
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
	rootCmd.AddCommand(statusCmd)

	// diagnostics commands
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(releaseNotesCmd)
	rootCmd.AddCommand(daemonCmd)

	// silence default error and usage handling - we handle errors ourselves
	// usage is not shown on runtime errors (network failures, auth issues, etc.)
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true

	// disable Cobra's built-in suggestions - we use uxfriction for "did you mean?"
	rootCmd.DisableSuggestions = true

	// hide completion command unless post-MVP features are enabled
	// completion is for power users who want shell auto-complete
	if !auth.IsPostMVPEnabled() {
		rootCmd.CompletionOptions.DisableDefaultCmd = true
	}
}

// Execute runs the root command
func Execute() error {
	return rootCmd.Execute()
}

// TrackCommandError records a command error via telemetry.
// Called from main.go when a command returns an error.
// Non-blocking, fire-and-forget.
func TrackCommandError(cmd *cobra.Command, err error) {
	if cliCtx != nil {
		cliCtx.TrackCommandError(cmd, err)
	}
}

// brandedHelp renders help with SageOx brand colors
func brandedHelp(cmd *cobra.Command, args []string) {
	isRoot := cmd.Parent() == nil

	fmt.Println()

	if isRoot {
		// root command header
		fmt.Println(cli.StyleBrand.Render("ox") + " - Shared team context for AI and human coworkers")
		fmt.Println(cli.StyleDim.Render("Making agentic engineering multiplayer."))
		fmt.Println()
	} else {
		// subcommand header
		fmt.Printf("%s %s\n", cli.StyleBrand.Render("ox "+cmd.Name()), cli.StyleDim.Render("- "+cmd.Short))
		if cmd.Long != "" && cmd.Long != cmd.Short {
			fmt.Println()
			fmt.Println(cli.StyleDim.Render(cmd.Long))
		}
		fmt.Println()
	}

	// usage section
	printSectionHeader("Usage")
	if isRoot {
		fmt.Printf("  %s %s\n", cli.StyleCommand.Render("ox"), cli.StyleDim.Render("[command]"))
	} else {
		useLine := cmd.UseLine()
		fmt.Printf("  %s\n", cli.StyleCommand.Render(useLine))
	}
	fmt.Println()

	// subcommands (for root or commands with subcommands)
	if isRoot {
		// commands by group
		groups := cmd.Groups()
		for _, group := range groups {
			printSectionHeader(group.Title[:len(group.Title)-1]) // remove trailing colon
			for _, subcmd := range cmd.Commands() {
				if subcmd.GroupID == group.ID && subcmd.IsAvailableCommand() {
					printCommandEntry(subcmd)
				}
			}
			fmt.Println()
		}

		// additional commands (ungrouped)
		hasUngrouped := false
		for _, subcmd := range cmd.Commands() {
			if subcmd.GroupID == "" && subcmd.IsAvailableCommand() {
				if !hasUngrouped {
					printSectionHeader("Additional Commands")
					hasUngrouped = true
				}
				printCommandEntry(subcmd)
			}
		}
		if hasUngrouped {
			fmt.Println()
		}
	} else if cmd.HasAvailableSubCommands() {
		// subcommands for non-root commands
		printSectionHeader("Commands")
		for _, subcmd := range cmd.Commands() {
			if subcmd.IsAvailableCommand() {
				printCommandEntry(subcmd)
			}
		}
		fmt.Println()
	}

	// flags
	if cmd.HasAvailableLocalFlags() {
		printSectionHeader("Flags")
		cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
			if flag.Hidden {
				return
			}
			var flagStr string
			if flag.Shorthand != "" {
				flagStr = fmt.Sprintf("  -%s, --%s", flag.Shorthand, flag.Name)
			} else {
				flagStr = fmt.Sprintf("      --%s", flag.Name)
			}
			if flag.Value.Type() != "bool" {
				flagStr += " " + flag.Value.Type()
			}
			padded := fmt.Sprintf("%-26s", flagStr)
			fmt.Printf("%s %s\n", cli.StyleFlag.Render(padded), formatUsage(flag.Usage))
		})
		fmt.Println()
	}

	// global flags for subcommands
	if !isRoot && cmd.HasAvailableInheritedFlags() {
		printSectionHeader("Global Flags")
		cmd.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
			if flag.Hidden {
				return
			}
			var flagStr string
			if flag.Shorthand != "" {
				flagStr = fmt.Sprintf("  -%s, --%s", flag.Shorthand, flag.Name)
			} else {
				flagStr = fmt.Sprintf("      --%s", flag.Name)
			}
			if flag.Value.Type() != "bool" {
				flagStr += " " + flag.Value.Type()
			}
			padded := fmt.Sprintf("%-26s", flagStr)
			fmt.Printf("%s %s\n", cli.StyleFlag.Render(padded), formatUsage(flag.Usage))
		})
		fmt.Println()
	}

	// footer hint (Tufte: no decorative separator, let text speak)
	if isRoot {
		fmt.Printf("%s %s %s\n",
			cli.StyleDim.Render("Run"),
			cli.StyleCommand.Render("ox [command] --help"),
			cli.StyleDim.Render("for more information."))
		fmt.Println(cli.StyleCallout.Render("★") + cli.StyleDim.Render(" = suggested next action"))
	}
	fmt.Println()
}

// printSectionHeader prints a section header with underline
func printSectionHeader(title string) {
	fmt.Println(cli.StyleGroupHeader.Render(title))
	// underline length matches title
	underline := strings.Repeat("─", len(title))
	fmt.Println(cli.StyleDim.Render(underline))
}

// stepPattern matches (Step N) for highlighting in help output
var stepPattern = regexp.MustCompile(`\(Step \d+\)`)

// filePathPattern matches file/directory paths for highlighting
// matches: .foo, ./foo, ~/foo, /foo, and extensions like foo.yaml
var filePathPattern = regexp.MustCompile(`[.~/][a-zA-Z0-9._/-]+`)

// formatDescription styles command description with highlighted "(Step N)" callouts
func formatDescription(desc string) string {
	match := stepPattern.FindString(desc)
	if match == "" {
		return cli.StyleDim.Render(desc)
	}
	// split around the step marker and render with accent color
	before := desc[:len(desc)-len(match)-1] // -1 for the space before (Step N)
	return cli.StyleDim.Render(before) + " " + cli.StyleAccent.Render(match)
}

// formatUsage styles flag usage text with highlighted file paths
func formatUsage(usage string) string {
	// find all file path matches
	matches := filePathPattern.FindAllStringIndex(usage, -1)
	if len(matches) == 0 {
		return cli.StyleDim.Render(usage)
	}

	// build result with highlighted file paths
	var result strings.Builder
	lastEnd := 0
	for _, match := range matches {
		pathStart, pathEnd := match[0], match[1]
		// render text before the path
		if pathStart > lastEnd {
			result.WriteString(cli.StyleDim.Render(usage[lastEnd:pathStart]))
		}
		// render the file path with highlighting
		result.WriteString(cli.StyleFile.Render(usage[pathStart:pathEnd]))
		lastEnd = pathEnd
	}
	// render remaining text
	if lastEnd < len(usage) {
		result.WriteString(cli.StyleDim.Render(usage[lastEnd:]))
	}
	return result.String()
}

// commandHighlight describes how a command should be visually emphasized
type commandHighlight struct {
	hasStar bool   // show ★ prefix
	step    int    // step number (0 = no step shown)
	reason  string // why this is highlighted (for debugging)
}

// getContextualHighlight returns highlight info for a command based on current state.
// This guides users through the setup journey by emphasizing the logical next action.
func getContextualHighlight(cmdName string) *commandHighlight {
	// detect current state (cached for performance)
	gitRoot := findGitRoot()
	hasSageox := gitRoot != "" && dirExists(filepath.Join(gitRoot, ".sageox"))

	// check auth: use project-specific endpoint if available, otherwise default
	var isLoggedIn bool
	if projectEndpoint := endpoint.GetForProject(gitRoot); projectEndpoint != "" {
		isLoggedIn, _ = auth.IsAuthenticatedForEndpoint(projectEndpoint)
	} else {
		isLoggedIn, _ = auth.IsAuthenticated()
	}

	switch cmdName {
	case "login":
		// Step 1: login is highlighted if not logged in
		if !isLoggedIn {
			return &commandHighlight{hasStar: true, step: 1, reason: "not authenticated"}
		}
	case "init":
		// Step 2: init is highlighted if logged in but no .sageox/ exists yet
		if isLoggedIn && !hasSageox {
			return &commandHighlight{hasStar: true, step: 2, reason: "no .sageox/ detected"}
		}
	case "doctor", "status":
		// Always useful once initialized (no step, just star + bold)
		if hasSageox {
			return &commandHighlight{hasStar: true, step: 0, reason: ".sageox/ detected"}
		}
	}

	return nil
}

// dirExists checks if a directory exists
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// shouldHeartbeat returns true if the command should send a fire-and-forget heartbeat.
// Most commands should — skip only zero-side-effect commands like help and version.
func shouldHeartbeat(cmd *cobra.Command) bool {
	rootCmd := cmd
	for rootCmd.Parent() != nil && rootCmd.Parent().Parent() != nil {
		rootCmd = rootCmd.Parent()
	}

	switch rootCmd.Name() {
	case "version", "help", "completion":
		return false
	}

	return true
}

// printCommandEntry renders a command with contextual highlighting based on user state
func printCommandEntry(cmd *cobra.Command) {
	cmdName := cmd.Name()

	// check for contextual highlighting
	highlight := getContextualHighlight(cmdName)
	if highlight != nil {
		// highlighted command: star prefix, bold name, callout description, optional step
		var prefix string
		if highlight.hasStar {
			prefix = cli.StyleCallout.Render("★ ")
		} else {
			prefix = "  "
		}

		// use same width as normal commands (star replaces leading 2 spaces)
		name := fmt.Sprintf("%-16s", cmdName)
		styledName := cli.StyleCalloutBold.Render(name)
		desc := cli.StyleCallout.Render(cmd.Short)

		var suffix string
		if highlight.step > 0 {
			suffix = " " + cli.StyleCallout.Render(fmt.Sprintf("(Step %d)", highlight.step))
		}

		fmt.Printf("%s%s %s%s\n", prefix, styledName, desc, suffix)
	} else {
		// normal command rendering
		name := fmt.Sprintf("  %-16s", cmdName)
		fmt.Printf("%s %s\n", cli.StyleCommand.Render(name), formatDescription(cmd.Short))
	}
}
