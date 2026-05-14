package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

// Pre-push auto-redact + quarantine pipeline (ox-y3ok).
//
// The pre-push secret gate (cmd/ox/prepush_scan.go::runPrePushSecretGate)
// MUST NOT block the push. This file implements the recovery path it
// runs instead:
//
//   1. autoRedactSessionFindings — for each affected session, redact
//      JSONL files in-place via the canonical chokepoint, append a
//      RedactionPass to meta.json, stage + amend the holding commit.
//      Reuses redactFileInPlace, writeRedactionPassesPerSession, and
//      stageAndAmendRedactedFiles so behavior matches `ox session redact`.
//
//   2. quarantineUnredactableFindings — for any remaining findings
//      (non-JSONL paths, IO errors), atomically move the offending file
//      to <ledger>/.sageox/cache/quarantine/<name>/<file> so the data
//      isn't lost; carve the path out of the holding commit so the rest
//      of the push proceeds; write a marker under
//      <ledger>/.sageox/cache/redaction-debt/ so `ox doctor` can surface
//      it.
//
// The daemon's session-finalize watches <ledger>/.sageox/cache/sessions/
// and <ledger>/sessions/ — the quarantine path (cache/quarantine/) is
// outside both, so anti-entropy won't re-add the quarantined file.

// redactionDebtRecord is the on-disk format for a quarantined session.
// Lives at <ledger>/.sageox/cache/redaction-debt/<session>.json.
// Recomputable in principle (re-scan finds the same bytes), persisted so
// `ox doctor` and future tooling don't have to re-hydrate every session
// to find them.
type redactionDebtRecord struct {
	SessionName     string                  `json:"session_name"`
	QuarantinedAt   time.Time               `json:"quarantined_at"`
	Reason          string                  `json:"reason"`
	Findings        []redactionDebtFinding  `json:"findings"`
	QuarantinePaths []redactionDebtLocation `json:"quarantine_paths"`
}

// redactionDebtFinding records WHAT detector fired on WHICH line.
// Never carries matched bytes (ox-zyg7).
type redactionDebtFinding struct {
	Detector string `json:"detector"`
	Filename string `json:"filename"`
	Line     int    `json:"line"`
}

// redactionDebtLocation maps the original in-place ledger-relative path
// to its current quarantine ledger-relative path. Lets the user (or a
// future `ox session accept-unredacted`) move bytes back to publish.
type redactionDebtLocation struct {
	From string `json:"from"` // sessions/<name>/<file>
	To   string `json:"to"`   // .sageox/cache/quarantine/<name>/<file>
}

// autoRedactResult summarizes one auto-redact pass.
type autoRedactResult struct {
	// RedactedSessions is the set of session names whose JSONL files
	// were rewritten through the canonical chokepoint. Their meta.json
	// carries a RedactionPass entry; the holding commit was amended.
	RedactedSessions []string
	// FixedPaths is the set of ledger-relative paths whose findings
	// were cleared by the redact pass. Subset of the original findings'
	// paths.
	FixedPaths []string
	// CatalogVersion + CatalogHash identify the rule set that did the
	// rewrite. Persisted in meta.json via writeRedactionPassesPerSession.
	CatalogVersion string
	CatalogHash    string
}

