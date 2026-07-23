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
	Kind    string `json:"kind"` // consult | cross-session | distillation | injection | attribution
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

// gradeOf maps an event mechanism to a rendering grade.
func gradeOf(m contexttrace.Mechanism) Grade {
	switch m {
	case contexttrace.MechanismRetrieval, contexttrace.MechanismInjection:
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
