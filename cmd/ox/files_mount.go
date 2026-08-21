package main

import (
	"context"
	"os"
	"time"

	"github.com/sageox/ox/internal/filesmount"
)

// SageOx Files mounts a read-only view of team context in Finder. Where it is
// present it is a better read path than the git checkout: it is hydrated from
// the Drive API on access rather than at the last `ox sync`, so it cannot be
// stale in the way a checkout that missed a pull is.
//
// It is off by default. Detection is free and always reported by `ox status`,
// but preferring the mount changes where every team document is read from, and
// that is a deliberate flip to make once someone has watched it work
// end-to-end — not a default to arrive by surprise in an upgrade.
const (
	filesMountEnv = "OX_FILES_MOUNT"

	// mountDiscoveryBudget bounds the whole lookup. Reads against the mount can
	// reach the network, and no ox command should stall on an accelerator: past
	// this, the git checkout answers instead.
	mountDiscoveryBudget = 3 * time.Second
)

func filesMountEnabled() bool { return os.Getenv(filesMountEnv) == "1" }

// mountedTeamRoot returns the mounted folder to read a team's context from.
//
// Reports false whenever the mount is absent, unreadable, disabled, or does not
// carry this team, so every caller falls back to the checkout by doing nothing.
func mountedTeamRoot(teamID string) (string, bool) {
	if !filesMountEnabled() {
		return "", false
	}
	return discoverMountedTeamRoot(teamID)
}

// discoverMountedTeamRoot looks for the team regardless of the flag, so `ox
// status` can report what is mounted without also opting the session into
// reading from it.
func discoverMountedTeamRoot(teamID string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), mountDiscoveryBudget)
	defer cancel()
	return filesmount.FindTeam(ctx, teamID)
}
