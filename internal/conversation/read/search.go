package read

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sageox/ox/internal/conversation/format"
	"github.com/sageox/ox/internal/vtt"
)

// Search disclosure caps.
const (
	DefaultSearchLimit = 10
	MaxSearchLimit     = 50
	// maxHitsPerResult keeps one result to a few lines an AI coworker can
	// act on; the transcript command is the way to read further.
	maxHitsPerResult = 3
	// snippetRunes is the snippet budget. Long enough to recognize the
	// moment (the web's 200-char cap is the complaint this replaces), short
	// enough that ten results stay cheap.
	snippetRunes = 280
	// duplicateWindow: two recordings of one meeting (two laptops, the dot
	// and a laptop) start within minutes of each other.
	duplicateWindow = 15 * time.Minute
)

// Hit kinds, in the order a person recognizes a meeting by.
const (
	HitChapter    = "chapter"
	HitTranscript = "transcript"
	HitDecision   = "decision"
	HitActionItem = "action_item"
	HitSummary    = "summary"
)

// Match modes reported in SearchData.Match.
const (
	MatchAllTerms    = "all_terms"
	MatchAnyTerm     = "any_term"
	MatchFiltersOnly = "filters_only"
)

// SearchOptions narrows a search. Query is keywords; conversational filler
// ("what did we talk about") is dropped before matching. At least one
// keyword or one filter is required.
type SearchOptions struct {
	Query string
	// Participants: every value must match (case-insensitive substring) a
	// summary participant or a transcript speaker of the conversation.
	Participants []string
	// Speaker restricts keyword matching to what that person said in the
	// transcript; summary fields stop counting.
	Speaker string
	// Since/Until bound the recording instant (zero = unbounded).
	Since, Until time.Time
	Limit        int
}

// SearchHit is one place in a conversation that matched. Citation is a
// sageox:// URI that ox conversation transcript accepts as-is, landing on
// the matched cues.
type SearchHit struct {
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Speaker  string `json:"speaker,omitempty"`
	Start    string `json:"start,omitempty"`
	Cues     []int  `json:"cues,omitempty"`
	Citation string `json:"citation"`
}

// SearchResult is one matching conversation.
type SearchResult struct {
	ConversationID string   `json:"conversation_id"`
	RecordingID    string   `json:"recording_id"`
	Title          string   `json:"title"`
	RecordedAt     string   `json:"recorded_at,omitempty"`
	Participants   []string `json:"participants,omitempty"`
	// Speakers are the transcript's voice tags resolved to display names —
	// who actually spoke, as opposed to the summary's inferred participants.
	Speakers  []string    `json:"speakers,omitempty"`
	Topics    []string    `json:"topics,omitempty"`
	Score     float64     `json:"score"`
	MatchedIn []string    `json:"matched_in,omitempty"`
	Hits      []SearchHit `json:"hits"`
	// AlsoRecordedAs lists other recordings of the same meeting, folded
	// into this result so one meeting is one row.
	AlsoRecordedAs []string `json:"also_recorded_as,omitempty"`
}

// SearchData is the search envelope payload.
type SearchData struct {
	// Terms are the keywords actually matched, after filler words are
	// dropped — so a caller can see what the query meant.
	Terms   []string       `json:"terms,omitempty"`
	Match   string         `json:"match"`
	Results []SearchResult `json:"results"`
	// Searched counts conversations inside the filters; Summarized counts
	// every summarized conversation on disk.
	Searched   int  `json:"searched"`
	Summarized int  `json:"summarized"`
	Truncated  bool `json:"truncated,omitempty"`
}

