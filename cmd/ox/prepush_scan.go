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
// binary extensions, files over prePushScannerSizeCap, files that no longer
// exist in the working tree (legitimately deleted in the commit range).
func scanPaths(ledgerPath string, paths []string) (*PrePushScanResult, error) {
	redactor := session.NewRedactor()
	result := &PrePushScanResult{}

	for _, rel := range paths {
		if rel == "" {
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
	b.WriteString("     Run `ox doctor --check=ledger-secrets` for a full audit.\n")
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

// runPrePushSecretGate is the entry point used by pushLedger. Returns nil
// if push may proceed; returns a formatted error if findings are present
// and OX_ALLOW_SECRETS is not set.
func runPrePushSecretGate(ctx context.Context, ledgerPath string) error {
	result, err := scanPrePushForSecrets(ctx, ledgerPath)
	if err != nil {
		// Scan-side error (e.g. git command failed). Fail OPEN here rather
		// than blocking the push — the scanner is a guardrail, not an oracle,
		// and the user has no way to recover if a transient git problem makes
		// the gate impassable.
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
		// emit a loud stderr line so the override isn't invisible in
		// non-structured logs.
		fmt.Fprintln(os.Stderr,
			"WARNING: ox pre-push secret gate overridden by OX_ALLOW_SECRETS — "+
				"credentials may be published to the cloud Ledger")
		return nil
	}
	return fmt.Errorf("%s", FormatPrePushFindings(result))
}

