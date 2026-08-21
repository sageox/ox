package prheader

import (
	"strings"
	"testing"
)

// baseInput is a fully-populated header: a team, two sessions, one plan, and
// material enrichment (2 prior art + 1 collision). Individual tests clone and
// mutate it to exercise one axis at a time.
func baseInput() Input {
	return Input{
		TeamName: "SageOx Internal",
		TeamURL:  "https://sageox.ai/t/sageox-internal",
		Sessions: []Session{{URL: "https://sageox.ai/c/ses_9f2a3c"}, {URL: "https://sageox.ai/c/ses_7c1b8d"}},
		Plans:    []Plan{{URL: "https://sageox.ai/plan/pln_4d8e2f"}},
		Signals:  Signals{PriorArt: 2, Collisions: 1, Material: true},
		ShowStat: true,
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

func TestRender_full(t *testing.T) {
	got := Render(baseInput())

	mustContain(t, got,
		markerStart, markerEnd,
		`<source media="(prefers-color-scheme: dark)" srcset="https://sageox.ai/sageox-wordmark-dark.png">`,
		`src="https://sageox.ai/sageox-wordmark-light.png"`,
		`<a href="https://sageox.ai/t/sageox-internal">`,
		"<b>SageOx&nbsp;Internal</b>",
		`<a href="https://sageox.ai/c/ses_9f2a3c">Session&nbsp;1</a>`,
		`<a href="https://sageox.ai/c/ses_7c1b8d">Session&nbsp;2</a>`,
		`<a href="https://sageox.ai/plan/pln_4d8e2f">Plan</a>`, // one plan => unnumbered
		"<sub>",
		"Guided&nbsp;by&nbsp;SageOx",
		"2&nbsp;prior&nbsp;art",
		"1&nbsp;collision",
	)
	// Sessions grouped by whitespace, not a divider.
	mustContain(t, got, "Session&nbsp;1</a>&nbsp;&nbsp;<a")
	// Markers wrap the whole thing on their own lines.
	if !strings.HasPrefix(got, markerStart+"\n") || !strings.HasSuffix(got, "\n"+markerEnd) {
		t.Errorf("markers not wrapping content:\n%s", got)
	}
}

func TestRender_degradedStates(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(Input) Input
		contains   []string
		notContain []string
	}{
		{
			name: "one session, no plans, no enrichment",
			mutate: func(in Input) Input {
				in.Sessions = []Session{{URL: "https://sageox.ai/c/ses_9f2a3c"}}
				in.Plans = nil
				in.Signals = Signals{}
				return in
			},
			contains:   []string{`>Session</a>`}, // singular, unnumbered
			notContain: []string{"Session&nbsp;1", "<sub>", "Guided", "Plan"},
		},
		{
			name: "bare: no sessions, no plans, no enrichment",
			mutate: func(in Input) Input {
				in.Sessions = nil
				in.Plans = nil
				in.Signals = Signals{}
				return in
			},
			contains:   []string{"<picture>", "<b>SageOx&nbsp;Internal</b>"},
			notContain: []string{"Session", "Plan", "<sub>", "Guided"},
		},
		{
			name: "no sessions, two plans, material with zero counts",
			mutate: func(in Input) Input {
				in.Sessions = nil
				in.Plans = []Plan{{URL: "https://sageox.ai/plan/pln_a"}, {URL: "https://sageox.ai/plan/pln_b"}}
				in.Signals = Signals{Material: true}
				return in
			},
			contains:   []string{">Plan&nbsp;1</a>", ">Plan&nbsp;2</a>", "<sub>Guided&nbsp;by&nbsp;SageOx</sub>"},
			notContain: []string{"Session", "prior", "collision"},
		},
		{
			name: "material but ShowStat off => no whisper",
			mutate: func(in Input) Input {
				in.ShowStat = false
				return in
			},
			contains:   []string{"Session&nbsp;1"},
			notContain: []string{"<sub>", "Guided"},
		},
		{
			name: "no team name => team segment omitted",
			mutate: func(in Input) Input {
				in.TeamName = ""
				return in
			},
			contains:   []string{"<picture>", "Session&nbsp;1"},
			notContain: []string{"<b>"},
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

func TestRender_teamNameEscaped(t *testing.T) {
	in := baseInput()
	in.TeamName = `Ac & <script>alert(1)</script>`
	got := Render(in)
	mustContain(t, got, "&amp;", "&lt;script&gt;")
	mustNotContain(t, got, "<script>alert")
}

func TestRender_noTeamURL_wordmarkUnlinked(t *testing.T) {
	in := baseInput()
	in.TeamURL = ""
	got := Render(in)
	// The wordmark <picture> must appear, but not wrapped in an anchor to a team.
	mustContain(t, got, "<picture>")
	mustNotContain(t, got, `<a href="https://sageox.ai/t/`)
}

func TestRender_tierB_floatedStripFirstNoSub(t *testing.T) {
	in := baseInput()
	in.Strip = &StripURLs{
		Light: "https://assets.sageox.ai/pr/enrich-2-1-0-light.svg",
		Dark:  "https://assets.sageox.ai/pr/enrich-2-1-0-dark.svg",
	}
	got := Render(in)

	mustContain(t, got,
		`align="right"`,
		`srcset="https://assets.sageox.ai/pr/enrich-2-1-0-dark.svg"`,
		`alt="Guided by SageOx · 2 prior art · 1 collision"`,
	)
	// The floated strip must precede the wordmark in source order (so it floats
	// onto the same line), and Tier B replaces the <sub> text whisper.
	stripIdx := strings.Index(got, `align="right"`)
	markIdx := strings.Index(got, `alt="SageOx"`)
	if stripIdx < 0 || markIdx < 0 || stripIdx > markIdx {
		t.Errorf("floated strip must come before the wordmark; strip=%d wordmark=%d", stripIdx, markIdx)
	}
	mustNotContain(t, got, "<sub>")
}

func TestRender_tierB_ignoredWithoutWhisper(t *testing.T) {
	in := baseInput()
	in.Signals = Signals{} // not material => no whisper => strip irrelevant
	in.Strip = &StripURLs{Light: "l.svg", Dark: "d.svg"}
	got := Render(in)
	mustNotContain(t, got, `align="right"`, "<sub>")
}

func TestRender_tierB_ignoredWhenStripIncomplete(t *testing.T) {
	in := baseInput()
	in.Strip = &StripURLs{Light: "only-light.svg"} // missing dark => fall back to Tier A text
	got := Render(in)
	mustContain(t, got, "<sub>", "Guided&nbsp;by&nbsp;SageOx")
	mustNotContain(t, got, `align="right"`)
}
