package format

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Well-known root files of a discussion folder (D5, D7). transcript.vtt is
// read at the folder root; summary.md is the pre-JSON fallback for show.
const (
	MetadataFileName        = "metadata.json"
	SummaryFileName         = "summary.json"
	SummaryMarkdownFileName = "summary.md"
	TranscriptFileName      = "transcript.vtt"
)

// readSidecar reads one optional well-known file through the folder root:
// absence is (nil, nil) — data, not an error — and every other read failure
// is wrapped with the sidecar's file name, so a caller's warning names the
// file that actually failed instead of a bare I/O message.
func readSidecar(root *os.Root, name string) ([]byte, error) {
	data, err := root.ReadFile(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return data, nil
}

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
	root, err := openOptionalRoot(discussionRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", MetadataFileName, err)
	}
	if root == nil {
		return nil, nil
	}
	defer root.Close()
	return LoadMetadataIn(root)
}

// LoadMetadataIn is LoadMetadata over an already-open discussion-folder root,
// for callers that hold the folder open (derived from a validated discussions
// root) and must not re-open it by absolute path.
func LoadMetadataIn(root *os.Root) (*Metadata, error) {
	data, err := readSidecar(root, MetadataFileName)
	if err != nil || data == nil {
		return nil, err
	}
	var m Metadata
	if err := decodeJSON(MetadataFileName, data, &m); err != nil {
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
	root, err := openOptionalRoot(discussionRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", SummaryFileName, err)
	}
	if root == nil {
		return nil, nil
	}
	defer root.Close()
	return LoadSummaryIn(root)
}

// LoadSummaryIn is LoadSummary over an already-open discussion-folder root.
func LoadSummaryIn(root *os.Root) (*Summary, error) {
	data, err := readSidecar(root, SummaryFileName)
	if err != nil || data == nil {
		return nil, err
	}
	var s Summary
	if err := decodeJSON(SummaryFileName, data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// LoadSummaryMarkdown reads the pre-JSON summary.md fallback. Missing file is
// (nil, nil).
func LoadSummaryMarkdown(discussionRoot string) ([]byte, error) {
	root, err := openOptionalRoot(discussionRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", SummaryMarkdownFileName, err)
	}
	if root == nil {
		return nil, nil
	}
	defer root.Close()
	return LoadSummaryMarkdownIn(root)
}

// LoadSummaryMarkdownIn is LoadSummaryMarkdown over an already-open
// discussion-folder root.
func LoadSummaryMarkdownIn(root *os.Root) ([]byte, error) {
	return readSidecar(root, SummaryMarkdownFileName)
}
