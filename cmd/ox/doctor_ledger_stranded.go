package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/gitutil"
)

// CheckSlugLedgerStrandedCommits detects ledger commits that are reachable from
// HEAD and from nowhere else — no branch, no remote. Those commits exist in
// exactly one place and become unreferenced the moment HEAD moves.
//
// bd ox-akab: an interactive rebase left the ledger on a detached HEAD, session
// commits kept landing there for ~6 weeks, and nothing surfaced it. Doctor saw
// only "detached HEAD" as a bare warning with no fix, and the daemon's recovery
// correctly declines to abort a detached rebase (aborting would strand the
// replay) — so every automated path looked at the problem and, reasonably, did
// nothing. The gap was that no path made the commits SAFE first.
const CheckSlugLedgerStrandedCommits = "ledger-stranded-commits"

// strandedCheckTimeout bounds the git plumbing this check runs. These are all
// local ref walks; a slow one means something is wrong with the repo, and
// doctor should report rather than hang.
const strandedCheckTimeout = 30 * time.Second

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugLedgerStrandedCommits,
		Name:     "Ledger stranded commits",
		Category: "Ledger Git Health",
		// Confirm, never Auto: the repair can escalate to clearing a wedged
		// rebase, and the ledger holds the user's only copy of unpushed
		// sessions. A human seeing `ox doctor --fix` pauses; an agent executes
		// every step it is handed.
		FixLevel:    FixLevelConfirm,
		Description: "Detects ledger commits reachable only from HEAD (no branch, no remote); --fix creates a verified rescue branch before touching any wedge",
		Run:         checkLedgerStrandedCommits,
	})
}

// checkLedgerStrandedCommits reports commits that exist only on HEAD and, under
// --fix, makes them reachable from a named branch.
//
// The fix splits along risk, which is the whole design:
//
//   - Creating the rescue branch is purely ADDITIVE — it adds a ref and mutates
//     nothing else — so it runs even non-interactively. An automated pass should
//     always be able to make data safe.
//   - Clearing a wedged rebase is destructive, so it requires an interactive
//     human. Non-TTY and agent contexts fail closed: the data is rescued, the
//     wedge is left, and the check says so.
func checkLedgerStrandedCommits(fix bool) checkResult {
	return strandedCommitsCheck(getLedgerPath(), fix, cli.IsInteractive())
}

// strandedCommitsCheck is the testable core: it takes the ledger path and the
// interactivity decision as parameters instead of reading ambient state.
//
// That split is deliberate. The fail-closed behavior — never clearing a wedge
// without a human — is the single most important property here, and a test that
// depends on whether the test runner happens to have a TTY cannot pin it. Making
// interactivity an argument turns "hope the environment cooperates" into an
// assertion.
func strandedCommitsCheck(ledgerPath string, fix, interactive bool) checkResult {
	const name = "Ledger stranded commits"

	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger configured", "")
	}
	if !isGitRepo(ledgerPath) {
		return SkippedCheck(name, "ledger is not a git repo", "")
	}

	ctx, cancel := context.WithTimeout(context.Background(), strandedCheckTimeout)
	defer cancel()

	stranded, err := gitutil.StrandedCommitCount(ctx, ledgerPath)
	if err != nil {
		return SkippedCheck(name, "could not count stranded commits", err.Error())
	}
	if stranded == 0 {
		return PassedCheck(name, "no stranded commits")
	}

	rebaseWedged := gitutil.IsRebaseInProgress(ledgerPath)
	summary := fmt.Sprintf("%d commit(s) reachable only from HEAD", stranded)

	if !fix {
		detail := fmt.Sprintf("These commits exist in exactly one place and are lost if HEAD moves. "+
			"Run `ox doctor --fix-slug=%s` to create a rescue branch.", CheckSlugLedgerStrandedCommits)
		if rebaseWedged {
			detail = fmt.Sprintf("These commits exist in exactly one place, and a rebase is wedged on top of them. "+
				"Run `ox doctor --fix-slug=%s` (interactively) to rescue them and clear the wedge.",
				CheckSlugLedgerStrandedCommits)
		}
		return FailedCheck(name, summary, detail)
	}

	log := slog.Default()

	// The destructive path: rescue AND clear the wedge. Interactive only.
	if rebaseWedged && interactive {
		rescueRef, err := gitutil.RescueThenAbort(ctx, ledgerPath, "doctor: stranded commits on wedged ledger", log)
		if rescueRef != "" && err != nil {
			// Data is safe even though the wedge persists. Lead with the branch.
			return FailedCheck(name,
				fmt.Sprintf("rescued %d commit(s) to %s; wedge NOT cleared", stranded, rescueRef),
				"Recovery failed after the rescue branch was created: "+err.Error())
		}
		if err != nil {
			if errors.Is(err, gitutil.ErrNoStrandedCommits) {
				return PassedCheck(name, "no stranded commits")
			}
			return FailedCheck(name, summary, "rescue failed: "+err.Error())
		}
		return WarningCheck(name,
			fmt.Sprintf("rescued %d commit(s) to %s, wedge cleared", stranded, rescueRef),
			"Replay the rescue branch onto origin/main when you are ready; it is not pushed automatically.")
	}

	// The additive path: make the data safe without touching the wedge.
	rescueRef, err := gitutil.CreateRescueBranch(ctx, ledgerPath, "doctor: stranded commits", log)
	if err != nil {
		if errors.Is(err, gitutil.ErrNoStrandedCommits) {
			return PassedCheck(name, "no stranded commits")
		}
		return FailedCheck(name, summary, "could not create rescue branch: "+err.Error())
	}

	if rebaseWedged {
		// Fail closed on the destructive half: rescued, but a human has to
		// clear the wedge. Reported as a failure so it stays visible.
		return FailedCheck(name,
			fmt.Sprintf("rescued %d commit(s) to %s; wedge NOT cleared", stranded, rescueRef),
			"Clearing a wedged rebase needs an interactive terminal. Re-run "+
				"`ox doctor --fix-slug="+CheckSlugLedgerStrandedCommits+"` from a TTY.")
	}

	return WarningCheck(name,
		fmt.Sprintf("rescued %d commit(s) to %s", stranded, rescueRef),
		"Those commits now have a branch. Replay it onto origin/main when you are ready; it is not pushed automatically.")
}
