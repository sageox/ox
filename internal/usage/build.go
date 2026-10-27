package usage

import (
	"encoding/json"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
	"github.com/sageox/ox/internal/weblink"
)

// Input is everything Build needs. The caller resolves it; Build does no I/O.
type Input struct {
	Events []contexttrace.Event

	// Trace says where Events came from. Source "none" means no trace was
	// found; Reason then says why.
	TraceSource string
	TraceReason string

	SessionName string
	AgentID     string
	Harness     string
	Recording   bool // a live recording owns this report's trace
	Turns       int

	ScoreCategory   string
	ScoreReason     string
	ProducedCommits int

	// Endpoint is the project's SageOx web endpoint (https://sageox.ai). When
	// set, surfaced and used discussions, sessions and plans with an opaque id
	// get a web link (see webURL).
	Endpoint string

	// ScoreThreshold is the project's attribution threshold (the minimum
	// category value that earns a commit trailer); 0 → the config default.
	ScoreThreshold float64
	// AllowJudged lets LLM-judge evidence reach the derived attribution
	// category. Judged uses are always listed either way.
	AllowJudged bool

	Now   time.Time
	Width int // display width; 0 → 80

	// RecentLimit caps calls.recent: 0 → the default (10), RecentAll (or any
	// negative value) → every call. Display is unaffected.
	RecentLimit int
}

// RecentAll is the Input.RecentLimit that keeps every call in calls.recent.
const RecentAll = -1

// defaultScoreThreshold mirrors the attribution config default.
const defaultScoreThreshold = 0.5

// primeCommand is the Command prime records for itself.
const primeCommand = "agent prime"

// Build turns a usage trace and recording facts into the session Report.
func Build(in Input) Report {
	r := Report{
		View: ViewSession,
		Session: SessionInfo{
			Name:      in.SessionName,
			AgentID:   in.AgentID,
			Harness:   in.Harness,
			Recording: in.Recording,
			Turns:     in.Turns,
		},
		Trace: TraceInfo{
			Available: in.TraceSource != "" && in.TraceSource != TraceSourceNone,
			Source:    in.TraceSource,
			Reason:    in.TraceReason,
		},
		Surfaced:    []Surfaced{},
		NextActions: []NextAction{},
	}
	if r.Trace.Source == "" {
		r.Trace.Source = TraceSourceNone
	}
	r.Calls = buildCalls(in.Events, in.RecentLimit)
	r.Context = buildContext(in.Events)
	var all []contextuse.SurfacedRef
	r.Surfaced, all = buildSurfaced(in.Events)
	fillURLs(r.Surfaced, in.Endpoint)
	r.Used = buildUsed(in, r.Surfaced)
	rollup := contextuse.Build(in.Events, all, in.ProducedCommits, in.AllowJudged)
	if r.Trace.Available {
		r.Relevant = buildRelevant(rollup, r.Surfaced, all)
		fillItemURLs(r.Relevant.Items, in.Endpoint)
		r.Relevant.ReportedNone = rollup.ReportedNone && r.Relevant.Count == 0 && len(r.Used.SelfReported) == 0
	}
	r.Attribution = buildAttribution(in, rollup)
	r.NextActions = nextActions(r)
	r.Guidance = guidanceText
	r.Display = Display(r, in.Width)
	return r
}

