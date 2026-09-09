package prheader

import (
	"strings"
	"testing"
)

// baseInput is a fully-populated header: a team (distinct from the brand), two
// sessions, one plan, and one discussion. Individual tests clone and mutate it to
// exercise one axis at a time.
func baseInput() Input {
	return Input{
		TeamName:    "Acme Rockets",
		TeamURL:     "https://sageox.ai/t/acme",
		Sessions:    []Session{{URL: "https://sageox.ai/c/ses_9f2a3c"}, {URL: "https://sageox.ai/c/ses_7c1b8d"}},
		Plans:       []Plan{{URL: "https://sageox.ai/plan/pln_4d8e2f"}},
		Discussions: []Discussion{{URL: "https://sageox.ai/c/cnv_019ff2f5"}},
	}
}

func mustContain(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Errorf("output missing %q\n---\n%s", s, got)
		}
	}
}

func mustNotContain(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(got, s) {
			t.Errorf("output unexpectedly contains %q\n---\n%s", s, got)
		}
	}
}

// TestRender_full pins every fragment of a fully-populated line, and — because
// the whole point of this round is compaction — asserts it is ONE line inside the
// markers. Failure prevented: a future edit reintroduces a stacked row and the
// credit line quietly grows back into the reviewer's reading space.
func TestRender_full(t *testing.T) {
	got := Render(baseInput())

	mustContain(t, got,
		"<!-- sageox:pr-header v2 -->",
		"<!-- /sageox:pr-header -->",
		"<small>guided&nbsp;by</small>&nbsp;",
		`<a href="https://sageox.ai/t/acme">`,
		`<source media="(prefers-color-scheme: dark)" srcset="https://sageox.ai/sageox-wordmark-dark.png">`,
		`<img alt="SageOx" height="16" src="https://sageox.ai/sageox-wordmark-light.png">`,
		"&nbsp;/&nbsp;<b>Acme&nbsp;Rockets</b>",
		`<a href="https://sageox.ai/c/ses_9f2a3c">Session&nbsp;1</a>`,
		`<a href="https://sageox.ai/c/ses_7c1b8d">Session&nbsp;2</a>`,
		`<a href="https://sageox.ai/plan/pln_4d8e2f">Plan</a>`,
		`<a href="https://sageox.ai/c/cnv_019ff2f5">Discussion</a>`,
	)

	// One line of content, and it is a blockquote line.
	body := strings.TrimPrefix(got, markerStart+"\n")
	body = strings.TrimSuffix(body, "\n"+markerEnd)
	if strings.Contains(body, "\n") {
		t.Errorf("credit line must be a single line, got %d lines:\n%s",
			strings.Count(body, "\n")+1, body)
	}
	if !strings.HasPrefix(body, "> ") {
		t.Errorf("content line must be a blockquote line, got %q", body)
	}
}

// TestRender_noSubElement is the alignment regression guard. <sub> shrinks text
// but also carries vertical-align:sub, dropping it below the baseline the rest of
// the row sits on — the visible wobble this round removed. <small> shrinks
// without moving the baseline.
// Failure prevented: someone reaches for <sub> again for "smaller text" and the
// line silently goes crooked in every PR body.
func TestRender_noSubElement(t *testing.T) {
	got := Render(baseInput())
	mustNotContain(t, got, "<sub>", "</sub>")
	mustContain(t, got, "<small>")
}

// TestRender_wordmarkHasNoAlignAttribute is the other half of the alignment
// guard. align="middle" — what shipped before — sinks the mark 4.5px below the
// text baseline; the default (no attribute) leaves it 3.5px high, the closest of
// the spec-defined, text-relative options.
// Failure prevented: an align attribute is added back "to center it" and the mark
// sinks, or a line-box-relative value is used whose offset shifts whenever a
// taller element joins the row.
func TestRender_wordmarkHasNoAlignAttribute(t *testing.T) {
	got := Render(baseInput())
	mustNotContain(t, got, "align=")
}

