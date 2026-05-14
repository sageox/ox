package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/session"
)

// safeRelativeUnder returns the absolute path for rel joined under base,
// rejecting any path that escapes base via "..", absolute paths, or
// symlink resolution. Used everywhere we read a path out of an
// attacker-influenceable marker file (.sageox/cache/redaction-debt/*.json):
// without this guard, a crafted `quarantine_paths[].to` like
// "../../../etc/passwd.bak" would let a ledger-writer cause the redact
// loop to overwrite arbitrary files reachable by the operator's UID
// when it runs `os.Rename(quarantineAbs, inPlaceAbs)`. Threat model is
// narrow (attacker already has ledger write) but the escalation
// "ledger-writer → arbitrary-file-writer" is worth blocking.
func safeRelativeUnder(base, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute path not allowed: %q", rel)
	}
	cleaned := filepath.Clean(rel)
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, string(filepath.Separator)+"..") {
		return "", fmt.Errorf("path escapes base: %q", rel)
	}
	abs := filepath.Join(base, cleaned)
	// filepath.Join + Clean already removed ".." inside the string; the
	// belt-and-suspenders check above guards against an embedded ".."
	// that survived (e.g. on a platform with unusual separators). The
	// explicit prefix check below catches anything that slipped past.
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absAbs, err := filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(absAbs+string(filepath.Separator), baseAbs+string(filepath.Separator)) && absAbs != baseAbs {
		return "", fmt.Errorf("path escapes base after resolution: %q", rel)
	}
	return abs, nil
}

// validQuarantineSessionName rejects session names that would let a
// crafted marker filename or in-memory SessionName field traverse
// outside the per-session subtree. The forward path
// (prepush_autoredact.go) keys quarantine state on session names derived
// from `splitSessionPath` of paths under `sessions/`, but the on-disk
// shape is untrusted at read time.
func validQuarantineSessionName(name string) error {
	if name == "" {
		return fmt.Errorf("empty session name")
	}
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return fmt.Errorf("invalid session name: %q", name)
	}
	return nil
}

// enumerateQuarantineFindings reads <ledger>/.sageox/cache/redaction-debt/
// markers, filters by the scope matcher, and returns one
// redactHistoryFinding per (file, line, detector) for the quarantined
// bytes. The Path on each finding is the IN-PLACE git-tracked path
// (sessions/<name>/<filename>) — that's the destination on a successful
// move-back; QuarantinePath holds the source location where the redact
// pass will actually rewrite bytes.
//
// Without this pass, `ox session redact --session X` would walk
// sessions/X/ for a quarantined session and find nothing — the forward
// path (prepush_autoredact.quarantineUnredactableFindings) moved the
// bytes OUT, so the doctor warning pointed at a no-op command.
//
// The marker file itself stores per-line findings recorded at quarantine
// time. We trust those rather than re-scanning the quarantined bytes —
// the marker is authoritative; the detector catalog could shift between
// quarantine and now, but the at-rest record of WHAT fired stays
// stable. (Re-scanning would risk losing visibility into a leak that an
// older catalog version flagged but a newer one doesn't.)
//
// Per-line entries are mapped through session.NewRedactor's catalog so
// the surfaced detector names are normalized to the current ruleset
// vocabulary where they overlap; unknown detector names from older
// markers pass through unchanged.
func enumerateQuarantineFindings(ledgerPath string, match func(name string) bool) ([]redactHistoryFinding, error) {
	debtDir := filepath.Join(ledgerPath, ".sageox", "cache", "redaction-debt")
	entries, err := os.ReadDir(debtDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read redaction-debt dir: %w", err)
	}
	var out []redactHistoryFinding
	var errs []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		markerAbs := filepath.Join(debtDir, entry.Name())
		buf, err := os.ReadFile(markerAbs)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", entry.Name(), err))
			continue
		}
		var rec redactionDebtRecord
		if err := json.Unmarshal(buf, &rec); err != nil {
			errs = append(errs, fmt.Sprintf("%s: parse: %v", entry.Name(), err))
			continue
		}
		if err := validQuarantineSessionName(rec.SessionName); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", entry.Name(), err))
			continue
		}
		// Marker filename must match the embedded session name, otherwise
		// an attacker who writes `<x>.json` could carry an unrelated
		// SessionName and silently re-target where redact moves bytes.
		if entry.Name() != rec.SessionName+".json" {
			errs = append(errs, fmt.Sprintf("%s: filename does not match session_name %q", entry.Name(), rec.SessionName))
			continue
		}
		if match != nil && !match(rec.SessionName) {
			continue
		}
		// Every From must live directly under sessions/<sess>/ and every
		// To must live directly under .sageox/cache/quarantine/<sess>/.
		// This is what the forward path writes (prepush_autoredact.go);
		// rejecting anything else blocks a marker-driven path-traversal
		// write when the operator later runs `os.Rename(quarantineAbs,
		// inPlaceAbs)` to move bytes back.
		fromPrefix := "sessions/" + rec.SessionName + "/"
		toPrefix := ".sageox/cache/quarantine/" + rec.SessionName + "/"
		quarantineByFile := map[string]string{}
		invalidLoc := false
		for _, loc := range rec.QuarantinePaths {
			from := filepath.ToSlash(loc.From)
			to := filepath.ToSlash(loc.To)
			if !strings.HasPrefix(from, fromPrefix) || strings.Contains(from[len(fromPrefix):], "/") || strings.Contains(from, "..") {
				errs = append(errs, fmt.Sprintf("%s: from %q must be a direct child of sessions/%s/", entry.Name(), loc.From, rec.SessionName))
				invalidLoc = true
				continue
			}
			if !strings.HasPrefix(to, toPrefix) || strings.Contains(to[len(toPrefix):], "/") || strings.Contains(to, "..") {
				errs = append(errs, fmt.Sprintf("%s: to %q must be a direct child of .sageox/cache/quarantine/%s/", entry.Name(), loc.To, rec.SessionName))
				invalidLoc = true
				continue
			}
			quarantineByFile[loc.From] = loc.To
		}
		if invalidLoc {
			continue
		}
		for _, f := range rec.Findings {
			if strings.ContainsAny(f.Filename, "/\\") {
				errs = append(errs, fmt.Sprintf("%s: filename %q contains path separator", entry.Name(), f.Filename))
				continue
			}
			inPlaceRel := filepath.ToSlash(filepath.Join("sessions", rec.SessionName, f.Filename))
			quarantineRel, ok := quarantineByFile[inPlaceRel]
			if !ok {
				errs = append(errs, fmt.Sprintf("%s: finding for %q has no quarantine_paths entry", entry.Name(), f.Filename))
				continue
			}
			out = append(out, redactHistoryFinding{
				Detector:       f.Detector,
				Path:           inPlaceRel,
				Line:           f.Line,
				Quarantined:    true,
				QuarantinePath: quarantineRel,
				SessionName:    rec.SessionName,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Detector < out[j].Detector
	})
	if len(errs) > 0 {
		return out, fmt.Errorf("quarantine enumeration incomplete (%d issue(s)):\n  %s",
			len(errs), strings.Join(errs, "\n  "))
	}
	return out, nil
}

