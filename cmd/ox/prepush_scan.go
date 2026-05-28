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

// prePushScannerSkipExts skips obvious non-text and high-volume binary types
// that the detectors would never meaningfully match. Keeps the scan O(diff)
// instead of O(diff * regex-corpus * bytes-per-binary-file).
var prePushScannerSkipExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".pdf": true, ".zip": true, ".gz": true, ".tar": true, ".tgz": true,
	".bin": true, ".woff": true, ".woff2": true, ".ttf": true, ".otf": true,
	".mp3": true, ".mp4": true, ".mov": true, ".wav": true,
}

// prePushScannerSizeCap is the maximum file size the scanner will read. Above
// this, the file is skipped with a slog.Warn — secret regexes don't usefully
// fire on large binary blobs anyway, and reading e.g. a 500MB pointer-free
// blob would defeat the perf budget. LFS pointer files are tiny so legitimate
// session content always fits.
const prePushScannerSizeCap = 8 * 1024 * 1024 // 8 MiB

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
// path in paths, recording detector matches as PrePushFindings. Skip rules:
// out-of-scope paths (see prePushScannerScopePrefixes), binary extensions,
// files over prePushScannerSizeCap, files that no longer exist in the
// working tree (legitimately deleted in the commit range).
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
		if prePushScannerSkipExts[strings.ToLower(filepath.Ext(rel))] {
			continue
		}
		abs := filepath.Join(ledgerPath, rel)
		info, err := os.Stat(abs)
		if err != nil {
			// File deleted in this push — nothing to scan.
			continue
		}
		if info.Size() > prePushScannerSizeCap {
			slog.Warn("pre-push scan: file exceeds size cap, skipping",
				"path", rel, "size", info.Size(), "cap", prePushScannerSizeCap)
			continue
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
	// allow very long JSONL lines (session entries can be 1-2 MB per line).
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

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
	b.WriteString("  3. If this is a false positive, set OX_ALLOW_SECRETS=1 to override.\n")
	b.WriteString("     The override is logged and emits a loud warning.\n")
	return b.String()
}

func countDistinctPaths(findings []PrePushFinding) int {
	seen := map[string]struct{}{}
	for _, f := range findings {
		seen[f.Path] = struct{}{}
	}
	return len(seen)
}

// prePushSecretsAllowed returns true if the user has explicitly opted out of
// the pre-push gate via OX_ALLOW_SECRETS=1. Any non-empty value other than
// "0" / "false" / "no" enables the override — we err on the side of "the user
// meant to override" because the failure mode of mis-parsing this flag is
// blocking a legitimate push, not leaking a credential.
func prePushSecretsAllowed() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("OX_ALLOW_SECRETS")))
	if v == "" {
		return false
	}
	switch v {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// runPrePushSecretGate is the entry point used by pushLedger. As of
// ox-y3ok the gate NEVER blocks the push: a single bad session must not
// hold up other sessions and unrelated commits from syncing. Instead the
// gate runs a recovery pipeline:
//
//  1. Scan (scoped to sessions/ per ox-cqdo).
//  2. Auto-redact every JSONL finding via the canonical chokepoint
//     (autoRedactSessionFindings → redactFileInPlace + meta.json
//     RedactionPass + amend). Re-scan.
//  3. Anything still remaining is quarantined: the file is atomically
//     moved to <ledger>/.sageox/cache/quarantine/ (preserved, just
//     outside the daemon's session-finalize surface) and its path is
//     carved out of the holding commit. A debt marker is written under
//     <ledger>/.sageox/cache/redaction-debt/ so `ox doctor` can surface
//     the persistent state.
//
// Always returns nil. Scan errors and recovery errors are logged and
// the push proceeds; making the gate impassable is a worse outcome than
// publishing whatever the chokepoint missed (the same content already
// hit the chokepoint at write time).
func runPrePushSecretGate(ctx context.Context, ledgerPath string) error {
	result, err := scanPrePushForSecrets(ctx, ledgerPath)
	if err != nil {
		slog.Warn("pre-push secret gate: scan failed, allowing push", "error", err)
		return nil
	}
	if len(result.Findings) == 0 {
		slog.Debug("pre-push secret gate: clean", "files_scanned", result.FilesScanned)
		return nil
	}
	if prePushSecretsAllowed() {
		slog.Warn("pre-push secret gate: OVERRIDDEN by OX_ALLOW_SECRETS",
			"findings", len(result.Findings),
			"files", countDistinctPaths(result.Findings))
		fmt.Fprintln(os.Stderr,
			"WARNING: ox pre-push secret gate overridden by OX_ALLOW_SECRETS — "+
				"credentials may be published to the cloud Ledger")
		return nil
	}

	// Try the canonical chokepoint first; it can clear anything the
	// scanner found (they share a detector catalog).
	auto, redactErr := autoRedactSessionFindings(ctx, ledgerPath, result)
	if redactErr != nil {
		slog.Warn("pre-push secret gate: auto-redact failed; will quarantine remainder",
			"error", redactErr)
	}

	// Re-scan post-redact. Anything left is either non-JSONL or hit a
	// per-file error in the redact pass.
	rescan, rescanErr := scanPrePushForSecrets(ctx, ledgerPath)
	if rescanErr != nil {
		slog.Warn("pre-push secret gate: rescan after auto-redact failed; allowing push",
			"error", rescanErr)
		return nil
	}
	if len(rescan.Findings) == 0 {
		if auto != nil && len(auto.FixedPaths) > 0 {
			fmt.Fprintf(os.Stderr,
				"ox pre-push: auto-redacted %d file(s) across %d session(s) before push (catalog %s).\n",
				len(auto.FixedPaths), len(auto.RedactedSessions), auto.CatalogVersion)
		}
		return nil
	}

	// Quarantine the remainder so the rest of the push can proceed.
	q, qErr := quarantineUnredactableFindings(ledgerPath, rescan.Findings)
	if qErr != nil {
		slog.Warn("pre-push secret gate: quarantine partial; push will still proceed",
			"error", qErr)
	}

	// Loud user-visible summary. Stderr (not the structured slog) so
	// `ox session stop` callers see it even when slog is at WARN level.
	emitQuarantineWarning(os.Stderr, auto, q, rescan)
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
		b.WriteString("  Run `ox doctor` for next steps. Other sessions and unrelated commits will sync normally.\n")
	} else if rescan != nil && len(rescan.Findings) > 0 {
		b.WriteString("  Some findings could not be quarantined; they remain in the push.\n")
	}
	fmt.Fprint(w, b.String())
}
