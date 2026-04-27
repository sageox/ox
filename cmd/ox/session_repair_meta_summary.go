// session_repair_meta_summary.go is the ox-l4mj retro-cleanup tool.
//
// Even after the producer-side fixes (ox-qqka, ox-wstd) ship, every
// existing ledger carries the leak forever unless retro-cleared. This
// command walks the ledger and fixes the on-disk truth: clears
// validator-error strings from meta.summary / meta.title, recovers a
// good title from summary.json when available, and stamps SummaryStatus
// so consumers don't have to keep sniffing for sentinel strings.
//
// Idempotent and safe to run repeatedly. Designed to be invoked both
// from the CLI and (later) from the daemon's anti-entropy loop.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/spf13/cobra"
)

var sessionRepairMetaSummaryCmd = &cobra.Command{
	Use:   "repair-meta-summary",
	Short: "Retro-clear validator-failure strings leaked into meta.json (ox-qqka cleanup)",
	Long: `Walk the ledger and clear validator-error strings that pre-fix
versions of ox wrote into meta.summary / meta.title.

Detection: a meta.json field is considered "leaked" if its value matches
one of the known validator/error string shapes (see lfs.IsLeakySummaryString).

Action per session:
  - If the field is leaky and summary.json has a clean title, use that title.
  - Otherwise, clear the leaky field to "" so the UI renders blank instead of
    the diagnostic.
  - Stamp meta.summary_status so consumers can rely on the structured signal:
      ok                  → summary.json title is non-empty and clean
      failed_validation   → summary.json missing or also leaky
  - Move any diagnostic prose from meta.summary into meta.validation_error
    (ops-facing) when no clean replacement is available.
  - Preserve meta.Files (OID manifest) verbatim — we never re-upload LFS.

Idempotent: a meta.json that's already clean is left untouched.

Examples:
  ox session repair-meta-summary           # repair the current repo's ledger
  ox session repair-meta-summary --dry-run # show what would change
  ox session repair-meta-summary --ledger /path/to/ledger`,
	RunE: runSessionRepairMetaSummary,
}

func init() {
	sessionRepairMetaSummaryCmd.Flags().Bool("dry-run", false, "list what would change without modifying anything")
	sessionRepairMetaSummaryCmd.Flags().String("ledger", "", "ledger path (defaults to current repo's ledger)")
	sessionCmd.AddCommand(sessionRepairMetaSummaryCmd)
}

// repairOutcome describes a single session's per-run result. We keep
// the type small and explicit so the CLI summary, JSON output (future),
// and daemon anti-entropy use can all share the same shape.
type repairOutcome struct {
	SessionName       string
	ChangedTitle      bool
	ChangedSummary    bool
	ChangedStatus     bool
	RecoveredFromJSON bool   // recovered title from summary.json
	Skipped           bool   // already clean
	Error             string // non-empty when the session couldn't be processed
}

func runSessionRepairMetaSummary(cmd *cobra.Command, _ []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	ledgerFlag, _ := cmd.Flags().GetString("ledger")

	ledgerPath := ledgerFlag
	if ledgerPath == "" {
		resolved, err := resolveLedgerPath()
		if err != nil {
			return fmt.Errorf("resolve ledger: %w (pass --ledger to override)", err)
		}
		ledgerPath = resolved
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			cli.PrintInfo("no sessions directory found — nothing to repair")
			return nil
		}
		return fmt.Errorf("read sessions dir: %w", err)
	}

	var sessionDirs []string
	for _, e := range entries {
		if e.IsDir() {
			sessionDirs = append(sessionDirs, filepath.Join(sessionsDir, e.Name()))
		}
	}
	sort.Strings(sessionDirs)

	var (
		repaired  []repairOutcome
		clean     int
		errored   int
		processed int
	)
	for _, sd := range sessionDirs {
		oc := repairSessionMetaSummary(sd, dryRun)
		processed++
		switch {
		case oc.Error != "":
			errored++
			repaired = append(repaired, oc)
		case oc.Skipped:
			clean++
		default:
			repaired = append(repaired, oc)
		}
	}

	for _, oc := range repaired {
		printRepairOutcome(oc, dryRun)
	}

	verb := "repaired"
	if dryRun {
		verb = "would repair"
	}
	cli.PrintSuccess(fmt.Sprintf("%s %d session(s); %d already clean; %d errored (of %d total)",
		verb, len(repaired)-errored, clean, errored, processed))

	return nil
}

