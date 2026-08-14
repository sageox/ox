package main

import (
	"fmt"

	"github.com/sageox/ox/internal/session"
)

// checkSessionLifecycleRegistrationRetry retries only registrations that the
// start path already marked pending. It runs as a safe doctor repair: the
// session ID is the idempotency key and no Ledger content is uploaded here.
func checkSessionLifecycleRegistrationRetry(projectRoot string) checkResult {
	if projectRoot == "" {
		return SkippedCheck("Session link registration", "no git repository", "")
	}
	states, err := session.LoadAllRecordingStates(projectRoot)
	if err != nil {
		return WarningCheck("Session link registration", "could not inspect recordings", err.Error())
	}

	pending := 0
	confirmed := 0
	for _, state := range states {
		if state.LifecycleRegistrationState != "pending" {
			continue
		}
		pending++
		notifySessionStartedAsync(projectRoot, state)
		if state.LifecycleRegistrationState == "confirmed" {
			confirmed++
		}
	}
	if pending == 0 {
		return PassedCheck("Session link registration", "no pending registrations")
	}
	if confirmed == pending {
		return PassedCheck("Session link registration", fmt.Sprintf("confirmed %d pending registration(s)", confirmed))
	}
	return WarningCheck(
		"Session link registration",
		fmt.Sprintf("confirmed %d of %d pending registration(s)", confirmed, pending),
		"Run `ox login` if authentication expired, then re-run `ox doctor`",
	)
}
