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

	registrationRetryResult := checkSessionLifecycleRegistrationRetry(gitRoot)
	if !registrationRetryResult.passed || registrationRetryResult.message != "no pending registrations" {
		results = append(results, registrationRetryResult)
	}

	// transcripts stranded in the content store with no local copy (GH #710).
	// Quiet on the overwhelmingly common healthy case — dehydration is the
	// normal steady state, so only surface sessions that actually need
	// their transcript and cannot get it.
	// Only surface a real finding: a skipped result (no ledger, no sessions
	// dir) has passed=false too, and including it would put a permanent
	// "skipped" line in doctor output for every user without a ledger.
	dehydratedResult := checkSessionDehydrated(opts.shouldFix(CheckSlugSessionDehydrated))
	if !dehydratedResult.skipped && (!dehydratedResult.passed || dehydratedResult.warning) {
		results = append(results, dehydratedResult)
	}

	// create session checks from internal/doctor package
	checks := []doctor.Check{
		doctor.NewSessionModeCheck(gitRoot),   // show effective mode and source
		doctor.NewSessionLedgerCheck(gitRoot), // verify ledger when mode requires it
		doctor.NewSessionStorageCheck(gitRoot),
		doctor.NewSessionRepoCheck(gitRoot),
		doctor.NewSessionRecordingCheck(gitRoot),
		doctor.NewSessionStaleCheck(gitRoot),
		doctor.NewSessionOrphanedCheck(gitRoot, opts.shouldFix(CheckSlugSessionOrphaned)), // detect orphaned recordings
		doctor.NewSessionStopIncompleteCheck(gitRoot),                                     // detect stuck stop-incomplete recordings
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
		result := check.Run(ctx, false)

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
	pushResult := sessionPushCheck.Run(ctx, opts.shouldFix(CheckSlugSessionPush))
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

	// identity integrity: meta.json session_id vs raw.jsonl header
	// session_id. Detect-only (see checkSessionIDDivergence doc comment
	// for why); always shown rather than filtered on a boring-pass
	// message, since a genuine divergence is meant to be rare and loud.
	results = append(results, checkSessionIDDivergence())

	// linkage soft signals: trailer coverage on recent commits, reachability
	// of closed-session ProducedCommits SHAs, and PR-body attribution
	// coverage. None block; all are diagnostic.
	results = append(results, checkSessionTrailerRatio())
	results = append(results, checkSessionProducedCommitsStaleness())
	results = append(results, checkPRAttributionCoverage())

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