// repairSessionMetaSummary inspects one session's meta.json and applies
// the cleanup if needed. Pure-ish: no global state, returns an outcome
// rather than printing. Easy to unit-test.
//
// Cleanup logic:
//
//   - If meta.title or meta.summary is leaky (per lfs.IsLeakySummaryString),
//     try to recover a clean title from summary.json. If recovered, set both
//     meta.title and meta.summary to it and stamp SummaryStatus=ok.
//   - If summary.json is absent or its title is also leaky/empty, clear the
//     fields to "" and stamp SummaryStatus=failed_validation. Move the
//     diagnostic prose (the original leaky meta.summary, when present) into
//     meta.validation_error so ops can still find it.
//   - Anything already clean is skipped without rewriting meta.json (the
//     idempotency contract — running daily must not churn mtimes).
func repairSessionMetaSummary(sessionDir string, dryRun bool) repairOutcome {
	name := filepath.Base(sessionDir)
	oc := repairOutcome{SessionName: name}

	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		// missing meta.json is normal for sessions that never finished —
		// this tool is only about repairing an existing leak, not about
		// authoring new metadata. Use errors.Is(fs.ErrNotExist) rather
		// than os.IsNotExist: the latter does NOT unwrap, so a wrapped
		// not-found error from lfs.ReadSessionMeta would be missed and
		// the session would be flagged as an error instead.
		if errors.Is(err, fs.ErrNotExist) {
			oc.Skipped = true
			return oc
		}
		oc.Error = err.Error()
		return oc
	}

	titleLeaky := lfs.IsLeakySummaryString(meta.Title)
	summaryLeaky := lfs.IsLeakySummaryString(meta.Summary)
	if !titleLeaky && !summaryLeaky {
		oc.Skipped = true
		return oc
	}

	// Try to recover from summary.json.
	cleanTitle, cleanSummary := readSummaryJSONForRepair(sessionDir)

	// Diagnostic preservation: ONLY pull from fields that were actually
	// flagged as leaky. If meta.Summary is leaky but meta.Title is a real
	// title, we must not treat the title as a diagnostic source —
	// pickDiagnosticForOps's "longer string wins" heuristic would
	// otherwise mis-attribute clean text into ValidationError. See the
	// CodeRabbit review on PR #566 for the trap this avoids.
	originalDiagnostic := ""
	switch {
	case titleLeaky && summaryLeaky:
		originalDiagnostic = pickDiagnosticForOps(meta.Summary, meta.Title)
	case summaryLeaky:
		originalDiagnostic = meta.Summary
	case titleLeaky:
		originalDiagnostic = meta.Title
	}

	switch {
	case cleanTitle != "":
		// Recovered: a successful regenerate (or earlier success) wrote
		// summary.json but the sticky-tombstone bug (ox-wstd) prevented
		// meta.summary from being overwritten. Apply the recovery.
		// Recovery overwrites both Title and Summary with the clean
		// values from summary.json, regardless of which side(s) were
		// leaky — the goal is consistency between meta and summary.json
		// as the source of truth.
		if meta.Title != cleanTitle {
			meta.Title = cleanTitle
			oc.ChangedTitle = true
		}
		// Mirror title into summary unless summary.json itself has a
		// distinct, non-leaky summary — in that case prefer it.
		newSummary := cleanTitle
		if cleanSummary != "" && !lfs.IsLeakySummaryString(cleanSummary) {
			newSummary = cleanSummary
		}
		if meta.Summary != newSummary {
			meta.Summary = newSummary
			oc.ChangedSummary = true
		}
		if meta.SummaryStatus != sessionsummary.SummaryStatusOK {
			meta.SummaryStatus = sessionsummary.SummaryStatusOK
			oc.ChangedStatus = true
		}
		// Stale validation error from the original failure — clear it.
		meta.ValidationError = ""
		oc.RecoveredFromJSON = true

	default:
		// No clean replacement available. Clear ONLY the field(s) that
		// were actually flagged as leaky — never erase a legitimate
		// value just because the OTHER side was bad. Then mark the
		// session structurally as failed_validation so consumers don't
		// rely on string-sniffing.
		if titleLeaky && meta.Title != "" {
			meta.Title = ""
			oc.ChangedTitle = true
		}
		if summaryLeaky && meta.Summary != "" {
			meta.Summary = ""
			oc.ChangedSummary = true
		}
		if meta.SummaryStatus != sessionsummary.SummaryStatusFailedValidation {
			meta.SummaryStatus = sessionsummary.SummaryStatusFailedValidation
			oc.ChangedStatus = true
		}
		// Preserve the diagnostic (ops-facing) so we don't lose context.
		// Only overwrite ValidationError if it's currently empty —
		// running this tool repeatedly must be idempotent.
		if meta.ValidationError == "" && originalDiagnostic != "" {
			meta.ValidationError = originalDiagnostic
		}
	}

	if dryRun {
		return oc
	}

	if err := lfs.WriteSessionMetaOnly(sessionDir, meta); err != nil {
		oc.Error = fmt.Sprintf("write meta.json: %v", err)
	}
	return oc
}

// readSummaryJSONForRepair reads summary.json and returns its title and
// summary IF they look clean (non-empty + not leaky). Returns empty
// strings for anything we shouldn't propagate.
func readSummaryJSONForRepair(sessionDir string) (title, summary string) {
	data, err := os.ReadFile(filepath.Join(sessionDir, "summary.json"))
	if err != nil {
		return "", ""
	}
	var sj struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(data, &sj); err != nil {
		return "", ""
	}
	if lfs.IsLeakySummaryString(sj.Title) {
		sj.Title = ""
	}
	if lfs.IsLeakySummaryString(sj.Summary) {
		sj.Summary = ""
	}
	return sj.Title, sj.Summary
}

// pickDiagnosticForOps returns the leaky string we want to preserve as
// ops-facing context in ValidationError, when no clean replacement is
// available. Prefers the longer of the two (it usually carries more
// information). Returns "" if both are empty.
func pickDiagnosticForOps(summary, title string) string {
	if len(summary) >= len(title) {
		return summary
	}
	return title
}

func printRepairOutcome(oc repairOutcome, dryRun bool) {
	prefix := "fixed"
	if dryRun {
		prefix = "would fix"
	}
	if oc.Error != "" {
		cli.PrintWarning(fmt.Sprintf("%s: ERROR — %s", oc.SessionName, oc.Error))
		return
	}
	source := "cleared"
	if oc.RecoveredFromJSON {
		source = "recovered from summary.json"
	}
	cli.PrintInfo(fmt.Sprintf("%s %s (%s)", prefix, oc.SessionName, source))
}
