package format

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func openTestRoot(t *testing.T, files map[string]string) *os.Root {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

// TestLoadSearchSummaryIn: Failure prevented: search drops the fields a
// person remembers a meeting by (topics, decisions, chapters with cue
// ranges), treats an unsummarized conversation as an error, or accepts a
// corrupt summary as an empty one.
func TestLoadSearchSummaryIn(t *testing.T) {
	t.Run("decodes the searchable fields", func(t *testing.T) {
		root := openTestRoot(t, map[string]string{SummaryFileName: `{
			"recording_id": "rec_1", "title": "Search planning", "human_summary": "We planned search.",
			"participants": [{"name": "Ryan"}, {"name": ""}, {"name": "Ajit"}],
			"topics": ["search"],
			"decisions": [{"description": "ship search", "owner": "Ryan"}],
			"action_items": [{"description": "write docs", "assignee": "Ajit"}],
			"chapters": [{"title": "Intro", "summary": "hello", "cue_range": [1, 4]}]
		}`})
		s, err := LoadSearchSummaryIn(root)
		if err != nil || s == nil {
			t.Fatalf("LoadSearchSummaryIn = %v, %v", s, err)
		}
		if s.Title != "Search planning" || s.Topics[0] != "search" || s.Decisions[0].Owner != "Ryan" ||
			s.ActionItems[0].Assignee != "Ajit" || !reflect.DeepEqual(s.Chapters[0].CueRange, []int{1, 4}) {
			t.Errorf("decoded summary = %+v", s)
		}
		if got := s.ParticipantNames(); !reflect.DeepEqual(got, []string{"Ryan", "Ajit"}) {
			t.Errorf("ParticipantNames = %v, want [Ryan Ajit]", got)
		}
	})
	t.Run("missing file is nil nil", func(t *testing.T) {
		s, err := LoadSearchSummaryIn(openTestRoot(t, nil))
		if s != nil || err != nil {
			t.Fatalf("got %v, %v; want nil, nil", s, err)
		}
	})
	t.Run("malformed file is an error", func(t *testing.T) {
		if _, err := LoadSearchSummaryIn(openTestRoot(t, map[string]string{SummaryFileName: "{not json"})); err == nil {
			t.Fatal("want a decode error")
		}
	})
	t.Run("nil summary has no names", func(t *testing.T) {
		var s *SearchSummary
		if got := s.ParticipantNames(); got != nil {
			t.Errorf("ParticipantNames on nil = %v", got)
		}
	})
}

// TestLoadSpeakerNamesIn: Failure prevented: transcripts and search show
// opaque usr_ ids although a timeline names the speaker, a pipeline
// placeholder ("inferred-…") is shown as a name, or one malformed row hides
// every name after it.
func TestLoadSpeakerNamesIn(t *testing.T) {
	want := map[string]bool{"usr_a": true, "usr_b": true, "usr_c": true}
	t.Run("live first, batch fills the gaps", func(t *testing.T) {
		root := openTestRoot(t, map[string]string{
			"live.csv": "word,speaker_uid,speaker_name\n" +
				"hi,usr_a,  Ryan  \n" +
				"yo,usr_b,inferred-1\n" +
				"short\n" +
				"x,usr_a,Someone Else\n" +
				"x,usr_zzz,Not Wanted\n",
			"batch.csv": "speaker_name,speaker_uid\nAjit,usr_b\n,usr_c\n",
		})
		got, err := LoadSpeakerNamesIn(root, want)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, map[string]string{"usr_a": "Ryan", "usr_b": "Ajit"}) {
			t.Errorf("names = %v", got)
		}
	})
	t.Run("stops once every id is found", func(t *testing.T) {
		root := openTestRoot(t, map[string]string{
			"live.csv":  "speaker_uid,speaker_name\nusr_a,Ryan\n",
			"batch.csv": "speaker_uid,speaker_name\nusr_a,Wrong\n",
		})
		got, err := LoadSpeakerNamesIn(root, map[string]bool{"usr_a": true})
		if err != nil || got["usr_a"] != "Ryan" {
			t.Fatalf("names = %v, %v", got, err)
		}
	})
	t.Run("a file that is not a word timeline is skipped", func(t *testing.T) {
		root := openTestRoot(t, map[string]string{
			"live.csv":     "",
			"batch.csv":    "a,b,c\n1,2,3\n",
			"polished.csv": "speaker_uid,speaker_name\nusr_c,Emory\n",
		})
		got, err := LoadSpeakerNamesIn(root, want)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, map[string]string{"usr_c": "Emory"}) {
			t.Errorf("names = %v", got)
		}
	})
	t.Run("no timelines is empty, not an error", func(t *testing.T) {
		got, err := LoadSpeakerNamesIn(openTestRoot(t, nil), want)
		if err != nil || len(got) != 0 {
			t.Fatalf("names = %v, %v", got, err)
		}
	})
	t.Run("an unreadable timeline is an error", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "live.csv"), 0o700); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		if _, err := LoadSpeakerNamesIn(root, want); err == nil || !strings.Contains(err.Error(), "live.csv") {
			t.Fatalf("err = %v, want a read error naming live.csv", err)
		}
	})
}
