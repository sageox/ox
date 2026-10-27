package usage

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session/contexttrace"
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
	// set, surfaced discussions and plans with an opaque id get a web link.
	Endpoint string

	Now   time.Time
	Width int // display width; 0 → 80

	// RecentLimit caps calls.recent: 0 → the default (10), RecentAll (or any
	// negative value) → every call. Display is unaffected.
	RecentLimit int
}

// RecentAll is the Input.RecentLimit that keeps every call in calls.recent.
const RecentAll = -1

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
	r.Surfaced = buildSurfaced(in.Events)
	fillURLs(r.Surfaced, in.Endpoint)
	r.Used = buildUsed(in, r.Surfaced)
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

// docsOf tolerates both the singular Doc and the plural Docs shapes.
func docsOf(ev contexttrace.Event) []string {
	if ev.Doc != "" {
		return []string{ev.Doc}
	}
	return ev.Docs
}

func buildSurfaced(events []contexttrace.Event) []Surfaced {
	out := []Surfaced{}
	seen := map[string]bool{}
	add := func(s Surfaced) {
		if s.Ref == "" || len(out) >= maxSurfaced {
			return
		}
		key := s.RefType + "\x00" + s.Ref
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, s)
	}
	for _, ev := range events {
		switch {
		case ev.Type == contexttrace.EventProvided && isWhisper(ev):
			ref := ev.Ref
			if ref == "" {
				ref = ev.WhisperID
			}
			add(Surfaced{Ref: ref, RefType: "murmur", From: ev.From, Topic: ev.Topic, Via: "whisper"})
		case ev.Type == contexttrace.EventProvided:
			for _, d := range docsOf(ev) {
				add(Surfaced{Ref: d, RefType: "doc", Via: "prime"})
			}
		case ev.Type == contexttrace.EventInvoked && ev.Query != "" && kindOf(ev) == oxactivity.KindRetrieval:
			rt := ev.RefType
			if rt == "" {
				rt = "query"
			}
			add(Surfaced{Ref: ev.Query, RefType: rt, Via: "retrieval"})
		}
	}
	return out
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

// isSelfReport reports whether an influenced event is the AI coworker's own
// attribution. Legacy influenced events carry no mechanism; they were written
// by the same AI-coworker-reported path, so they count as self-assessed too.
func isSelfReport(ev contexttrace.Event) bool {
	return ev.Type == contexttrace.EventInfluenced &&
		(ev.Mechanism == contexttrace.MechanismSelfReport || ev.Mechanism == "")
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
		u.SelfReport = &SelfReportCmd{
			Command:     "ox agent " + in.AgentID + " session context-trace",
			ExampleLine: exampleLine(surfaced),
		}
	}
	return u
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

// fillURLs gives a surfaced discussion or plan its web link when the trace
// did not carry one and its ref is an opaque id the web app resolves. Slugs,
// docs and paths never get a guessed link.
func fillURLs(surfaced []Surfaced, endpoint string) {
	if endpoint == "" {
		return
	}
	for i := range surfaced {
		s := &surfaced[i]
		if s.URL != "" {
			continue
		}
		switch s.RefType {
		case "discussion":
			if p := weblink.IDTypePrefix(s.Ref); p == "cnv_" || p == "rec_" {
				s.URL = weblink.Conversation(endpoint, s.Ref)
			}
		case "plan":
			s.URL = weblink.Plan(endpoint, s.Ref)
		}
	}
}
