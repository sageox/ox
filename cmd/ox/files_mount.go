package main

import (
	"context"
	"os"
	"time"

	"github.com/sageox/ox/internal/filesmount"
)

// SageOx Files mounts a read-only view of team context in Finder. Where it is
// present it is a SECOND place the same team's documents live, hydrated from
// the Drive API on access rather than at the last `ox sync`.
//
// It is a secondary source, not a replacement. The git team-context checkout
// and the git ledger stay primary and authoritative: they are what `ox sync`
// writes, what the daemon reports on, what every other ox command reads, and
// what a person can inspect with plain git. The mount is read alongside them
// and fills gaps — a document the checkout has not pulled yet, or a team a
// machine never cloned. Where both carry the same document, the checkout wins.
//
// Detection is free and always reported by `ox status`. Reading the mount at
// all is opt-in: a new source earns standing by being watched agreeing with the
// proven one on real drives, not by being newer.
const (
	// filesMountEnv opts a session into reading the mount as a second source.
	//
	// SAGEOX_* is the canonical namespace for customer-facing environment
	// variables, and a customer-facing OX_* name is an anti-pattern here — see
	// AGENTS.md and sageox-mono ADR-047.
	filesMountEnv = "SAGEOX_FILES_MOUNT"

	// mountDiscoveryBudget bounds the whole lookup. Reads against the mount can
	// reach the network, and no ox command should stall on an accelerator: past
	// this, the git sources answer alone.
	mountDiscoveryBudget = 3 * time.Second
)

// filesMountEnabled reports whether this session reads a mounted drive as a
// second source alongside the checkout.
func filesMountEnabled() bool { return os.Getenv(filesMountEnv) == "1" }

// mountedTeamRoot returns a mounted folder to read a team's context from in
// addition to the checkout.
//
// Reports false whenever the mount is absent, unreadable, disabled, or does not
// carry this team, so every caller reads the git sources alone by doing
// nothing.
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
	scan := scanFilesMounts()
	defer scan.close()
	return scan.teamRoot(teamID)
}

// filesMountScan is one discovery pass, reused for every team a command
// renders.
//
// `ox status` draws a card per team context. Discovering per card would spend
// the budget per card, and on a machine whose drive is signed out that repeated
// stall is the whole delay a user feels. Drives are discovered once and every
// team is resolved from that result, under one shared deadline.
type filesMountScan struct {
	ctx    context.Context
	cancel context.CancelFunc
	mounts []filesmount.Mount
}

// scanFilesMounts discovers every mounted SageOx drive once, under a single
// budget. Callers must close the result.
func scanFilesMounts() *filesMountScan {
	ctx, cancel := context.WithTimeout(context.Background(), mountDiscoveryBudget)
	return &filesMountScan{ctx: ctx, cancel: cancel, mounts: filesmount.Discover(ctx)}
}

// close releases the scan's shared budget.
func (s *filesMountScan) close() {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
}

// teamRoot returns the mounted folder carrying a team, and whether any
// discovered drive carries it at all.
func (s *filesMountScan) teamRoot(teamID string) (string, bool) {
	if s == nil {
		return "", false
	}
	for _, mount := range s.mounts {
		if path, ok := mount.TeamPath(s.ctx, teamID); ok {
			return path, true
		}
	}
	return "", false
}
