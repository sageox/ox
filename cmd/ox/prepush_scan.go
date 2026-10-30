package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/perf"
	"github.com/sageox/ox/internal/session"
)

// PrePushScanResult collects the credential findings the scanner detected
// in the commit range that pushLedger is about to publish to the cloud.
type PrePushScanResult struct {
	Findings []PrePushFinding
	// FilesScanned counts how many distinct paths in the push range were
	// inspected. Useful for the "scanned N files, found 0 secrets" message
	// the CLI can print to give users confidence the gate ran.
	FilesScanned int
}

// PrePushFinding is a single credential pattern hit in a specific file.
// The matched bytes themselves are NEVER stored or surfaced — only the
// detector name, path, and 1-indexed line number. Printing the bytes would
// just re-leak the credential into terminal scrollback / CI logs.
type PrePushFinding struct {
	Detector string // pattern name, e.g. "aws_access_key"
	Path     string // path relative to ledger root
	Line     int    // 1-indexed line number
}

// prePushScannerScopePrefixes restricts the gate to ledger paths the
// recovery tooling can actually fix. As of ox-cqdo:
//
//   - sessions/      raw AI session recordings — assistant chat and tool
//     I/O. The one path where unscrubbed credentials can
//     plausibly land in a ledger from ox-controlled writes.
//     Covered by `ox session audit` / `ox session redact`.
//
// Out of scope (intentionally not gated):
//
//   - data/github/   verbatim cache of PR/Issue bytes already published on
//     GitHub. Replicating them into the ledger is the same
//     exposure surface as ingesting the PR at all; rewriting
//     them at write time produced a false-positive class
//     that blocked routine pushes (the regression that drove
//     this fix).
//   - kb/            user-curated knowledge bubbles. User-authored content
//     is the user's responsibility; not an ox write path.
//   - team-context/  same — user-edited markdown.
//
// If you widen this list, also widen the recovery surface in
// FormatPrePushFindings. The test
// TestFormatPrePushFindings_RecoveryMatchesScannerScope pins that contract.
var prePushScannerScopePrefixes = []string{
	"sessions/",
}

// inPrePushScannerScope returns true iff rel (a forward-slash-relative path
// from the ledger root) sits inside one of prePushScannerScopePrefixes.
// Paths produced by `git diff --name-only` and `git ls-tree` are already
// forward-slash, so no normalization is needed.
func inPrePushScannerScope(rel string) bool {
	for _, prefix := range prePushScannerScopePrefixes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// scanPrePushForSecrets enumerates files that the next push would publish
// (between `origin/main` and `HEAD`) and runs the credential detectors
// against each file's current working-tree content.
//
// Per ox-1uss, this runs INSIDE the ox CLI's Ledger push pipeline, not as a
// global `.git/hooks/pre-push` hook — global hooks are too easy for users to
// remove or bypass with `--no-verify`. Putting the check in the ox push code
// path makes it opinionated: default behavior is refuse.
//
// Performance: O(diff size), not O(repo size). A 100 MB diff scans in under
// 2 seconds on a typical dev machine; see BenchmarkPrePushScan in the test
// file for the budget assertion.
func scanPrePushForSecrets(ctx context.Context, ledgerPath string) (*PrePushScanResult, error) {
	ctx, span := perf.Start(ctx, "secret_scan")
	defer span.End()
	// Enumerate files in the push range. The triple-dot syntax includes
	// every file touched by commits on HEAD that aren't on origin/main —
	// even if a later commit reverts them, the bytes are still in the
	// pack that would be pushed, so the scan correctly flags them.
	cmd := exec.CommandContext(ctx, "git", "-C", ledgerPath,
		"diff", "--name-only", "origin/main...HEAD")
	out, err := cmd.Output()
	if err != nil {
		// If origin/main doesn't exist yet (new ledger, never pushed), fall
		// back to scanning every tracked file on HEAD — there's no upstream
		// reference, so anything we'd push is "new".
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && strings.Contains(string(exitErr.Stderr), "unknown revision") {
			slog.Info("pre-push scan: origin/main not found, scanning all tracked files")
			return scanAllTrackedFiles(ctx, ledgerPath)
		}
		return nil, fmt.Errorf("git diff --name-only: %w", err)
	}

	paths := strings.Split(strings.TrimSpace(string(out)), "\n")
	// trim empty
	filtered := paths[:0]
	for _, p := range paths {
		if p != "" {
			filtered = append(filtered, p)
		}
	}
	return scanPaths(ledgerPath, filtered)
}

// scanAllTrackedFiles handles the "no upstream yet" case by enumerating
// every tracked file on HEAD instead.
func scanAllTrackedFiles(ctx context.Context, ledgerPath string) (*PrePushScanResult, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", ledgerPath, "ls-tree", "-r", "--name-only", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree HEAD: %w", err)
	}
	paths := strings.Split(strings.TrimSpace(string(out)), "\n")
	return scanPaths(ledgerPath, paths)
}

