package main

import (
	"context"

	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/session"
)

// checkSessionHealth returns health checks for the session system.
// The opts parameter provides fix flags for checks that support auto-remediation.
func checkSessionHealth(opts doctorOptions) []checkResult {
	gitRoot := findGitRoot()
	ctx := context.Background()

	// compute health status once (runs multiple git commands internally)
	// and share across all session checks to avoid redundant work
	var healthStatus *session.HealthStatus
	if gitRoot != "" {
		healthStatus = session.CheckHealth(gitRoot)
	}

	var results []checkResult

	// retry failed session uploads first (auto-fix: creates ledger files
	// that downstream auto-stage/commit/push checks operate on)
	uploadRetryResult := checkSessionUploadRetry()
	if !uploadRetryResult.passed || uploadRetryResult.message != "no pending uploads" {
		results = append(results, uploadRetryResult)
	}

	// create session checks from internal/doctor package
	checks := []doctor.Check{
		doctor.NewSessionModeCheck(gitRoot),   // show effective mode and source
		doctor.NewSessionLedgerCheck(gitRoot), // verify ledger when mode requires it
		doctor.NewSessionStorageCheck(gitRoot),
		doctor.NewSessionRepoCheck(gitRoot),
		doctor.NewSessionRecordingCheck(gitRoot),
		doctor.NewSessionStaleCheck(gitRoot),
		doctor.NewSessionOrphanedCheck(gitRoot),       // detect orphaned recordings
		doctor.NewSessionStopIncompleteCheck(gitRoot), // detect stuck stop-incomplete recordings
		doctor.NewSessionPendingCheck(gitRoot),
		doctor.NewSessionSyncCheck(gitRoot),
		doctor.NewSessionAutoStageCheck(gitRoot), // auto-stage session files (FixLevelAuto)
	}

	// inject cached health status into checks that support it
	if healthStatus != nil {
		for _, check := range checks {
			if cacheable, ok := check.(doctor.SessionHealthCacheable); ok {
				cacheable.SetHealthStatus(healthStatus)
			}
		}
	}

	// run checks and convert to checkResult format
	for _, check := range checks {
		result := check.Run(ctx)

		// skip empty results (StatusSkip with no message)
		if result.Status == doctor.StatusSkip && result.Message == "" {
			continue
		}

		results = append(results, convertDoctorResult(result))
	}

	// add registered DoctorCheck for session commit (supports --fix)
	sessionCommitResult := checkSessionCommit(opts.shouldFix(CheckSlugSessionCommit))
	// only include if not a pass with "no staged sessions" (reduce noise)
	if !sessionCommitResult.passed || sessionCommitResult.message != "no staged sessions" {
		results = append(results, sessionCommitResult)
	}

	// add session push check (runs after commit, supports --fix)
	// this check pushes committed session data to remote when local is ahead
	sessionPushCheck := doctor.NewSessionPushCheck(gitRoot, opts.shouldFix(CheckSlugSessionPush))
	if healthStatus != nil {
		sessionPushCheck.SetHealthStatus(healthStatus)
	}
	pushResult := sessionPushCheck.Run(ctx)
	// only include if not skipped without message
	if pushResult.Status != doctor.StatusSkip || pushResult.Message != "" {
		results = append(results, convertDoctorResult(pushResult))
	}

	// add incomplete sessions check (context-aware: human vs agent guidance)
	incompleteResult := checkSessionIncomplete(opts.shouldFix(CheckSlugSessionIncomplete))
	// only include if not a pass with "all sessions complete" (reduce noise)
	if !incompleteResult.passed || incompleteResult.message != "all sessions complete" {
		results = append(results, incompleteResult)
	}

	return results
}

// convertDoctorResult converts a doctor.CheckResult to the CLI's checkResult format.
func convertDoctorResult(dr doctor.CheckResult) checkResult {
	switch dr.Status {
	case doctor.StatusPass:
		return PassedCheck(dr.Name, dr.Message)
	case doctor.StatusFail:
		return FailedCheck(dr.Name, dr.Message, dr.Fix)
	case doctor.StatusWarn:
		return WarningCheck(dr.Name, dr.Message, dr.Fix)
	case doctor.StatusSkip:
		return SkippedCheck(dr.Name, dr.Message, dr.Fix)
	default:
		return FailedCheck(dr.Name, "unknown status", "")
	}
}
