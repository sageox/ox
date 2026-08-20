package read

import (
	"fmt"
	"os"
	"time"

	"github.com/sageox/ox/internal/conversation/format"
)

// EpisodeInfo is the projected episode header served by topics (D10:
// always projected; draft and finalized served identically, status is
// point-in-time metadata).
type EpisodeInfo struct {
	Status        string `json:"status"`
	ExtractedAt   string `json:"extracted_at,omitempty"`
	TTLExpiresAt  string `json:"ttl_expires_at,omitempty"`
	SkippedReason string `json:"skipped_reason,omitempty"`
}

// TopicRow is one topic overview row (L2: no atom bodies — one rung down).
type TopicRow struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	AtomCount int      `json:"atom_count"`
	CueURIs   []string `json:"cue_uris,omitempty"`
}

// TopicsData is the topics envelope payload.
type TopicsData struct {
	Episode         EpisodeInfo `json:"episode"`
	Topics          []TopicRow  `json:"topics"`
	AtomsTotal      int         `json:"atoms_total"`
	AtomsSuperseded int         `json:"atoms_superseded"`
}

// Topics serves the distillation overview: projected episode status plus
// topic rows with projected-current atom counts (D10/D11/D15).
func (r *Reader) Topics(rawID string) *Envelope {
	start := r.now()
	id, idErr := ParseID(rawID)
	if idErr != nil {
		return r.finishError(start, idErr, nil)
	}
	_, droot, lookErr := r.lookup(id.RecordingID)
	if lookErr != nil {
		return r.finishError(start, lookErr, nil)
	}
	defer droot.Close()
	projected, warnings, projErr := r.loadProjected(droot, id)
	if projErr != nil {
		return r.finishError(start, projErr, warnings)
	}

	counts := make(map[string]int, len(projected.Topics))
	for _, a := range format.CurrentAtoms(projected.Atoms) {
		counts[a.TopicID]++
	}
	topicsOut := make([]TopicRow, 0, len(projected.Topics))
	for _, tp := range projected.Topics {
		topicsOut = append(topicsOut, TopicRow{
			ID:        tp.ID,
			Title:     tp.Title,
			Summary:   tp.Summary,
			AtomCount: counts[tp.ID],
			CueURIs:   tp.CueURIs,
		})
	}
	current, superseded := projected.AtomCounts()
	data := &TopicsData{
		Episode:         episodeInfo(projected),
		Topics:          topicsOut,
		AtomsTotal:      current,
		AtomsSuperseded: superseded,
	}
	guidance := fmt.Sprintf("ox conversation topic %s <tp_id> for a topic's atoms.", id.ConversationID)
	return r.finishSuccess(start, data, guidance, warnings)
}

// loadProjected loads and folds the distillation through the open
// discussion-folder handle (D10) — derived from the validated discussions
// root, never an absolute-path re-open. A folder without a distillation is
// the typed no_distillation error; invalid records surfaced by the fold
// become a single advisory warning.
func (r *Reader) loadProjected(droot *os.Root, id *ID) (*format.Projected, []string, *Error) {
	projected, err := format.LoadProjectedDistillationIn(droot)
	if err != nil {
		return nil, nil, newError(ErrCodeReadError, fmt.Sprintf("load distillation: %v", err))
	}
	if projected == nil {
		return nil, nil, newError(ErrCodeNoDistillation,
			fmt.Sprintf("%s has no distillation yet; distillations are produced after summarization", id.ConversationID))
	}
	var warnings []string
	if n := len(projected.Invalid); n > 0 {
		warnings = append(warnings, fmt.Sprintf("%d unusable distillation record(s) were skipped", n))
	}
	return projected, warnings, nil
}

func episodeInfo(p *format.Projected) EpisodeInfo {
	info := EpisodeInfo{Status: p.Status, SkippedReason: p.SkippedReason}
	if !p.ExtractedAt.IsZero() {
		info.ExtractedAt = p.ExtractedAt.UTC().Format(time.RFC3339)
	}
	if !p.TTLExpiresAt.IsZero() {
		info.TTLExpiresAt = p.TTLExpiresAt.UTC().Format(time.RFC3339)
	}
	return info
}
