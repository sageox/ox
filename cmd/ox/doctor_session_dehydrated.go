package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/sessionsummary"
)

// dehydratedFixBudget caps how many sessions one `ox doctor --fix` run
// will download. Hydration is real network I/O against the content store
// and transcripts are large — the GH #710 reporter's manual recovery
// pulled 159 MB. A bounded pass that says "run again for more" is far
// better than an unbounded one nobody expected.
const dehydratedFixBudget = 20

const dehydratedCheckName = "session content"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugSessionDehydrated,
		Name:     dehydratedCheckName,
		Category: "Sessions",
		// Suggested, not Auto: the fix does network I/O and can pull
		// hundreds of MB. Auto-fix runs on a bare `ox doctor`, and nobody
		// expects that to start downloading.
		FixLevel:    FixLevelSuggested,
		Description: "Detects sessions whose transcript has no local copy and cannot be summarized",
		Run:         checkSessionDehydrated,
	})
}

// dehydratedSession is one session that needs its transcript but has no
// local copy.
type dehydratedSession struct {
	Name string
	Dir  string
	// Reason explains why hydration is not possible right now. Empty
	// means "not attempted yet".
	Reason string
	// Permanent is true when the transcript was never uploaded, so no
	// amount of retrying will produce it.
	Permanent bool
}

// checkSessionDehydrated reports sessions whose raw.jsonl is a
// content-store pointer with no local copy AND which still need
// summarizing.
//
// # Why this is not "git-lfs is not installed"
//
// GH #710 was filed as "undetected git-lfs misconfiguration", on the
// theory that an uninstalled git-lfs binary left transcripts as
// unsmudged pointers. That diagnosis is wrong, and acting on it would
// violate .claude/rules/lfs-no-git-lfs-binary.md.
//
// ox ledgers ship no .gitattributes at all, so git's LFS filters never
// apply to them. The pointer files are ox's own, written by
// lfs.WritePointerFile, and ledger clones are DEHYDRATED BY DESIGN —
// content lives in the store and is fetched on demand over the Batch API
// in pure Go. The reporter's workaround (install git-lfs, hand-write
// .git/info/attributes, git lfs fetch) appeared to work only because ox
// uploads to the same store, and it is actively dangerous: by their own
// account it also converts data/plans/*.md into pointers on the daemon's
// next commit, corrupting the ledger for every teammate.
//
// So the real defect is not a missing binary. It is that ox read a stub
// as if it were a transcript, produced an empty summary, and retried
// forever. This check detects that content state directly.
//
// # Why the failing condition is narrow
//
// Dehydration is the normal steady state. A healthy ledger can hold
// thousands of dehydrated sessions and be perfectly fine. Reporting that
// as a problem would be noise that trains people to ignore doctor. So a
// session only fails when ALL THREE hold:
//
//  1. raw.jsonl is a pointer stub, AND
//  2. there is no hydrated copy in the ledger cache, AND
//  3. it still needs summarizing (no title, or a non-terminal summary status).
//
// A dehydrated session that already has a good summary needs nothing.
func checkSessionDehydrated(fix bool) checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(dehydratedCheckName, "no ledger found", "")
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return SkippedCheck(dehydratedCheckName, "no sessions directory", "")
	}

	var stranded []dehydratedSession
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionDir := filepath.Join(sessionsDir, entry.Name())
		if !sessionNeedsHydration(sessionDir, ledgerPath, entry.Name()) {
			continue
		}
		stranded = append(stranded, dehydratedSession{Name: entry.Name(), Dir: sessionDir})
	}

	if len(stranded) == 0 {
		return PassedCheck(dehydratedCheckName, "all sessions readable")
	}

	if !fix {
		return dehydratedWarning(stranded, nil, nil)
	}
	return hydrateStrandedSessions(stranded, ledgerPath)
}

