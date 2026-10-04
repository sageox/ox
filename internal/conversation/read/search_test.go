package read

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Fixture recordings. Ids are UUIDv7 so recorded_at derives from them.
const (
	// oldRec is summarized but ABSENT from INDEX.json — the shape of every
	// conversation older than the server's index window on a real team.
	oldRec = "rec_019fec9d-ea80-7000-8000-000000000001" // 2026-08-10 17:00Z
	// searchRec and searchDupRec are the same meeting recorded from two
	// laptops four minutes apart.
	searchRec    = "rec_01a068e9-fc00-7000-8000-000000000002" // 2026-09-03 20:16Z
	searchDupRec = "rec_01a068ed-a580-7000-8000-000000000003" // 2026-09-03 20:20Z
	recentRec    = "rec_01a0bfc2-a680-7000-8000-000000000004" // 2026-09-20 17:00Z
	// unsummarizedRec has a transcript but no summary.json yet.
	unsummarizedRec = "rec_01a0bfc2-a680-7000-8000-000000000005"
)

const (
	ajitUID  = "usr_ajit000000000000000000000"
	emoryUID = "usr_emory00000000000000000000"
	ryanUID  = "usr_ryan000000000000000000000"
)

func cnvOf(rec string) string { return "cnv_" + strings.TrimPrefix(rec, "rec_") }

type fixtureConversation struct {
	folder  string
	summary map[string]any // nil: no summary.json
	vtt     string
	liveCSV string
}

// stageSearchCorpus writes a discussions root. Only searchRec is in
// INDEX.json, so every other hit proves search does not depend on it.
func stageSearchCorpus(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "discussions")
	convs := []fixtureConversation{
		{
			folder: "2026-08-10-17-00-ryan",
			summary: map[string]any{
				"recording_id":  oldRec,
				"title":         "Drive catalog on Iceberg",
				"human_summary": "Ryan and Emory walked through moving the drive catalog to Iceberg tables.",
				"participants":  []map[string]any{{"name": "Ryan Snodgrass"}, {"name": "Emory Clark"}},
				"topics":        []string{"iceberg", "drive catalog"},
			},
			vtt: vttOf(
				cue{ryanUID, "The catalog moves to Iceberg this week."},
				cue{emoryUID, "Does search still work against the drive afterwards?"},
			),
			liveCSV: liveCSVOf(map[string]string{ryanUID: "Ryan Snodgrass", emoryUID: "Emory Clark"}),
		},
		{
			folder: "2026-09-03-20-16-milkana",
			summary: map[string]any{
				"recording_id":  searchRec,
				"title":         "Discussion search feature and onboarding review",
				"human_summary": "The team reviewed conversation search and how results should land on the exact moment.",
				// The summarizer missed Ajit: only the transcript knows he spoke.
				"participants": []map[string]any{{"name": "Milkana"}, {"name": "Emory Clark"}},
				"topics":       []string{"conversation search", "onboarding"},
				"decisions":    []map[string]any{{"description": "Distilled results rank above raw transcripts", "owner": "Emory Clark"}},
				"chapters": []map[string]any{
					{"title": "Onboarding checklist", "summary": "New hires and the checklist.", "cue_range": []int{1, 1}},
					{"title": "Search results that land on the moment", "summary": "Clicking a search result should open the transcript at the cue.", "cue_range": []int{2, 3}},
				},
			},
			vtt: vttOf(
				cue{emoryUID, "Let's start with onboarding."},
				cue{ajitUID, "Search is the thing."},
				cue{ajitUID, "If search over files works for agents we are golden."},
				cue{emoryUID, "Agreed, results should land right on the cue."},
			),
			liveCSV: liveCSVOf(map[string]string{ajitUID: "Ajit Banerjee", emoryUID: "Emory Clark"}),
		},
		{
			folder: "2026-09-03-20-20-emory",
			summary: map[string]any{
				"recording_id":  searchDupRec,
				"title":         "Search feature review",
				"human_summary": "Second laptop's copy of the search review.",
				"participants":  []map[string]any{{"name": "Milkana"}, {"name": "Emory Clark"}, {"name": "Ajit Banerjee"}},
			},
			vtt: vttOf(
				cue{ajitUID, "Search is the thing, search over files."},
			),
			liveCSV: liveCSVOf(map[string]string{ajitUID: "Ajit Banerjee"}),
		},
		{
			folder: "2026-09-20-17-00-ryan",
			summary: map[string]any{
				"recording_id":  recentRec,
				"title":         "Standup",
				"human_summary": "Daily standup about the release.",
				"participants":  []map[string]any{{"name": "Ryan Snodgrass"}},
			},
			vtt:     vttOf(cue{ryanUID, "The release goes out today."}),
			liveCSV: liveCSVOf(map[string]string{ryanUID: "Ryan Snodgrass"}),
		},
		{
			folder: "2026-09-20-17-05-pending",
			vtt:    vttOf(cue{ajitUID, "Search search search, not summarized yet."}),
		},
	}
	for _, c := range convs {
		dir := filepath.Join(root, c.folder)
		mustMkdir(t, dir)
		if c.summary != nil {
			b, _ := json.Marshal(c.summary)
			mustWrite(t, filepath.Join(dir, "summary.json"), string(b))
		}
		if c.vtt != "" {
			mustWrite(t, filepath.Join(dir, "transcript.vtt"), c.vtt)
		}
		if c.liveCSV != "" {
			mustWrite(t, filepath.Join(dir, "live.csv"), c.liveCSV)
		}
	}
	index, _ := json.Marshal([]map[string]any{{"folder": "2026-09-03-20-16-milkana", "recording_id": searchRec, "title": "Discussion search feature and onboarding review"}})
	mustWrite(t, filepath.Join(root, "INDEX.json"), string(index))
	return root
}

