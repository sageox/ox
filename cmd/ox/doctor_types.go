package main

// FixLevel categorizes how a doctor check's fix should behave.
// This allows the doctor command to understand what kind of remediation
// is appropriate for each check, enabling smarter auto-fix behavior.
type FixLevel string

const (
	// FixLevelCheckOnly indicates the check reports issues but has no automated fix.
	// Example: authentication status (user must run `ox login`)
	FixLevelCheckOnly FixLevel = "check-only"

	// FixLevelAuto indicates the fix can be applied silently without --fix flag.
	// Reserved for non-destructive, low-risk fixes that are always safe.
	// Example: creating .gitignore entries, fixing file permissions
	FixLevelAuto FixLevel = "auto"

	// FixLevelSuggested indicates the fix applies with --fix flag and notifies user.
	// For fixes that are generally safe but the user should be aware of.
	// Example: updating config files, creating missing directories
	FixLevelSuggested FixLevel = "suggested"

	// FixLevelConfirm indicates the fix requires explicit user confirmation.
	// For fixes that may have side effects or are potentially destructive.
	// Example: migrating directory structures, changing remote URLs
	FixLevelConfirm FixLevel = "confirm"
)

// DoctorCheck extends the Check interface with additional metadata.
// This provides richer information about each check for tooling and display.
type DoctorCheck struct {
	Slug        string                     // unique identifier (e.g., "ledger-path")
	Name        string                     // display name
	Category    string                     // category grouping
	FixLevel    FixLevel                   // how fix should behave
	Description string                     // what the check does
	Run         func(fix bool) checkResult // the actual check function
}

// Implement the Check interface for DoctorCheck

func (d *DoctorCheck) GetName() string {
	return d.Name
}

func (d *DoctorCheck) GetCategory() string {
	return d.Category
}

func (d *DoctorCheck) RunCheck(fix bool) checkResult {
	return d.Run(fix)
}

// DoctorCheckRegistry holds all registered checks with metadata.
// This extends the basic CheckRegistry with slug-based lookup.
var DoctorCheckRegistry = make(map[string]*DoctorCheck)

// RegisterDoctorCheck adds a check with metadata to the registry.
// The slug must be unique; duplicate registrations will panic.
func RegisterDoctorCheck(check *DoctorCheck) {
	if _, exists := DoctorCheckRegistry[check.Slug]; exists {
		panic("duplicate doctor check slug: " + check.Slug)
	}
	DoctorCheckRegistry[check.Slug] = check
}

// GetDoctorCheck retrieves a check by slug.
// Returns nil if no check with that slug is registered.
func GetDoctorCheck(slug string) *DoctorCheck {
	return DoctorCheckRegistry[slug]
}

// GetDoctorChecksByCategory returns all checks in a given category.
func GetDoctorChecksByCategory(category string) []*DoctorCheck {
	var checks []*DoctorCheck
	for _, check := range DoctorCheckRegistry {
		if check.Category == category {
			checks = append(checks, check)
		}
	}
	return checks
}

// GetDoctorChecksByFixLevel returns all checks with a given fix level.
func GetDoctorChecksByFixLevel(level FixLevel) []*DoctorCheck {
	var checks []*DoctorCheck
	for _, check := range DoctorCheckRegistry {
		if check.FixLevel == level {
			checks = append(checks, check)
		}
	}
	return checks
}

// IsAutoFixable returns true if the check can be fixed automatically without --fix.
func (d *DoctorCheck) IsAutoFixable() bool {
	return d.FixLevel == FixLevelAuto
}

// RequiresConfirmation returns true if the check requires user confirmation to fix.
func (d *DoctorCheck) RequiresConfirmation() bool {
	return d.FixLevel == FixLevelConfirm
}

// HasFix returns true if the check has any kind of fix available.
func (d *DoctorCheck) HasFix() bool {
	return d.FixLevel != FixLevelCheckOnly
}