// Search finds conversations by keyword, participant, speaker and date over
// every summarized conversation in the team context — not only the ones
// INDEX.json lists (see catalogEntry). It is lexical on purpose (ox
// ADR-024: agentic and tool-driven, not vector RAG): an AI coworker reads the
// hits, then descends with transcript --cues or show. Semantic recall over
// the whole team context stays with ox query.
func (r *Reader) Search(opts SearchOptions) *Envelope {
	start := r.now()
	terms := withoutPeople(searchTerms(opts.Query), append([]string{opts.Speaker}, opts.Participants...))
	hasFilter := len(opts.Participants) > 0 || opts.Speaker != "" || !opts.Since.IsZero() || !opts.Until.IsZero()
	if len(terms) == 0 && !hasFilter {
		msg := "nothing to search for: pass keywords, or at least one of --participant, --speaker, --since, --until"
		if strings.TrimSpace(opts.Query) != "" {
			msg = fmt.Sprintf("the query %q has no keywords once filler words are dropped; name the concept (e.g. \"search files\")", truncateID(opts.Query))
		}
		return r.finishError(start, newError(ErrCodeInvalidSelector, msg), nil)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}

	root, rootErr := r.openDiscussionsRoot()
	if rootErr != nil {
		return r.finishError(start, rootErr, nil)
	}
	if root != nil {
		defer root.Close()
	}
	catalog, unreadable, catErr := loadCatalog(root, folderInWindow(opts.Since, opts.Until))
	if catErr != nil {
		return r.finishError(start, catErr, nil)
	}
	var warnings []string
	if unreadable > 0 {
		warnings = append(warnings, fmt.Sprintf("%d discussion folder(s) could not be read and were skipped", unreadable))
	}

	docs := make([]*searchDoc, 0, len(catalog))
	for i := range catalog {
		e := &catalog[i]
		if !opts.Since.IsZero() && (e.recordedAt.IsZero() || e.recordedAt.Before(opts.Since)) {
			continue
		}
		if !opts.Until.IsZero() && (e.recordedAt.IsZero() || !e.recordedAt.Before(opts.Until)) {
			continue
		}
		docs = append(docs, newSearchDoc(e))
	}
	// Transcripts are read only when something needs them: keywords, or a
	// participant/speaker filter that may only be satisfied by who spoke.
	if len(terms) > 0 || len(opts.Participants) > 0 || opts.Speaker != "" {
		for _, d := range docs {
			d.loadCues(root)
		}
		resolveSpeakers(root, docs)
	}

	var kept []*searchDoc
	for _, d := range docs {
		if d.matchesParticipants(opts.Participants) && d.selectSpeaker(opts.Speaker) {
			kept = append(kept, d)
		}
	}

	data := &SearchData{Terms: terms, Searched: len(kept), Summarized: len(catalog)}
	var scored []*searchDoc
	switch {
	case len(terms) == 0:
		data.Match = MatchFiltersOnly
		scored = kept
	default:
		data.Match = MatchAllTerms
		for _, d := range kept {
			if d.score(terms, true) {
				scored = append(scored, d)
			}
		}
		if len(scored) == 0 && len(terms) > 1 {
			data.Match = MatchAnyTerm
			for _, d := range kept {
				if d.score(terms, false) {
					scored = append(scored, d)
				}
			}
			if len(scored) > 0 {
				warnings = append(warnings, fmt.Sprintf("no conversation matched all of %s; showing conversations that match some of them", strings.Join(terms, ", ")))
			}
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].total != scored[j].total {
			return scored[i].total > scored[j].total
		}
		return scored[i].entry.recordedAt.After(scored[j].entry.recordedAt)
	})

	results := foldDuplicates(scored, terms)
	if len(results) > limit {
		results = results[:limit]
		data.Truncated = true
	}
	data.Results = results
	return r.finishSuccess(start, data, searchGuidance(results), warnings)
}

// folderInWindow pre-filters folders by the date their name starts with.
// The name is when the folder was created, not the exact recording instant,
// so the window is widened by a day on each side; the exact bound is applied
// after the summary is read. Names without a date are always read.
func folderInWindow(since, until time.Time) func(string) bool {
	if since.IsZero() && until.IsZero() {
		return nil
	}
	return func(folder string) bool {
		t, ok := folderNameDate(folder)
		if !ok {
			return true
		}
		if !since.IsZero() && t.Before(since.Add(-24*time.Hour)) {
			return false
		}
		if !until.IsZero() && t.After(until.Add(24*time.Hour)) {
			return false
		}
		return true
	}
}