// moveQuarantinedFileBack moves bytes from
// .sageox/cache/quarantine/<name>/<file> back to sessions/<name>/<file>
// after a successful redact. Idempotent: if the destination already
// exists (operator manually moved the file back), prefer that and
// remove the quarantine copy.
func moveQuarantinedFileBack(ledgerPath, quarantineRel, inPlaceRel string) error {
	// Belt-and-suspenders containment check at the os.Rename boundary.
	// The caller (enumerateQuarantineFindings) already validates the
	// marker shape, but a future code path that constructs the args
	// differently must not be able to bypass the guard.
	quarantineAbs, err := safeRelativeUnder(ledgerPath, quarantineRel)
	if err != nil {
		return fmt.Errorf("quarantine path: %w", err)
	}
	inPlaceAbs, err := safeRelativeUnder(ledgerPath, inPlaceRel)
	if err != nil {
		return fmt.Errorf("in-place path: %w", err)
	}

	if _, err := os.Stat(quarantineAbs); err != nil {
		if os.IsNotExist(err) {
			// Already moved (likely by a previous partial run). Nothing
			// to do — the in-place file is canonical.
			return nil
		}
		return fmt.Errorf("stat quarantine source: %w", err)
	}

	if _, err := os.Stat(inPlaceAbs); err == nil {
		// In-place exists. Operator likely beat us to it. Remove the
		// quarantine copy so the marker can be cleaned up cleanly.
		if err := os.Remove(quarantineAbs); err != nil {
			return fmt.Errorf("remove quarantine after in-place exists: %w", err)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(inPlaceAbs), 0o700); err != nil {
		return fmt.Errorf("mkdir in-place parent: %w", err)
	}
	if err := os.Rename(quarantineAbs, inPlaceAbs); err != nil {
		return fmt.Errorf("rename quarantine → in-place: %w", err)
	}
	return nil
}

// removeDebtMarkerIfDrained removes the redaction-debt marker for a
// session if every quarantined file listed inside it is no longer at
// its quarantine location (i.e. all paths have been moved back or
// otherwise resolved). Best-effort — leaves the marker in place on any
// error so the next `ox doctor` run still sees the unresolved state.
//
// The accompanying quarantine directory
// (.sageox/cache/quarantine/<name>/) is also cleaned up if empty.
func removeDebtMarkerIfDrained(ledgerPath, sessionName string) error {
	if err := validQuarantineSessionName(sessionName); err != nil {
		return err
	}
	markerRel := filepath.ToSlash(filepath.Join(".sageox", "cache", "redaction-debt", sessionName+".json"))
	markerAbs := filepath.Join(ledgerPath, markerRel)

	buf, err := os.ReadFile(markerAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var rec redactionDebtRecord
	if err := json.Unmarshal(buf, &rec); err != nil {
		return err
	}
	for _, loc := range rec.QuarantinePaths {
		if _, err := os.Stat(filepath.Join(ledgerPath, loc.To)); err == nil {
			// Still quarantined; not done.
			return nil
		}
	}
	if err := os.Remove(markerAbs); err != nil {
		return err
	}
	quarantineDir := filepath.Join(ledgerPath, ".sageox", "cache", "quarantine", sessionName)
	if entries, err := os.ReadDir(quarantineDir); err == nil && len(entries) == 0 {
		_ = os.Remove(quarantineDir)
	}
	return nil
}

// redactQuarantinedFile applies the canonical chokepoint at the
// quarantine source path. Non-JSONL quarantined files are surfaced as
// "manual scrub required" rather than auto-redacted — the chokepoint
// applies the raw-writer redaction stack and expects each line to be a
// JSON record. For non-JSONL, the operator follows the manual flow
// (inspect → scrub → move back) documented in `ox doctor`'s
// redaction-debt detail.
func redactQuarantinedFile(ledgerPath, quarantineRel string, redactor *session.Redactor) error {
	abs, err := safeRelativeUnder(ledgerPath, quarantineRel)
	if err != nil {
		return fmt.Errorf("quarantine path: %w", err)
	}
	if !isJSONLPath(filepath.Base(abs)) {
		return fmt.Errorf("quarantined file %s is not JSONL; manual scrub required (see `ox doctor` step 2)", quarantineRel)
	}
	return redactFileInPlace(abs, redactor)
}
