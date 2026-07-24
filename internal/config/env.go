package config

// Environment variable names for configuration overrides.
// Each variable is consumed in exactly one resolver function.
const (
	// EnvProjectRoot overrides project root discovery (walk-up from cwd).
	// Consumed by: ResolveProjectRootOverride()
	EnvProjectRoot = "OX_PROJECT_ROOT"

	// EnvSessionRecording overrides the session recording mode.
	// Consumed by: ResolveSessionRecording()
	EnvSessionRecording = "OX_SESSION_RECORDING"

	// EnvUserConfig overrides the user config file path.
	// Consumed by: LoadUserConfig()
	EnvUserConfig = "OX_USER_CONFIG"

	// EnvGitHubSync overrides the master GitHub sync mode.
	// Consumed by: ResolveGitHubSync()
	EnvGitHubSync = "OX_GITHUB_SYNC"

	// EnvGitHubSyncPRs overrides the PR sync mode.
	// Consumed by: ResolveGitHubSyncPRs()
	EnvGitHubSyncPRs = "OX_GITHUB_SYNC_PRS"

	// EnvGitHubSyncIssues overrides the issue sync mode.
	// Consumed by: ResolveGitHubSyncIssues()
	EnvGitHubSyncIssues = "OX_GITHUB_SYNC_ISSUES"

	// EnvPlanHTML directly overrides the plan.html render mode. It must be one
	// of the enum values off | recommend | always; any other value falls
	// through to config/default. Customer-facing, so it uses the canonical
	// SAGEOX_* namespace (ADR-047).
	// Consumed by: PlanHTML()
	EnvPlanHTML = "SAGEOX_PLAN_HTML"

	// EnvPlanOpen directly overrides the plan.open browser-open policy. It
	// must be one of the enum values never | ask | always; any other value
	// falls through to config/default. Customer-facing, so it uses the
	// canonical SAGEOX_* namespace (ADR-047) — mirrors EnvPlanHTML exactly.
	// Consumed by: PlanOpen()
	EnvPlanOpen = "SAGEOX_PLAN_OPEN"
)
