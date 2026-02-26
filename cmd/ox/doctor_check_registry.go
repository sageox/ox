package main

import (
	"context"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/doctor"
)

// This file registers all doctor checks with their metadata.
// Checks are organized by category and include slugs for programmatic reference.
//
// FixLevel categories:
// - check-only: No automated fix (user must take manual action)
// - auto: Fix applied automatically without --fix flag
// - suggested: Fix applied with --fix flag
// - confirm: Fix requires explicit user confirmation

func init() {
	// ============================================================
	// Authentication checks (FIRST - most critical)
	// ============================================================

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugAuthStatus,
		Name:        "Logged in",
		Category:    "Authentication",
		FixLevel:    FixLevelCheckOnly,
		Description: "Verifies user is authenticated with SageOx",
		Run:         func(fix bool) checkResult { return checkAuthentication() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugAuthPermissions,
		Name:        "Auth file permissions",
		Category:    "Authentication",
		FixLevel:    FixLevelAuto,
		Description: "Ensures auth token file has secure permissions (0600)",
		Run:         checkAuthFilePermissions,
	})

	// ============================================================
	// Project Health checks
	// ============================================================

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSageoxDir,
		Name:        ".sageox directory",
		Category:    "Project Structure",
		FixLevel:    FixLevelCheckOnly,
		Description: "Checks if .sageox directory exists",
		Run:         func(fix bool) checkResult { return checkSageoxDirectory() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugConfigJSON,
		Name:        "config.json",
		Category:    "Project Structure",
		FixLevel:    FixLevelAuto,
		Description: "Validates .sageox/config.json exists and is valid",
		Run:         checkConfigFile,
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSageoxGitignore,
		Name:        ".sageox/.gitignore",
		Category:    "Project Structure",
		FixLevel:    FixLevelAuto,
		Description: "Ensures .sageox/.gitignore has required entries",
		Run:         checkSageoxGitignore,
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugReadme,
		Name:        ".sageox/README.md",
		Category:    "Project Structure",
		FixLevel:    FixLevelAuto,
		Description: "Checks if .sageox/README.md exists and is up to date",
		Run:         checkReadmeFile,
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugRepoMarker,
		Name:        ".repo_* marker",
		Category:    "Project Structure",
		FixLevel:    FixLevelCheckOnly,
		Description: "Checks for repo initialization marker file",
		Run:         func(fix bool) checkResult { return checkRepoMarker() },
	})

	// ============================================================
	// Git Status checks
	// ============================================================

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitignore,
		Name:        ".gitignore",
		Category:    "Git Status",
		FixLevel:    FixLevelAuto,
		Description: "Ensures .gitignore has SageOx entries",
		Run:         checkGitignore,
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitattributes,
		Name:        ".gitattributes",
		Category:    "Git Status",
		FixLevel:    FixLevelAuto,
		Description: "Ensures .gitattributes has SageOx linguist entries",
		Run:         checkGitattributes,
	})

	// ============================================================
	// Git Repository Health checks
	// ============================================================

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitConfig,
		Name:        "Git config",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Verifies git user.name and user.email are set",
		Run:         checkGitConfig,
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitRemotes,
		Name:        "Git remotes",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Validates configured git remotes",
		Run:         func(fix bool) checkResult { return checkGitRemotes() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitRepoState,
		Name:        "SageOx config",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Checks for uncommitted SageOx config changes in .sageox/",
		Run:         func(fix bool) checkResult { return checkGitRepoState() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugMergeConflicts,
		Name:        "Merge conflicts",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Checks for unresolved merge conflicts",
		Run:         func(fix bool) checkResult { return checkMergeConflicts() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitConnectivity,
		Name:        "Git connectivity",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Verifies network connectivity to git remotes",
		Run:         func(fix bool) checkResult { return checkGitConnectivity() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitAuth,
		Name:        "Git auth",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Verifies git authentication is configured",
		Run:         func(fix bool) checkResult { return checkGitAuth() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitHooks,
		Name:        "Git hooks",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Reports active git hooks",
		Run:         func(fix bool) checkResult { return checkGitHooks() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitLFS,
		Name:        "Git LFS",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Checks if Git LFS is properly configured when needed",
		Run:         func(fix bool) checkResult { return checkGitLFS() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugStashedChanges,
		Name:        "Git stash",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Reports stashed changes",
		Run:         func(fix bool) checkResult { return checkStashedChanges() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitFsck,
		Name:        "Git integrity",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Validates git object database integrity using git fsck",
		Run:         func(fix bool) checkResult { return checkGitFsck() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitLock,
		Name:        "Git locks",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelSuggested,
		Description: "Checks for stale git lock files from crashed processes",
		Run:         func(fix bool) checkResult { return checkGitLockFiles() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitRepoPaths,
		Name:        "Git repo paths",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelSuggested,
		Description: "Validates configured git repo paths exist and are valid",
		Run:         checkGitRepoPaths,
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerPath,
		Name:        "Ledger path",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelSuggested,
		Description: "Validates ledger path exists and is a git repository",
		Run: func(fix bool) checkResult {
			// ledger path validation is part of checkGitRepoPaths
			// this is a placeholder for direct ledger path checks
			return SkippedCheck("Ledger path", "validated by git repo paths check", "")
		},
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerPathMismatch,
		Name:        "Ledger path config",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelConfirm,
		Description: "Detects when config.local.toml ledger path differs from computed default",
		Run:         checkLedgerPathMismatch,
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerRemote,
		Name:        "Ledger remote URL",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelConfirm,
		Description: "Validates ledger remote URL matches cloud configuration",
		Run: func(fix bool) checkResult {
			gitRoot := findGitRoot()
			if gitRoot == "" {
				return SkippedCheck("Ledger remote URL", "not in git repo", "")
			}
			localCfg, err := config.LoadLocalConfig(gitRoot)
			if err != nil {
				return SkippedCheck("Ledger remote URL", "config error", "")
			}
			return checkLedgerRemoteURL(localCfg)
		},
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugTeamContextPath,
		Name:        "Team context path",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelSuggested,
		Description: "Validates team context paths exist and are valid",
		Run: func(fix bool) checkResult {
			// team context path validation is part of checkGitRepoPaths
			return SkippedCheck("Team context path", "validated by git repo paths check", "")
		},
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugTeamSymlink,
		Name:        "Team symlinks",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelSuggested,
		Description: "Validates team context symlinks are valid",
		Run:         func(fix bool) checkResult { return checkTeamContextSymlinks() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugProjectSymlinks,
		Name:        "Project symlinks",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelAuto,
		Description: "Ensures .sageox/ledger and .sageox/teams/primary symlinks exist for short path display",
		Run:         checkProjectSymlinks,
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLegacyStructure,
		Name:        "Legacy structure",
		Category:    "Git Repository Health",
		FixLevel:    FixLevelConfirm,
		Description: "Detects legacy directory structure that should be migrated",
		Run:         func(fix bool) checkResult { return checkLedgerStructureMigration() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitignoreMissing,
		Name:        "Checkout .gitignore",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelAuto,
		Description: "Checks .sageox/.gitignore in ledger/team context checkouts to prevent committing checkout.json",
		Run: func(fix bool) checkResult {
			// This runs both ledger and team context checks
			// The individual checks are run in checkLedgerGitHealth
			ledgerCheck := checkLedgerCheckoutGitignore(fix)
			if !ledgerCheck.passed && !ledgerCheck.skipped {
				return ledgerCheck
			}
			teamCheck := checkTeamContextCheckoutGitignore(fix)
			if !teamCheck.passed && !teamCheck.skipped {
				return teamCheck
			}
			// both passed or skipped
			if ledgerCheck.skipped && teamCheck.skipped {
				return SkippedCheck("Checkout .gitignore", "no checkouts found", "")
			}
			return PassedCheck("Checkout .gitignore", "checkout.json properly ignored")
		},
	})

	// ============================================================
	// Integration checks (hooks)
	// ============================================================

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugHookCommands,
		Name:        "Hook commands",
		Category:    "Integration",
		FixLevel:    FixLevelCheckOnly,
		Description: "Validates ox commands in hook configurations",
		Run:         func(fix bool) checkResult { return checkHookCommands() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSessionStartHookBug,
		Name:        "SessionStart hook reliability",
		Category:    "Integration",
		FixLevel:    FixLevelCheckOnly,
		Description: "Checks for Claude Code bug #10373 workaround (SessionStart hooks don't work for new sessions)",
		Run:         func(fix bool) checkResult { return checkSessionStartHookBug() },
	})

	// ============================================================
	// SageOx Configuration checks
	// ============================================================

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugEndpointConsistency,
		Name:        "Endpoint consistency",
		Category:    "SageOx Configuration",
		FixLevel:    FixLevelConfirm,
		Description: "Verifies project endpoint matches local team context and ledger paths",
		Run:         checkEndpointConsistency,
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugEndpointNormalization,
		Name:        "Endpoint normalization",
		Category:    "SageOx Configuration",
		FixLevel:    FixLevelSuggested,
		Description: "Detects subdomain-prefixed endpoints (api., www., app., git.) in config, auth, and marker files",
		Run:         checkEndpointNormalization,
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugDuplicateRepoMarkers,
		Name:        "Duplicate repo registrations",
		Category:    "SageOx Configuration",
		FixLevel:    FixLevelConfirm,
		Description: "Detects multiple repo registrations from the same endpoint",
		Run:         checkDuplicateRepoMarkers,
	})

	// ============================================================
	// Team Context checks
	// ============================================================

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugTeamRegistration,
		Name:        "Team registration",
		Category:    "Team Context",
		FixLevel:    FixLevelConfirm,
		Description: "Verifies repo is registered with SageOx",
		Run: func(fix bool) checkResult {
			return checkTeamRegistration(fix)
		},
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLegacyTeamCtx,
		Name:        "Legacy team contexts",
		Category:    "Team Context",
		FixLevel:    FixLevelConfirm,
		Description: "Detects legacy team context directories",
		Run: func(fix bool) checkResult {
			gitRoot := findGitRoot()
			if gitRoot == "" {
				return SkippedCheck("Legacy team contexts", "not in git repo", "")
			}
			return checkLegacyTeamContexts(gitRoot)
		},
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugOrphanedTeamDirs,
		Name:        "Orphaned team dirs",
		Category:    "Team Context",
		FixLevel:    FixLevelConfirm,
		Description: "Detects team directories with no valid workspace references",
		Run: func(fix bool) checkResult {
			opts := doctorOptions{fix: fix}
			return checkOrphanedTeamDirs(opts)
		},
	})

	// ============================================================
	// Daemon checks
	// ============================================================

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugDaemonRunning,
		Name:        "Daemon running",
		Category:    "Daemon",
		FixLevel:    FixLevelCheckOnly,
		Description: "Checks if the background daemon is running",
		Run: func(fix bool) checkResult {
			// daemon checks are handled specially in checkDaemonHealth()
			return SkippedCheck("Daemon running", "see daemon health checks", "")
		},
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugDaemonSocket,
		Name:        "Daemon socket",
		Category:    "Daemon",
		FixLevel:    FixLevelCheckOnly,
		Description: "Checks if the daemon socket is accessible",
		Run: func(fix bool) checkResult {
			// daemon checks are handled specially in checkDaemonHealth()
			return SkippedCheck("Daemon socket", "see daemon health checks", "")
		},
	})

	// ============================================================
	// Session checks
	// ============================================================

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSessionAutoStage,
		Name:        "session files staged",
		Category:    "Sessions",
		FixLevel:    FixLevelAuto,
		Description: "Auto-stages session files (.jsonl, .html, summary.md) in ledger",
		Run: func(fix bool) checkResult {
			// This check runs automatically (FixLevelAuto) - always performs the fix
			gitRoot := findGitRoot()
			check := doctor.NewSessionAutoStageCheck(gitRoot)
			result := check.Run(context.Background())
			return convertDoctorResult(result)
		},
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSessionPush,
		Name:        "session push",
		Category:    "Sessions",
		FixLevel:    FixLevelSuggested,
		Description: "Pushes committed session data to remote when local is ahead",
		Run: func(fix bool) checkResult {
			gitRoot := findGitRoot()
			check := doctor.NewSessionPushCheck(gitRoot, fix)
			result := check.Run(context.Background())
			return convertDoctorResult(result)
		},
	})
}
