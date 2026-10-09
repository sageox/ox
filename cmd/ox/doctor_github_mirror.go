package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/sageox/ox/internal/githubmirror"
)

// CheckSlugGitHubMirror reports the health of the daemon's GitHub mirror relay.
const CheckSlugGitHubMirror = "github-mirror"

const (
	githubMirrorCheckName = "GitHub mirror"

	// githubMirrorStaleAfter is how long the relay may go without a success,
	// while failing, before doctor calls it out. A machine that was simply off
	// has no newer error and is not flagged — only a relay that tried and failed.
	githubMirrorStaleAfter = 24 * time.Hour

	// githubMirrorMaxErrorLen bounds the last error shown on a doctor row. The
	// text comes from the daemon's own error chain, which can carry a response
	// body, and one doctor row must stay one scannable line.
	githubMirrorMaxErrorLen = 200
)

func init() {
	// Registered in "Ledger Git Health" because the relay state lives in the
	// Ledger's local cache; TestLedgerGitHealthOrderCoversRegistry requires the
	// slug in ledgerGitHealthOrder. Report-only: nothing here is safe to repair
	// from the CLI — the daemon owns the state file and the server owns the
	// refusals.
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitHubMirror,
		Name:        githubMirrorCheckName,
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Reports whether the daemon's relay of GitHub pull requests and issues to the team bulletin board is succeeding",
		Run:         func(fix bool) checkResult { return checkGitHubMirror() },
	})
}

// checkGitHubMirror reads the relay state from the current project's Ledger.
func checkGitHubMirror() checkResult {
	return checkGitHubMirrorAt(getLedgerPath(), time.Now())
}

// checkGitHubMirrorAt is the testable core of checkGitHubMirror. It reads only
// the state file the daemon keeps in the Ledger cache and never touches the
// network: doctor must stay quick, and the state already records what the last
// relay saw.
func checkGitHubMirrorAt(ledgerPath string, now time.Time) checkResult {
	if ledgerPath == "" {
		return SkippedCheck(githubMirrorCheckName, "no ledger found", "")
	}

	// LoadState treats a missing file as an empty state, which would look like
	// a mirror that has not tried yet. A file that was never written means the
	// mirror is not active on this machine, and that is a different answer.
	if _, err := os.Stat(githubmirror.StatePath(ledgerPath)); errors.Is(err, fs.ErrNotExist) {
		return SkippedCheck(githubMirrorCheckName, "not active on this machine", "")
	}

	state, err := githubmirror.LoadState(ledgerPath)
	if err != nil {
		return WarningCheck(githubMirrorCheckName, "relay state unreadable",
			fmt.Sprintf("%s; safe to delete the file, the daemon relays from scratch without it", summarizeRelayError(err.Error())))
	}
	return evaluateGitHubMirrorState(state, now)
}

// evaluateGitHubMirrorState turns a relay state into a doctor verdict.
//
// Order matters. A repo the server refused is a known, explained condition —
// not a malfunction — so it outranks the failure rules; whatever error is
// recorded beside it is the refusal's side effect. Failures outrank backoff,
// because backoff is the daemon's reaction to a failure, not a finding.
func evaluateGitHubMirrorState(state *githubmirror.State, now time.Time) checkResult {
	retry := ""
	if state.NextAllowedAt.After(now) {
		retry = "next attempt after " + formatMirrorTime(state.NextAllowedAt)
	}

	// the server declined this repo, e.g. a private repo the team has not
	// opted in. Informational: the fix is a decision on the SageOx side.
	if status := state.RepoStatus; status != "" && status != githubmirror.RepoEnabled {
		return InfoCheck(githubMirrorCheckName,
			fmt.Sprintf("server declined this repo (%s)", status),
			joinMirrorDetail(describeRepoStatus(state.Repo, status), retry))
	}

	lastError := summarizeRelayError(state.LastError)
	errorAt := state.LastErrorAt
	if errorAt.IsZero() {
		// an error recorded without a time still came from the latest attempt;
		// treating it as undated would hide a failing relay
		errorAt = state.LastAttemptAt
	}

	// attempts have been made, none ever got through, and the daemon says why
	if state.LastSuccessAt.IsZero() && !state.LastAttemptAt.IsZero() && state.LastError != "" {
		return WarningCheck(githubMirrorCheckName,
			"has never relayed successfully",
			joinMirrorDetail(fmt.Sprintf("last error %s: %s", mirrorAgo(now, errorAt), lastError), retry, "the daemon retries on its own"))
	}

	// it worked once, has been quiet past the threshold, and the newest thing
	// that happened since is an error
	if !state.LastSuccessAt.IsZero() && now.Sub(state.LastSuccessAt) > githubMirrorStaleAfter &&
		state.LastError != "" && errorAt.After(state.LastSuccessAt) {
		return WarningCheck(githubMirrorCheckName,
			fmt.Sprintf("last success %s", mirrorAgo(now, state.LastSuccessAt)),
			joinMirrorDetail(fmt.Sprintf("last error %s: %s", mirrorAgo(now, errorAt), lastError), retry, "the daemon retries on its own"))
	}

	if retry != "" {
		reason := lastError
		if reason == "" {
			reason = "no reason recorded"
		}
		return InfoCheck(githubMirrorCheckName,
			"backing off until "+formatMirrorTime(state.NextAllowedAt),
			"reason: "+reason)
	}

	switch {
	case !state.LastSuccessAt.IsZero():
		return PassedCheck(githubMirrorCheckName,
			fmt.Sprintf("last relayed %s, %d item(s) tracked", mirrorAgo(now, state.LastSuccessAt), len(state.Items)))
	case !state.LastAttemptAt.IsZero():
		return PassedCheck(githubMirrorCheckName, "first relay in progress")
	default:
		return PassedCheck(githubMirrorCheckName, "no relay attempted yet")
	}
}

// describeRepoStatus explains a server refusal in the words a person acts on.
func describeRepoStatus(repo, status string) string {
	subject := "this repo"
	if repo != "" {
		subject = repo
	}
	switch status {
	case githubmirror.RepoNotOptedIn:
		return subject + " is private and the team has not opted in to mirroring private repos"
	case githubmirror.RepoNotLinked:
		return subject + " is not linked to this team"
	case githubmirror.RepoNotEligible:
		return "the server will not mirror " + subject
	default:
		return fmt.Sprintf("the server answered %q for %s", status, subject)
	}
}

// summarizeRelayError collapses an error to one bounded line. Whitespace runs
// (including newlines from a quoted response body) become single spaces.
func summarizeRelayError(s string) string {
	line := strings.Join(strings.Fields(s), " ")
	if runes := []rune(line); len(runes) > githubMirrorMaxErrorLen {
		line = string(runes[:githubMirrorMaxErrorLen]) + "…"
	}
	return line
}

// joinMirrorDetail joins the non-empty parts of a detail line.
func joinMirrorDetail(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "; ")
}

// mirrorAgo renders how long before now t was, e.g. "3d ago".
func mirrorAgo(now, t time.Time) string {
	return formatDurationRough(now.Sub(t)) + " ago"
}

// formatMirrorTime renders an absolute time in UTC so a report reads the same
// on every machine it is pasted into.
func formatMirrorTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04 UTC")
}
