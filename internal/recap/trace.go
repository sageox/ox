package recap

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/contexttrace"
)

// docReach accumulates, for one team-context doc, which of the user's sessions
// it was injected into and when. It is the raw material for an ArtifactReach.
type docReach struct {
	Doc        string
	Source     string
	SessionSet map[string]bool
	Samples    []string // session titles, in reach order
	Latest     time.Time
}

// traceScan is the aggregate result of reading every in-scope session's
// context-trace: which docs reached the user's work, plus the coverage
// denominators (how many sessions had a readable trace vs. only a dehydrated
// LFS stub).
type traceScan struct {
	docs             map[string]*docReach
	order            []string // doc names in first-seen order for stable output
	withTraces       int
	tracesDehydrated int
}

// scanTraces reads the context-trace of each session and aggregates the
// team-context docs that prime injected (`provided` events bearing a doc name).
// Whisper/murmur delivery events are intentionally excluded — they carry no
// quotable doc content, and this report is about team KNOWLEDGE, not delivery
// counts. Fail-open per session.
func scanTraces(ledgerPath string, sessions []SessionFacts) traceScan {
	scan := traceScan{docs: map[string]*docReach{}}
	for _, s := range sessions {
		events, dehydrated := readTrace(ledgerPath, s.Name)
		if dehydrated {
			scan.tracesDehydrated++
			continue
		}
		if len(events) == 0 {
			continue
		}
		scan.withTraces++
		for _, ev := range events {
			if ev.Type != contexttrace.EventProvided {
				continue
			}
			for _, doc := range docsOf(ev) {
				scan.record(doc, string(ev.Source), s.displayTitle(), parseTS(ev.Timestamp))
			}
		}
	}
	return scan
}

// record folds one (doc, session) reach into the aggregate.
func (scan *traceScan) record(doc, source, sessionTitle string, when time.Time) {
	dr, ok := scan.docs[doc]
	if !ok {
		dr = &docReach{Doc: doc, Source: source, SessionSet: map[string]bool{}}
		scan.docs[doc] = dr
		scan.order = append(scan.order, doc)
	}
	if sessionTitle != "" && !dr.SessionSet[sessionTitle] {
		dr.SessionSet[sessionTitle] = true
		if len(dr.Samples) < maxSampleWork {
			dr.Samples = append(dr.Samples, sessionTitle)
		}
	}
	if when.After(dr.Latest) {
		dr.Latest = when
	}
}

// sorted returns the doc reaches ordered by how many sessions each touched
// (most-reaching first), so the most load-bearing team knowledge leads.
func (scan traceScan) sorted() []*docReach {
	out := make([]*docReach, 0, len(scan.order))
	for _, d := range scan.order {
		out = append(out, scan.docs[d])
	}
	sort.SliceStable(out, func(i, j int) bool {
		return len(out[i].SessionSet) > len(out[j].SessionSet)
	})
	return out
}

// readTrace resolves a session's context-trace with cache-first, hydration-never
// semantics. Returns (events, dehydrated). `dehydrated` is true when the trace
// exists only as an LFS stub not present locally — counted, never fetched (a
// recap is offline and fast by contract).
func readTrace(ledgerPath, sessionName string) (events []contexttrace.Event, dehydrated bool) {
	// 1. ledger cache holds hydrated bytes (recordings land here pre-upload)
	cachePath := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName, contexttrace.FileName)
	if info, err := os.Stat(cachePath); err == nil && info.Size() > 0 {
		ev, _ := contexttrace.ReadEvents(cachePath)
		return ev, false
	}

	// 2. in-place file, only when it is real content (not an LFS pointer)
	inPlace := filepath.Join(ledgerPath, "sessions", sessionName, contexttrace.FileName)
	info, err := os.Stat(inPlace)
	if err != nil || info.Size() == 0 {
		return nil, false // no trace artifact at all
	}
	if !lfs.IsPointerFile(inPlace) {
		ev, _ := contexttrace.ReadEvents(inPlace)
		return ev, false
	}

	// 3. in-place is a pointer and cache is empty — evidence exists in LFS but
	//    is not downloaded. Count it as dehydrated; never hydrate here.
	return nil, true
}

// docsOf returns the doc name(s) a provided event carries, tolerating both the
// singular Doc and the plural Docs shapes.
func docsOf(ev contexttrace.Event) []string {
	if ev.Doc != "" {
		return []string{ev.Doc}
	}
	return ev.Docs
}

// displayTitle is the human-facing label for a session: its title, or the
// folder name when the summary hasn't produced one yet.
func (s SessionFacts) displayTitle() string {
	if s.Title != "" {
		return s.Title
	}
	return s.Name
}

// parseTS parses an RFC3339 context-trace timestamp, returning the zero time on
// failure (callers treat zero as "unknown", never as epoch).
func parseTS(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}