func buildCalls(events []contexttrace.Event, recentLimit int) Calls {
	c := Calls{ByCommand: []CommandStat{}, Recent: []RecentCall{}}
	idx := map[string]int{}
	var all []RecentCall
	for _, ev := range events {
		if ev.Type != contexttrace.EventInvoked {
			continue
		}
		c.Total++
		if ev.Failed {
			c.Failed++
		}
		key := ev.Label + "\x00" + ev.Command
		i, ok := idx[key]
		if !ok {
			i = len(c.ByCommand)
			idx[key] = i
			c.ByCommand = append(c.ByCommand, CommandStat{Label: ev.Label, Command: ev.Command})
		}
		c.ByCommand[i].Count++
		c.ByCommand[i].Tokens += ev.InlineTokens
		if ev.Seq > c.ByCommand[i].LastSeq {
			c.ByCommand[i].LastSeq = ev.Seq
		}
		all = append(all, RecentCall{
			Label: ev.Label, Command: ev.Command, Seq: ev.Seq,
			Tokens: ev.InlineTokens, Failed: ev.Failed, TS: ev.Timestamp,
		})
	}
	sort.SliceStable(c.ByCommand, func(i, j int) bool { return c.ByCommand[i].Count > c.ByCommand[j].Count })
	if len(c.ByCommand) > maxByCmd {
		c.ByCommand = c.ByCommand[:maxByCmd]
	}
	limit := recentLimit
	switch {
	case limit == 0:
		limit = maxRecent
	case limit < 0:
		limit = len(all)
	}
	// newest first: the trace is append-only, so reverse file order.
	for i := len(all) - 1; i >= 0 && len(c.Recent) < limit; i-- {
		c.Recent = append(c.Recent, all[i])
	}
	return c
}

// isWhisper reports whether a provided event is a whisper delivery.
func isWhisper(ev contexttrace.Event) bool {
	return ev.Source == contexttrace.SourceTeamWhisper || ev.Source == contexttrace.SourceProjectWhisper
}

func buildContext(events []contexttrace.Event) Context {
	cx := Context{Estimated: true, BySource: []SourceStat{}, Unmetered: append([]string(nil), Unmetered...)}

	primeTokens := map[string]int{}
	var whisperTokens, whisperCount, callTokens, callCount int
	providedTokens := map[string]int{}
	var providedOrder []string
	sawPrime := false

	for _, ev := range events {
		switch {
		case ev.Type == contexttrace.EventInvoked && ev.Command == primeCommand:
			sawPrime = true
			for src, n := range ev.TokensBySource {
				primeTokens[src] += n
			}
			if len(ev.TokensBySource) == 0 && ev.InlineTokens > 0 {
				primeTokens["total"] += ev.InlineTokens
			}
			cx.Prime = &PrimeBudget{EmittedBytes: ev.OutputBytes, FullBytes: ev.FullBytes, Deferred: ev.Deferred}
		case ev.Type == contexttrace.EventInvoked:
			callTokens += ev.InlineTokens
			callCount++
		case ev.Type == contexttrace.EventProvided && isWhisper(ev):
			whisperTokens += ev.InlineTokens
			whisperCount++
		case ev.Type == contexttrace.EventProvided && ev.InlineTokens > 0:
			src := string(ev.Source)
			if _, ok := providedTokens[src]; !ok {
				providedOrder = append(providedOrder, src)
			}
			providedTokens[src] += ev.InlineTokens
		}
	}

	for src, n := range primeTokens {
		if n > 0 {
			cx.BySource = append(cx.BySource, SourceStat{Source: "prime:" + src, Tokens: n})
		}
	}
	// Prime's own provided events (team instructions, MEMORY.md) are already
	// inside its tokens_by_source. Count them only for traces written before
	// prime recorded its budget, so nothing is counted twice.
	if !sawPrime {
		for _, src := range providedOrder {
			cx.BySource = append(cx.BySource, SourceStat{Source: src, Tokens: providedTokens[src]})
		}
	}
	if whisperCount > 0 {
		cx.BySource = append(cx.BySource, SourceStat{Source: "whispers", Tokens: whisperTokens, Count: whisperCount})
	}
	if callCount > 0 && callTokens > 0 {
		cx.BySource = append(cx.BySource, SourceStat{Source: "ox calls", Tokens: callTokens, Count: callCount})
	}
	sort.SliceStable(cx.BySource, func(i, j int) bool {
		if cx.BySource[i].Tokens != cx.BySource[j].Tokens {
			return cx.BySource[i].Tokens > cx.BySource[j].Tokens
		}
		return cx.BySource[i].Source < cx.BySource[j].Source
	})
	for _, s := range cx.BySource {
		cx.Tokens += s.Tokens
	}
	return cx
}