// scanPaths runs the redactor against the working-tree content of every
// path in paths, recording detector matches as PrePushFindings. Only paths
// outside sessions/ and legitimately deleted files are skipped. Extensions and
// scan limits cannot authorize uninspected session content for publication.
// File size is not a limit: long recordings are streamed line by line, and a
// scan failure holds publication rather than skipping the file.
func scanPaths(ledgerPath string, paths []string) (*PrePushScanResult, error) {
	redactor := session.NewRedactor()
	result := &PrePushScanResult{}

	for _, rel := range paths {
		if rel == "" {
			continue
		}
		if !inPrePushScannerScope(rel) {
			continue
		}
		abs := filepath.Join(ledgerPath, rel)
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				// The immutable publication scan still checks deleted Git blobs.
				continue
			}
			return nil, fmt.Errorf("inspect session path %s: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("session path %s is not a regular file", rel)
		}
		result.FilesScanned++

		if err := scanFileForSecrets(redactor, abs, rel, result); err != nil {
			return nil, fmt.Errorf("scan %s: %w", rel, err)
		}
	}

	// Stable ordering — detector then path then line — so error output is
	// reproducible and easy to diff against a previous run.
	sort.Slice(result.Findings, func(i, j int) bool {
		a, b := result.Findings[i], result.Findings[j]
		if a.Detector != b.Detector {
			return a.Detector < b.Detector
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})

	return result, nil
}

// scanFileForSecrets reads a file line-by-line so the line number recorded
// in the finding is accurate, and so we don't have to load the whole file
// into memory at once.
func scanFileForSecrets(r *session.Redactor, abs, rel string, result *PrePushScanResult) error {
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Memory is bounded by the longest line, not the file, so a recording of any
	// length scans. A line past rawJSONLMaxLineBytes fails the scan closed
	// (bufio.ErrTooLong) instead of being skipped.
	scanner.Buffer(make([]byte, 64*1024), rawJSONLMaxLineBytes)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		hits := r.ScanForSecrets(line)
		for _, h := range hits {
			result.Findings = append(result.Findings, PrePushFinding{
				Detector: h,
				Path:     rel,
				Line:     lineNo,
			})
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// FormatPrePushFindings builds the human-facing error message. Deliberately
// verbose: the user is reading it because their push was just refused, so a
// terse single-line error wastes the moment for showing them how to recover.
// Never includes the matched bytes — surfacing them re-leaks the credential
// into terminal scrollback, CI logs, and chat-bot transcripts.
func FormatPrePushFindings(r *PrePushScanResult) string {
	if r == nil || len(r.Findings) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Push refused: %d credential pattern(s) detected in %d file(s):\n\n",
		len(r.Findings), countDistinctPaths(r.Findings))
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "  %s\n    %s:%d\n", f.Detector, f.Path, f.Line)
	}
	b.WriteString("\nTo proceed:\n")
	b.WriteString("  1. Inspect the flagged paths and remove or redact the credential.\n")
	b.WriteString("     Run `ox session audit` for a per-line audit, then `ox session redact` for interactive cleanup.\n")
	b.WriteString("  2. Amend the holding commit, then retry the push.\n")
	b.WriteString("  3. Retry only after the session content passes verification.\n")
	return b.String()
}

func countDistinctPaths(findings []PrePushFinding) int {
	seen := map[string]struct{}{}
	for _, f := range findings {
		seen[f.Path] = struct{}{}
	}
	return len(seen)
}

// runPrePushSecretGate repairs or quarantines current session artifacts.
// PushWithRetry separately validates the immutable objects before publication.
// The caller must hold the repo lock: repairs may stage and amend HEAD.
func runPrePushSecretGate(ctx context.Context, ledgerPath string) error {
	return runSessionSecretGate(ctx, ledgerPath, func() (*PrePushScanResult, error) {
		return scanPrePushForSecrets(ctx, ledgerPath)
	}, true)
}