// TestRender_gateRequiresALinkableArtifact is the headline behavior change: a
// header exists so a reviewer can GO LOOK, so it renders only when it carries at
// least one openable link. A team name is not a credit.
// Failure prevented: a bare SageOx logo is stamped onto a pull request that
// SageOx has nothing to show for.
func TestRender_gateRequiresALinkableArtifact(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(Input) Input
		want   bool // want a rendered header
	}{
		{
			name:   "session only",
			mutate: func(in Input) Input { in.Plans, in.Discussions = nil, nil; return in },
			want:   true,
		},
		{
			name:   "plan only",
			mutate: func(in Input) Input { in.Sessions, in.Discussions = nil, nil; return in },
			want:   true,
		},
		{
			name:   "discussion only",
			mutate: func(in Input) Input { in.Sessions, in.Plans = nil, nil; return in },
			want:   true,
		},
		{
			name: "no links, team present => nothing",
			mutate: func(in Input) Input {
				in.Sessions, in.Plans, in.Discussions = nil, nil, nil
				return in
			},
			want: false,
		},
		{
			name: "no links, no team => nothing",
			mutate: func(in Input) Input {
				in.Sessions, in.Plans, in.Discussions = nil, nil, nil
				in.TeamName, in.TeamURL = "", ""
				return in
			},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.mutate(baseInput())
			got := Render(in)
			if tc.want && got == "" {
				t.Fatalf("want a rendered header, got empty")
			}
			if !tc.want && got != "" {
				t.Fatalf("want nothing rendered, got:\n%s", got)
			}
			// The gate and its exported predicate must never disagree — the
			// command relies on HasLinks to explain the no-op on stderr.
			if in.HasLinks() != tc.want {
				t.Errorf("HasLinks() = %v, want %v", in.HasLinks(), tc.want)
			}
		})
	}
}

// TestRender_dedupBrandTeamName proves the dogfood case: a team literally named
// "SageOx" does not render the brand twice (mark, then the same word in bold).
// The links still carry the header, so it renders.
func TestRender_dedupBrandTeamName(t *testing.T) {
	in := baseInput()
	in.TeamName = "sageox" // case-insensitive
	got := Render(in)
	mustContain(t, got, "<picture>", `>Plan</a>`)
	mustNotContain(t, got, "<b>", "&nbsp;/&nbsp;")
}

// TestRender_degradedStates walks the axes that vary independently.
func TestRender_degradedStates(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(Input) Input
		contains   []string
		notContain []string
	}{
		{
			name: "one session, nothing else",
			mutate: func(in Input) Input {
				in.Sessions = []Session{{URL: "https://sageox.ai/c/ses_9f2a3c"}}
				in.Plans, in.Discussions = nil, nil
				return in
			},
			contains:   []string{`>Session</a>`, "<small>guided&nbsp;by</small>"}, // singular, unnumbered
			notContain: []string{"Session&nbsp;1", "Plan", "Discussion"},
		},
		{
			name: "no team name => team segment omitted, links carry it",
			mutate: func(in Input) Input {
				in.TeamName = ""
				return in
			},
			contains:   []string{"<picture>", "Session&nbsp;1"},
			notContain: []string{"<b>"},
		},
		{
			name: "no team URL => wordmark renders unlinked",
			mutate: func(in Input) Input {
				in.TeamURL = ""
				return in
			},
			contains:   []string{"<picture>", "<b>Acme&nbsp;Rockets</b>"},
			notContain: []string{`<a href="https://sageox.ai/t/`},
		},
		{
			name: "two discussions get numbered",
			mutate: func(in Input) Input {
				in.Discussions = []Discussion{{URL: "https://sageox.ai/c/cnv_a"}, {URL: "https://sageox.ai/c/cnv_b"}}
				return in
			},
			contains:   []string{"Discussion&nbsp;1", "Discussion&nbsp;2"},
			notContain: []string{">Discussion</a>"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Render(tc.mutate(baseInput()))
			mustContain(t, got, tc.contains...)
			mustNotContain(t, got, tc.notContain...)
		})
	}
}

// TestRender_teamNameEscaped proves the untrusted-input path: team_name is
// team-editable config and lands in a public PR body.
func TestRender_teamNameEscaped(t *testing.T) {
	in := baseInput()
	in.TeamName = `<script>alert("x")</script>`
	got := Render(in)
	mustNotContain(t, got, "<script>", `alert("x")`)
	mustContain(t, got, "&lt;script&gt;")
}
