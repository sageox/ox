package format

import (
	"encoding/csv"
	"errors"
	"io"
	"os"
	"strings"
)

// SearchSummary carries the summary.json fields conversation search reads.
// It is a separate projection from Summary on purpose: show decodes only
// what it serves (D19), while search needs the fields a person remembers a
// meeting by — topics, decisions, action items, and chapters (whose cue
// ranges let a hit land on the exact moment instead of the whole recording).
type SearchSummary struct {
	RecordingID  string               `json:"recording_id"`
	Title        string               `json:"title"`
	HumanSummary string               `json:"human_summary"`
	Participants []SummaryParticipant `json:"participants,omitempty"`
	Topics       []string             `json:"topics,omitempty"`
	Decisions    []SummaryDecision    `json:"decisions,omitempty"`
	ActionItems  []SummaryActionItem  `json:"action_items,omitempty"`
	Chapters     []SummaryChapter     `json:"chapters,omitempty"`
}

// SummaryDecision is one decisions[] row of summary.json.
type SummaryDecision struct {
	Description string `json:"description"`
	Owner       string `json:"owner,omitempty"`
}

// SummaryActionItem is one action_items[] row of summary.json.
type SummaryActionItem struct {
	Description string `json:"description"`
	Assignee    string `json:"assignee,omitempty"`
}

// SummaryChapter is one chapters[] row of summary.json. CueRange is the
// inclusive 1-based [first, last] cue span into transcript.vtt; a malformed
// or missing range leaves it empty and the chapter is still searchable.
type SummaryChapter struct {
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	CueRange []int  `json:"cue_range,omitempty"`
}

// ParticipantNames returns the participant display names in file order,
// skipping unnamed rows.
func (s *SearchSummary) ParticipantNames() []string {
	if s == nil {
		return nil
	}
	return (&Summary{Participants: s.Participants}).ParticipantNames()
}

// LoadSearchSummaryIn reads summary.json through an open discussion-folder
// root. Missing file is (nil, nil): the conversation is not summarized yet.
func LoadSearchSummaryIn(root *os.Root) (*SearchSummary, error) {
	data, err := readSidecar(root, SummaryFileName)
	if err != nil || data == nil {
		return nil, err
	}
	var s SearchSummary
	if err := decodeJSON(SummaryFileName, data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Word-timeline CSVs written beside a transcript. Each row carries the
// speaker's opaque user id and, when the capture knew it, the display name.
// live.csv is tried first because it is written from the live roster;
// batch.csv is the re-transcription and sometimes leaves the name blank.
var speakerTimelineFiles = []string{"live.csv", "batch.csv", "polished.csv"}

// maxSpeakerTimelineBytes bounds one timeline read. Real timelines are a few
// megabytes; the cap only matters for a malformed or hostile file.
const maxSpeakerTimelineBytes = 64 << 20

// LoadSpeakerNamesIn resolves opaque speaker ids (transcript voice tags such
// as <v usr_…>) to display names from a discussion folder's word-timeline
// CSVs. Only ids in want are resolved; the read stops as soon as every one
// is found, so a multi-megabyte timeline is rarely read in full. Names are
// untrusted team-context content: callers sanitize before rendering them on
// a terminal. Placeholder names the pipeline uses for unattributed speech
// ("inferred-…") are not names and are skipped.
func LoadSpeakerNamesIn(root *os.Root, want map[string]bool) (map[string]string, error) {
	found := map[string]string{}
	for _, name := range speakerTimelineFiles {
		if len(found) == len(want) {
			break
		}
		f, err := root.Open(name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return found, err
		}
		err = scanSpeakerNames(io.LimitReader(f, maxSpeakerTimelineBytes), want, found)
		f.Close()
		if err != nil {
			return found, err
		}
	}
	return found, nil
}

func scanSpeakerNames(r io.Reader, want map[string]bool, found map[string]string) error {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	uidCol, nameCol := -1, -1
	for i, h := range header {
		switch h {
		case "speaker_uid":
			uidCol = i
		case "speaker_name":
			nameCol = i
		}
	}
	if uidCol < 0 || nameCol < 0 {
		return nil // not a word timeline: nothing to resolve from it
	}
	for len(found) < len(want) {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			var perr *csv.ParseError
			if errors.As(err, &perr) {
				continue // one malformed row never hides its siblings
			}
			return err
		}
		if uidCol >= len(rec) || nameCol >= len(rec) {
			continue
		}
		uid, displayName := rec[uidCol], strings.TrimSpace(rec[nameCol])
		if !want[uid] || displayName == "" || strings.HasPrefix(displayName, "inferred-") {
			continue
		}
		if _, ok := found[uid]; !ok {
			found[uid] = displayName
		}
	}
	return nil
}
