package recap

import (
	"fmt"

	"github.com/sageox/ox/internal/knowledgeflow"
	"github.com/sageox/ox/internal/session/contexttrace"
)

// gatherFlows mines the user's sessions for consulted/influenced trace events —
// written by the turn-tagging layer (epic ox-bcgb) — and turns them into graded
// knowledge-flow chains: the "how team knowledge changed your work" axis. Each
// line is grade-honest (a deterministic consult reads as "you consulted X"; an
// inferred one is labeled). Returns [] when nothing is instrumented yet, which
// keeps the section on its honest placeholder.
func gatherFlows(ledgerPath string, sessions []SessionFacts) []string {
	var out []string
	for _, s := range sessions {
		if len(out) >= maxFlows {
			break
		}
		events, _ := readTrace(ledgerPath, s.Name)
		infl := influenceEvents(events)
		if len(infl) == 0 {
			continue
		}
		for _, f := range knowledgeflow.Build(s.displayTitle(), infl) {
			out = append(out, flowLine(f))
			if len(out) >= maxFlows {
				break
			}
		}
	}
	return out
}

// influenceEvents keeps only the events that carry influence: consulted and
// influenced, plus provided events that were injections (delivered whispers).
func influenceEvents(events []contexttrace.Event) []contexttrace.Event {
	var out []contexttrace.Event
	for _, ev := range events {
		switch ev.Type {
		case contexttrace.EventConsulted, contexttrace.EventInfluenced:
			out = append(out, ev)
		case contexttrace.EventProvided:
			if ev.Mechanism == contexttrace.MechanismInjection {
				out = append(out, ev)
			}
		}
	}
	return out
}

// flowLine renders one flow as a receipt-bearing line: the phrasing, then the
// session it happened in.
func flowLine(f knowledgeflow.Flow) string {
	return fmt.Sprintf("%s — %s", f.Text, f.Session)
}
