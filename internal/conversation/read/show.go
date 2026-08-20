package read

import (
	"strings"
	"time"

	"github.com/sageox/ox/internal/conversation/format"
)

// SummaryReasonNotYetGenerated is the typed absence reason for a folder that
// has no summary yet (D13: server writes land in stages; a missing summary is
// data, not an error).
const SummaryReasonNotYetGenerated = "not_yet_generated"

// summaryReasonUnreadable marks a summary.json that exists but cannot be
// parsed — still absence-shaped, surfaced with a warning.
const summaryReasonUnreadable = "unreadable"

// ShowSummary is the summary block of the show payload.
type ShowSummary struct {
	Available    bool   `json:"available"`
	HumanSummary string `json:"human_summary,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// ShowData is the show envelope payload (L1: metadata plus the human
// summary, nothing else — D19).
type ShowData struct {
	ConversationID string      `json:"conversation_id"`
	RecordingID    string      `json:"recording_id"`
	Title          string      `json:"title"`
	RecordedAt     string      `json:"recorded_at,omitempty"`
	Participants   []string    `json:"participants,omitempty"`
	Summary        ShowSummary `json:"summary"`
}

// Show returns metadata and the human summary for one conversation.
// summary.json is the source; summary.md is the pre-JSON fallback (D19).
func (r *Reader) Show(rawID string) *Envelope {
	start := r.now()
	id, idErr := ParseID(rawID)
	if idErr != nil {
		return r.finishError(start, idErr, nil)
	}
	rw, droot, lookErr := r.lookup(id.RecordingID)
	if lookErr != nil {
		return r.finishError(start, lookErr, nil)
	}
	defer droot.Close()

	var warnings []string
	data := &ShowData{
		ConversationID: id.ConversationID,
		RecordingID:    id.RecordingID,
		Title:          rw.entry.Title,
		Participants:   rw.entry.Participants,
	}
	if !rw.recordedAt.IsZero() {
		data.RecordedAt = rw.recordedAt.UTC().Format(time.RFC3339)
	}

	meta, metaErr := format.LoadMetadataIn(droot)
	if metaErr != nil {
		warnings = append(warnings, "metadata.json unreadable: "+metaErr.Error())
	}
	if data.Title == "" && meta != nil && meta.Title != "" {
		data.Title = meta.Title
	}
	if data.Title == "" {
		data.Title = rw.entry.Folder
	}

	summary, sumErr := format.LoadSummaryIn(droot)
	switch {
	case sumErr != nil:
		warnings = append(warnings, "summary.json unreadable: "+sumErr.Error())
		data.Summary = ShowSummary{Reason: summaryReasonUnreadable}
	case summary != nil:
		data.Summary = ShowSummary{Available: true, HumanSummary: summary.HumanSummary}
		if names := summary.ParticipantNames(); len(names) > 0 {
			data.Participants = names // D12: summary participants, unjoined
		}
	default:
		md, mdErr := format.LoadSummaryMarkdownIn(droot)
		if mdErr != nil {
			warnings = append(warnings, "summary.md unreadable: "+mdErr.Error())
		}
		if len(md) > 0 {
			data.Summary = ShowSummary{Available: true, HumanSummary: strings.TrimSpace(string(md))}
		} else {
			data.Summary = ShowSummary{Reason: SummaryReasonNotYetGenerated}
		}
	}

	return r.finishSuccess(start, data, guidanceShow(id.ConversationID), warnings)
}