// Check slug constants for programmatic reference.
// These can be used by other parts of the codebase to reference specific checks.
const (
	// Project Health checks
	CheckSlugSageoxDir       = "sageox-dir"
	CheckSlugConfigJSON      = "config-json"
	CheckSlugGitignore       = "gitignore"
	CheckSlugGitattributes   = "gitattributes"
	CheckSlugSageoxGitignore = "sageox-gitignore"
	CheckSlugReadme          = "readme"
	CheckSlugRepoMarker      = "repo-marker"

	// Git Repository Health checks
	CheckSlugLedgerPath         = "ledger-path"
	CheckSlugLedgerPathMismatch = "ledger-path-mismatch"
	CheckSlugLedgerRemote       = "ledger-remote"
	CheckSlugTeamContextPath    = "team-context-path"
	CheckSlugTeamSymlink        = "team-symlink"
	CheckSlugProjectSymlinks    = "project-symlinks"
	CheckSlugLegacyStructure    = "legacy-structure"
	CheckSlugGitConfig          = "git-config"
	CheckSlugGitRemotes         = "git-remotes"
	CheckSlugGitRepoState       = "git-repo-state"
	CheckSlugMergeConflicts     = "merge-conflicts"
	CheckSlugGitConnectivity    = "git-connectivity"
	CheckSlugGitAuth            = "git-auth"
	CheckSlugGitHooks           = "git-hooks"
	CheckSlugGitLFS             = "git-lfs"
	CheckSlugStashedChanges     = "stashed-changes"
	CheckSlugGitRepoPaths       = "git-repo-paths"
	CheckSlugGitignoreMissing   = "gitignore-missing" // .sageox/.gitignore in ledger/team checkouts
	CheckSlugGitFsck            = "git-fsck"
	CheckSlugGitLock            = "git-lock"

	// Authentication checks
	CheckSlugAuthStatus      = "auth-status"
	CheckSlugAuthPermissions = "auth-permissions"
	CheckSlugTokenExpiry     = "token-expiry"

	// Daemon checks
	CheckSlugDaemonRunning = "daemon-running"
	CheckSlugDaemonSocket  = "daemon-socket"
	CheckSlugDaemonVersion = "daemon-version"

	// Integration checks
	CheckSlugClaudeCodeHooks     = "claude-code-hooks"
	CheckSlugOpenCodeHooks       = "open-code-hooks"
	CheckSlugGeminiHooks         = "gemini-hooks"
	CheckSlugCodexHooks          = "codex-hooks"
	CheckSlugCodePuppyHooks      = "code-puppy-hooks"
	CheckSlugHookCommands          = "hook-commands"
	CheckSlugHookCompleteness      = "hook-completeness"
	CheckSlugSessionStartHookBug   = "session-start-hook-bug"
	CheckSlugGitCommitHooks        = "git-commit-hooks"

	// Team Context checks
	CheckSlugTeamRegistration = "team-registration"
	CheckSlugLegacyTeamCtx    = "legacy-team-contexts"
	CheckSlugOrphanedTeamDirs = "orphaned-team-dirs"

	// SageOx Configuration checks
	CheckSlugEndpointConsistency   = "endpoint-consistency"
	CheckSlugEndpointNormalization = "endpoint-normalization"
	CheckSlugDuplicateRepoMarkers  = "duplicate-repo-markers"

	// Agent Health checks
	CheckSlugInstanceStale       = "instance-stale"
	CheckSlugDaemonInstanceStale = "daemon-instance-stale"

	// Command checks
	CheckSlugClaudeCommands = "claude-commands"

	// Session checks
	CheckSlugSessionCommit      = "session-commit"
	CheckSlugSessionPush        = "session-push"
	CheckSlugSessionIncomplete  = "session-incomplete"
	CheckSlugSessionAutoStage   = "session-auto-stage"
	CheckSlugSessionUploadRetry  = "session-upload-retry"
	CheckSlugSessionUncommitted = "session-uncommitted"

	// Authentication checks (credential health)
	CheckSlugGitCredsFreshness   = "git-creds-freshness"
	CheckSlugCredentialIntegrity = "credential-integrity"
)
