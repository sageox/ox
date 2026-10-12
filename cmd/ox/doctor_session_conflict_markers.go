package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/sessionsummary"
)

// CheckSlugSessionConflictMarkers repairs session files committed with git conflict markers, e.g.
// an autostash pop conflict swept into an `Update sessions` commit. Unpushed markers block every
// push; markers already pushed make the session unreadable on every teammate's clone.
// merge-conflicts and ledger-unmerged-paths read the index; this reads committed content.
const CheckSlugSessionConflictMarkers = "session-conflict-markers"

const conflictMarkersCheckName = "session conflict markers"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugSessionConflictMarkers,
		Name:     conflictMarkersCheckName,
		Category: "Sessions",
		// Suggested: the repair adds one commit, taking the version upstream already holds
		// (or one side of the conflict) and never rewriting an existing commit.
		FixLevel:    FixLevelSuggested,
		Description: "Resolves conflict markers committed to Ledger session files, pushed or not",
		Run:         checkSessionConflictMarkers,
	})
}

// conflictMarkerReport is the outcome of one scan (and repair) of the Ledger's HEAD.
type conflictMarkerReport struct {
	Marked []string // session files whose HEAD content holds conflict markers
	// Pushed is the subset upstream holds byte-for-byte: already on every teammate's clone.
	Pushed       []string
	Resolved     []string                // rewritten in the new commit
	Unrepairable []pointerRestoreFailure // no clean version could be produced; left untouched
	Committed    bool
	// Remaining is what the push validator still reports after the repair.
	Remaining error
}

func checkSessionConflictMarkers(fix bool) checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(conflictMarkersCheckName, "no ledger found", "")
	}
	return runSessionConflictMarkers(ledgerPath, fix)
}

// runSessionConflictMarkers is the check body for one resolved Ledger.
func runSessionConflictMarkers(ledgerPath string, fix bool) checkResult {
	fixCommand := "ox doctor --fix-slug=" + CheckSlugSessionConflictMarkers
	report, err := resolveCommittedConflictMarkers(context.Background(), ledgerPath, fix)
	switch {
	case errors.Is(err, errNoUpstream):
		return SkippedCheck(conflictMarkersCheckName, "ledger has no upstream branch", "")
	case gitutil.IsRepoLockBusy(err):
		return SkippedCheck(conflictMarkersCheckName, "ledger busy with another ox process", "Rerun `"+fixCommand+"` in a moment")
	case err != nil:
		return FailedCheck(conflictMarkersCheckName, "could not inspect committed session files", err.Error())
	}

	if len(report.Marked) == 0 {
		return PassedCheck(conflictMarkersCheckName, "no conflict markers in session files")
	}
	if !fix {
		return FailedCheck(conflictMarkersCheckName, markedSummary(report),
			fmt.Sprintf("Run `%s` to resolve them (adds one commit)", fixCommand))
	}
	if len(report.Unrepairable) > 0 {
		var detail strings.Builder
		fmt.Fprintf(&detail, "resolved %d; %d cannot be resolved automatically:", len(report.Resolved), len(report.Unrepairable))
		for _, failure := range report.Unrepairable {
			fmt.Fprintf(&detail, "\n       %s: %s", failure.Path, failure.Reason)
		}
		fmt.Fprintf(&detail, "\n       Fix those files by hand, commit, then rerun `%s`.", fixCommand)
		return FailedCheck(conflictMarkersCheckName, "some session files could not be resolved", detail.String())
	}
	if report.Remaining != nil {
		return FailedCheck(conflictMarkersCheckName, "resolved markers but the push validator still refuses the tip", report.Remaining.Error())
	}
	return PassedCheck(conflictMarkersCheckName, fmt.Sprintf("resolved conflict markers in %d session file(s)", len(report.Resolved)))
}

