// Package knowledgeflow turns the turn-anchored consulted/influenced events that
// the tagging layer writes (epic ox-bcgb) into graded, human-readable causal
// chains for `ox recap` — the "how team knowledge changed your work" story.
//
// It is the consumer half of the epic, kept as a standalone package so its
// logic and tests live with the schema they depend on; recap calls Build and
// renders the result. The cardinal rule mirrors the rest of recap: a chain is
// rendered at exactly its evidence grade. A deterministic consult
// (mechanism=retrieval/injection) is phrased as "you consulted X" — provable,
// never "X caused Y". An inferred one (self-report) is explicitly labeled.
package knowledgeflow

import (
	"fmt"

	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
)

// Grade is the evidence tier of a flow, derived from the event mechanism.
type Grade string

const (
	// GradeProvable — the agent demonstrably pulled/received the context.
	GradeProvable Grade = "provable"
	// GradeInferred — self-reported or LLM-classified; labeled as such.
	GradeInferred Grade = "inferred"
)

// Flow is one causal-chain item: knowledge that reached a turn of the user's
// work, with the receipt (ref) and the grade of evidence behind it.
type Flow struct {
	Session string `json:"session"`
	Seq     int    `json:"seq"`
	Text    string `json:"text"`
	Ref     string `json:"ref,omitempty"`
	RefType string `json:"ref_type,omitempty"`
	Grade   Grade  `json:"grade"`
	Kind    string `json:"kind"` // consult | cross-session | distillation | injection | attribution | use
}

// Build turns one session's context-trace events into flow items. Only the
// influence events (consulted/influenced) become flows; provided/ambient events
// are ignored. Deterministic and pure.
func Build(sessionName string, events []contexttrace.Event) []Flow {
	var flows []Flow
	for _, ev := range events {
		f, ok := flowFor(sessionName, ev)
		if ok {
			flows = append(flows, f)
		}
	}
	return flows
}

// flowFor maps a single event to a flow item, or reports ok=false to skip it.
func flowFor(session string, ev contexttrace.Event) (Flow, bool) {
	grade := gradeOf(ev.Mechanism)
	base := Flow{Session: session, Seq: ev.Seq, Ref: ev.Ref, RefType: ev.RefType, Grade: grade}

	switch ev.Type {
	case contexttrace.EventConsulted:
		base.Kind, base.Text = consultPhrasing(ev)
		return base, true
	case contexttrace.EventUsed:
		return usePhrasing(base, ev)
	case contexttrace.EventInfluenced:
		base.Kind = "attribution"
		base.Text = fmt.Sprintf("You attributed this work to %s (self-assessed)", refLabel(ev))
		if ev.Decision != "" {
			base.Text = fmt.Sprintf("You attributed “%s” to %s (self-assessed)", ev.Decision, refLabel(ev))
		}
		return base, true
	case contexttrace.EventProvided:
		// A provided event is only a flow when it's an injection (a whisper that
		// landed) — plain prime docs are covered by the "reached you" section.
		if ev.Mechanism == contexttrace.MechanismInjection {
			base.Kind = "injection"
			from := ev.From
			if from == "" {
				from = "a teammate"
			}
			base.Text = fmt.Sprintf("A murmur from %s reached this work", from)
			return base, true
		}
	}
	return Flow{}, false
}

// consultPhrasing phrases a consulted event by what was consulted, and names the
// flow kind — cross-session (a teammate's session) and distillation (a knowledge
// bubble) are the two highest-value chains.
func consultPhrasing(ev contexttrace.Event) (kind, text string) {
	switch ev.RefType {
	case "session":
		return "cross-session", fmt.Sprintf("You consulted a prior session (%s) while working here", ev.Ref)
	case "kb":
		return "distillation", fmt.Sprintf("A distilled knowledge bubble (%s) informed this work", ev.Ref)
	case "discussion":
		return "consult", fmt.Sprintf("You consulted the team discussion %s", ev.Ref)
	case "query", "code", "decision", "team-context":
		subject := ev.Query
		if subject == "" {
			subject = ev.Ref
		}
		return "consult", fmt.Sprintf("You searched team knowledge for “%s”", subject)
	default:
		return "consult", fmt.Sprintf("You consulted %s", refLabel(ev))
	}
}

// usePhrasing renders a used event at exactly its evidence grade: observed
// use (read/requery/cited) is provable; a self-report and a judge verdict are
// inferred and say so. A "nothing used" report is not a flow.
func usePhrasing(base Flow, ev contexttrace.Event) (Flow, bool) {
	evidence := ev.Evidence
	if evidence == "" {
		evidence = evidenceForMechanism(ev.Mechanism)
	}
	if evidence == contexttrace.EvidenceNone {
		return Flow{}, false
	}
	base.Kind = "use"
	switch contextuse.GradeOf(evidence) {
	case contextuse.GradeProvable:
		base.Grade = GradeProvable
		base.Text = fmt.Sprintf("You used %s (%s", refLabel(ev), contextuse.BucketOf(evidence))
		if ev.Seq > 0 {
			base.Text += fmt.Sprintf(", turn %d", ev.Seq)
		}
		base.Text += ")"
	case contextuse.GradeReported:
		base.Grade = GradeInferred
		base.Text = fmt.Sprintf("You reported using %s", refLabel(ev))
		if ev.Decision != "" {
			base.Text += fmt.Sprintf(": “%s”", ev.Decision)
		}
	case contextuse.GradeJudged:
		base.Grade = GradeInferred
		base.Text = fmt.Sprintf("Judged used: %s", refLabel(ev))
	default:
		return Flow{}, false // unknown evidence from a newer writer
	}
	return base, true
}

// evidenceForMechanism is the fallback for a used event with no evidence.
func evidenceForMechanism(m contexttrace.Mechanism) string {
	switch m {
	case contexttrace.MechanismRetrieval:
		return contexttrace.EvidenceRead
	case contexttrace.MechanismCitation:
		return contexttrace.EvidenceCited
	case contexttrace.MechanismJudge:
		return contexttrace.EvidenceJudged
	}
	return contexttrace.EvidenceReported
}

// gradeOf maps an event mechanism to a rendering grade. Retrieval, injection
// and citation are observed, so provable; self-report and judge are inferred.
func gradeOf(m contexttrace.Mechanism) Grade {
	switch m {
	case contexttrace.MechanismRetrieval, contexttrace.MechanismInjection, contexttrace.MechanismCitation:
		return GradeProvable
	default:
		return GradeInferred
	}
}

// refLabel is a readable label for an event's referent.
func refLabel(ev contexttrace.Event) string {
	if ev.Ref != "" {
		return ev.Ref
	}
	if ev.Doc != "" {
		return ev.Doc
	}
	return "team context"
}

// HasProvable reports whether any flow is provable — recap uses this to decide
// whether to flip KnowledgeFlow.Available to true honestly (a set of only
// inferred flows still surfaces, but labeled).
func HasProvable(flows []Flow) bool {
	for _, f := range flows {
		if f.Grade == GradeProvable {
			return true
		}
	}
	return false
}
