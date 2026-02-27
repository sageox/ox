package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/constants"
	"github.com/sageox/ox/internal/tips"
	"github.com/sageox/ox/internal/ui"
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
	oxPrimeUserCommand       = "ox agent prime --user"
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
	// opencode hook events
	openCodeSessionCreated = "session.created"

	// opencode paths and files
	openCodePluginFileName = "ox-prime.ts"
	openCodeProjectPath    = ".opencode/plugin"
	openCodeUserPath       = ".config/opencode/plugin"

	// gemini cli paths and files
	geminiSettingsFileName = "settings.json"
	geminiProjectPath      = ".gemini"
	geminiUserPath         = ".gemini"
	geminiSessionStart     = "SessionStart"

	// timeouts (milliseconds)
	defaultHookTimeout = 30000

	// matcher patterns
	emptyMatcher = ""

	// file permissions
	dirPerm      = 0755
	settingsPerm = 0600
	pluginPerm   = 0644
)

var (
	integrateUserFlag      bool
	integrateOpenCodeFlag  bool
	integrateGeminiFlag    bool
	integrateCodePuppyFlag bool
	integrateAllFlag       bool
	integrateForceFlag     bool
)

var integrateCmd = &cobra.Command{
	Use:   "integrate",
	Short: "Set up SageOx integration with Claude Code",
	Long: `Install or manage SageOx integrations with AI coding agents.

Supported agents:
  Claude Code (default)    JSON hooks in ~/.claude/settings.json

Other agents can use 'ox' CLI via AGENTS.md or CLAUDE.md references.
Run 'ox init' to set up the project with appropriate guidance files.

The integration ensures that 'ox agent prime' runs when an AI coding session starts.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// show status when run without subcommand
		return runIntegrateList(cmd, args)
	},
}

var integrateInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install SageOx integration to Claude Code",
	Long: `Install hooks for Claude Code.

Adds hooks to ~/.claude/settings.json for SessionStart and PreCompact events.
Use --user to add guidance to ~/.claude/CLAUDE.md for ALL projects.

Other agents can use 'ox' CLI via AGENTS.md or CLAUDE.md references.`,
	RunE: runIntegrateInstall,
}

var integrateUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall SageOx integration from Claude Code",
	Long: `Remove SageOx integration from Claude Code.

Removes hooks from ~/.claude/settings.json.`,
	RunE: runIntegrateUninstall,
}

var integrateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List integration status for all AI agents",
	Long:  `Show the status of SageOx integrations for all supported AI agents.`,
	RunE:  runIntegrateList,
}

func runIntegrateInstall(cmd *cobra.Command, args []string) error {
	// code_puppy installation
	if integrateCodePuppyFlag {
		if err := installCodePuppyHooks(integrateUserFlag); err != nil {
			return fmt.Errorf("installing code_puppy integration: %w", err)
		}

		location := "user"
		path := "~/" + codePuppyUserPath
		if !integrateUserFlag {
			location = "project"
			path = codePuppyProjectPath + "/plugins"
		}
		fmt.Println(ui.PassStyle.Render("✓") + " code_puppy integration installed")
		fmt.Println()
		fmt.Printf("Installed %s-level plugin:\n", location)
		fmt.Printf("  - %s/%s/%s\n", path, codePuppyPluginDir, codePuppyPluginFileName)

		userCfg, _ := config.LoadUserConfig("")
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// Gemini CLI installation
	if integrateGeminiFlag {
		if err := installGeminiHooks(integrateUserFlag); err != nil {
			return fmt.Errorf("installing Gemini CLI integration: %w", err)
		}

		location := "project"
		path := geminiProjectPath
		if integrateUserFlag {
			location = "user"
			path = "~/" + geminiUserPath
		}
		fmt.Println(ui.PassStyle.Render("✓") + " Gemini CLI integration installed")
		fmt.Println()
		fmt.Printf("Installed %s-level hooks:\n", location)
		fmt.Printf("  - %s/%s (SessionStart)\n", path, geminiSettingsFileName)

		userCfg, _ := config.LoadUserConfig("")
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// OpenCode installation
	if integrateOpenCodeFlag {
		if err := installOpenCodeHooks(integrateUserFlag); err != nil {
			return fmt.Errorf("installing OpenCode integration: %w", err)
		}

		location := "project"
		path := openCodeProjectPath
		if integrateUserFlag {
			location = "user"
			path = "~/" + openCodeUserPath
		}
		fmt.Println(ui.PassStyle.Render("✓") + " OpenCode integration installed")
		fmt.Println()
		fmt.Printf("Installed %s-level plugin:\n", location)
		fmt.Printf("  - %s/%s\n", path, openCodePluginFileName)

		userCfg, _ := config.LoadUserConfig("")
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
		userCfg, _ := config.LoadUserConfig("")
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// install project-level hooks to .claude/settings.local.json
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not in a git repository — run from a project directory")
	}

	if err := InstallProjectClaudeHooks(gitRoot); err != nil {
		return fmt.Errorf("installing Claude Code integration: %w", err)
	}

	fmt.Println(ui.PassStyle.Render("✓") + " Claude Code project hooks installed")
	fmt.Println()
	fmt.Println("Installed lifecycle hooks to .claude/settings.local.json:")
	fmt.Println("  - SessionStart, PreCompact, PostToolUse, Stop, SessionEnd, UserPromptSubmit")

	// show contextual tip
	userCfg, _ := config.LoadUserConfig("")
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

	// code_puppy uninstallation
	if integrateCodePuppyFlag {
		if err := uninstallCodePuppyHooks(integrateUserFlag); err != nil {
			return fmt.Errorf("uninstalling code_puppy integration: %w", err)
		}

		location := "user"
		if !integrateUserFlag {
			location = "project"
		}
		fmt.Printf("✓ code_puppy %s-level integration uninstalled\n", location)

		userCfg, _ := config.LoadUserConfig("")
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

		userCfg, _ := config.LoadUserConfig("")
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

		userCfg, _ := config.LoadUserConfig("")
		tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
		return nil
	}

	// Claude Code uninstallation
	if err := uninstallClaudeHooks(); err != nil {
		return fmt.Errorf("uninstalling Claude Code integration: %w", err)
	}

	fmt.Println("✓ Claude Code integration uninstalled")

	// show contextual tip
	userCfg, _ := config.LoadUserConfig("")
	tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
	return nil
}

func runIntegrateList(cmd *cobra.Command, args []string) error {
	// Claude Code status (project-level hooks)
	fmt.Println("Claude Code (project):")
	gitRoot := findGitRoot()
	if gitRoot == "" {
		fmt.Println("  (not in a git repo)")
	} else if HasProjectClaudeHooks(gitRoot) {
		fmt.Printf("  %s hooks: installed (.claude/settings.local.json)\n", ui.PassStyle.Render("✓"))
	} else {
		fmt.Printf("  %s hooks: not installed\n", ui.FailStyle.Render("✗"))
	}

	// User-level CLAUDE.md marker
	fmt.Println("Claude Code (user):")
	if hasUserLevelOxPrime() {
		fmt.Printf("  %s marker: enabled (~/.claude/CLAUDE.md)\n", ui.PassStyle.Render("✓"))
	} else {
		fmt.Printf("  %s marker: not enabled\n", ui.FailStyle.Render("✗"))
	}

	fmt.Println()

	// MVP: Only Claude Code integrations are supported for now.
	// Other agents can use 'ox' via AGENTS.md/CLAUDE.md references.
	//
	// Commented out for MVP - uncomment when ready to support:
	//
	// // OpenCode status
	// fmt.Println("OpenCode:")
	// openCodeStatus := listOpenCodeHooks()
	// for location, installed := range openCodeStatus {
	// 	if installed {
	// 		fmt.Printf("  %s %s: installed\n", ui.PassStyle.Render("✓"), location)
	// 	} else {
	// 		fmt.Printf("  %s %s: not installed\n", ui.FailStyle.Render("✗"), location)
	// 	}
	// }
	//
	// fmt.Println()
	//
	// // Gemini CLI status
	// fmt.Println("Gemini CLI:")
	// geminiStatus := listGeminiHooks()
	// for location, installed := range geminiStatus {
	// 	if installed {
	// 		fmt.Printf("  %s %s: installed\n", ui.PassStyle.Render("✓"), location)
	// 	} else {
	// 		fmt.Printf("  %s %s: not installed\n", ui.FailStyle.Render("✗"), location)
	// 	}
	// }
	//
	// fmt.Println()
	//
	// // code_puppy status
	// fmt.Println("code_puppy:")
	// codePuppyStatus := listCodePuppyHooks()
	// for location, installed := range codePuppyStatus {
	// 	if installed {
	// 		fmt.Printf("  %s %s: installed\n", ui.PassStyle.Render("✓"), location)
	// 	} else {
	// 		fmt.Printf("  %s %s: not installed\n", ui.FailStyle.Render("✗"), location)
	// 	}
	// }

	// show contextual tip
	userCfg, _ := config.LoadUserConfig("")
	tips.MaybeShow("hooks", tips.WhenMinimal, false, !userCfg.AreTipsEnabled(), false)
	return nil
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

	// check Gemini CLI
	if hasGeminiHooks(false) {
		installed = append(installed, "Gemini CLI (project)")
	}
	if hasGeminiHooks(true) {
		installed = append(installed, "Gemini CLI (user)")
	}

	// check code_puppy
	if hasCodePuppyHooks(true) {
		installed = append(installed, "code_puppy (user plugin)")
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
	if err := uninstallGeminiHooks(false); err != nil {
		errors = append(errors, fmt.Sprintf("Gemini CLI (project): %v", err))
	}
	if err := uninstallGeminiHooks(true); err != nil {
		errors = append(errors, fmt.Sprintf("Gemini CLI (user): %v", err))
	}
	if err := uninstallCodePuppyHooks(true); err != nil {
		errors = append(errors, fmt.Sprintf("code_puppy (user): %v", err))
	}

	if len(errors) > 0 {
		return fmt.Errorf("some uninstalls failed: %s", strings.Join(errors, "; "))
	}

	fmt.Println(ui.PassStyle.Render("✓") + " All integrations uninstalled")
	return nil
}

func init() {
	// install flags
	integrateInstallCmd.Flags().BoolVar(&integrateUserFlag, "user", false, "install to user-level config for all projects")

	// MVP: Hide non-Claude-Code integrations - they still work but aren't shown in help
	// Other agents can use 'ox' via AGENTS.md/CLAUDE.md references
	integrateInstallCmd.Flags().BoolVar(&integrateOpenCodeFlag, "opencode", false, "install OpenCode plugin instead of Claude Code hooks")
	integrateInstallCmd.Flags().BoolVar(&integrateGeminiFlag, "gemini", false, "install Gemini CLI hooks instead of Claude Code hooks")
	integrateInstallCmd.Flags().BoolVar(&integrateCodePuppyFlag, "codepuppy", false, "install code_puppy plugin instead of Claude Code hooks")
	_ = integrateInstallCmd.Flags().MarkHidden("opencode")
	_ = integrateInstallCmd.Flags().MarkHidden("gemini")
	_ = integrateInstallCmd.Flags().MarkHidden("codepuppy")

	// uninstall flags
	integrateUninstallCmd.Flags().BoolVar(&integrateUserFlag, "user", false, "uninstall from user-level config")
	integrateUninstallCmd.Flags().BoolVar(&integrateOpenCodeFlag, "opencode", false, "uninstall OpenCode plugin instead of Claude Code hooks")
	integrateUninstallCmd.Flags().BoolVar(&integrateGeminiFlag, "gemini", false, "uninstall Gemini CLI hooks instead of Claude Code hooks")
	integrateUninstallCmd.Flags().BoolVar(&integrateCodePuppyFlag, "codepuppy", false, "uninstall code_puppy plugin instead of Claude Code hooks")
	integrateUninstallCmd.Flags().BoolVar(&integrateAllFlag, "all", false, "uninstall from all AI agents")
	integrateUninstallCmd.Flags().BoolVar(&integrateForceFlag, "force", false, "skip confirmation prompts - use with --all")
	_ = integrateUninstallCmd.Flags().MarkHidden("opencode")
	_ = integrateUninstallCmd.Flags().MarkHidden("gemini")
	_ = integrateUninstallCmd.Flags().MarkHidden("codepuppy")
	_ = integrateUninstallCmd.Flags().MarkHidden("all")
	_ = integrateUninstallCmd.Flags().MarkHidden("force")

	integrateCmd.AddCommand(integrateInstallCmd)
	integrateCmd.AddCommand(integrateUninstallCmd)
	integrateCmd.AddCommand(integrateListCmd)

	integrateCmd.GroupID = "agent-interface"
	rootCmd.AddCommand(integrateCmd)
}
