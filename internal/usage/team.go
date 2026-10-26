package usage

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/recap"
	"github.com/sageox/ox/internal/theme"
)

// maxTeamItems caps each aggregate list in the team view.
const maxTeamItems = 10

// guidanceTeam narrates the aggregate team view.
const guidanceTeam = "Show the team-wide ox brief aggregate. Print the display field verbatim inside a " +
	"fenced ```text block, then at most 3 sentences of your own. This view is aggregate-only by design: " +
	"never name, count, compare or rank individual coworkers, and never infer who did what. Token counts " +
	"are estimates (~4 bytes per token) — say so once. For the value narrative (what SageOx delivered), " +
	"point to `ox recap`. Never state or imply time saved or dollar figures."

// TeamReport is the `ox brief --team --json` payload. It is aggregate-only
// (ADR-027 rule 2): it carries no principal IDs, names, session names,
// per-person counts or rankings — nothing that says who did what.
type TeamReport struct {
	View        string       `json:"view"`
	Window      string       `json:"window"`
	Sessions    int          `json:"sessions"`
	Traced      int          `json:"traced"`
	NotHydrated int          `json:"not_hydrated"`
	Calls       []LabelStat  `json:"calls"`
	Injected    []SourceStat `json:"injected"`
	TopSurfaced []DocStat    `json:"top_surfaced"`
	Display     string       `json:"display"`
	Guidance    string       `json:"guidance"`
	NextActions []NextAction `json:"next_actions"`
}

// LabelStat is one kind of ox call, summed across the team.
type LabelStat struct {
	Label  string `json:"label"`
	Count  int    `json:"count"`
	Tokens int    `json:"tokens"`
}

// DocStat is a team doc and how many sessions it was surfaced in.
type DocStat struct {
	Ref      string `json:"ref"`
	Sessions int    `json:"sessions"`
}

// BuildTeam aggregates every teammate's sessions in the window. Only docs are
// listed as surfaced: whisper murmurs carry their author and retrieval
// queries are a person's own words, so neither is aggregated.
func BuildTeam(in RollupInput) TeamReport {
	r := TeamReport{
		View: ViewTeam, Window: in.Window,
		Calls: []LabelStat{}, Injected: []SourceStat{}, TopSurfaced: []DocStat{},
		Guidance:    guidanceTeam,
		NextActions: []NextAction{{Action: "ox recap", Why: "the value narrative: what team knowledge reached your work"}},
	}
	calls := map[string]*LabelStat{}
	tokens := map[string]int{}
	docs := map[string]int{}

	for _, s := range windowSessions(in) {
		r.Sessions++
		events, dehydrated := recap.ReadTrace(in.LedgerPath, s.Name)
		if dehydrated {
			r.NotHydrated++
			continue
		}
		if !hasInvoked(events) {
			continue
		}
		r.Traced++
		sr := Build(Input{Events: events, TraceSource: TraceSourceSession})
		for _, c := range sr.Calls.ByCommand {
			ls, ok := calls[c.Label]
			if !ok {
				ls = &LabelStat{Label: c.Label}
				calls[c.Label] = ls
			}
			ls.Count += c.Count
			ls.Tokens += c.Tokens
		}
		for _, src := range sr.Injected.BySource {
			tokens[src.Source] += src.Tokens
		}
		for _, sf := range sr.Surfaced {
			if sf.RefType == "doc" {
				docs[sf.Ref]++
			}
		}
	}

	for _, ls := range calls {
		r.Calls = append(r.Calls, *ls)
	}
	sort.Slice(r.Calls, func(i, j int) bool {
		if r.Calls[i].Count != r.Calls[j].Count {
			return r.Calls[i].Count > r.Calls[j].Count
		}
		return r.Calls[i].Label < r.Calls[j].Label
	})
	for src, n := range tokens {
		r.Injected = append(r.Injected, SourceStat{Source: src, Tokens: n})
	}
	sort.Slice(r.Injected, func(i, j int) bool {
		if r.Injected[i].Tokens != r.Injected[j].Tokens {
			return r.Injected[i].Tokens > r.Injected[j].Tokens
		}
		return r.Injected[i].Source < r.Injected[j].Source
	})
	for ref, n := range docs {
		r.TopSurfaced = append(r.TopSurfaced, DocStat{Ref: ref, Sessions: n})
	}
	sort.Slice(r.TopSurfaced, func(i, j int) bool {
		if r.TopSurfaced[i].Sessions != r.TopSurfaced[j].Sessions {
			return r.TopSurfaced[i].Sessions > r.TopSurfaced[j].Sessions
		}
		return r.TopSurfaced[i].Ref < r.TopSurfaced[j].Ref
	})
	if len(r.Calls) > maxTeamItems {
		r.Calls = r.Calls[:maxTeamItems]
	}
	if len(r.TopSurfaced) > maxTeamItems {
		r.TopSurfaced = r.TopSurfaced[:maxTeamItems]
	}
	r.Display = DisplayTeam(r, in.Width)
	return r
}

