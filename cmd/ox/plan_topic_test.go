package main

import (
	"testing"

	"github.com/sageox/ox/internal/plan"
)

// plan_topic_test.go covers planTopic directly — ox-1tjj.8's coverage hole
// (`git grep -n planTopic -- '*_test.go'` returned nothing before this file
// existed). planTopic used to independently walk in.Sections for the first
// non-empty Heading, which — because plan.Parse splits markdown on H2 ("## ")
// only — could never see a document's H1 (it lives in the Heading=="" preamble
// section): a plan whose H1 was a real title followed by a numbered-context or
// TL;DR H2 saved to the ledger under the H2's text instead. These cases pin
// the fix: planTopic now delegates to plan.Title (already covered in depth by
// internal/plan/title_test.go) for everything except the --topic short-circuit.
func TestPlanTopic(t *testing.T) {
	tests := []struct {
		name string
		in   plan.Input
		want string
	}{
		{
			name: "the ox-1tjj.8 regression: H1 followed by a numbered context H2",
			in: plan.Parse("# Conversation model update — the execution plan\n\n" +
				"## 1. Context — Why Now\n\nSome framing prose.\n\n" +
				"## Approach\n\nDo the thing.\n"),
			want: "Conversation model update — the execution plan",
		},
		{
			name: "the ox-1tjj.8 regression: H1 followed by a TL;DR H2",
			in: plan.Parse("# ox plan — Team-Context-Enriched Plans\n\n" +
				"## TL;DR\n\nShip it.\n\n" +
				"## Design\n\nDetails.\n"),
			want: "ox plan — Team-Context-Enriched Plans",
		},
		{
			name: "no H1 — falls back to the first H2 heading",
			in:   plan.Parse("preamble prose\n\n## First Section\n\nbody\n"),
			want: "First Section",
		},
		{
			name: "no headings at all falls back to the generic default",
			in:   plan.Parse("just a paragraph\n"),
			want: "Implementation Plan",
		},
		{
			name: "in.Topic short-circuits even when in.Raw carries a different heading",
			in: plan.Input{
				Topic: "explicit consult topic",
				Raw:   "# A Different Document Title\n\nbody\n",
			},
			want: "explicit consult topic",
		},
		{
			name: "--topic consult mode: no document exists yet",
			in:   plan.Input{Topic: "pre-draft subject", Sections: []plan.Section{{Body: "pre-draft subject"}}},
			want: "pre-draft subject",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := planTopic(tc.in); got != tc.want {
				t.Errorf("planTopic(...) = %q, want %q", got, tc.want)
			}
		})
	}
}