// sessionNeedsHydration applies the three-part failing condition.
func sessionNeedsHydration(sessionDir, ledgerPath, sessionName string) bool {
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	if !lfs.IsPointerFile(rawPath) {
		return false // real content on disk, or no raw.jsonl at all
	}

	// a hydrated copy in the cache means the daemon can already read it
	cachePath := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName, "raw.jsonl")
	if info, err := os.Stat(cachePath); err == nil && info.Size() > 0 {
		return false
	}

	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		return false // unreadable meta is a different check's problem
	}
	return summaryStillNeeded(meta)
}

// summaryStillNeeded reports whether a session is still waiting on a
// usable summary. Terminal states are excluded: retrying them is exactly
// the loop GH #710 was about.
func summaryStillNeeded(meta *lfs.SessionMeta) bool {
	if meta.SummaryStatus == sessionsummary.SummaryStatusUnrecoverable {
		return false
	}
	if strings.TrimSpace(meta.Title) != "" {
		return false // already has something a human can read
	}
	switch meta.SummaryStatus {
	case "", sessionsummary.SummaryStatusPending, sessionsummary.SummaryStatusFailedValidation:
		return true
	default:
		return false
	}
}

// hydrateStrandedSessions downloads transcripts into the ledger cache,
// bounded by dehydratedFixBudget.
//
// Hydration is CACHE-ONLY. Writing content over the git-tracked
// sessions/<name>/raw.jsonl would break LFS linkage on the next commit
// and every subsequent push — see .claude/rules/cache-only-design.md.
// lfs.HydrateRawToCacheErr already enforces this.
func hydrateStrandedSessions(stranded []dehydratedSession, ledgerPath string) checkResult {
	projectRoot := findGitRoot()
	client, err := lfs.NewClientFromLedger(ledgerPath, endpoint.GetForProject(projectRoot))
	if err != nil {
		return dehydratedWarning(stranded, nil, fmt.Errorf("cannot reach the content store: %w", err))
	}

	budget := min(len(stranded), dehydratedFixBudget)

	var recovered int
	var remaining, lost []dehydratedSession
	for i, s := range stranded {
		if i >= budget {
			remaining = append(remaining, s)
			continue
		}
		_, hydrateErr := lfs.HydrateRawToCacheErr(client, s.Dir, ledgerPath)
		if hydrateErr == nil {
			recovered++
			continue
		}

		s.Reason = hydrateErr.Error()
		s.Permanent = errors.Is(hydrateErr, lfs.ErrNoLFSManifest)
		if s.Permanent {
			// The transcript was never uploaded. Mark it terminal so the
			// daemon stops retrying and this check stops reporting it —
			// otherwise it is flagged forever with no possible remedy.
			if err := markSessionUnrecoverable(s.Dir); err != nil {
				// couldn't record the terminal state, so it is NOT settled:
				// report it rather than claiming a clean pass.
				s.Reason = fmt.Sprintf("%s (and could not mark it unrecoverable: %v)", s.Reason, err)
				remaining = append(remaining, s)
				continue
			}
			lost = append(lost, s)
			continue
		}
		remaining = append(remaining, s)
	}

	// Permanent loss must be reported on the SAME pass it is detected.
	// Marking those sessions terminal means they will never be collected
	// again, so returning only the retryable warning here would drop the
	// loss report on the floor permanently.
	if len(remaining) > 0 {
		return dehydratedWarning(remaining, lost, nil)
	}
	if len(lost) > 0 {
		// Permanently gone is NOT a clean pass — the user has sessions whose
		// transcripts no longer exist anywhere, and silently reporting
		// success would hide that. They are marked terminal so this is a
		// one-time report, not a recurring nag.
		return dehydratedPermanentLoss(lost, recovered)
	}
	return PassedCheck(dehydratedCheckName, fmt.Sprintf("downloaded %d session transcript(s)", recovered))
}

