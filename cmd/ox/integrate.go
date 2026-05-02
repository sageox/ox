package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"log/slog"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/constants"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/tips"
	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/spf13/cobra"
)

const (
	// commands - with fallback for when ox is not installed
	// If ox is in PATH: run ox agent prime with stderr merged to stdout (errors visible)
	// If ox is not in PATH: show install instructions
	//
	// Agent-specific commands include AGENT_ENV prefix because Claude Code runs
	// SessionStart/PreCompact hooks BEFORE setting CLAUDECODE=1 in the subprocess.
	// See pkg/agentx/agents/claudecode.go for details on this timing issue.
	oxPrimeCommand           = constants.OxPrimeCommandClaudeCode           // Claude Code hooks (force mode)
	oxPrimeCommandIdempotent = constants.OxPrimeCommandClaudeCodeIdempotent // Claude Code hooks (idempotent mode)
	oxPrimeCommandGemini     = constants.OxPrimeCommandGemini               // Gemini CLI hooks
	oxPrimeLegacy            = constants.OxPrimeCommand                     // legacy command without AGENT_ENV (for detection)
	hookType                 = "command"

	// claude code hook events
	claudeSessionStart = "SessionStart"
	claudePreCompact   = "PreCompact"

	// claude code hook matchers for SessionStart
	matcherStartup = "startup" // new session
	matcherResume  = "resume"  // --resume/--continue
	matcherClear   = "clear"   // /clear command
	matcherCompact = "compact" // auto/manual compaction

	// claude code paths and files
	claudeDirName      = ".claude"
	claudeSettingsFile = "settings.json"

	// matcher patterns
	emptyMatcher = ""

	// file permissions
	dirPerm            = 0755
	settingsPerm       = 0600
	sharedSettingsPerm = 0644 // git-tracked shared settings
	pluginPerm         = 0644
)

var (
	integrateUserFlag     bool
	integrateOpenCodeFlag bool
	integrateGeminiFlag   bool
	integrateCodexFlag    bool
	integrateAmpFlag      bool
	integratePiFlag       bool
	integrateAllFlag      bool
	integrateForceFlag    bool
)