// DisplayTeam renders the team aggregate as plain text (no ANSI).
func DisplayTeam(r TeamReport, width int) string { return renderTeam(r, width, plain) }

// HumanTeam is DisplayTeam with theme colors when the terminal supports them.
func HumanTeam(r TeamReport, width int) string {
	if !theme.ColorEnabled() {
		return DisplayTeam(r, width)
	}
	return renderTeam(r, width, themed())
}

func renderTeam(r TeamReport, width int, st styler) string {
	if width <= 0 {
		width = 80
	}
	window := "last " + r.Window
	if r.Window == "" {
		window = "recent"
	}
	lines := []string{st.head("ox brief") + st.dim(fmt.Sprintf(" · team  %s · %d session%s (%d with usage trace)",
		window, r.Sessions, plural(r.Sessions, "", "s"), r.Traced))}
	if r.Traced == 0 {
		lines = append(lines, st.dim("no usage traces in this window yet"))
	} else {
		total := 0
		var callItems []string
		for _, c := range r.Calls {
			total += c.Count
			callItems = append(callItems, fmt.Sprintf("%s ×%d", c.Label, c.Count))
		}
		callItems = append([]string{st.accent(fmt.Sprintf("%d", total)) + "  "}, callItems...)
		lines = append(lines, wrapItems(st.label("calls"), callItems, "  ", width)...)

		tok, maxTok := 0, 0
		for _, s := range r.Injected {
			tok += s.Tokens
			maxTok = max(maxTok, s.Tokens)
		}
		injItems := []string{st.accent("~"+FmtK(tok)+" tok") + "  "}
		for _, s := range r.Injected {
			item := s.Source + " " + FmtK(s.Tokens)
			if n := barLen(s.Tokens, maxTok); n > 0 {
				item += " " + st.accent(strings.Repeat("▇", n))
			}
			injItems = append(injItems, item)
		}
		lines = append(lines, wrapItems(st.label("injected"), injItems, "  ", width)...)

		var docItems []string
		for _, d := range r.TopSurfaced {
			docItems = append(docItems, fmt.Sprintf("%s ×%d", d.Ref, d.Sessions))
		}
		if len(docItems) == 0 {
			docItems = []string{st.dim("no docs by name")}
		}
		lines = append(lines, wrapItems(st.label("surfaced"), docItems, st.dim(" · "), width)...)
	}
	if r.NotHydrated > 0 {
		lines = append(lines, wrapItems("", []string{st.dim(fmt.Sprintf("%d session%s not hydrated locally, not counted", r.NotHydrated, plural(r.NotHydrated, "", "s")))}, " ", width)...)
	}
	lines = append(lines, wrapItems("", []string{st.dim("aggregate only — for what SageOx delivered, run ox recap")}, " ", width)...)
	return finish(lines)
}
