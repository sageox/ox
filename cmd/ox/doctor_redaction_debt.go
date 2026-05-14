package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
)

// checkLedgerRedactionDebt surfaces sessions that the pre-push secret
// gate auto-quarantined because it could not redact them through the
// canonical chokepoint. See cmd/ox/prepush_autoredact.go for the
// quarantine pipeline: bytes are moved to
// <ledger>/.sageox/cache/quarantine/<session>/<file> and a JSON marker
// is written under <ledger>/.sageox/cache/redaction-debt/<session>.json.
//
// This check is read-only and local-only. It does not re-scan content;
// the markers are authoritative — they were produced by a fresh scan at
// quarantine time. If the user manually moves a quarantined file back
// to its in-place ledger path or removes the marker, the check
// gracefully reflects the new state.
//
// --fix is intentionally a no-op: the recovery action is either
// `ox session redact --session <name>` (interactive cleanup, supported
// for JSONL quarantine via the Path Y integration in #608) or the user
// manually moving the quarantined file back (the only path for
// non-JSONL files). The doctor command cannot safely choose between
// those for the user; it surfaces the state and gets out of the way.
func checkLedgerRedactionDebt(fix bool) checkResult {
	name := "Ledger redaction debt"
	_ = fix // recovery is intentionally user-driven; see doc above.

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "not in git repo", "")
	}
	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return SkippedCheck(name, "config error", "")
	}
	ledgerPath := resolveLedgerPathForAudit(localCfg)
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger configured", "")
	}
	if !ledger.Exists(ledgerPath) {
		return SkippedCheck(name, "ledger directory does not exist", "")
	}

	debtDir := filepath.Join(ledgerPath, ".sageox", "cache", "redaction-debt")
	if _, err := os.Stat(debtDir); err != nil {
		if os.IsNotExist(err) {
			return PassedCheck(name, "no quarantined sessions")
		}
		return FailedCheck(name, fmt.Sprintf("stat debt dir: %v", err), "")
	}
	summaries, malformed := readDebtSummaries(debtDir)

	if len(summaries) == 0 && len(malformed) == 0 {
		return PassedCheck(name, "no quarantined sessions")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d session(s) quarantined; bytes preserved under .sageox/cache/quarantine/",
		len(summaries))
	if len(malformed) > 0 {
		fmt.Fprintf(&b, "; %d marker(s) unreadable", len(malformed))
	}
	msg := b.String()

	var detail strings.Builder
	for _, s := range summaries {
		fmt.Fprintf(&detail, "  %s — %d finding(s) across %d file(s); detectors: %s\n",
			s.session, s.findings, s.files, strings.Join(s.detectors, ", "))
	}
	detail.WriteString("\nNext steps:\n")
	detail.WriteString("  1. For interactive cleanup (JSONL quarantine; redact + move back automatically), run:\n")
	const maxCmdLines = 5
	shown := 0
	for _, s := range summaries {
		if shown >= maxCmdLines {
			break
		}
		fmt.Fprintf(&detail, "       ox session redact --session %s\n", s.session)
		shown++
	}
	if len(summaries) > maxCmdLines {
		fmt.Fprintf(&detail, "       ... and %d more (see list above)\n", len(summaries)-maxCmdLines)
	}
	detail.WriteString("  2. For non-JSONL quarantine (or to scrub manually):\n")
	detail.WriteString("       a. Inspect bytes at .sageox/cache/quarantine/<session>/<file>\n")
	detail.WriteString("       b. Edit to remove the secret\n")
	detail.WriteString("       c. Move back to sessions/<session>/<file>\n")
	detail.WriteString("       d. Remove .sageox/cache/redaction-debt/<session>.json\n")
	detail.WriteString("  3. Re-stage and commit; the next push will publish the cleaned bytes\n")
	if len(malformed) > 0 {
		detail.WriteString("\nUnreadable markers (remove and re-run if no quarantined bytes exist):\n")
		for _, m := range malformed {
			fmt.Fprintf(&detail, "  .sageox/cache/redaction-debt/%s\n", m)
		}
	}

	// Use WarningCheck (passed=true, warning=true) rather than
	// FailedCheck — debt is a state the user opted into by attempting to
	// push something the gate couldn't clean, and the system already
	// handled it gracefully (rest of push proceeded). Doctor shouldn't
	// treat this as an error; it's a reminder.
	return WarningCheck(name, msg, detail.String())
}

// debtSummary is the per-marker shape produced by readDebtSummaries.
// Aggregates the parts of redactionDebtRecord the doctor surface uses;
// hides the full record from the caller (markers carry filenames and
// line numbers, never matched bytes — ox-zyg7).
type debtSummary struct {
	session   string
	marker    string
	findings  int
	files     int
	detectors []string
}

// readDebtSummaries walks debtDir and parses every *.json marker into
// a debtSummary. Returns (summaries, malformed) — malformed lists
// filenames whose contents could not be parsed as a redactionDebtRecord.
// Separated from checkLedgerRedactionDebt so a unit test can exercise
// the parse + aggregation logic without needing a full project /
// ledger-config harness.
func readDebtSummaries(debtDir string) (summaries []debtSummary, malformed []string) {
	entries, err := os.ReadDir(debtDir)
	if err != nil {
		return nil, nil
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		markerAbs := filepath.Join(debtDir, entry.Name())
		buf, err := os.ReadFile(markerAbs)
		if err != nil {
			malformed = append(malformed, entry.Name())
			continue
		}
		var rec redactionDebtRecord
		if err := json.Unmarshal(buf, &rec); err != nil {
			malformed = append(malformed, entry.Name())
			continue
		}
		detSet := map[string]struct{}{}
		for _, f := range rec.Findings {
			detSet[f.Detector] = struct{}{}
		}
		dets := make([]string, 0, len(detSet))
		for d := range detSet {
			dets = append(dets, d)
		}
		sort.Strings(dets)
		summaries = append(summaries, debtSummary{
			session:   rec.SessionName,
			marker:    entry.Name(),
			findings:  len(rec.Findings),
			files:     len(rec.QuarantinePaths),
			detectors: dets,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].session < summaries[j].session
	})
	return summaries, malformed
}