var integrateCmd = &cobra.Command{
	Use:   "integrate",
	Short: "Set up SageOx integration with Claude Code",
	Long: `Install or manage SageOx integrations with AI coding agents.

Supported agents:
  Claude Code (default)    JSON hooks in ~/.claude/settings.json

Other agents (Codex, Gemini) can be installed with their respective flags.
Run 'ox init' to set up the project with appropriate guidance files.

The integration ensures that 'ox agent prime' runs when an AI coding session starts.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// show status when run without subcommand
		return runIntegrateList(cmd, args)
	},
}

var integrateInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install SageOx integration for AI coworkers",
	Long: `Install SageOx hooks for AI coworkers.

Default: Claude Code (adds hooks to ~/.claude/settings.json).
Use --user to add guidance to ~/.claude/CLAUDE.md for ALL projects.

Other agents can be installed with their respective flags:
  --gemini    Gemini CLI hooks
  --codex     Codex CLI hooks
  --amp       Amp CLI integration (AGENTS.md marker)
  --opencode  OpenCode plugin`,
	RunE: runIntegrateInstall,
}

var integrateUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall SageOx integration from AI coworkers",
	Long: `Remove SageOx integration from AI coworkers.

Default: Claude Code (removes hooks from ~/.claude/settings.json).
Use agent-specific flags to uninstall from other agents.`,
	RunE: runIntegrateUninstall,
}

var integrateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List integration status for all AI agents",
	Long:  `Show the status of SageOx integrations for all supported AI agents.`,
	RunE:  runIntegrateList,
}

// hasAnyAgentFlag returns true if any agent-specific install flag was set.
func hasAnyAgentFlag() bool {
	return integratePiFlag || integrateAmpFlag || integrateCodexFlag ||
		integrateGeminiFlag || integrateOpenCodeFlag || integrateUserFlag
}

// integrateAgentInfo pairs adapter metadata with its current install status.
type integrateAgentInfo struct {
	name        string // adapter name (e.g. "gemini")
	displayName string
	installed   bool
	installFn   func() error
}

// runIntegrateInteractive shows a multi-select of detected agents and installs
// the ones the user picks. Already-installed agents are shown greyed out.
func runIntegrateInteractive() error {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not in a git repository — run from a project directory")
	}

	// build the list of agents with their install status
	agents := []integrateAgentInfo{
		{
			name:        "claude-code",
			displayName: "Claude Code",
			installed:   HasProjectClaudeHooks(gitRoot),
			installFn: func() error {
				if err := InstallProjectClaudeHooks(gitRoot); err != nil {
					return err
				}
				installAdapterRules(gitRoot)
				return InstallGitHooks(gitRoot)
			},
		},
	}

	// discover external adapters that are detected on this machine
	for _, ea := range adapters.DiscoverExternalAdapters() {
		if !ea.Detect() {
			continue
		}
		adapterName := ea.Name()
		label := adapterName
		if info := ea.Info(); info != nil && info.DisplayName != "" {
			label = info.DisplayName
		}
		agents = append(agents, integrateAgentInfo{
			name:        adapterName,
			displayName: label,
			installed:   checkExternalAdapterHooks(adapterName, false),
			installFn: func() error {
				return installExternalAdapterHooks(adapterName, false)
			},
		})
	}

	// check for OpenCode
	openCodeDir := filepath.Join(gitRoot, ".opencode")
	if _, err := os.Stat(openCodeDir); err == nil {
		agents = append(agents, integrateAgentInfo{
			name:        "opencode",
			displayName: "OpenCode",
			installed:   hasOpenCodeHooks(false),
			installFn: func() error {
				return installOpenCodeHooks(false)
			},
		})
	}

	// build multi-select options
	var options []cli.MultiSelectOption
	for _, a := range agents {
		opt := cli.MultiSelectOption{
			Label:    a.displayName,
			Value:    a.name,
			Selected: a.installed,
			Disabled: a.installed,
		}
		if a.installed {
			opt.Hint = "(installed)"
		}
		options = append(options, opt)
	}

	// if everything is already installed, just report status
	allInstalled := true
	for _, a := range agents {
		if !a.installed {
			allInstalled = false
			break
		}
	}
	if allInstalled {
		fmt.Println("All detected AI coworkers are already integrated.")
		for _, a := range agents {
			fmt.Printf("  %s %s\n", ui.PassStyle.Render("✓"), a.displayName)
		}
		return nil
	}

	chosen, err := cli.SelectMany(
		"Which AI coworkers should ox integrate with this repo?",
		options,
	)
	if err != nil {
		return fmt.Errorf("selection canceled")
	}

	// install only newly selected agents (skip already-installed)
	chosenSet := map[string]bool{}
	for _, name := range chosen {
		chosenSet[name] = true
	}

	installed := 0
	for _, a := range agents {
		if a.installed || !chosenSet[a.name] {
			continue
		}
		if err := a.installFn(); err != nil {
			cli.PrintWarning(fmt.Sprintf("Could not install %s: %v", a.displayName, err))
		} else {
			cli.PrintSuccess(fmt.Sprintf("Installed %s integration", a.displayName))
			installed++
		}
	}

	if installed == 0 {
		fmt.Println("No new integrations installed.")
	}

	return nil
}

func runIntegrateInstall(cmd *cobra.Command, args []string) error {
	// if no agent-specific flags, show interactive multi-select
	if !hasAnyAgentFlag() && cli.IsInteractive() {
		return runIntegrateInteractive()
	}

	// Pi installation
	if integratePiFlag {
		if integrateUserFlag {
			return fmt.Errorf("pi does not support user-level integration")
		}
		if err := installPiHooks(false); err != nil {
			return fmt.Errorf("installing Pi integration: %w", err)
		}

		userCfg, _ := config.LoadUserConfig()
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// Amp CLI installation
	if integrateAmpFlag {
		if integrateUserFlag {
			return fmt.Errorf("amp CLI does not support user-level integration")
		}
		if err := installAmpHooks(false); err != nil {
			return fmt.Errorf("installing Amp CLI integration: %w", err)
		}

		userCfg, _ := config.LoadUserConfig()
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// Codex CLI installation
	if integrateCodexFlag {
		if err := installCodexHooks(integrateUserFlag); err != nil {
			return fmt.Errorf("installing Codex CLI integration: %w", err)
		}

		userCfg, _ := config.LoadUserConfig()
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// Gemini CLI installation
	if integrateGeminiFlag {
		if err := installGeminiHooks(integrateUserFlag); err != nil {
			return fmt.Errorf("installing Gemini CLI integration: %w", err)
		}

		location := "project"
		if integrateUserFlag {
			location = "user"
		}
		fmt.Println(ui.PassStyle.Render("✓") + fmt.Sprintf(" Gemini CLI %s-level integration installed", location))

		userCfg, _ := config.LoadUserConfig()
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// OpenCode installation
	if integrateOpenCodeFlag {
		if err := installOpenCodeHooks(integrateUserFlag); err != nil {
			return fmt.Errorf("installing OpenCode integration: %w", err)
		}

		location := "project"
		if integrateUserFlag {
			location = "user"
		}
		fmt.Println(ui.PassStyle.Render("✓") + fmt.Sprintf(" OpenCode %s-level integration installed", location))

		userCfg, _ := config.LoadUserConfig()
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// Claude Code installation
	if integrateUserFlag {
		// update user-level context file with ox:prime marker (agent-aware)
		if err := updateUserAgentsMD(); err != nil {
			return fmt.Errorf("installing user-level integration: %w", err)
		}

		// show contextual tip
		userCfg, _ := config.LoadUserConfig()
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// install project-level hooks to .claude/settings.json (shared)
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not in a git repository — run from a project directory")
	}

	if err := InstallProjectClaudeHooks(gitRoot); err != nil {
		return fmt.Errorf("installing Claude Code integration: %w", err)
	}

	fmt.Println(ui.PassStyle.Render("✓") + " Claude Code project hooks installed")
	fmt.Println()
	fmt.Println("Installed lifecycle hooks to .claude/settings.json:")
	fmt.Println("  - SessionStart, PreCompact, PostToolUse, Stop, SessionEnd, UserPromptSubmit")

	// install rules via adapters that support it
	installAdapterRules(gitRoot)

	// install git commit hooks (prepare-commit-msg for trailers)
	if err := InstallGitHooks(gitRoot); err != nil {
		cli.PrintWarning(fmt.Sprintf("Could not install git commit hooks: %v", err))
	} else {
		fmt.Println(ui.PassStyle.Render("✓") + " Git commit hooks installed")
		fmt.Println("  - prepare-commit-msg (Co-Authored-By, SageOx-Session trailers)")
	}

	// show contextual tip
	userCfg, _ := config.LoadUserConfig()
	tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
	return nil
}

func runIntegrateUninstall(cmd *cobra.Command, args []string) error {
	// Uninstall all integrations
	if integrateAllFlag {
		if err := uninstallAllIntegrations(integrateForceFlag); err != nil {
			return fmt.Errorf("uninstalling integrations: %w", err)
		}
		return nil
	}

	// Pi uninstallation
	if integratePiFlag {
		if integrateUserFlag {
			return fmt.Errorf("pi does not support user-level integration")
		}
		if err := uninstallPiHooks(false); err != nil {
			return fmt.Errorf("uninstalling Pi integration: %w", err)
		}

		fmt.Printf("✓ Pi project-level integration uninstalled\n")

		userCfg, _ := config.LoadUserConfig()
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// Amp CLI uninstallation
	if integrateAmpFlag {
		if integrateUserFlag {
			return fmt.Errorf("amp CLI does not support user-level integration")
		}
		if err := uninstallAmpHooks(false); err != nil {
			return fmt.Errorf("uninstalling Amp CLI integration: %w", err)
		}

		fmt.Printf("✓ Amp CLI project-level integration uninstalled\n")

		userCfg, _ := config.LoadUserConfig()
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// Codex CLI uninstallation
	if integrateCodexFlag {
		if err := uninstallCodexHooks(integrateUserFlag); err != nil {
			return fmt.Errorf("uninstalling Codex CLI integration: %w", err)
		}

		location := "project"
		if integrateUserFlag {
			location = "user"
		}
		fmt.Printf("✓ Codex CLI %s-level integration uninstalled\n", location)

		userCfg, _ := config.LoadUserConfig()
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// Gemini CLI uninstallation
	if integrateGeminiFlag {
		if err := uninstallGeminiHooks(integrateUserFlag); err != nil {
			return fmt.Errorf("uninstalling Gemini CLI integration: %w", err)
		}

		location := "project"
		if integrateUserFlag {
			location = "user"
		}
		fmt.Printf("✓ Gemini CLI %s-level integration uninstalled\n", location)

		userCfg, _ := config.LoadUserConfig()
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// OpenCode uninstallation
	if integrateOpenCodeFlag {
		if err := uninstallOpenCodeHooks(integrateUserFlag); err != nil {
			return fmt.Errorf("uninstalling OpenCode integration: %w", err)
		}

		location := "project"
		if integrateUserFlag {
			location = "user"
		}
		fmt.Printf("✓ OpenCode %s-level integration uninstalled\n", location)

		userCfg, _ := config.LoadUserConfig()
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// Claude Code uninstallation
	if err := uninstallClaudeHooks(); err != nil {
		return fmt.Errorf("uninstalling Claude Code integration: %w", err)
	}

	fmt.Println("✓ Claude Code integration uninstalled")

	// uninstall git commit hooks
	gitRoot := findGitRoot()
	if gitRoot != "" {
		if err := UninstallGitHooks(gitRoot); err != nil {
			cli.PrintWarning(fmt.Sprintf("Could not uninstall git commit hooks: %v", err))
		} else {
			fmt.Println("✓ Git commit hooks uninstalled")
		}
	}

	// show contextual tip
	userCfg, _ := config.LoadUserConfig()
	tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
	return nil
}

func runIntegrateList(cmd *cobra.Command, args []string) error {
	gitRoot := findGitRoot()

	fmt.Println(ui.RenderCategory("AI Coworker Integrations"))
	fmt.Println()

	// Claude Code
	printAgentStatus("Claude Code", gitRoot != "" && HasProjectClaudeHooks(gitRoot), gitRoot == "")

	// all discovered external adapters (includes bundled ones)
	for _, ea := range adapters.DiscoverExternalAdapters() {
		label := ea.Name()
		if info := ea.Info(); info != nil && info.DisplayName != "" {
			label = info.DisplayName
		}
		installed := false
		if gitRoot != "" {
			installed = checkExternalAdapterHooks(ea.Name(), false)
		}
		printAgentStatus(label, installed, gitRoot == "")
	}

	// OpenCode (built-in detection)
	if gitRoot != "" {
		openCodeDir := filepath.Join(gitRoot, ".opencode")
		if _, err := os.Stat(openCodeDir); err == nil {
			printAgentStatus("OpenCode", hasOpenCodeHooks(false), false)
		}
	}

	fmt.Println()

	// git commit hooks
	fmt.Println(ui.RenderCategory("Git Hooks"))
	fmt.Println()
	if gitRoot == "" {
		fmt.Println("  (not in a git repo)")
	} else if HasGitHooks(gitRoot) {
		fmt.Printf("  %s prepare-commit-msg\n", ui.PassStyle.Render("✓"))
	} else {
		fmt.Printf("  %s prepare-commit-msg\n", ui.FailStyle.Render("✗"))
	}

	// show contextual tip
	userCfg, _ := config.LoadUserConfig()
	tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
	return nil
}

func printAgentStatus(name string, installed bool, noRepo bool) {
	if noRepo {
		fmt.Printf("  %s %s (not in a git repo)\n", ui.FailStyle.Render("✗"), name)
	} else if installed {
		fmt.Printf("  %s %s\n", ui.PassStyle.Render("✓"), name)
	} else {
		fmt.Printf("  %s %s\n", ui.FailStyle.Render("✗"), name)
	}
}

// uninstallAllIntegrations removes ox prime integrations from all AI agents
func uninstallAllIntegrations(force bool) error {
	// detect installed integrations
	var installed []string

	// check Claude Code
	claudeStatus, err := listClaudeHooks()
	if err == nil {
		if claudeStatus[claudeSessionStart] || claudeStatus[claudePreCompact] {
			installed = append(installed, "Claude Code (SessionStart, PreCompact)")
		}
	}

	// check OpenCode
	if hasOpenCodeHooks(false) {
		installed = append(installed, "OpenCode (project plugin)")
	}
	if hasOpenCodeHooks(true) {
		installed = append(installed, "OpenCode (user plugin)")
	}

	// check Codex CLI
	if hasCodexHooks(false) {
		installed = append(installed, "Codex CLI (project)")
	}
	if hasCodexHooks(true) {
		installed = append(installed, "Codex CLI (user)")
	}

	// check Gemini CLI
	if hasGeminiHooks(false) {
		installed = append(installed, "Gemini CLI (project)")
	}
	if hasGeminiHooks(true) {
		installed = append(installed, "Gemini CLI (user)")
	}

	// check Amp CLI
	if hasAmpHooks(false) {
		installed = append(installed, "Amp CLI (project)")
	}

	// check Pi
	if hasPiHooks(false) {
		installed = append(installed, "Pi (project)")
	}

	// check git commit hooks
	gitRoot := findGitRoot()
	if gitRoot != "" && HasGitHooks(gitRoot) {
		installed = append(installed, "Git commit hooks (prepare-commit-msg)")
	}

	if len(installed) == 0 {
		fmt.Println("No integrations found")
		return nil
	}

	// show what was found
	fmt.Println("Found integrations:")
	for _, h := range installed {
		fmt.Printf("  - %s\n", h)
	}
	fmt.Println()

	// prompt unless force
	if !force {
		if !cli.ConfirmYesNo("Uninstall all?", true) {
			fmt.Println("Canceled.")
			return nil
		}
	}

	// uninstall all
	var errors []string

	if err := uninstallClaudeHooks(); err != nil {
		errors = append(errors, fmt.Sprintf("Claude Code: %v", err))
	}
	if err := uninstallOpenCodeHooks(false); err != nil {
		errors = append(errors, fmt.Sprintf("OpenCode (project): %v", err))
	}
	if err := uninstallOpenCodeHooks(true); err != nil {
		errors = append(errors, fmt.Sprintf("OpenCode (user): %v", err))
	}
	if err := uninstallCodexHooks(false); err != nil {
		errors = append(errors, fmt.Sprintf("Codex CLI (project): %v", err))
	}
	if err := uninstallCodexHooks(true); err != nil {
		errors = append(errors, fmt.Sprintf("Codex CLI (user): %v", err))
	}
	if err := uninstallGeminiHooks(false); err != nil {
		errors = append(errors, fmt.Sprintf("Gemini CLI (project): %v", err))
	}
	if err := uninstallGeminiHooks(true); err != nil {
		errors = append(errors, fmt.Sprintf("Gemini CLI (user): %v", err))
	}
	if err := uninstallAmpHooks(false); err != nil {
		errors = append(errors, fmt.Sprintf("Amp CLI (project): %v", err))
	}
	if err := uninstallPiHooks(false); err != nil {
		errors = append(errors, fmt.Sprintf("Pi (project): %v", err))
	}
	if gitRoot != "" {
		if err := UninstallGitHooks(gitRoot); err != nil {
			errors = append(errors, fmt.Sprintf("Git commit hooks: %v", err))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("some uninstalls failed: %s", strings.Join(errors, "; "))
	}

	fmt.Println(ui.PassStyle.Render("✓") + " All integrations uninstalled")
	return nil
}

// installAdapterRules discovers adapters with rules_installer capability and
// installs their rules into the project.
func installAdapterRules(gitRoot string) {
	for _, ea := range adapters.DiscoverExternalAdapters() {
		if !ea.HasCapability(adapterprotocol.CapRulesInstaller) {
			continue
		}
		resp, err := ea.InstallRules(gitRoot, version.Version)
		if err != nil {
			slog.Warn("failed to install rules", "adapter", ea.Name(), "error", err)
			continue
		}
		if resp.Installed && len(resp.FilesWritten) > 0 {
			fmt.Printf("%s %s rules installed (%s)\n",
				ui.PassStyle.Render("✓"), ea.Name(), strings.Join(resp.FilesWritten, ", "))
		}
	}
}

func init() {
	// install flags
	integrateInstallCmd.Flags().BoolVar(&integrateUserFlag, "user", false, "install to user-level config for all projects")

	// MVP: Hide non-Claude-Code integrations - they still work but aren't shown in help
	integrateInstallCmd.Flags().BoolVar(&integrateOpenCodeFlag, "opencode", false, "install OpenCode plugin instead of Claude Code hooks")
	integrateInstallCmd.Flags().BoolVar(&integrateGeminiFlag, "gemini", false, "install Gemini CLI hooks instead of Claude Code hooks")
	integrateInstallCmd.Flags().BoolVar(&integrateCodexFlag, "codex", false, "install Codex CLI hooks instead of Claude Code hooks")
	integrateInstallCmd.Flags().BoolVar(&integrateAmpFlag, "amp", false, "install Amp CLI integration (AGENTS.md marker)")
	_ = integrateInstallCmd.Flags().MarkHidden("opencode")
	_ = integrateInstallCmd.Flags().MarkHidden("gemini")
	_ = integrateInstallCmd.Flags().MarkHidden("codex")
	_ = integrateInstallCmd.Flags().MarkHidden("amp")
	integrateInstallCmd.Flags().BoolVar(&integratePiFlag, "pi", false, "install Pi integration (AGENTS.md marker)")
	_ = integrateInstallCmd.Flags().MarkHidden("pi")

	// uninstall flags
	integrateUninstallCmd.Flags().BoolVar(&integrateUserFlag, "user", false, "uninstall from user-level config")
	integrateUninstallCmd.Flags().BoolVar(&integrateOpenCodeFlag, "opencode", false, "uninstall OpenCode plugin instead of Claude Code hooks")
	integrateUninstallCmd.Flags().BoolVar(&integrateGeminiFlag, "gemini", false, "uninstall Gemini CLI hooks instead of Claude Code hooks")
	integrateUninstallCmd.Flags().BoolVar(&integrateCodexFlag, "codex", false, "uninstall Codex CLI hooks instead of Claude Code hooks")
	integrateUninstallCmd.Flags().BoolVar(&integrateAmpFlag, "amp", false, "uninstall Amp CLI integration (AGENTS.md marker)")
	integrateUninstallCmd.Flags().BoolVar(&integrateAllFlag, "all", false, "uninstall from all AI agents")
	integrateUninstallCmd.Flags().BoolVar(&integrateForceFlag, "force", false, "skip confirmation prompts - use with --all")
	_ = integrateUninstallCmd.Flags().MarkHidden("opencode")
	_ = integrateUninstallCmd.Flags().MarkHidden("gemini")
	_ = integrateUninstallCmd.Flags().MarkHidden("codex")
	_ = integrateUninstallCmd.Flags().MarkHidden("amp")
	integrateUninstallCmd.Flags().BoolVar(&integratePiFlag, "pi", false, "uninstall Pi integration (AGENTS.md marker)")
	_ = integrateUninstallCmd.Flags().MarkHidden("pi")
	_ = integrateUninstallCmd.Flags().MarkHidden("all")
	_ = integrateUninstallCmd.Flags().MarkHidden("force")

	integrateCmd.AddCommand(integrateInstallCmd)
	integrateCmd.AddCommand(integrateUninstallCmd)
	integrateCmd.AddCommand(integrateListCmd)

	integrateCmd.GroupID = "agent-interface"
	rootCmd.AddCommand(integrateCmd)
}