func searchGuidance(results []SearchResult) string {
	if len(results) == 0 {
		return `No conversation matched. Try fewer or different keywords, widen --since/--until, or ox query "<question>" for semantic search across team context.`
	}
	top := results[0]
	target := top.ConversationID
	for _, h := range top.Hits {
		if len(h.Cues) > 0 {
			target = h.Citation
			break
		}
	}
	return fmt.Sprintf("Read around a hit: ox conversation transcript %q. Summary: ox conversation show %s.", target, top.ConversationID)
}

// --- query terms ---

// fillerWords are dropped from the query: they appear in how a person asks
// ("what did I talk to Ajit about…") but not in what was said, and an
// all-terms match would otherwise fail on them.
var fillerWords = map[string]bool{
	"a": true, "about": true, "after": true, "ago": true, "all": true, "an": true, "and": true,
	"any": true, "are": true, "as": true, "at": true, "be": true, "before": true, "but": true,
	"by": true, "can": true, "conversation": true, "conversations": true, "could": true,
	"day": true, "days": true, "did": true, "discuss": true, "discussed": true, "discussion": true,
	"do": true, "does": true, "for": true, "from": true, "had": true, "has": true, "have": true,
	"he": true, "her": true, "him": true, "his": true, "how": true, "i": true, "if": true,
	"in": true, "into": true, "is": true, "it": true, "its": true, "last": true, "me": true,
	"meeting": true, "meetings": true, "mention": true, "mentioned": true, "month": true,
	"months": true, "my": true, "of": true, "on": true, "or": true, "our": true, "said": true,
	"say": true, "she": true, "should": true, "so": true, "talk": true, "talked": true,
	"that": true, "the": true, "their": true, "them": true, "then": true, "there": true,
	"they": true, "this": true, "to": true, "us": true, "was": true, "we": true, "week": true,
	"weeks": true, "were": true, "what": true, "when": true, "where": true, "which": true,
	"who": true, "whom": true, "why": true, "will": true, "with": true, "would": true,
	"yesterday": true, "you": true, "your": true,
}

// maxSearchTerms bounds the work a pasted paragraph can cause.
const maxSearchTerms = 12

