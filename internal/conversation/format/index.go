package format

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// IndexFileName is the discussions-root index file. It is uppercase and
// always has been (verified against writer history) — no lowercase probe.
const IndexFileName = "INDEX.json"

// IndexEntry is one row of discussions/INDEX.json. The production index
// carries only folder / recording_id / title / participants / decision_count /
// action_item_count / has_keyframes / topics — every other field is optional
// and tolerated when absent (real-shape indexes have no recorded_at and no
// has_distillation; those are derived upstream). Empty titles are valid.
//
// The loader does not verify that Folder exists on disk: phantom entries
// (folder deleted after indexing) are returned as-is; dropping them is
// read-layer policy, not format policy.
type IndexEntry struct {
	Folder          string   `json:"folder"`
	RecordingID     string   `json:"recording_id"`
	Title           string   `json:"title"`
	Participants    []string `json:"participants,omitempty"`
	DecisionCount   int      `json:"decision_count"`
	ActionItemCount int      `json:"action_item_count"`
	HasKeyframes    bool     `json:"has_keyframes"`
	Topics          []string `json:"topics,omitempty"`
	RecordedAt      string   `json:"recorded_at,omitempty"`
	HasDistillation *bool    `json:"has_distillation,omitempty"`
}

// LoadIndex reads discussions/INDEX.json from a discussions root. A missing
// index is (nil, nil, nil). A file that is not a JSON array at the top level
// is a hard error (the writer restarts the index on parse failure, so a
// non-array index is unexpected corruption). Individual malformed entries are
// skipped and surfaced as InvalidRecords, never fatal for their siblings.
func LoadIndex(discussionsRoot string) ([]IndexEntry, []InvalidRecord, error) {
	root, err := openOptionalRoot(discussionsRoot)
	if err != nil || root == nil {
		return nil, nil, err
	}
	defer root.Close()
	return LoadIndexIn(root)
}

// LoadIndexIn is LoadIndex over an already-open discussions root, so a caller
// that validates entries against a held *os.Root reads the index through the
// same directory descriptor instead of re-opening the path.
func LoadIndexIn(root *os.Root) ([]IndexEntry, []InvalidRecord, error) {
	path := filepath.Join(root.Name(), IndexFileName)
	data, err := readOptionalFileIn(root, IndexFileName)
	if err != nil {
		return nil, nil, err
	}
	if data == nil {
		return nil, nil, nil
	}

	var raw []json.RawMessage
	if err := decodeJSON(path, data, &raw); err != nil {
		return nil, nil, err
	}

	entries := make([]IndexEntry, 0, len(raw))
	var invalid []InvalidRecord
	for i, msg := range raw {
		var entry IndexEntry
		if err := json.Unmarshal(msg, &entry); err != nil {
			invalid = append(invalid, InvalidRecord{
				Path:   IndexFileName,
				Line:   i + 1,
				Reason: "malformed index entry: " + err.Error(),
			})
			continue
		}
		if entry.Folder == "" && entry.RecordingID == "" {
			invalid = append(invalid, InvalidRecord{
				Path:   IndexFileName,
				Line:   i + 1,
				Reason: "index entry has neither folder nor recording_id",
			})
			continue
		}
		entries = append(entries, entry)
	}
	return entries, invalid, nil
}
