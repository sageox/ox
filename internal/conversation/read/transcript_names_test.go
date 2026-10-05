package read

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stageNamedTranscript writes a one-conversation discussions root whose
// transcript mixes opaque usr_ tags with a tag that is already a name.
// timeline is the live.csv body; "" writes none, and "DIR" makes live.csv a
// directory so reading it fails.
func stageNamedTranscript(t *testing.T, timeline string) *Reader {
	t.Helper()
	root := filepath.Join(t.TempDir(), "discussions")
	dir := filepath.Join(root, "2026-09-03-20-16-milkana")
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, "transcript.vtt"), vttOf(
		cue{emoryUID, "Let's start with onboarding."},
		cue{ajitUID, "Search is the thing."},
		cue{"Milkana", "Guests get a plain name tag."},
		cue{ryanUID, "Nobody named me in this folder."},
	))
	switch timeline {
	case "":
	case "DIR":
		mustMkdir(t, filepath.Join(dir, "live.csv"))
	default:
		mustWrite(t, filepath.Join(dir, "live.csv"), timeline)
	}
	index, _ := json.Marshal([]map[string]any{{"folder": "2026-09-03-20-16-milkana", "recording_id": searchRec, "title": "Search review"}})
	mustWrite(t, filepath.Join(root, "INDEX.json"), string(index))
	return New(root, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
}

func speakerNames(data *TranscriptData) []string {
	var out []string
	for _, c := range data.Cues {
		out = append(out, c.SpeakerName)
	}
	return out
}

// TestTranscriptNamesOpaqueSpeakers: an agent following a citation must see
// who said what. Failure prevented: cues carry only usr_ ids, so the reader
// cannot tell Ajit from Emory — or resolution breaks the raw id citations key
// on, or a missing/unreadable timeline fails the whole read.
func TestTranscriptNamesOpaqueSpeakers(t *testing.T) {
	names := liveCSVOf(map[string]string{ajitUID: "Ajit Banerjee", emoryUID: "Emory Clark"})
	cases := []struct {
		name         string
		timeline     string
		opts         TranscriptOptions
		wantNames    []string
		wantWarnings bool
	}{
		{
			// A tag that is already a name gets no speaker_name (it would
			// only repeat it); an id absent from the timeline stays unnamed.
			name:      "timeline names usr ids only",
			timeline:  names,
			opts:      TranscriptOptions{Full: true},
			wantNames: []string{"Emory Clark", "Ajit Banerjee", "", ""},
		},
		{
			name:      "window resolves only its own cues",
			timeline:  names,
			opts:      TranscriptOptions{CueFirst: 2, CueLast: 2},
			wantNames: []string{"Ajit Banerjee"},
		},
		{
			name:      "no timeline serves ids unchanged",
			timeline:  "",
			opts:      TranscriptOptions{Full: true},
			wantNames: []string{"", "", "", ""},
		},
		{
			name:         "unreadable timeline warns, never errors",
			timeline:     "DIR",
			opts:         TranscriptOptions{Full: true},
			wantNames:    []string{"", "", "", ""},
			wantWarnings: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := stageNamedTranscript(t, tc.timeline).Transcript(cnvOf(searchRec), tc.opts)
			data := transcriptData(t, env)
			if got := speakerNames(data); strings.Join(got, "|") != strings.Join(tc.wantNames, "|") {
				t.Errorf("speaker_name per cue = %q, want %q", got, tc.wantNames)
			}
			if tc.opts.Full && data.Cues[1].Speaker != ajitUID {
				t.Errorf("speaker must stay the raw tag for citations, got %q", data.Cues[1].Speaker)
			}
			if got := len(env.Warnings) > 0; got != tc.wantWarnings {
				t.Errorf("warnings = %q, want any=%v", env.Warnings, tc.wantWarnings)
			}
		})
	}
}