func searchTerms(query string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range words(query) {
		if fillerWords[w] || seen[w] || len(out) >= maxSearchTerms {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// withoutPeople drops query words that name a --participant or --speaker:
// "what did I talk to Ajit about search --participant Ajit" is about search,
// with Ajit as the filter. Kept as a keyword, "ajit" would rank every cue
// that merely mentions him.
func withoutPeople(terms, people []string) []string {
	names := map[string]bool{}
	for _, p := range people {
		for _, w := range words(p) {
			names[w] = true
		}
	}
	out := terms[:0:0]
	for _, t := range terms {
		if !names[t] {
			out = append(out, t)
		}
	}
	return out
}

// words lowercases text and splits it on anything that is not a letter or a
// digit.
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// termMatches is prefix matching on word boundaries, so "search" finds
// "searching" and "searches"; short terms must match whole words, or "ai"
// would match "aim" and "air".
func termMatches(word, term string) bool {
	if utf8.RuneCountInString(term) < 4 {
		return word == term
	}
	return strings.HasPrefix(word, term)
}

func countMatches(ws []string, term string) int {
	n := 0
	for _, w := range ws {
		if termMatches(w, term) {
			n++
		}
	}
	return n
}

// --- per-conversation document ---

type searchCue struct {
	cue   vtt.Cue
	words []string
	// name is the resolved speaker display name ("" when unknown).
	name string
}

type searchField struct {
	name   string
	weight float64
	words  []string
}

type searchDoc struct {
	entry  *catalogEntry
	fields []searchField
	cues   []searchCue
	// uids are the opaque speaker ids this transcript uses.
	uids map[string]bool
	// speakerOnly restricts cue matching to one speaker (--speaker).
	speakerOnly map[int]bool

	total     float64
	matchedIn []string
}

// Field weights: where a word appears says how central it was to the
// meeting. A title or topic hit beats a passing mention in the transcript.
func newSearchDoc(e *catalogEntry) *searchDoc {
	s := e.summary
	d := &searchDoc{entry: e}
	add := func(name string, weight float64, text string) {
		if text != "" {
			d.fields = append(d.fields, searchField{name: name, weight: weight, words: words(text)})
		}
	}
	add("title", 6, s.Title)
	add("topics", 5, strings.Join(s.Topics, " "))
	var chapterTitles, chapterSummaries, decisions, actions strings.Builder
	for _, c := range s.Chapters {
		chapterTitles.WriteString(c.Title + " ")
		chapterSummaries.WriteString(c.Summary + " ")
	}
	for _, x := range s.Decisions {
		decisions.WriteString(x.Description + " ")
	}
	for _, x := range s.ActionItems {
		actions.WriteString(x.Description + " ")
	}
	add("chapters", 4, chapterTitles.String())
	add("decisions", 3, decisions.String())
	add("action_items", 3, actions.String())
	add("summary", 2, s.HumanSummary+" "+chapterSummaries.String())
	return d
}

func (d *searchDoc) loadCues(root *os.Root) {
	if root == nil {
		return
	}
	droot, err := openDiscussion(root, d.entry.folder)
	if err != nil || droot == nil {
		return
	}
	defer droot.Close()
	raw, rErr := readDiscussionFile(droot, format.TranscriptFileName)
	if rErr != nil {
		return // no transcript yet: the summary fields still search
	}
	cues, pErr := vtt.Parse(raw)
	if pErr != nil {
		return
	}
	d.cues = make([]searchCue, 0, len(cues))
	for _, c := range cues {
		sc := searchCue{cue: c, words: words(c.Text)}
		if isOpaqueUserID(c.Speaker) {
			if d.uids == nil {
				d.uids = map[string]bool{}
			}
			d.uids[c.Speaker] = true
		} else {
			sc.name = c.Speaker // older transcripts carry the name itself
		}
		d.cues = append(d.cues, sc)
	}
}

func isOpaqueUserID(s string) bool {
	return strings.HasPrefix(s, "usr_") && len(s) > len("usr_")
}

// resolveSpeakers maps every opaque speaker id used by docs to a display
// name. For each unresolved id it reads the word timeline of the newest
// conversation that id spoke in; that read resolves every other wanted id it
// sees too, so a team of N people costs at most N timeline reads, and
// usually one or two.
func resolveSpeakers(root *os.Root, docs []*searchDoc) {
	if root == nil {
		return
	}
	newestFor := map[string]*searchDoc{}
	for _, d := range docs {
		for uid := range d.uids {
			if cur, ok := newestFor[uid]; !ok || d.entry.recordedAt.After(cur.entry.recordedAt) {
				newestFor[uid] = d
			}
		}
	}
	names := map[string]string{}
	// Deterministic order: newest source conversation first.
	uids := make([]string, 0, len(newestFor))
	for uid := range newestFor {
		uids = append(uids, uid)
	}
	sort.Slice(uids, func(i, j int) bool {
		ti, tj := newestFor[uids[i]].entry.recordedAt, newestFor[uids[j]].entry.recordedAt
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return uids[i] < uids[j]
	})
	tried := map[string]bool{}
	for _, uid := range uids {
		if _, ok := names[uid]; ok {
			continue
		}
		src := newestFor[uid]
		if tried[src.entry.folder] {
			continue
		}
		tried[src.entry.folder] = true
		want := map[string]bool{}
		for u := range src.uids {
			if _, ok := names[u]; !ok {
				want[u] = true
			}
		}
		droot, err := openDiscussion(root, src.entry.folder)
		if err != nil || droot == nil {
			continue
		}
		found, _ := format.LoadSpeakerNamesIn(droot, want)
		droot.Close()
		for u, n := range found {
			names[u] = n
		}
	}
	for _, d := range docs {
		for i := range d.cues {
			if n, ok := names[d.cues[i].cue.Speaker]; ok {
				d.cues[i].name = n
			}
		}
	}
}

func (d *searchDoc) speakerNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range d.cues {
		if c.name != "" && !seen[c.name] {
			seen[c.name] = true
			out = append(out, c.name)
		}
	}
	return out
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(strings.TrimSpace(needle)))
}

func (d *searchDoc) matchesParticipants(want []string) bool {
	names := append(d.entry.summary.ParticipantNames(), d.speakerNames()...)
	for _, p := range want {
		ok := false
		for _, n := range names {
			if containsFold(n, p) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// selectSpeaker applies --speaker: the conversation qualifies only when that
// person spoke in it, and from here on only their cues are matched.
func (d *searchDoc) selectSpeaker(speaker string) bool {
	if speaker == "" {
		return true
	}
	d.speakerOnly = map[int]bool{}
	for i, c := range d.cues {
		if c.name != "" && containsFold(c.name, speaker) {
			d.speakerOnly[i] = true
		}
	}
	return len(d.speakerOnly) > 0
}

func (d *searchDoc) cueEligible(i int) bool {
	return d.speakerOnly == nil || d.speakerOnly[i]
}

// score computes the relevance of d for terms and reports whether d
// matches: every term somewhere (all=true) or at least one (all=false).
func (d *searchDoc) score(terms []string, all bool) bool {
	d.total, d.matchedIn = 0, nil
	matchedFields := map[string]bool{}
	matchedTerms := 0
	for _, t := range terms {
		termHit := false
		if d.speakerOnly == nil {
			for _, f := range d.fields {
				if n := countMatches(f.words, t); n > 0 {
					d.total += f.weight * float64(min(n, 3))
					matchedFields[f.name] = true
					termHit = true
				}
			}
		}
		cueHits := 0
		for i, c := range d.cues {
			if d.cueEligible(i) {
				cueHits += countMatches(c.words, t)
			}
		}
		if cueHits > 0 {
			d.total += 1.5 * math.Log2(1+float64(cueHits))
			matchedFields["transcript"] = true
			termHit = true
		}
		if termHit {
			matchedTerms++
		}
	}
	if matchedTerms == 0 || (all && matchedTerms < len(terms)) {
		return false
	}
	// Terms said together in one breath are the strongest signal there is.
	together := 0
	for i, c := range d.cues {
		if d.cueEligible(i) && distinctTerms(c.words, terms) == len(terms) && len(terms) > 1 {
			together++
		}
	}
	d.total += 2 * float64(min(together, 3))
	for _, name := range []string{"title", "topics", "chapters", "decisions", "action_items", "summary", "transcript"} {
		if matchedFields[name] {
			d.matchedIn = append(d.matchedIn, name)
		}
	}
	return true
}

func distinctTerms(ws []string, terms []string) int {
	n := 0
	for _, t := range terms {
		if countMatches(ws, t) > 0 {
			n++
		}
	}
	return n
}

// --- hits ---

func (d *searchDoc) citation(first, last int) string {
	cid := d.entry.id.ConversationID
	switch {
	case first <= 0:
		return "sageox://" + cid
	case last <= first:
		return fmt.Sprintf("sageox://%s#cue=%d", cid, first)
	default:
		return fmt.Sprintf("sageox://%s#cue=%d-%d", cid, first, last)
	}
}

func (d *searchDoc) hits(terms []string) []SearchHit {
	s := d.entry.summary
	var out []SearchHit
	if len(terms) == 0 {
		if s.HumanSummary != "" {
			out = append(out, SearchHit{Kind: HitSummary, Text: snippet(s.HumanSummary, nil), Citation: d.citation(0, 0)})
		}
		return out
	}

	// Best chapter: the one covering the most terms, title hits first. Its
	// cue range is the "take me to that part of the meeting" link.
	if d.speakerOnly == nil {
		bestIdx, bestScore := -1, 0
		for i, c := range s.Chapters {
			sc := 2*distinctTerms(words(c.Title), terms) + distinctTerms(words(c.Summary), terms)
			if sc > bestScore {
				bestIdx, bestScore = i, sc
			}
		}
		if bestIdx >= 0 {
			c := s.Chapters[bestIdx]
			h := SearchHit{Kind: HitChapter, Text: c.Title + " — " + snippet(c.Summary, terms)}
			if len(c.CueRange) == 2 && c.CueRange[0] >= 1 && c.CueRange[1] >= c.CueRange[0] && c.CueRange[1] <= len(d.cues) {
				h.Cues = []int{c.CueRange[0], c.CueRange[1]}
				h.Start = formatVTTTimestamp(d.cues[c.CueRange[0]-1].cue.Start)
			}
			h.Citation = d.citation(firstOr0(h.Cues), lastOr0(h.Cues))
			out = append(out, h)
		}
	}

	// Best transcript moments: most distinct terms, then most matches.
	type ranked struct{ idx, distinct, count int }
	var rs []ranked
	for i, c := range d.cues {
		if !d.cueEligible(i) {
			continue
		}
		if dt := distinctTerms(c.words, terms); dt > 0 {
			cnt := 0
			for _, t := range terms {
				cnt += countMatches(c.words, t)
			}
			rs = append(rs, ranked{i, dt, cnt})
		}
	}
	sort.SliceStable(rs, func(a, b int) bool {
		if rs[a].distinct != rs[b].distinct {
			return rs[a].distinct > rs[b].distinct
		}
		return rs[a].count > rs[b].count
	})
	var used []int
	for _, r := range rs {
		if len(out) >= maxHitsPerResult {
			break
		}
		near := false
		for _, u := range used {
			if abs(u-r.idx) <= 3 {
				near = true
				break
			}
		}
		if near {
			continue
		}
		used = append(used, r.idx)
		out = append(out, d.cueHit(r.idx, terms))
	}

	if len(out) == 0 {
		for _, x := range s.Decisions {
			if distinctTerms(words(x.Description), terms) > 0 {
				out = append(out, SearchHit{Kind: HitDecision, Text: snippet(x.Description, terms), Speaker: x.Owner, Citation: d.citation(0, 0)})
				break
			}
		}
		for _, x := range s.ActionItems {
			if len(out) < maxHitsPerResult && distinctTerms(words(x.Description), terms) > 0 {
				out = append(out, SearchHit{Kind: HitActionItem, Text: snippet(x.Description, terms), Speaker: x.Assignee, Citation: d.citation(0, 0)})
				break
			}
		}
		if len(out) == 0 && s.HumanSummary != "" {
			out = append(out, SearchHit{Kind: HitSummary, Text: snippet(s.HumanSummary, terms), Citation: d.citation(0, 0)})
		}
	}
	return out
}

// cueHit renders a transcript moment. Transcription splits speech into short
// cues, so a thin cue borrows the same speaker's following cues until the
// snippet reads as a sentence; the citation spans every cue shown.
func (d *searchDoc) cueHit(i int, terms []string) SearchHit {
	first := d.cues[i]
	text := first.cue.Text
	last := i
	for j := i + 1; j < len(d.cues) && utf8.RuneCountInString(text) < 120 && j-i < 4; j++ {
		if d.cues[j].cue.Speaker != first.cue.Speaker {
			break
		}
		text += " " + d.cues[j].cue.Text
		last = j
	}
	speaker := first.name
	if speaker == "" {
		speaker = first.cue.Speaker
	}
	return SearchHit{
		Kind:     HitTranscript,
		Text:     snippet(text, terms),
		Speaker:  speaker,
		Start:    formatVTTTimestamp(first.cue.Start),
		Cues:     []int{first.cue.Index, d.cues[last].cue.Index},
		Citation: d.citation(first.cue.Index, d.cues[last].cue.Index),
	}
}

func firstOr0(c []int) int {
	if len(c) == 0 {
		return 0
	}
	return c[0]
}

func lastOr0(c []int) int {
	if len(c) == 0 {
		return 0
	}
	return c[len(c)-1]
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// snippet trims text to the snippet budget, centered on the first term it
// contains, cut on word boundaries and marked with ellipses where cut.
func snippet(text string, terms []string) string {
	text = strings.Join(strings.Fields(text), " ")
	rs := []rune(text)
	if len(rs) <= snippetRunes {
		return text
	}
	lower := []rune(strings.ToLower(text))
	at := 0
	if len(lower) == len(rs) { // lowering never changed the rune count
		best := -1
		for _, t := range terms {
			if k := runeIndex(lower, []rune(t)); k >= 0 && (best < 0 || k < best) {
				best = k
			}
		}
		if best > 0 {
			at = best
		}
	}
	from := max(0, at-snippetRunes/3)
	to := min(len(rs), from+snippetRunes)
	from = max(0, to-snippetRunes)
	// Snap to word boundaries only inside the window: text with no
	// whitespace there (CJK, a long URL or log line) keeps the hard cut.
	if from > 0 {
		f := from
		for f < to && !unicode.IsSpace(rs[f-1]) {
			f++
		}
		if f < to {
			from = f
		}
	}
	if to < len(rs) {
		t := to
		for t > from && !unicode.IsSpace(rs[t]) {
			t--
		}
		if t > from {
			to = t
		}
	}
	out := strings.TrimSpace(string(rs[from:to]))
	if from > 0 {
		out = "…" + out
	}
	if to < len(rs) {
		out += "…"
	}
	return out
}

func runeIndex(haystack, needle []rune) int {
	if len(needle) == 0 {
		return -1
	}
outer:
	for i := 0; i+len(needle) <= len(haystack); i++ {
		for j := range needle {
			if haystack[i+j] != needle[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

// --- duplicate recordings ---

// foldDuplicates turns ranked docs into results, folding a recording into a
// higher-ranked one when both are the same meeting: started within
// duplicateWindow and sharing a title or most of their participants.
func foldDuplicates(ranked []*searchDoc, terms []string) []SearchResult {
	type kept struct {
		doc *searchDoc
		res SearchResult
	}
	var out []kept
	for _, d := range ranked {
		dup := false
		for k := range out {
			if sameMeeting(out[k].doc, d) {
				out[k].res.AlsoRecordedAs = append(out[k].res.AlsoRecordedAs, d.entry.id.ConversationID)
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, kept{doc: d, res: d.result(terms)})
		}
	}
	results := make([]SearchResult, 0, len(out))
	for _, k := range out {
		results = append(results, k.res)
	}
	return results
}

func sameMeeting(a, b *searchDoc) bool {
	ta, tb := a.entry.recordedAt, b.entry.recordedAt
	if ta.IsZero() || tb.IsZero() {
		return false
	}
	if d := ta.Sub(tb); d > duplicateWindow || d < -duplicateWindow {
		return false
	}
	if t := strings.TrimSpace(a.entry.summary.Title); t != "" && strings.EqualFold(t, strings.TrimSpace(b.entry.summary.Title)) {
		return true
	}
	pa := lowerSet(append(a.entry.summary.ParticipantNames(), a.speakerNames()...))
	pb := lowerSet(append(b.entry.summary.ParticipantNames(), b.speakerNames()...))
	if len(pa) < 2 || len(pb) < 2 {
		return false // one shared name is not a meeting
	}
	inter := 0
	for n := range pa {
		if pb[n] {
			inter++
		}
	}
	union := len(pa) + len(pb) - inter
	return float64(inter)/float64(union) >= 0.5
}

func lowerSet(names []string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			out[n] = true
		}
	}
	return out
}

func (d *searchDoc) result(terms []string) SearchResult {
	e := d.entry
	title := e.summary.Title
	if title == "" {
		title = e.folder
	}
	res := SearchResult{
		ConversationID: e.id.ConversationID,
		RecordingID:    e.id.RecordingID,
		Title:          title,
		Participants:   e.summary.ParticipantNames(),
		Speakers:       d.speakerNames(),
		Topics:         e.summary.Topics,
		Score:          math.Round(d.total*100) / 100,
		MatchedIn:      d.matchedIn,
		Hits:           d.hits(terms),
	}
	if !e.recordedAt.IsZero() {
		res.RecordedAt = e.recordedAt.UTC().Format(time.RFC3339)
	}
	return res
}