// dehydratedPermanentLoss reports sessions whose transcript was never
// uploaded and therefore cannot be recovered by anyone.
func dehydratedPermanentLoss(lost []dehydratedSession, recovered int) checkResult {
	var sb strings.Builder
	sb.WriteString("These sessions have no transcript in the content store — it was never uploaded, ")
	sb.WriteString("so it cannot be recovered. They are now marked unrecoverable and will not be retried.\n")
	shown := min(len(lost), 5)
	for _, s := range lost[:shown] {
		fmt.Fprintf(&sb, "  %s\n", s.Name)
	}
	if len(lost) > shown {
		fmt.Fprintf(&sb, "  ... and %d more\n", len(lost)-shown)
	}
	if recovered > 0 {
		fmt.Fprintf(&sb, "\nRecovered %d other session transcript(s) in this run.", recovered)
	}
	return checkResult{
		name:    dehydratedCheckName,
		warning: true,
		message: fmt.Sprintf("%d transcript(s) permanently unavailable", len(lost)),
		detail:  sb.String(),
	}
}

// markSessionUnrecoverable records that a session's transcript is gone.
// Goes through MutateSessionMeta so it cannot strip the other fields —
// see GH #710 and `make check-session-meta-rmw`.
func markSessionUnrecoverable(sessionDir string) error {
	return lfs.MutateSessionMeta(context.Background(), sessionDir, func(current *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if current == nil {
			return nil, nil
		}
		current.SummaryStatus = sessionsummary.SummaryStatusUnrecoverable
		current.SummaryAttempts = lfs.MaxSummaryAttempts
		// Ops-facing only; never rendered as a title or summary.
		current.ValidationError = "transcript was never uploaded to the content store; nothing to summarize"
		return current, nil
	})
}

// dehydratedWarning renders the user-facing result.
//
// LOAD-BEARING: none of this text may mention git-lfs, `git lfs`,
// .gitattributes, or filter=lfs. Ledger clones are dehydrated by design
// and the remedy is an ox command, never a git-lfs one. Sending someone
// down the git-lfs path is what turned GH #710 from an empty summary into
// a corrupted shared ledger. Asserted in doctor_session_dehydrated_test.go.
func dehydratedWarning(stranded, lost []dehydratedSession, clientErr error) checkResult {
	var sb strings.Builder
	sb.WriteString("Session transcripts live in the ledger content store; clones are content-free by design.\n")
	if clientErr != nil {
		sb.WriteString(clientErr.Error())
		sb.WriteString("\n")
	}

	shown := min(len(stranded), 5)
	for _, s := range stranded[:shown] {
		if s.Reason != "" {
			fmt.Fprintf(&sb, "  %s — %s\n", s.Name, s.Reason)
			continue
		}
		fmt.Fprintf(&sb, "  %s\n", s.Name)
	}
	if len(stranded) > shown {
		fmt.Fprintf(&sb, "  ... and %d more\n", len(stranded)-shown)
	}

	sb.WriteString("\nRun `ox doctor --fix` to download them, ")
	sb.WriteString("or `ox session download <name>` for one.")

	// Fold in any permanently-lost sessions. They are marked terminal, so
	// this is the only pass that will ever collect them — reporting the
	// retryable ones alone would drop the loss report for good.
	msg := fmt.Sprintf("%d transcript(s) not available locally", len(stranded))
	if len(lost) > 0 {
		fmt.Fprintf(&sb, "\n\n%d further transcript(s) were never uploaded and cannot be recovered; "+
			"they are now marked unrecoverable:\n", len(lost))
		lostShown := min(len(lost), 5)
		for _, s := range lost[:lostShown] {
			fmt.Fprintf(&sb, "  %s\n", s.Name)
		}
		if len(lost) > lostShown {
			fmt.Fprintf(&sb, "  ... and %d more\n", len(lost)-lostShown)
		}
		msg = fmt.Sprintf("%d not available locally, %d permanently lost", len(stranded), len(lost))
	}

	return checkResult{
		name:    dehydratedCheckName,
		warning: true,
		message: msg,
		detail:  sb.String(),
	}
}
