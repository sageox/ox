package format

import (
	"path/filepath"
)

// Well-known root files of a discussion folder (D5, D7). transcript.vtt is
// read at the folder root; summary.md is the pre-JSON fallback for show.
const (
	MetadataFileName        = "metadata.json"
	SummaryFileName         = "summary.json"
	SummaryMarkdownFileName = "summary.md"
	TranscriptFileName      = "transcript.vtt"
)

// Metadata is metadata.json at a discussion-folder root. Empty titles are
// valid (D13) — the pre-existing strict metadata loader that rejects them is
// deliberately not reused.
type Metadata struct {
	RecordingID string `json:"recording_id"`
	Title       string `json:"title"`
	CreatedAt   string `json:"created_at"`
	UserID      string `json:"user_id"`
	ContextType string `json:"context_type"`
	ContextID   string `json:"context_id"`
}

// LoadMetadata reads metadata.json from a discussion folder. Missing file is
// (nil, nil).
func LoadMetadata(discussionRoot string) (*Metadata, error) {
	path := filepath.Join(discussionRoot, MetadataFileName)
	data, err := readOptionalFile(path)
	if err != nil || data == nil {
		return nil, err
	}
	var m Metadata
	if err := decodeJSON(path, data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// SummaryParticipant is one participant row of summary.json. Only the name is
// consumed (D12: unjoined, surfaced alongside opaque voice tags).
type SummaryParticipant struct {
	Name string `json:"name"`
}

// Summary carries the summary.json fields the show command needs (D19) —
// metadata plus human_summary, nothing else. Decisions, action items, open
// questions, and chapters are deliberately not decoded.
type Summary struct {
	SchemaVersion int                  `json:"schema_version"`
	RecordingID   string               `json:"recording_id"`
	Title         string               `json:"title"`
	HumanSummary  string               `json:"human_summary"`
	Participants  []SummaryParticipant `json:"participants,omitempty"`
}

// ParticipantNames returns the participant display names in file order,
// skipping unnamed rows.
func (s *Summary) ParticipantNames() []string {
	if s == nil {
		return nil
	}
	var names []string
	for _, p := range s.Participants {
		if p.Name != "" {
			names = append(names, p.Name)
		}
	}
	return names
}

// LoadSummary reads summary.json from a discussion folder. Missing file is
// (nil, nil) — a missing summary is data (not_yet_generated), not an error.
func LoadSummary(discussionRoot string) (*Summary, error) {
	path := filepath.Join(discussionRoot, SummaryFileName)
	data, err := readOptionalFile(path)
	if err != nil || data == nil {
		return nil, err
	}
	var s Summary
	if err := decodeJSON(path, data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// LoadSummaryMarkdown reads the pre-JSON summary.md fallback. Missing file is
// (nil, nil).
func LoadSummaryMarkdown(discussionRoot string) ([]byte, error) {
	return readOptionalFile(filepath.Join(discussionRoot, SummaryMarkdownFileName))
}
