package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
)

// CheckSlugSessionConflictMarkers repairs session files that unpushed Ledger commits hold with
// committed git conflict markers, e.g. an autostash pop conflict swept into an `Update sessions`
// commit. merge-conflicts and ledger-unmerged-paths read the index; this reads committed content.
const CheckSlugSessionConflictMarkers = "session-conflict-markers"

const conflictMarkersCheckName = "session conflict markers"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugSessionConflictMarkers,
		Name:     conflictMarkersCheckName,
		Category: "Sessions",
		// Suggested: the repair adds one commit, taking the version upstream already holds
		// (or the "Updated upstream" side) and never rewriting an existing commit.
		FixLevel:    FixLevelSuggested,
		Description: "Resolves conflict markers that unpushed Ledger commits hold in session files",
		Run:         checkSessionConflictMarkers,
	})
}

// conflictMarkerReport is the outcome of one scan (and repair) of @{u}..HEAD.
type conflictMarkerReport struct {
	Marked       []string                // session files whose HEAD content holds conflict markers
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
	fixCommand := "ox doctor --fix-slug=" + CheckSlugSessionConflictMarkers
	report, err := resolveCommittedConflictMarkers(context.Background(), ledgerPath, fix)
	switch {
	case errors.Is(err, errNoUpstream):
		return SkippedCheck(conflictMarkersCheckName, "ledger has no upstream branch", "")
	case gitutil.IsRepoLockBusy(err):
		return SkippedCheck(conflictMarkersCheckName, "ledger busy with another ox process", "Rerun `"+fixCommand+"` in a moment")
	case err != nil:
		return FailedCheck(conflictMarkersCheckName, "could not inspect unpushed session commits", err.Error())
	}

	if len(report.Marked) == 0 {
		return PassedCheck(conflictMarkersCheckName, "no conflict markers in unpushed session files")
	}
	if !fix {
		return FailedCheck(conflictMarkersCheckName,
			fmt.Sprintf("%d session file(s) in unpushed commits hold conflict markers; pushes are refused", len(report.Marked)),
			fmt.Sprintf("Run `%s` to resolve them (adds one commit)", fixCommand))
	}
	if len(report.Unrepairable) > 0 {
		var detail strings.Builder
		fmt.Fprintf(&detail, "resolved %d; %d cannot be resolved automatically and still block the push:", len(report.Resolved), len(report.Unrepairable))
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

// resolveCommittedConflictMarkers scans @{u}..HEAD for session files whose HEAD content holds
// conflict markers. With fix, each is replaced by the version upstream holds, else by its
// "Updated upstream" side (the autostash convention), and the result is committed once through
// CommitLedgerSnapshot with explicit pathspecs. A meta.json must parse as JSON or it is reported
// and left untouched. Existing commits are never amended or rewritten.
func resolveCommittedConflictMarkers(ctx context.Context, ledgerPath string, fix bool) (conflictMarkerReport, error) {
	var report conflictMarkerReport
	upstream, err := ledgerUpstream(ctx, ledgerPath)
	if err != nil {
		return report, err
	}
	marked := map[string][]byte{}
	err = sessionBlobsAhead(ctx, ledgerPath, upstream, func(path string, blob []byte) error {
		if gitutil.HasConflictMarkersBytes(blob) {
			marked[path] = blob
			report.Marked = append(report.Marked, path)
		}
		return nil
	})
	if err != nil || len(marked) == 0 || !fix {
		return report, err
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
		guard := newSessionStageGuard(ledgerPath, filepath.Join(ledgerPath, "sessions"))
		if err := guard.gitPathspec(ctx, report.Resolved, "add", "--sparse"); err != nil {
			return fmt.Errorf("stage resolved session files: %w", err)
		}
		msg := fmt.Sprintf("doctor: resolve committed conflict markers in %d session files", len(report.Resolved))
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

// resolveMarkedFile rewrites one working-tree file to its clean version, or returns the reason
// it cannot. The file is touched only when it still holds exactly the committed bytes.
func resolveMarkedFile(ctx context.Context, ledgerPath, upstream, path string, committed []byte) string {
	abs := filepath.Join(ledgerPath, filepath.FromSlash(path))
	if working, err := os.ReadFile(abs); err != nil || !bytes.Equal(working, committed) {
		return "working copy differs from the committed content (uncommitted local edit); commit or discard it first"
	}
	clean, ok := upstreamCleanContent(ctx, ledgerPath, upstream, path)
	if !ok {
		if clean, ok = keepUpstreamSide(committed); !ok {
			return "conflict markers are not a balanced Updated upstream / Stashed changes block"
		}
	}
	if strings.HasSuffix(path, "/meta.json") && !json.Valid(clean) {
		return "meta.json is not valid JSON after dropping the stash side"
	}
	if err := os.WriteFile(abs, clean, 0o644); err != nil {
		return fmt.Sprintf("cannot write resolved file: %v", err)
	}
	return ""
}

// upstreamCleanContent returns the marker-free version the upstream branch holds at path.
func upstreamCleanContent(ctx context.Context, ledgerPath, upstream, path string) ([]byte, bool) {
	out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "show", upstream+":"+path).Output()
	if err != nil || gitutil.HasConflictMarkersBytes(out) {
		return nil, false
	}
	return out, true
}

// keepUpstreamSide drops the stash half (and any diff3 base section) and the marker lines, keeping
// the "Updated upstream" half. It reports false on an unbalanced or orphaned marker.
func keepUpstreamSide(data []byte) ([]byte, bool) {
	const (
		outside = iota
		upstreamSide
		baseSide // diff3 and zdiff3 add a "||||||| base" section between the two halves
		stashSide
	)
	state := outside
	var kept []string
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "<<<<<<< "):
			if state != outside {
				return nil, false
			}
			state = upstreamSide
		case strings.HasPrefix(line, "||||||| ") && state == upstreamSide:
			state = baseSide
		case trimmed == "=======" && (state == upstreamSide || state == baseSide):
			state = stashSide
		case strings.HasPrefix(line, ">>>>>>> "):
			if state != stashSide {
				return nil, false
			}
			state = outside
		case state == outside || state == upstreamSide:
			kept = append(kept, line)
		}
	}
	if state != outside {
		return nil, false
	}
	return []byte(strings.Join(kept, "")), true
}
