package teamconverge

import (
	"fmt"
	"io"
	"strings"
)

// WriteText renders the same typed outcomes carried by Report's versioned JSON
// shape. It deliberately includes delivery and commit so a successful
// transport cannot obscure a failed or unsupported local convergence phase.
//
// Skipped (unsupported) artifacts are grouped: one line per reason, then the
// artifacts it covers. A repository with no native skill target skips every
// team skill for the same reason, and a line per skill buried the one fix
// (`ox init`) under dozens of identical rows.
func WriteText(w io.Writer, report Report) error {
	commit := report.Snapshot.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if _, err := fmt.Fprintf(w, "Team Context convergence at %s\n", commit); err != nil {
		return err
	}
	if len(report.Outcomes) == 0 {
		_, err := fmt.Fprintln(w, "  No applicable artifacts")
		return err
	}
	for _, outcome := range report.Outcomes {
		if outcome.State == StateUnsupported {
			continue
		}
		delivery := ""
		if outcome.Delivery != "" {
			delivery = " via " + outcome.Delivery
		}
		detail := ""
		if outcome.Detail != "" {
			detail = " — " + outcome.Detail
		}
		if _, err := fmt.Fprintf(w, "  %s/%s: %s%s%s\n",
			outcome.Kind, outcome.Name, outcome.State, delivery, detail); err != nil {
			return err
		}
	}
	return writeSkipped(w, report.Skipped())
}

func writeSkipped(w io.Writer, skipped []Outcome) error {
	var reasons []string
	byReason := map[string][]string{}
	for _, outcome := range skipped {
		reason := outcome.Detail
		if reason == "" {
			reason = "this repository cannot receive it"
		}
		if _, seen := byReason[reason]; !seen {
			reasons = append(reasons, reason)
		}
		byReason[reason] = append(byReason[reason], string(outcome.Kind)+"/"+outcome.Name)
	}
	for _, reason := range reasons {
		names := byReason[reason]
		noun := "artifacts"
		if len(names) == 1 {
			noun = "artifact"
		}
		if _, err := fmt.Fprintf(w, "  Skipped %d %s — %s:\n    %s\n",
			len(names), noun, reason, strings.Join(names, ", ")); err != nil {
			return err
		}
	}
	return nil
}