// autoRedactSessionFindings groups findings by session, redacts each
// JSONL file in-place via the canonical chokepoint (redactFileInPlace),
// appends a RedactionPass to each affected session's meta.json, and
// stages + amends the holding commit so the next push carries the
// scrubbed bytes.
//
// Non-JSONL paths are skipped here and surface as remaining findings on
// the caller's re-scan — they get the quarantine path instead.
//
// Returns a summary so the caller can format a user-facing message.
// Errors are returned only for catastrophic failures (commit amend
// rejected, meta.json mutation failed). Per-file errors degrade
// gracefully: that file stays in the "remaining findings" bucket and
// the caller quarantines it.
func autoRedactSessionFindings(ctx context.Context, ledgerPath string, result *PrePushScanResult) (*autoRedactResult, error) {
	if result == nil || len(result.Findings) == 0 {
		return &autoRedactResult{}, nil
	}

	redactor := session.NewRedactor()
	out := &autoRedactResult{
		CatalogVersion: redactor.CatalogVersion(),
		CatalogHash:    redactor.CatalogHash(),
	}

	// Group findings by (session, file). For each (session, file) we
	// rewrite the file once; for each session we append one meta entry.
	type fileKey struct{ session, filename, rel string }
	byFile := map[fileKey][]PrePushFinding{}
	for _, f := range result.Findings {
		sess, fname, ok := splitSessionPath(f.Path)
		if !ok {
			// Not a session path — shouldn't happen post-cqdo scope fix,
			// but defense in depth: leave it for the quarantine pass.
			continue
		}
		key := fileKey{session: sess, filename: fname, rel: f.Path}
		byFile[key] = append(byFile[key], f)
	}

	bySession := map[string][]lfs.RedactionEntry{}
	var redactedRels []string

	// Deterministic order so failures are reproducible.
	keys := make([]fileKey, 0, len(byFile))
	for k := range byFile {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].session != keys[j].session {
			return keys[i].session < keys[j].session
		}
		return keys[i].filename < keys[j].filename
	})

	for _, k := range keys {
		if !isJSONLPath(k.filename) {
			// Only JSONL goes through the canonical chokepoint today.
			// Anything else falls through to quarantine.
			continue
		}
		abs := filepath.Join(ledgerPath, k.rel)
		if err := redactFileInPlace(abs, redactor); err != nil {
			slog.Warn("pre-push auto-redact: file rewrite failed; will quarantine",
				"path", k.rel, "error", err)
			continue
		}
		redactedRels = append(redactedRels, k.rel)
		for _, f := range byFile[k] {
			bySession[k.session] = append(bySession[k.session], lfs.RedactionEntry{
				File:     k.filename,
				Line:     f.Line,
				Detector: f.Detector,
			})
		}
	}

	if len(bySession) == 0 {
		// Nothing redacted — caller will quarantine everything.
		return out, nil
	}

	// Persist the audit trail BEFORE staging so the amend captures it.
	metaPaths, err := writeRedactionPassesPerSession(ctx, ledgerPath,
		bySession, out.CatalogVersion, out.CatalogHash)
	if err != nil {
		return out, fmt.Errorf("write redaction passes: %w", err)
	}

	// Stage + amend in one go. Reuses the same primitive `ox session
	// redact` uses; auditors get one consistent shape regardless of
	// whether the redaction was interactive or automatic.
	byPath := map[string][]redactHistoryFinding{}
	for _, rel := range redactedRels {
		byPath[rel] = nil
	}
	for _, mp := range metaPaths {
		byPath[mp] = nil
	}
	if err := stageAndAmendRedactedFiles(ledgerPath, byPath); err != nil {
		return out, fmt.Errorf("stage/amend redacted files: %w", err)
	}

	out.FixedPaths = redactedRels
	out.RedactedSessions = sortedKeys(bySession)
	slog.Info("pre-push auto-redact: redacted in-place via canonical chokepoint",
		"sessions", out.RedactedSessions,
		"files", len(out.FixedPaths),
		"catalog_version", out.CatalogVersion)
	return out, nil
}

// quarantineResult summarizes what was carved out of the holding commit.
type quarantineResult struct {
	QuarantinedRels []string // ledger-relative ORIGINAL paths that were quarantined
	DebtMarkers     []string // ledger-relative paths to the marker .json files written
}

