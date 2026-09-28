package teamconverge

import (
	"fmt"
	"io"
)

// WriteText renders the same typed outcomes carried by Report's versioned JSON
// shape. It deliberately includes delivery and commit so a successful
// transport cannot obscure a failed or unsupported local convergence phase.
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
	return nil
}