type cue struct{ speaker, text string }

func vttOf(cues ...cue) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i, c := range cues {
		start := time.Duration(i*5) * time.Second
		b.WriteString(formatVTTTimestamp(start) + " --> " + formatVTTTimestamp(start+4*time.Second) + "\n")
		b.WriteString("<v " + c.speaker + ">" + c.text + "</v>\n\n")
	}
	return b.String()
}

func liveCSVOf(names map[string]string) string {
	var b strings.Builder
	b.WriteString("word_index,word,start_ms,end_ms,speaker_uid,speaker_name,attribution_confidence\n")
	i := 0
	for uid, name := range names {
		b.WriteString(strings.Join([]string{strconv.Itoa(i), "hello", "0", "1", uid, name, "0.9"}, ",") + "\n")
		i++
	}
	return b.String()
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func searchReader(t *testing.T) *Reader {
	t.Helper()
	root := stageSearchCorpus(t)
	r := New(root, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	r.SetFallback(summarizedFolderResolver{discussionsRoot: root})
	return r
}

func mustSearch(t *testing.T, r *Reader, opts SearchOptions) *SearchData {
	t.Helper()
	env := r.Search(opts)
	if !env.Success {
		t.Fatalf("Search(%+v) failed: %+v", opts, env.Error)
	}
	return env.Data.(*SearchData)
}

func resultIDs(d *SearchData) []string {
	var ids []string
	for _, r := range d.Results {
		ids = append(ids, r.ConversationID)
	}
	return ids
}

// --- A. Coverage: the whole history, not the index window ---

// TestSearchFindsConversationsMissingFromIndex: the Iceberg meeting is not in
// INDEX.json. Failure prevented: search silently skips every conversation
// older than the server's index window — most of a real team's history.
func TestSearchFindsConversationsMissingFromIndex(t *testing.T) {
	d := mustSearch(t, searchReader(t), SearchOptions{Query: "iceberg"})
	if len(d.Results) != 1 || d.Results[0].ConversationID != cnvOf(oldRec) {
		t.Fatalf("results = %v, want only %s", resultIDs(d), cnvOf(oldRec))
	}
	if d.Summarized != 4 {
		t.Errorf("Summarized = %d, want 4 (the unsummarized folder is not served)", d.Summarized)
	}
}

// TestSearchSkipsUnsummarizedFolders: a folder with a transcript but no
// summary.json matches every keyword yet must not be served. Failure
// prevented: search serves conversations the index itself would not, whose
// ids then fail every other ox conversation command.
func TestSearchSkipsUnsummarizedFolders(t *testing.T) {
	d := mustSearch(t, searchReader(t), SearchOptions{Query: "summarized"})
	for _, r := range d.Results {
		if r.ConversationID == cnvOf(unsummarizedRec) {
			t.Fatalf("unsummarized conversation served: %v", resultIDs(d))
		}
	}
}

// TestSearchIgnoresSymlinkedAndHostileFolders: team context is
// customer-writable. Failure prevented: a committed symlink pulls a summary
// from outside the discussions root, or a forged recording id becomes a
// served (and later opened) id.
func TestSearchIgnoresSymlinkedAndHostileFolders(t *testing.T) {
	root := stageSearchCorpus(t)
	outside := filepath.Join(t.TempDir(), "outside")
	mustMkdir(t, outside)
	b, _ := json.Marshal(map[string]any{"recording_id": "rec_01a0bfc2-a680-7000-8000-000000000009", "title": "exfiltrated secret"})
	mustWrite(t, filepath.Join(outside, "summary.json"), string(b))
	if err := os.Symlink(outside, filepath.Join(root, "2026-09-21-00-00-link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	hostile := filepath.Join(root, "2026-09-22-00-00-hostile")
	mustMkdir(t, hostile)
	b, _ = json.Marshal(map[string]any{"recording_id": "https://evil.example/rec_x", "title": "exfiltrated forged"})
	mustWrite(t, filepath.Join(hostile, "summary.json"), string(b))

	d := mustSearch(t, New(root, time.Time{}), SearchOptions{Query: "exfiltrated"})
	if len(d.Results) != 0 {
		t.Fatalf("symlinked or forged conversation served: %v", resultIDs(d))
	}
}

// --- B. Who and when ---

// TestSearchParticipantMatchesWhoSpoke: the summarizer left Ajit out of the
// participants, but he spoke. Failure prevented: "what did I talk to Ajit
// about" misses the meeting because only LLM-inferred participants are
// checked, and opaque usr_ voice tags are never resolved to names.
func TestSearchParticipantMatchesWhoSpoke(t *testing.T) {
	d := mustSearch(t, searchReader(t), SearchOptions{Query: "search", Participants: []string{"ajit"}})
	ids := resultIDs(d)
	if len(ids) == 0 || ids[0] != cnvOf(searchRec) {
		t.Fatalf("results = %v, want %s first", ids, cnvOf(searchRec))
	}
	for _, id := range ids {
		if id == cnvOf(oldRec) {
			t.Fatalf("Ajit was not in the Iceberg meeting, but it matched: %v", ids)
		}
	}
	var hasAjit bool
	for _, s := range d.Results[0].Speakers {
		hasAjit = hasAjit || s == "Ajit Banerjee"
	}
	if !hasAjit {
		t.Errorf("speakers = %v, want the resolved name Ajit Banerjee", d.Results[0].Speakers)
	}
}

// TestSearchSpeakerRestrictsToTheirWords: Emory asked about search in the
// Iceberg meeting; Ryan never said "search". Failure prevented: --speaker
// matches keywords anyone said in a meeting the speaker merely attended.
func TestSearchSpeakerRestrictsToTheirWords(t *testing.T) {
	r := searchReader(t)
	if d := mustSearch(t, r, SearchOptions{Query: "search drive", Speaker: "Ryan"}); len(d.Results) != 0 {
		t.Fatalf("Ryan never said search, got %v", resultIDs(d))
	}
	d := mustSearch(t, r, SearchOptions{Query: "search drive", Speaker: "Emory"})
	if len(d.Results) != 1 || d.Results[0].ConversationID != cnvOf(oldRec) {
		t.Fatalf("results = %v, want the Iceberg meeting", resultIDs(d))
	}
	for _, h := range d.Results[0].Hits {
		if h.Kind != HitTranscript || h.Speaker != "Emory Clark" {
			t.Errorf("hit %+v: --speaker must serve only that speaker's cues", h)
		}
	}
}

// TestSearchDateBounds: Until is exclusive, Since inclusive. Failure
// prevented: "three weeks ago" returns last week's meetings, or drops the
// meeting on the boundary day.
func TestSearchDateBounds(t *testing.T) {
	r := searchReader(t)
	d := mustSearch(t, r, SearchOptions{Query: "search", Since: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)})
	if len(d.Results) != 1 || d.Results[0].ConversationID != cnvOf(searchRec) {
		t.Fatalf("results = %v, want only the Sep 3 meeting", resultIDs(d))
	}
	d = mustSearch(t, r, SearchOptions{Since: time.Date(2026, 9, 20, 17, 0, 0, 0, time.UTC)})
	if d.Match != MatchFiltersOnly || len(d.Results) != 1 || d.Results[0].ConversationID != cnvOf(recentRec) {
		t.Fatalf("filters-only results = %v (match %s), want the Sep 20 standup", resultIDs(d), d.Match)
	}
}

// --- C. Matching and ranking ---

// TestSearchDropsFillerWords: a question phrased the way a person asks it
// must still match. Failure prevented: all-terms matching fails on "what",
// "did", "talk", and a natural question returns nothing.
func TestSearchDropsFillerWords(t *testing.T) {
	d := mustSearch(t, searchReader(t), SearchOptions{Query: "what did we talk about with the drive catalog"})
	if strings.Join(d.Terms, " ") != "drive catalog" {
		t.Fatalf("terms = %v, want [drive catalog]", d.Terms)
	}
	if len(d.Results) == 0 || d.Results[0].ConversationID != cnvOf(oldRec) {
		t.Fatalf("results = %v, want the Iceberg meeting", resultIDs(d))
	}

	env := searchReader(t).Search(SearchOptions{Query: "what did we talk about"})
	if env.Success || env.Error.Code != ErrCodeInvalidSelector {
		t.Fatalf("a filler-only query must be a usage error, got %+v", env)
	}
}

// TestSearchFallsBackToAnyTerm: Failure prevented: one unmatched keyword
// empties the result list with no hint why.
func TestSearchFallsBackToAnyTerm(t *testing.T) {
	env := searchReader(t).Search(SearchOptions{Query: "iceberg kubernetes"})
	d := env.Data.(*SearchData)
	if d.Match != MatchAnyTerm || len(d.Results) == 0 || d.Results[0].ConversationID != cnvOf(oldRec) {
		t.Fatalf("match %s results %v, want any_term with the Iceberg meeting", d.Match, resultIDs(d))
	}
	if len(env.Warnings) == 0 {
		t.Error("any-term fallback must warn that not every keyword matched")
	}
}

// TestSearchTitleOutranksPassingMention: Failure prevented: a meeting that
// was ABOUT search ranks below one where the word came up in passing.
func TestSearchTitleOutranksPassingMention(t *testing.T) {
	d := mustSearch(t, searchReader(t), SearchOptions{Query: "search"})
	if len(d.Results) < 2 || d.Results[0].ConversationID != cnvOf(searchRec) {
		t.Fatalf("results = %v, want the search review first", resultIDs(d))
	}
}

// --- D. Hits lead somewhere ---

// TestSearchHitCitationOpensTheCitedCues: every hit's citation must open in
// ox conversation transcript on the cited cues — including for a
// conversation INDEX.json does not list. Failure prevented: search returns a
// link that dead-ends in not_indexed, or lands on the start of the meeting
// instead of the moment.
func TestSearchHitCitationOpensTheCitedCues(t *testing.T) {
	r := searchReader(t)
	for _, q := range []string{"golden", "iceberg"} {
		d := mustSearch(t, r, SearchOptions{Query: q})
		if len(d.Results) == 0 {
			t.Fatalf("%q: no results", q)
		}
		var hit *SearchHit
		for i, h := range d.Results[0].Hits {
			if h.Kind == HitTranscript {
				hit = &d.Results[0].Hits[i]
				break
			}
		}
		if hit == nil {
			t.Fatalf("%q: no transcript hit in %+v", q, d.Results[0].Hits)
		}
		env := r.Transcript(hit.Citation, TranscriptOptions{})
		if !env.Success {
			t.Fatalf("%q: citation %s does not open: %+v", q, hit.Citation, env.Error)
		}
		td := env.Data.(*TranscriptData)
		if len(td.Cues) == 0 || td.Cues[0].N != hit.Cues[0] || !strings.Contains(strings.ToLower(td.Cues[0].Text+" "+td.Cues[len(td.Cues)-1].Text), q) {
			t.Fatalf("%q: citation %s opened cues %+v, want the matched cue %v", q, hit.Citation, td.Cues, hit.Cues)
		}
	}
}

// TestSearchChapterHitCarriesItsCueRange: Failure prevented: a chapter match
// links to the whole recording, so the reader has to search twice.
func TestSearchChapterHitCarriesItsCueRange(t *testing.T) {
	d := mustSearch(t, searchReader(t), SearchOptions{Query: "moment"})
	if len(d.Results) == 0 {
		t.Fatal("no results")
	}
	h := d.Results[0].Hits[0]
	if h.Kind != HitChapter || len(h.Cues) != 2 || h.Cues[0] != 2 || h.Cues[1] != 3 {
		t.Fatalf("first hit = %+v, want the chapter spanning cues 2-3", h)
	}
	if want := "sageox://" + cnvOf(searchRec) + "#cue=2-3"; h.Citation != want {
		t.Errorf("citation = %s, want %s", h.Citation, want)
	}
}

// TestSearchFoldsDuplicateRecordings: the same meeting recorded from two
// laptops. Failure prevented: one meeting fills two of the ten result slots
// and reads as two separate conversations.
func TestSearchFoldsDuplicateRecordings(t *testing.T) {
	d := mustSearch(t, searchReader(t), SearchOptions{Query: "search files"})
	if len(d.Results) != 1 {
		t.Fatalf("results = %v, want the two recordings folded into one", resultIDs(d))
	}
	r := d.Results[0]
	if len(r.AlsoRecordedAs) != 1 {
		t.Fatalf("also_recorded_as = %v, want the other recording", r.AlsoRecordedAs)
	}
	got := map[string]bool{r.ConversationID: true, r.AlsoRecordedAs[0]: true}
	if !got[cnvOf(searchRec)] || !got[cnvOf(searchDupRec)] {
		t.Errorf("folded ids = %v, want both recordings of the meeting", got)
	}
}

// TestSearchLimitTruncates: Failure prevented: an unbounded result list
// floods an AI coworker's context.
func TestSearchLimitTruncates(t *testing.T) {
	d := mustSearch(t, searchReader(t), SearchOptions{Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Limit: 1})
	if len(d.Results) != 1 || !d.Truncated {
		t.Fatalf("results %d truncated %v, want 1 true", len(d.Results), d.Truncated)
	}
}

func TestSnippetCentersOnTheMatch(t *testing.T) {
	long := strings.Repeat("filler words here ", 40) + "the golden search nugget" + strings.Repeat(" more filler", 40)
	s := snippet(long, []string{"golden"})
	if !strings.Contains(s, "golden") || !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") {
		t.Fatalf("snippet = %q, want the match inside ellipses", s)
	}
	if n := len([]rune(s)); n > snippetRunes+2 {
		t.Errorf("snippet is %d runes, want <= %d", n, snippetRunes+2)
	}
}

// TestSearchPersonInQueryIsAFilterNotAKeyword: Failure prevented: "what did I
// talk to Ajit about search --participant Ajit" ranks cues that merely say
// "Ajit" above cues about search.
func TestSearchPersonInQueryIsAFilterNotAKeyword(t *testing.T) {
	d := mustSearch(t, searchReader(t), SearchOptions{Query: "what did I talk to Ajit about search", Participants: []string{"Ajit"}})
	if strings.Join(d.Terms, " ") != "search" {
		t.Fatalf("terms = %v, want [search]", d.Terms)
	}
}

// TestSearchEmptyTeamIsNotAnError: Failure prevented: a team with no
// recordings yet gets read_error instead of an empty result.
func TestSearchEmptyTeamIsNotAnError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "discussions")
	mustMkdir(t, root)
	d := mustSearch(t, New(root, time.Time{}), SearchOptions{Query: "anything"})
	if len(d.Results) != 0 || d.Summarized != 0 {
		t.Fatalf("empty team: %+v", d)
	}
}
