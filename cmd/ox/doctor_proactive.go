package main

import (
	"io"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/tips"
)

// init registers proactive health checks with weighted probability.
// These checks run during tip display to surface actionable issues.
// Tips appear for both humans and agents - agents can auto-fix issues.
func init() {
	// HIGH VALUE: Claude Code hooks not installed (weight 10)
	// Huge impact - enables automatic priming on session start
	tips.RegisterProactiveCheck(tips.ProactiveCheck{
		Name:   "Claude Code hooks",
		Weight: 10,
		Prereq: detectClaudeCode,
		Check: func() bool {
			status, err := listClaudeHooks()
			if err != nil {
				return false // can't check, don't show tip
			}
			return !status[claudeSessionStart] || !status[claudePreCompact]
		},
		FixTip: "Claude Code detected but hooks not installed. They will be auto-installed on next `ox agent prime`.",
	})

	// HIGH VALUE: ox agent prime missing from AGENTS.md (weight 10)
	// Huge impact - agents won't know to prime without this
	tips.RegisterProactiveCheck(tips.ProactiveCheck{
		Name:   "AGENTS.md integration",
		Weight: 10,
		Check: func() bool {
			result := checkAgentsIntegrationWithFix(io.Discard, false)
			return !result.passed && !result.skipped
		},
		FixTip: "Agent file missing `ox agent prime` instruction. Run `ox doctor --fix` to add it.",
	})

	// MEDIUM VALUE: No agent file exists (weight 5)
	// High impact - foundational for agent work
	tips.RegisterProactiveCheck(tips.ProactiveCheck{
		Name:   "Agent file exists",
		Weight: 5,
		Check: func() bool {
			result := checkAgentFileExists()
			return result.warning // warning means no agent file found
		},
		FixTip: "No AGENTS.md or CLAUDE.md found. Create one to give AI agents project context.",
	})

	// MEDIUM VALUE: .sageox/ directory missing (weight 5)
	// High impact - indicates ox not initialized
	tips.RegisterProactiveCheck(tips.ProactiveCheck{
		Name:   ".sageox/ directory",
		Weight: 5,
		Check: func() bool {
			result := checkSageoxDirectory()
			return !result.passed
		},
		FixTip: "SageOx not initialized. Run `ox init` to set up team context.",
	})

	// LOWER VALUE: OpenCode hooks not installed (weight 3)
	// Only relevant if OpenCode project detected
	tips.RegisterProactiveCheck(tips.ProactiveCheck{
		Name:   "OpenCode hooks",
		Weight: 3,
		Prereq: func() bool { return (&OpenCodeAgent{}).DetectProject() },
		Check: func() bool {
			return !hasOpenCodeHooks(false) && !hasOpenCodeHooks(true)
		},
		FixTip: "OpenCode detected but hooks not installed. Run `ox hooks install --opencode`.",
	})

	// LOWER VALUE: Gemini CLI hooks not installed (weight 3)
	// Only relevant if Gemini project detected
	tips.RegisterProactiveCheck(tips.ProactiveCheck{
		Name:   "Gemini CLI hooks",
		Weight: 3,
		Prereq: func() bool { return (&GeminiAgent{}).DetectProject() },
		Check: func() bool {
			return !hasGeminiHooks(false) && !hasGeminiHooks(true)
		},
		FixTip: "Gemini CLI detected but hooks not installed. Run `ox hooks install --gemini`.",
	})

	// MEDIUM VALUE: doctor --fix hasn't been run recently (weight 5)
	// Uses progressive probability based on staleness
	tips.RegisterProactiveCheck(tips.ProactiveCheck{
		Name:   "doctor staleness",
		Weight: 5,
		Prereq: func() bool {
			// only check if we're in a git repo with .sageox/
			gitRoot := findGitRoot()
			return gitRoot != "" && checkSageoxDirectory().passed
		},
		Check: func() bool {
			gitRoot := findGitRoot()
			if gitRoot == "" {
				return false
			}
			health, err := config.LoadHealth(gitRoot)
			if err != nil {
				return false
			}
			// uses progressive probability: 0% < 1 week, 10% week 1, 30% week 2, etc.
			return health.ShouldHintDoctorFix()
		},
		FixTip: "It's been a while since last `ox doctor --fix`. Running it periodically helps catch config drift.",
	})
}