// quarantineUnredactableFindings handles findings that the auto-redact
// pass could not clear. For each affected file:
//
//  1. Atomically rename `<ledger>/sessions/<name>/<file>` →
//     `<ledger>/.sageox/cache/quarantine/<name>/<file>` so the bytes
//     remain on the user's machine but outside the daemon's
//     session-finalize watch surface (cache/quarantine/ is not scanned
//     by detectInDir; only cache/sessions/ and sessions/ are).
//
//  2. Drop the path from the holding commit (`git reset HEAD~ -- <path>`
//     when a parent exists, `git rm --cached --ignore-unmatch` otherwise)
//     so the next push carries the rest of the commit's changes.
//
//  3. Write a redactionDebtRecord under
//     `<ledger>/.sageox/cache/redaction-debt/<session>.json` so
//     `ox doctor` can surface the persistent state. One marker per
//     affected session aggregates all that session's quarantined files.
//
// Returns the carved-out paths + marker locations. Per-file errors are
// logged and skipped — the goal is to never block the push; partial
// quarantine is better than full block.
func quarantineUnredactableFindings(ledgerPath string, findings []PrePushFinding) (*quarantineResult, error) {
	out := &quarantineResult{}
	if len(findings) == 0 {
		return out, nil
	}

	// Group findings by session for the marker write.
	type sessionGroup struct {
		findings       []PrePushFinding
		quarantineLocs []redactionDebtLocation
	}
	groups := map[string]*sessionGroup{}
	for _, f := range findings {
		sess, _, ok := splitSessionPath(f.Path)
		if !ok {
			slog.Warn("pre-push quarantine: finding outside sessions/ scope; ignored",
				"path", f.Path)
			continue
		}
		if groups[sess] == nil {
			groups[sess] = &sessionGroup{}
		}
		groups[sess].findings = append(groups[sess].findings, f)
	}

	// Deduplicate moves per file (multiple findings per file → one move).
	seenPaths := map[string]bool{}
	for sess, g := range groups {
		for _, f := range g.findings {
			if seenPaths[f.Path] {
				continue
			}
			seenPaths[f.Path] = true

			fromRel := f.Path
			fromAbs := filepath.Join(ledgerPath, fromRel)
			// We only move the in-place ledger file (already scanned).
			// LFS pointer-bytes never match detectors, so anything we're
			// quarantining is hydrated content sitting in-place — moving
			// it is safe (the canonical state on disk for this session
			// is the LFS blob or the original local cache, depending on
			// the lifecycle stage).
			info, err := os.Stat(fromAbs)
			if err != nil {
				slog.Warn("pre-push quarantine: stat failed; skipping",
					"path", fromRel, "error", err)
				continue
			}
			if info.IsDir() {
				continue
			}
			_, fname, _ := splitSessionPath(fromRel)
			toRel := filepath.ToSlash(filepath.Join(".sageox", "cache", "quarantine", sess, fname))
			toAbs := filepath.Join(ledgerPath, toRel)
			if err := os.MkdirAll(filepath.Dir(toAbs), 0o700); err != nil {
				slog.Warn("pre-push quarantine: mkdir failed; skipping",
					"path", toRel, "error", err)
				continue
			}
			if err := os.Rename(fromAbs, toAbs); err != nil {
				slog.Warn("pre-push quarantine: rename failed; skipping",
					"from", fromRel, "to", toRel, "error", err)
				continue
			}
			if err := unstagePath(ledgerPath, fromRel); err != nil {
				slog.Warn("pre-push quarantine: unstage failed; bytes are quarantined but path may remain in commit",
					"path", fromRel, "error", err)
				// Don't return — best-effort. The push will still likely
				// succeed because the moved-aside file no longer exists
				// at the staged path; git may auto-detect the deletion.
			}
			g.quarantineLocs = append(g.quarantineLocs, redactionDebtLocation{
				From: fromRel,
				To:   toRel,
			})
			out.QuarantinedRels = append(out.QuarantinedRels, fromRel)
		}
	}

	// If we unstaged anything, amend the holding commit once so its
	// tree matches the now-quarantined working state.
	if len(out.QuarantinedRels) > 0 {
		if err := amendDroppingPaths(ledgerPath); err != nil {
			slog.Warn("pre-push quarantine: amend failed; push may still surface the bad paths",
				"error", err)
		}
	}

	// Write one marker per session aggregating its quarantined files +
	// finding metadata. Markers are recomputable, so write errors are
	// logged-not-returned.
	for sess, g := range groups {
		if len(g.quarantineLocs) == 0 {
			continue
		}
		markerRel := filepath.ToSlash(filepath.Join(".sageox", "cache", "redaction-debt", sess+".json"))
		markerAbs := filepath.Join(ledgerPath, markerRel)
		if err := os.MkdirAll(filepath.Dir(markerAbs), 0o700); err != nil {
			slog.Warn("pre-push quarantine: marker mkdir failed",
				"session", sess, "error", err)
			continue
		}
		rec := redactionDebtRecord{
			SessionName:     sess,
			QuarantinedAt:   time.Now().UTC(),
			Reason:          "pre-push secret gate could not auto-redact; bytes preserved in quarantine. Inspect, redact manually, and move back to publish.",
			QuarantinePaths: g.quarantineLocs,
		}
		for _, f := range g.findings {
			_, fname, _ := splitSessionPath(f.Path)
			rec.Findings = append(rec.Findings, redactionDebtFinding{
				Detector: f.Detector,
				Filename: fname,
				Line:     f.Line,
			})
		}
		buf, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			slog.Warn("pre-push quarantine: marker marshal failed",
				"session", sess, "error", err)
			continue
		}
		if err := writeFileAtomic(markerAbs, buf, 0o600); err != nil {
			slog.Warn("pre-push quarantine: marker write failed",
				"session", sess, "error", err)
			continue
		}
		out.DebtMarkers = append(out.DebtMarkers, markerRel)
	}
	return out, nil
}

