package lfs

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MetaRepairOutcome reports what RecoverEmptyTitleMeta did to a single
// session's meta.json. Designed to be cheap to produce on every call —
// the common path (nothing to repair) returns Skipped=true and the
// caller can ignore the rest.
type MetaRepairOutcome struct {
	SessionName       string
	Skipped           bool   // meta.json was already healthy or terminal
	RecoveredFromJSON bool   // pulled a clean title out of summary.json
	BumpedAttempts    bool   // no recovery available; SummaryAttempts incremented
	FlippedTerminal   bool   // hit MaxSummaryAttempts; status set to unrecoverable
	Error             string // non-fatal; meta.json was not modified
}

// RecoverEmptyTitleMeta inspects one session's meta.json for the
// post-Apr-27 empty-title failure shape (meta.title=="" with status !=
// unrecoverable). When summary.json carries a real title, promotes it
// back into meta and stamps SummaryStatus=ok. Otherwise increments
// SummaryAttempts and, at MaxSummaryAttempts, flips status to
// unrecoverable so future calls early-exit.
//
// This is the daemon-safe core of the empty-title repair flow. Both
// the CLI `ox session repair-meta-summary` tool and the autofix
// scheduler delegate to this so the on-disk behavior is identical.
//
// Idempotency contract: running this repeatedly on a healthy meta is a
// no-op (Skipped=true, no write). Running it repeatedly on an
// unrecoverable meta is also a no-op. Running it on a fixable meta
// applies the fix once and then early-exits on subsequent calls.
//
// dryRun=true returns the outcome without writing meta.json.
func RecoverEmptyTitleMeta(sessionDir string, dryRun bool) MetaRepairOutcome {
	name := filepath.Base(sessionDir)
	out := MetaRepairOutcome{SessionName: name}

	meta, err := ReadSessionMeta(sessionDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			out.Skipped = true
			return out
		}
		out.Error = err.Error()
		return out
	}

	// Healthy or terminal — nothing to do. We treat any non-empty
	// trimmed title as "healthy" because that's what the web UI will
	// render; we don't second-guess whether the title is good.
	if strings.TrimSpace(meta.Title) != "" {
		out.Skipped = true
		return out
	}
	// Terminal failure — daemon already exhausted MaxSummaryAttempts on
	// this session. Stop trying. The session is teammate-visible (LFS
	// upload happened) and the row title falls back to the session-name
	// slug in the UI.
	if meta.SummaryStatus == "unrecoverable" {
		out.Skipped = true
		return out
	}

	// Try to recover a clean title from summary.json. The daemon may
	// have written a failure-stub summary.json with title="" too — in
	// which case readSummaryJSONTitle returns "" and we fall through to
	// the bump-attempts path.
	if cleanTitle := readSummaryJSONTitle(sessionDir); cleanTitle != "" {
		meta.Title = cleanTitle
		// Mirror title into summary if summary.json doesn't carry a
		// distinct one. Keeps the two fields consistent for older
		// readers that prefer Summary over Title.
		if strings.TrimSpace(meta.Summary) == "" {
			meta.Summary = cleanTitle
		}
		meta.SummaryStatus = "ok"
		meta.ValidationError = ""
		meta.SummaryAttempts = 0
		out.RecoveredFromJSON = true
	} else {
		meta.SummaryAttempts++
		out.BumpedAttempts = true
		if meta.SummaryAttempts >= MaxSummaryAttempts {
			meta.SummaryStatus = "unrecoverable"
			out.FlippedTerminal = true
		}
	}

	if dryRun {
		return out
	}
	if err := WriteSessionMetaOnly(sessionDir, meta); err != nil {
		out.Error = "write meta.json: " + err.Error()
	}
	return out
}

// readSummaryJSONTitle returns a trimmed, non-leaky title from
// summary.json or "" if none can be safely recovered. Mirrors the
// shape of cmd/ox/session_repair_meta_summary.go's
// readSummaryJSONForRepair so the two callers can converge once the
// CLI tool migrates to delegate fully to RecoverEmptyTitleMeta.
func readSummaryJSONTitle(sessionDir string) string {
	data, err := os.ReadFile(filepath.Join(sessionDir, "summary.json"))
	if err != nil {
		return ""
	}
	var sj struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(data, &sj); err != nil {
		return ""
	}
	t := strings.TrimSpace(sj.Title)
	if t == "" {
		return ""
	}
	if IsLeakySummaryString(t) {
		return ""
	}
	return t
}
