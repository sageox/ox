package read

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/sageox/ox/internal/conversation/format"
)

// DefaultListLimit is the list disclosure cap (D15).
const DefaultListLimit = 20

// ListOptions narrows the list query.
type ListOptions struct {
	// Limit caps the number of rows; 0 means DefaultListLimit.
	Limit int
	// Since drops conversations recorded before the instant (zero = no
	// filter). Rows whose recorded_at could not be derived are dropped by a
	// non-zero filter — an unknown instant cannot satisfy a bound.
	Since time.Time
}

// ConversationRow is one list row (L0 of the disclosure ladder).
type ConversationRow struct {
	ConversationID  string   `json:"conversation_id"`
	RecordingID     string   `json:"recording_id"`
	Title           string   `json:"title"`
	RecordedAt      string   `json:"recorded_at,omitempty"`
	Participants    []string `json:"participants,omitempty"`
	DecisionCount   int      `json:"decision_count"`
	ActionItemCount int      `json:"action_item_count"`
	Topics          []string `json:"topics,omitempty"`
	HasDistillation bool     `json:"has_distillation"`
}

// ListData is the list envelope payload.
type ListData struct {
	Conversations []ConversationRow `json:"conversations"`
	// TotalIndexed counts every parseable INDEX.json entry, before phantom
	// dropping and windowing, so callers see the index's own size.
	TotalIndexed int  `json:"total_indexed"`
	Truncated    bool `json:"truncated,omitempty"`
}

// List browses the active team's conversations from INDEX.json alone (D1) —
// newest first by derived recorded_at, capped at the limit, phantom entries
// dropped.
func (r *Reader) List(opts ListOptions) *Envelope {
	start := r.now()
	root, rootErr := r.openDiscussionsRoot()
	if rootErr != nil {
		return r.finishError(start, rootErr, nil)
	}
	if root != nil {
		defer root.Close()
	}
	rows, totalIndexed, err := r.loadRows(root)
	if err != nil {
		return r.finishError(start, err, nil)
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}

	kept := make([]row, 0, len(rows))
	for _, rw := range rows {
		if !opts.Since.IsZero() && rw.recordedAt.Before(opts.Since) {
			continue
		}
		kept = append(kept, rw)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if !kept[i].recordedAt.Equal(kept[j].recordedAt) {
			return kept[i].recordedAt.After(kept[j].recordedAt)
		}
		return kept[i].entry.Folder > kept[j].entry.Folder
	})

	truncated := len(kept) > limit
	if truncated {
		kept = kept[:limit]
	}

	out := make([]ConversationRow, 0, len(kept))
	for _, rw := range kept {
		out = append(out, r.listRow(root, rw))
	}
	data := &ListData{Conversations: out, TotalIndexed: totalIndexed, Truncated: truncated}
	return r.finishSuccess(start, data, "ox conversation show <cnv_id> for the summary.", nil)
}

// listRow projects one live index row to its envelope shape, applying the
// D13 title fallback chain (INDEX.json → metadata.json → folder name). The
// metadata probe derives its folder handle from the held discussions root —
// never an absolute-path re-open — and stays best-effort: any failure just
// falls through to the folder-name title.
func (r *Reader) listRow(root *os.Root, rw row) ConversationRow {
	title := rw.entry.Title
	if title == "" && root != nil {
		if droot, derr := openDiscussion(root, rw.entry.Folder); derr == nil && droot != nil {
			if meta, err := format.LoadMetadataIn(droot); err == nil && meta != nil && meta.Title != "" {
				title = meta.Title
			}
			droot.Close()
		}
	}
	if title == "" {
		title = rw.entry.Folder
	}
	cr := ConversationRow{
		RecordingID:     rw.entry.RecordingID,
		Title:           title,
		Participants:    rw.entry.Participants,
		DecisionCount:   rw.entry.DecisionCount,
		ActionItemCount: rw.entry.ActionItemCount,
		Topics:          rw.entry.Topics,
		HasDistillation: rw.hasDistillation,
	}
	if id, err := ParseID(rw.entry.RecordingID); err == nil {
		cr.ConversationID = id.ConversationID
	}
	if !rw.recordedAt.IsZero() {
		cr.RecordedAt = rw.recordedAt.UTC().Format(time.RFC3339)
	}
	return cr
}

// guidanceShow names the L1 rung for a specific conversation.
func guidanceShow(conversationID string) string {
	return fmt.Sprintf("Topics: ox conversation topics %s. Transcript: ox conversation transcript %s --cues N-M.", conversationID, conversationID)
}
