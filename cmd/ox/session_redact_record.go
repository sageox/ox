package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/lfs"
)

// splitSessionPath splits a ledger-relative session content path of the
// form "sessions/<name>/<file>" into (sessionName, filename, ok). Other
// shapes (no "sessions/" prefix, no trailing filename) return ok=false.
//
// Used by the redact-history workflow to attribute per-file findings to
// their owning session so the audit trail (lfs.RedactionPass) lands in
// the right meta.json — without parsing rules duplicated at call sites.
func splitSessionPath(p string) (sessionName, filename string, ok bool) {
	p = filepath.ToSlash(p)
	const prefix = "sessions/"
	if !strings.HasPrefix(p, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(p, prefix)
	idx := strings.Index(rest, "/")
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	sessionName = rest[:idx]
	filename = rest[idx+1:]
	if strings.Contains(filename, "/") {
		// Don't support nested files under the session dir. Today every
		// scannable file lives directly under sessions/<name>/.
		return "", "", false
	}
	return sessionName, filename, true
}

// writeRedactionPassesPerSession appends an lfs.RedactionPass entry to
// each affected session's meta.json. Returns the relative paths of the
// meta.json files mutated, in ledger-relative form
// ("sessions/<name>/meta.json"), so the caller can stage them with the
// redacted content files in the amend commit.
//
// Concurrency safety:
//   - Each meta.json write goes through lfs.MutateSessionMeta, which
//     takes an advisory flock and writes via atomic rename. This is the
//     same primitive that prevents ox-lrrq-class races between the
//     daemon and the CLI — adding redaction passes does NOT widen that
//     surface.
//
// All sessions in this call share a single passID + appliedAt instant
// so an external auditor can correlate "these N sessions were all
// redacted in the same pass." catalogVersion / catalogHash come from
// the live Redactor used to do the actual rewrite, not from a
// pre-computed string — they prove which rules ran.
//
// Per ox-zyg7 / the design in ox-8bfh, entries carry detector + file +
// line only — NEVER the matched bytes.
func writeRedactionPassesPerSession(
	ctx context.Context,
	ledgerPath string,
	bySession map[string][]lfs.RedactionEntry,
	catalogVersion, catalogHash string,
) ([]string, error) {
	if len(bySession) == 0 {
		return nil, nil
	}
	passID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate pass id: %w", err)
	}
	appliedAt := time.Now().UTC()
	appliedBy := "ox session redact"

	out := make([]string, 0, len(bySession))
	for sessionName, entries := range bySession {
		sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
		summary := summarizeRedactionEntries(entries)
		pass := lfs.RedactionPass{
			PassID:         passID.String(),
			AppliedAt:      appliedAt,
			AppliedBy:      appliedBy,
			CatalogVersion: catalogVersion,
			CatalogHash:    catalogHash,
			Summary:        summary,
			Entries:        entries,
		}
		mutateErr := lfs.MutateSessionMeta(ctx, sessionDir, func(meta *lfs.SessionMeta) (*lfs.SessionMeta, error) {
			if meta == nil {
				// No meta.json yet — refuse rather than synthesize. A
				// session being redacted should have an existing
				// meta.json; if not, something more serious is wrong
				// and the redaction shouldn't have happened either.
				return nil, fmt.Errorf("session %s has no meta.json", sessionName)
			}
			meta.Redactions = append(meta.Redactions, pass)
			return meta, nil
		})
		if mutateErr != nil {
			return out, fmt.Errorf("update meta.json for %s: %w", sessionName, mutateErr)
		}
		out = append(out, filepath.ToSlash(filepath.Join("sessions", sessionName, "meta.json")))
	}
	return out, nil
}

// summarizeRedactionEntries builds a RedactionPassSummary from a slice
// of entries. Total = len(entries); ByDetector groups counts by
// detector name. Used by writeRedactionPassesPerSession but exposed for
// testability.
func summarizeRedactionEntries(entries []lfs.RedactionEntry) lfs.RedactionPassSummary {
	out := lfs.RedactionPassSummary{Total: len(entries)}
	if len(entries) == 0 {
		return out
	}
	byDet := make(map[string]int, len(entries))
	for _, e := range entries {
		byDet[e.Detector]++
	}
	out.ByDetector = byDet
	return out
}
