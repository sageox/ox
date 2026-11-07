// Package contextuse derives, from a session's context-trace events, which
// SageOx context was surfaced to an AI coworker and which of it was actually
// used, and turns that into a display-only attribution category.
//
// The package is pure: it reads events handed to it and does no I/O. Like the
// trace it reads, it handles refs only, never content (ADR-027).
package contextuse

import (
	"strings"

	"github.com/sageox/ox/internal/session/contexttrace"
)

// Via values name the channel that surfaced a ref.
const (
	ViaWhisper   = "whisper"
	ViaPrime     = "prime"
	ViaRecall    = "recall"
	ViaRetrieval = "retrieval"
)

// SurfacedRef is one piece of context that reached the AI coworker.
type SurfacedRef struct {
	Ref      string `json:"ref"`
	RefType  string `json:"ref_type"`
	Via      string `json:"via"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	FromName string `json:"from_name,omitempty"`
	Seq      int    `json:"seq,omitempty"`
	// Aliases are other names for the same item (see CanonicalRef and
	// Names). When an item is surfaced more than once, the aliases of every
	// delivery are unioned here.
	Aliases []string `json:"aliases,omitempty"`
}

// SurfacedOptions tunes Surfaced.
type SurfacedOptions struct {
	// IsRetrieval decides whether an invoked event with a Query was a
	// retrieval call. Nil counts every invoked event with a Query. Callers
	// that can classify commands (usage, via oxactivity) pass their own so
	// this package stays free of that dependency.
	IsRetrieval func(contexttrace.Event) bool
}

// Surfaced lists every ref surfaced by the trace, in trace order, deduped by
// RefType and item identity (any shared canonical name, ignoring case) keeping
// the first occurrence; later deliveries of the same item only add their
// names to its Aliases. It is uncapped; display caps are the caller's concern.
func Surfaced(events []contexttrace.Event) []SurfacedRef {
	return SurfacedWith(events, SurfacedOptions{})
}

// SurfacedWith is Surfaced with options.
func SurfacedWith(events []contexttrace.Event, opts SurfacedOptions) []SurfacedRef {
	isRetrieval := opts.IsRetrieval
	if isRetrieval == nil {
		isRetrieval = func(contexttrace.Event) bool { return true }
	}
	out := []SurfacedRef{}
	index := map[string]int{} // RefType + lowercased canonical name -> out index
	key := func(refType, name string) string { return refType + "\x00" + strings.ToLower(name) }
	add := func(s SurfacedRef) {
		if strings.TrimSpace(s.Ref) == "" {
			return
		}
		names := s.Names()
		for _, n := range names {
			i, ok := index[key(s.RefType, n)]
			if !ok {
				continue
			}
			// Same item again: keep the first delivery, learn its new names.
			for _, raw := range append([]string{s.Ref}, s.Aliases...) {
				if CanonicalRef(s.RefType, raw) != "" && !out[i].Matches(raw) {
					out[i].Aliases = append(out[i].Aliases, raw)
				}
			}
			for _, m := range names {
				index[key(s.RefType, m)] = i
			}
			return
		}
		for _, n := range names {
			index[key(s.RefType, n)] = len(out)
		}
		out = append(out, s)
	}
	for _, ev := range events {
		switch {
		case ev.Type == contexttrace.EventProvided && IsWhisper(ev):
			ref := ev.Ref
			if ref == "" {
				ref = ev.WhisperID
			}
			from := ev.FromName
			if from == "" {
				from = ev.From
			}
			add(SurfacedRef{Ref: ref, RefType: "murmur", Via: ViaWhisper, Title: ev.Title, URL: ev.URL, FromName: from, Seq: ev.Seq, Aliases: aliasesOf(ev)})
		case ev.Type == contexttrace.EventProvided && ev.Source == contexttrace.SourceLocalRecall && ev.Ref != "":
			add(SurfacedRef{Ref: ev.Ref, RefType: orDefault(ev.RefType, "session"), Via: ViaRecall, Title: ev.Title, URL: ev.URL, Seq: ev.Seq, Aliases: aliasesOf(ev)})
		case ev.Type == contexttrace.EventProvided && ev.Mechanism == contexttrace.MechanismRetrieval && ev.Ref != "":
			add(SurfacedRef{Ref: ev.Ref, RefType: orDefault(ev.RefType, "doc"), Via: ViaRetrieval, Title: ev.Title, URL: ev.URL, Seq: ev.Seq, Aliases: aliasesOf(ev)})
		case ev.Type == contexttrace.EventProvided:
			via := ViaPrime
			if ev.Source == contexttrace.SourceLocalRecall {
				via = ViaRecall
			}
			docs := docsOf(ev)
			var aliases []string
			if len(docs) == 1 {
				aliases = aliasesOf(ev) // a multi-doc event's aliases name no one doc
			}
			for _, d := range docs {
				add(SurfacedRef{Ref: d, RefType: "doc", Via: via, Title: ev.Title, URL: ev.URL, Seq: ev.Seq, Aliases: aliases})
			}
		case ev.Type == contexttrace.EventInvoked && ev.Query != "" && isRetrieval(ev):
			add(SurfacedRef{
				Ref: orDefault(ev.Ref, ev.Query), RefType: orDefault(ev.RefType, "query"),
				Via: ViaRetrieval, Title: ev.Title, URL: ev.URL, Seq: ev.Seq, Aliases: aliasesOf(ev),
			})
		}
	}
	return out
}

// IsWhisper reports whether a provided event is a whisper delivery.
func IsWhisper(ev contexttrace.Event) bool {
	return ev.Source == contexttrace.SourceTeamWhisper || ev.Source == contexttrace.SourceProjectWhisper
}

func docsOf(ev contexttrace.Event) []string {
	if ev.Doc != "" {
		return []string{ev.Doc}
	}
	return ev.Docs
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// aliasesOf copies an event's aliases so a SurfacedRef never shares the
// event's backing array (nil stays nil).
func aliasesOf(ev contexttrace.Event) []string {
	if len(ev.Aliases) == 0 {
		return nil
	}
	return append([]string(nil), ev.Aliases...)
}