// buildSurfaced lists what reached the model by name (via contextuse, so the
// report and the rollup agree on what was surfaced), capped for display. The
// uncapped list is returned too so use evidence is matched against all of it.
func buildSurfaced(events []contexttrace.Event) ([]Surfaced, []contextuse.SurfacedRef) {
	all := contextuse.SurfacedWith(events, contextuse.SurfacedOptions{
		IsRetrieval: func(ev contexttrace.Event) bool { return kindOf(ev) == oxactivity.KindRetrieval },
	})
	// Whisper sender id and topic are display fields contextuse does not carry.
	type whisperMeta struct{ from, topic string }
	whispers := map[string]whisperMeta{}
	for _, ev := range events {
		if ev.Type != contexttrace.EventProvided || !isWhisper(ev) {
			continue
		}
		ref := ev.Ref
		if ref == "" {
			ref = ev.WhisperID
		}
		if _, seen := whispers[ref]; !seen {
			whispers[ref] = whisperMeta{ev.From, ev.Topic}
		}
	}
	out := make([]Surfaced, 0, len(all))
	for _, s := range all {
		if len(out) >= maxSurfaced {
			break
		}
		item := Surfaced{
			Ref: s.Ref, RefType: s.RefType, Via: s.Via,
			Title: s.Title, URL: s.URL, FromName: s.FromName, Seq: s.Seq,
		}
		if s.Via == contextuse.ViaWhisper {
			item.From, item.Topic = whispers[s.Ref].from, whispers[s.Ref].topic
		}
		out = append(out, item)
	}
	return out, all
}

// kindOf re-derives an invoked event's Kind from its recorded command words —
// the trace stores the words, and oxactivity owns what they mean.
func kindOf(ev contexttrace.Event) oxactivity.Kind {
	if ev.Command == "" {
		return oxactivity.KindOther
	}
	var act oxactivity.Activity
	var ok bool
	if tool, isMCP := strings.CutPrefix(ev.Command, "mcp "); isMCP {
		act, ok = oxactivity.Classify("mcp__sageox__"+tool, nil)
	} else {
		act, ok = oxactivity.ClassifyCommand("ox " + ev.Command)
	}
	if !ok {
		return oxactivity.KindOther
	}
	return act.Kind
}

// isSelfReport reports whether an event is the AI coworker's own attribution:
// a used event it reported at the moment of use, or a legacy influenced event
// (those carry no mechanism; they were written by the same AI-coworker-reported
// path, so they count as self-assessed too).
func isSelfReport(ev contexttrace.Event) bool {
	switch ev.Type {
	case contexttrace.EventUsed:
		return ev.Evidence == contexttrace.EvidenceReported ||
			(ev.Evidence == "" && (ev.Mechanism == contexttrace.MechanismSelfReport || ev.Mechanism == ""))
	case contexttrace.EventInfluenced:
		return ev.Mechanism == contexttrace.MechanismSelfReport || ev.Mechanism == ""
	}
	return false
}