// unstagePath removes a single path from the index, leaving the working
// tree alone. Uses `git reset HEAD~` when a parent commit exists, falling
// back to `git rm --cached --ignore-unmatch` for the first-commit case.
func unstagePath(ledgerPath, rel string) error {
	// Try to resolve HEAD~ first. If it doesn't exist (single-commit
	// repo), fall back to the cached-remove path.
	if err := exec.Command("git", "-C", ledgerPath, "rev-parse", "--verify", "HEAD~").Run(); err == nil {
		// `git reset HEAD~ -- <path>` rewinds the index entry for this
		// path to its HEAD~ state. WT untouched (the file is already
		// moved aside; this just rewrites the staged tree).
		if out, err := exec.Command("git", "-C", ledgerPath, "reset", "HEAD~", "--", rel).CombinedOutput(); err != nil {
			return fmt.Errorf("git reset HEAD~ %s: %s: %w", rel, strings.TrimSpace(string(out)), err)
		}
		return nil
	}
	if out, err := exec.Command("git", "-C", ledgerPath, "rm", "--cached", "--ignore-unmatch", "--", rel).CombinedOutput(); err != nil {
		return fmt.Errorf("git rm --cached %s: %s: %w", rel, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// amendDroppingPaths runs `git commit --amend --no-edit --no-verify`
// after the caller has already mutated the index (via unstagePath calls).
// `--no-verify` matches stageAndAmendRedactedFiles' behavior; the secret
// gate is the verifier and it's the one driving this flow.
func amendDroppingPaths(ledgerPath string) error {
	out, err := exec.Command("git", "-C", ledgerPath, "commit", "--amend", "--no-edit", "--no-verify").CombinedOutput()
	if err != nil {
		// "nothing to commit" can happen if the amend would produce an
		// empty commit (we dropped every staged path). Tolerate it —
		// the push will simply carry nothing new, which is correct.
		if strings.Contains(string(out), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit --amend: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// writeFileAtomic writes data to path via a tmpfile + rename so a reader
// (e.g. doctor) never sees a half-written marker.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	cleanup := true
	tmpName := tmp.Name()
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// isJSONLPath returns true for filenames the canonical redactFileInPlace
// chokepoint can rewrite. Today that's `*.jsonl` (the session raw stream).
// Other text files (md, html, txt) are scanned but go to quarantine
// instead — the chokepoint applies the raw-writer redaction stack and
// expects each line to be a JSON record.
func isJSONLPath(filename string) bool {
	return strings.EqualFold(filepath.Ext(filename), ".jsonl")
}

// sortedKeys returns the keys of m in sorted order. Stable output for
// logs and tests.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
