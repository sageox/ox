// Package contextuse derives, from a session's context-trace events, which
// SageOx context was surfaced to an AI coworker and which of it was actually
// used, and turns that into a display-only attribution category.
//
// The package is pure: it reads events handed to it and does no I/O. Like the
// trace it reads, it handles refs only, never content (ADR-027).
package contextuse

import "github.com/sageox/ox/internal/session/contexttrace"

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
// (RefType, Ref) keeping the first occurrence. It is uncapped; display caps
// are the caller's concern.
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
	seen := map[string]bool{}
	add := func(s SurfacedRef) {
		if s.Ref == "" {
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
		case ev.Type == contexttrace.EventProvided && IsWhisper(ev):
			ref := ev.Ref
			if ref == "" {
				ref = ev.WhisperID
			}
			from := ev.FromName
			if from == "" {
				from = ev.From
			}
			add(SurfacedRef{Ref: ref, RefType: "murmur", Via: ViaWhisper, Title: ev.Title, URL: ev.URL, FromName: from, Seq: ev.Seq})
		case ev.Type == contexttrace.EventProvided && ev.Source == contexttrace.SourceLocalRecall && ev.Ref != "":
			add(SurfacedRef{Ref: ev.Ref, RefType: orDefault(ev.RefType, "session"), Via: ViaRecall, Title: ev.Title, URL: ev.URL, Seq: ev.Seq})
		case ev.Type == contexttrace.EventProvided && ev.Mechanism == contexttrace.MechanismRetrieval && ev.Ref != "":
			add(SurfacedRef{Ref: ev.Ref, RefType: orDefault(ev.RefType, "doc"), Via: ViaRetrieval, Title: ev.Title, URL: ev.URL, Seq: ev.Seq})
		case ev.Type == contexttrace.EventProvided:
			via := ViaPrime
			if ev.Source == contexttrace.SourceLocalRecall {
				via = ViaRecall
			}
			for _, d := range docsOf(ev) {
				add(SurfacedRef{Ref: d, RefType: "doc", Via: via, Title: ev.Title, URL: ev.URL, Seq: ev.Seq})
			}
		case ev.Type == contexttrace.EventInvoked && ev.Query != "" && isRetrieval(ev):
			add(SurfacedRef{
				Ref: orDefault(ev.Ref, ev.Query), RefType: orDefault(ev.RefType, "query"),
				Via: ViaRetrieval, Title: ev.Title, URL: ev.URL, Seq: ev.Seq,
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