// prepareSessionUpload runs the same redaction and quarantine policy on real
// content before LFS computes its OIDs. This pass removes quarantined files
// from the index but must not amend an unrelated holding commit. Scan errors
// keep this session pending without blocking unrelated ledger pushes.
func prepareSessionUpload(ctx context.Context, ledgerPath, sessionName string) error {
	paths := make([]string, 0, len(lfs.ContentFiles))
	for _, name := range lfs.ContentFiles {
		paths = append(paths, filepath.ToSlash(filepath.Join("sessions", sessionName, name)))
	}
	return runSessionSecretGate(ctx, ledgerPath, func() (*PrePushScanResult, error) {
		return scanPaths(ledgerPath, paths)
	}, false)
}

func runSessionSecretGate(ctx context.Context, ledgerPath string, scan func() (*PrePushScanResult, error), amendCommit bool) error {
	result, err := scan()
	if err != nil {
		return fmt.Errorf("scan session content for secrets: %w", err)
	}
	if result == nil {
		return errors.New("session secret scan returned no verification result")
	}
	if len(result.Findings) == 0 {
		slog.Debug("pre-push secret gate: clean", "files_scanned", result.FilesScanned)
		return nil
	}

	// Per-file rewrite failures remain findings for quarantine. A metadata or
	// amend failure must hold publication even if the working bytes are clean:
	// the original commit may still contain the unredacted content.
	auto, redactErr := autoRedactSessionFindings(ctx, ledgerPath, result, amendCommit)
	if redactErr != nil {
		return fmt.Errorf("auto-redact session content: %w", redactErr)
	}
	rescan, err := scan()
	if err != nil {
		return fmt.Errorf("rescan session content for secrets: %w", err)
	}
	if rescan == nil {
		return errors.New("session secret rescan returned no verification result")
	}
	if len(rescan.Findings) == 0 {
		if auto != nil && len(auto.FixedPaths) > 0 {
			fmt.Fprintf(os.Stderr,
				"ox pre-push: auto-redacted %d file(s) across %d session(s) before push (catalog %s).\n",
				len(auto.FixedPaths), len(auto.RedactedSessions), auto.CatalogVersion)
		}
		return nil
	}
	q, err := quarantineUnredactableFindings(ctx, ledgerPath, rescan.Findings, amendCommit)
	emitQuarantineWarning(os.Stderr, auto, q, rescan)
	if err != nil {
		return fmt.Errorf("prepare session quarantine: %w", err)
	}
	verified, err := scan()
	if err != nil {
		return fmt.Errorf("verify session quarantine: %w", err)
	}
	if verified == nil {
		return errors.New("session quarantine scan returned no verification result")
	}
	if len(verified.Findings) != 0 {
		return fmt.Errorf("%s", FormatPrePushFindings(verified))
	}
	return nil
}

// emitQuarantineWarning prints the user-facing summary of what was
// auto-redacted, what was quarantined, and what to do next. Centralized
// so the format stays consistent if call sites change.
func emitQuarantineWarning(w *os.File, auto *autoRedactResult, q *quarantineResult, rescan *PrePushScanResult) {
	var b strings.Builder
	b.WriteString("\nox pre-push: credential findings detected in session content.\n")
	if auto != nil && len(auto.FixedPaths) > 0 {
		fmt.Fprintf(&b, "  Auto-redacted in place via canonical chokepoint: %d file(s) across %d session(s).\n",
			len(auto.FixedPaths), len(auto.RedactedSessions))
	}
	if q != nil && len(q.QuarantinedRels) > 0 {
		fmt.Fprintf(&b, "  Quarantined (preserved on disk, dropped from this push): %d file(s).\n",
			len(q.QuarantinedRels))
		for _, p := range q.QuarantinedRels {
			fmt.Fprintf(&b, "    %s\n", p)
		}
		b.WriteString("  Bytes preserved under .sageox/cache/quarantine/<session>/.\n")
		b.WriteString("  Run `ox doctor` for next steps. Publication resumes only after verification passes.\n")
	} else if rescan != nil && len(rescan.Findings) > 0 {
		b.WriteString("  Some findings could not be quarantined; publication is blocked pending repair.\n")
	}
	fmt.Fprint(w, b.String())
}