func buildUsed(in Input, surfaced []Surfaced) Used {
	u := Used{
		SelfReported:    []SelfReported{},
		Retrieval:       []Retrieval{},
		Consulted:       []Consulted{},
		ProducedCommits: in.ProducedCommits,
	}
	surfacedRefs := map[string]bool{}
	for _, s := range surfaced {
		surfacedRefs[s.Ref] = true
	}
	for _, ev := range in.Events {
		switch {
		case isSelfReport(ev):
			ref := ev.Ref
			if ref == "" {
				ref = ev.Doc
			}
			if ref == "" {
				continue
			}
			u.SelfReported = append(u.SelfReported, SelfReported{
				Ref: ref, RefType: ev.RefType, Decision: ev.Decision,
				Grade: "self-assessed", MatchedSurfaced: surfacedRefs[ref],
			})
		case ev.Type == contexttrace.EventInvoked && ev.Command != primeCommand && kindOf(ev) == oxactivity.KindRetrieval:
			u.Retrieval = append(u.Retrieval, Retrieval{Label: ev.Label, Query: ev.Query, Seq: ev.Seq, Grade: "retrieval"})
		case ev.Type == contexttrace.EventConsulted:
			u.Consulted = append(u.Consulted, Consulted{
				Ref: ev.Ref, RefType: ev.RefType, Mechanism: string(ev.Mechanism), Seq: ev.Seq,
			})
		}
	}
	if in.ScoreCategory != "" {
		u.SageoxScore = &Score{Category: in.ScoreCategory, Reason: in.ScoreReason}
	}
	if in.Recording && in.AgentID != "" {
		ref := "SURFACED_REF"
		if len(surfaced) > 0 {
			ref = surfaced[0].Ref
		}
		u.SelfReport = &SelfReportCmd{
			Command:          "ox agent " + in.AgentID + " context used " + ref + " --why \"ONE LINE: what you did because of it\"",
			BatchCommand:     "ox agent " + in.AgentID + " session context-trace",
			BatchExampleLine: exampleLine(surfaced),
		}
	}
	return u
}

// buildRelevant turns the use-evidence rollup into the report's relevant
// section and marks each displayed surfaced item with its strongest evidence.
func buildRelevant(rollup contextuse.Rollup, surfaced []Surfaced, all []contextuse.SurfacedRef) *Relevant {
	rel := &Relevant{
		Count:         rollup.UsedProvable + rollup.UsedReported + rollup.UsedJudged,
		SurfacedCount: rollup.Surfaced,
		ByEvidence: map[string]int{
			contextuse.BucketRead: 0, contextuse.BucketCited: 0,
			contextuse.BucketReported: 0, contextuse.BucketJudged: 0,
		},
		Items:   []RelevantItem{},
		Derived: &Derived{Category: rollup.DerivedCategory, Reason: rollup.DerivedReason},
	}
	for k, n := range rollup.ByEvidence {
		rel.ByEvidence[k] = n
	}
	for _, u := range rollup.Refs {
		item := RelevantItem{
			Ref: u.Ref, RefType: u.RefType,
			Evidence: contextuse.BucketOf(u.Evidence), Detail: u.Evidence, Grade: u.Grade,
			Seq: u.Seq, Why: capRunes(u.Decision, maxWhyRunes),
		}
		if u.Evidence == contexttrace.EvidenceCitedCommit {
			item.Commit = shortSHA(u.Commit)
		}
		for i := range surfaced {
			if !sameRef(surfaced[i].Ref, u.Ref) {
				continue
			}
			if surfaced[i].Used == nil {
				surfaced[i].Used = &ItemUsed{Evidence: item.Evidence, Seq: u.Seq, Grade: u.Grade}
			}
			if item.Title == "" {
				item.Title, item.URL = surfaced[i].Title, surfaced[i].URL
			}
		}
		if item.Title == "" {
			for _, s := range all {
				if sameRef(s.Ref, u.Ref) {
					item.Title, item.URL = s.Title, s.URL
					break
				}
			}
		}
		rel.Items = append(rel.Items, item)
	}
	// A timeline: earliest use first; refs with equal seq keep rollup order.
	sort.SliceStable(rel.Items, func(i, j int) bool { return rel.Items[i].Seq < rel.Items[j].Seq })
	return rel
}

// maxWhyRunes caps a reported --why; the writer caps it too, this is defense.
const maxWhyRunes = 200