// markedSummary names what the markers cost: a pushed file is unreadable for the whole team, an
// unpushed one blocks this clone's pushes.
func markedSummary(report conflictMarkerReport) string {
	unpushed := len(report.Marked) - len(report.Pushed)
	if len(report.Pushed) == 0 {
		return fmt.Sprintf("%d session file(s) in unpushed commits hold conflict markers; pushes are refused", unpushed)
	}
	msg := fmt.Sprintf("%d session file(s) hold conflict markers; %d already pushed, unreadable on every teammate's clone", len(report.Marked), len(report.Pushed))
	if unpushed > 0 {
		msg += fmt.Sprintf("; %d unpushed, pushes are refused", unpushed)
	}
	return msg
}

// resolveCommittedConflictMarkers finds session files whose HEAD content holds conflict markers:
// any file @{u}..HEAD changes, plus every sessions/*/meta.json HEAD holds, which covers markers
// already pushed. With fix, each is replaced by the version upstream holds when that is clean, else
// by one side of the conflict (see resolveMarkedFile), and the result is committed once through
// CommitLedgerSnapshot with explicit pathspecs. Existing commits are never amended or rewritten,
// so the replaced bytes stay in history; the commit message names each replaced blob.
func resolveCommittedConflictMarkers(ctx context.Context, ledgerPath string, fix bool) (conflictMarkerReport, error) {
	var report conflictMarkerReport
	upstream, err := ledgerUpstream(ctx, ledgerPath)
	if err != nil {
		return report, err
	}
	marked := map[string][]byte{}
	unpushed := map[string]bool{}
	err = sessionBlobsAhead(ctx, ledgerPath, upstream, func(path string, blob []byte) error {
		unpushed[path] = true
		if gitutil.HasConflictMarkersBytes(blob) {
			marked[path] = blob
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	// markers already pushed never appear in @{u}..HEAD
	err = markedSessionMetaAt(ctx, ledgerPath, "HEAD", func(path string, blob []byte) {
		marked[path] = blob
	})
	if err != nil {
		return report, err
	}
	for path := range marked {
		report.Marked = append(report.Marked, path)
		if !unpushed[path] {
			report.Pushed = append(report.Pushed, path)
		}
	}
	slices.Sort(report.Marked)
	slices.Sort(report.Pushed)
	if len(marked) == 0 || !fix {
		return report, nil
	}

	err = gitutil.WithRepoLock(ctx, ledgerPath, func() error {
		for _, path := range report.Marked {
			if reason := resolveMarkedFile(ctx, ledgerPath, upstream, path, marked[path]); reason != "" {
				report.Unrepairable = append(report.Unrepairable, pointerRestoreFailure{Path: path, Reason: reason})
				continue
			}
			report.Resolved = append(report.Resolved, path)
		}
		if len(report.Resolved) == 0 {
			return nil
		}
		msg, err := conflictRepairMessage(ctx, ledgerPath, report.Resolved)
		if err != nil {
			return err
		}
		guard := newSessionStageGuard(ledgerPath, filepath.Join(ledgerPath, "sessions"))
		if err := guard.gitPathspec(ctx, report.Resolved, "add", "--sparse"); err != nil {
			return fmt.Errorf("stage resolved session files: %w", err)
		}
		report.Committed, err = gitutil.CommitLedgerSnapshot(ctx, ledgerPath, msg, report.Resolved...)
		if err != nil {
			return fmt.Errorf("commit resolved session files: %w", err)
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	// the same validation the push path runs, so a clean result proves the push is unblocked
	report.Remaining = lfs.ValidateUnpushedTip(ctx, ledgerPath, upstream)
	return report, nil
}

// markedSessionMetaAt calls visit for every sessions/*/meta.json that rev holds with conflict
// markers. It reads the commit, not the working tree, so sparse checkout does not hide a file.
func markedSessionMetaAt(ctx context.Context, ledgerPath, rev string, visit func(path string, blob []byte)) error {
	// git grep is only a prefilter; HasConflictMarkersBytes decides
	out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "grep", "-l", "-z", "-E", "^(<<<<<<<|>>>>>>>)", rev, "--", "sessions/*/meta.json").Output()
	if err != nil {
		// exit 1 with nothing on stderr is "no match"; anything else means the tree was not read
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(exitErr.Stderr) == 0 {
			return nil
		}
		return fmt.Errorf("search %s session metadata for conflict markers: %w", rev, err)
	}
	for _, name := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		path := strings.TrimPrefix(name, rev+":")
		blob, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "cat-file", "blob", rev+":"+path).Output()
		if err != nil {
			return fmt.Errorf("read %s:%s: %w", rev, path, err)
		}
		if gitutil.HasConflictMarkersBytes(blob) {
			visit(path, blob)
		}
	}
	return nil
}

// conflictRepairMessage names the blob each resolved path held at HEAD, so the replaced bytes are
// one `git cat-file -p <blob>` away.
func conflictRepairMessage(ctx context.Context, ledgerPath string, paths []string) (string, error) {
	revs := make([]string, len(paths))
	for i, path := range paths {
		revs[i] = "HEAD:" + path
	}
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", ledgerPath, "rev-parse"}, revs...)...).Output()
	if err != nil {
		return "", fmt.Errorf("read replaced blob ids: %w", err)
	}
	blobs := strings.Fields(string(out))
	if len(blobs) != len(paths) {
		return "", fmt.Errorf("read replaced blob ids: got %d for %d paths", len(blobs), len(paths))
	}
	var msg strings.Builder
	fmt.Fprintf(&msg, "doctor: resolve committed conflict markers in %d session files\n\nReplaced blobs:\n", len(paths))
	for i, path := range paths {
		fmt.Fprintf(&msg, "%s %s\n", blobs[i], path)
	}
	return msg.String(), nil
}

// resolveMarkedFile rewrites one working-tree file to its clean version, or returns the reason
// it cannot. The file is touched only when it still holds exactly the committed bytes.
//
// The version upstream holds wins when it is clean. Otherwise a meta.json takes the side
// pickMetaSide chooses, and any other file keeps its "Updated upstream" side.
func resolveMarkedFile(ctx context.Context, ledgerPath, upstream, path string, committed []byte) string {
	abs := filepath.Join(ledgerPath, filepath.FromSlash(path))
	if working, err := os.ReadFile(abs); err != nil || !bytes.Equal(working, committed) {
		return "working copy differs from the committed content (uncommitted local edit); commit or discard it first"
	}
	isMeta := strings.HasSuffix(path, "/meta.json")
	clean, ok := upstreamCleanContent(ctx, ledgerPath, upstream, path)
	if !ok {
		upstreamSide, stashSide, balanced := conflictSides(committed)
		if !balanced {
			return "conflict markers are not a balanced Updated upstream / Stashed changes block"
		}
		clean = upstreamSide
		if isMeta {
			if clean, ok = pickMetaSide(path, upstreamSide, stashSide); !ok {
				return "neither side of the conflict is a valid meta.json"
			}
		}
	}
	if gitutil.HasConflictMarkersBytes(clean) {
		return "still holds a conflict marker after resolving (an unlabeled or crossed marker line)"
	}
	if isMeta {
		if _, err := lfs.ParseSessionMeta(clean); err != nil {
			return fmt.Sprintf("meta.json is not readable session metadata after resolving the conflict: %v", err)
		}
	}
	if err := os.WriteFile(abs, clean, 0o644); err != nil {
		return fmt.Sprintf("cannot write resolved file: %v", err)
	}
	return ""
}

// pickMetaSide chooses between the two readings of a marked meta.json. A side qualifies only when
// both the commit validator and the daemon's reader accept it, so a repaired session is readable.
// Between two, the side whose summary state is worth keeping wins (summaryRank). Within one state
// the higher summary_attempts wins: the counter only grows until a repair or re-arm resets it, and
// those also change the state. A tie goes to the stash side, the local write the Ledger's rebase
// policy also keeps. The choice depends only on the bytes, so every clone that repairs the same
// file writes the same result and concurrent repairs merge cleanly.
func pickMetaSide(path string, upstreamSide, stashSide []byte) ([]byte, bool) {
	up, stash := readableMeta(path, upstreamSide), readableMeta(path, stashSide)
	switch {
	case up != nil && (stash == nil || outranks(up, stash)):
		return upstreamSide, true
	case stash != nil:
		return stashSide, true
	}
	return nil, false
}

// readableMeta parses one candidate, or returns nil when the commit validator or the daemon's
// reader would refuse it.
func readableMeta(path string, data []byte) *lfs.SessionMeta {
	if gitutil.ValidateLedgerBlob(path, data) != nil {
		return nil
	}
	meta, err := lfs.ParseSessionMeta(data)
	if err != nil {
		return nil
	}
	return meta
}

func outranks(a, b *lfs.SessionMeta) bool {
	if rankA, rankB := summaryRank(a), summaryRank(b); rankA != rankB {
		return rankA > rankB
	}
	return a.SummaryAttempts > b.SummaryAttempts
}

// summaryRank orders summary states by what keeping them preserves: a finished summary, then a
// session still eligible for summarization (including one re-armed by a repair), then a failed
// attempt, then the terminal state that stops retries. An empty status is pre-status metadata,
// finished when it carries a summary.
func summaryRank(meta *lfs.SessionMeta) int {
	switch status := meta.SummaryStatus; {
	case status == sessionsummary.SummaryStatusOK, status == "" && strings.TrimSpace(meta.Summary) != "":
		return 3
	case status == "", status == sessionsummary.SummaryStatusPending:
		return 2
	case status == sessionsummary.SummaryStatusUnrecoverable:
		return 0
	}
	return 1 // failed_validation, or a status this build does not know
}

// upstreamCleanContent returns the marker-free version the upstream branch holds at path.
func upstreamCleanContent(ctx context.Context, ledgerPath, upstream, path string) ([]byte, bool) {
	out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "show", upstream+":"+path).Output()
	if err != nil || gitutil.HasConflictMarkersBytes(out) {
		return nil, false
	}
	return out, true
}

// conflictSides reads a file with conflict blocks two ways: every block resolved to its first
// ("Updated upstream") half, and every block resolved to its second ("Stashed changes") half. A
// diff3 base section belongs to neither. Blocks may nest, as when a second stash pop lands on a
// file that already held markers; a line survives into a reading only when every enclosing block
// selects the half it sits in. It reports false on an unbalanced or orphaned marker.
func conflictSides(data []byte) (upstreamSide, stashSide []byte, ok bool) {
	const (
		firstHalf = iota
		baseHalf  // diff3 and zdiff3 add a "||||||| base" section between the two halves
		secondHalf
	)
	var open []int // the half each enclosing block is in, innermost last
	inAll := func(half int) bool {
		for _, h := range open {
			if h != half {
				return false
			}
		}
		return true
	}
	var first, second strings.Builder
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trimmed := strings.TrimRight(line, "\r\n")
		top := -1
		if len(open) > 0 {
			top = open[len(open)-1]
		}
		switch {
		case strings.HasPrefix(line, "<<<<<<< "):
			open = append(open, firstHalf)
		case strings.HasPrefix(line, "||||||| ") && top == firstHalf:
			open[len(open)-1] = baseHalf
		case trimmed == "=======" && (top == firstHalf || top == baseHalf):
			open[len(open)-1] = secondHalf
		case strings.HasPrefix(line, ">>>>>>> "):
			if top != secondHalf {
				return nil, nil, false
			}
			open = open[:len(open)-1]
		default:
			if inAll(firstHalf) {
				first.WriteString(line)
			}
			if inAll(secondHalf) {
				second.WriteString(line)
			}
		}
	}
	if len(open) > 0 {
		return nil, nil, false
	}
	return []byte(first.String()), []byte(second.String()), true
}