// capRunes trims s to at most n runes.
func capRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// shortSHA is the 7-character form of a commit sha.
func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// sameRef matches a used ref to a surfaced one the way contextuse does: exact
// case-insensitive, else by basename (a read of "/ledger/MEMORY.md" is a use
// of surfaced "MEMORY.md").
func sameRef(surfacedRef, usedRef string) bool {
	if strings.EqualFold(surfacedRef, usedRef) {
		return true
	}
	return strings.EqualFold(path.Base(strings.ReplaceAll(surfacedRef, "\\", "/")),
		path.Base(strings.ReplaceAll(usedRef, "\\", "/")))
}

// buildAttribution says whether the session clears the project's attribution
// threshold. A manual score (ox session score) is the AI coworker's own call
// and wins over the derived category; both are display-only here.
func buildAttribution(in Input, rollup contextuse.Rollup) Attribution {
	a := Attribution{Threshold: in.ScoreThreshold}
	if a.Threshold == 0 {
		a.Threshold = defaultScoreThreshold
	}
	if in.ScoreCategory != "" {
		a.Source, a.Category, a.Reason = AttributionSelfReported, in.ScoreCategory, in.ScoreReason
	} else {
		a.Source, a.Category, a.Reason = AttributionDerived, rollup.DerivedCategory, rollup.DerivedReason
	}
	a.WouldAttribute = session.CategoryValue(session.ScoreCategory(a.Category)) >= a.Threshold
	return a
}

// exampleLine is one JSONL self-report line, anchored to a real surfaced item
// when there is one (a placeholder otherwise — never an invented item).
func exampleLine(surfaced []Surfaced) string {
	ref, refType := "SURFACED_REF", "doc"
	if len(surfaced) > 0 {
		ref, refType = surfaced[0].Ref, surfaced[0].RefType
	}
	line, _ := json.Marshal(struct {
		Type      string `json:"type"`
		Source    string `json:"source"`
		Mechanism string `json:"mechanism"`
		Ref       string `json:"ref"`
		RefType   string `json:"ref_type"`
		Decision  string `json:"decision"`
	}{"influenced", string(contexttrace.SourceTeamContext), string(contexttrace.MechanismSelfReport), ref, refType, "ONE LINE: what you did because of it"})
	return string(line)
}

func nextActions(r Report) []NextAction {
	out := []NextAction{}
	if !r.Trace.Available {
		out = append(out, NextAction{Action: "ox session start", Why: "record the next session so ox brief can report it"})
	}
	return out
}

// fillURLs gives a surfaced item its web link when the trace did not carry
// one (see webURL).
func fillURLs(surfaced []Surfaced, endpoint string) {
	for i := range surfaced {
		if surfaced[i].URL == "" {
			surfaced[i].URL = webURL(surfaced[i].RefType, surfaced[i].Ref, endpoint)
		}
	}
}

// fillItemURLs does the same for used items the surfaced join left unlinked.
func fillItemURLs(items []RelevantItem, endpoint string) {
	for i := range items {
		if items[i].URL == "" {
			items[i].URL = webURL(items[i].RefType, items[i].Ref, endpoint)
		}
	}
}

// webURL is the one rule for which refs get a sageox.ai link: discussions
// with an opaque cnv_/rec_ id, sessions with a ses_ id (both /c/<id>), and
// pln_ plans. Slugs, docs, paths and murmurs (no web page) never get a
// guessed link; "" when the endpoint is unknown.
func webURL(refType, ref, endpoint string) string {
	if endpoint == "" {
		return ""
	}
	switch refType {
	case "discussion":
		if p := weblink.IDTypePrefix(ref); p == "cnv_" || p == "rec_" {
			return weblink.Conversation(endpoint, ref)
		}
	case "session":
		if weblink.IDTypePrefix(ref) == "ses_" {
			return weblink.Conversation(endpoint, ref)
		}
	case "plan":
		return weblink.Plan(endpoint, ref)
	}
	return ""
}
