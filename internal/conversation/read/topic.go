package read

import (
	"fmt"
	"time"

	"github.com/sageox/ox/internal/conversation/format"
)

// AtomQuoteView is the supporting quote of a served atom.
type AtomQuoteView struct {
	CueRef int    `json:"cue_ref"`
	Text   string `json:"text"`
}

// AtomSourceView carries an atom's citation URIs (legacy singular spellings
// folded in) and its opaque speaker id (D12: unjoined).
type AtomSourceView struct {
	URIs    []string `json:"uris,omitempty"`
	Speaker string   `json:"speaker,omitempty"`
}

// AtomView is one served atom (L3). The bi-temporal fields appear only on
// tombstones (D11: --include-superseded makes succession chains auditable).
type AtomView struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Signal       string          `json:"signal"`
	Text         string          `json:"text"`
	Quote        *AtomQuoteView  `json:"quote,omitempty"`
	Source       *AtomSourceView `json:"source,omitempty"`
	Confidence   float64         `json:"confidence"`
	ValidFrom    string          `json:"valid_from,omitempty"`
	ValidTo      string          `json:"valid_to,omitempty"`
	SupersededBy string          `json:"superseded_by,omitempty"`
}

// TopicDetail is the topic header of the topic payload.
type TopicDetail struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// TopicData is the topic envelope payload.
type TopicData struct {
	Topic           TopicDetail `json:"topic"`
	Atoms           []AtomView  `json:"atoms"`
	AtomsTotal      int         `json:"atoms_total"`
	AtomsSuperseded int         `json:"atoms_superseded"`
}

// Topic serves one topic with its atoms (D11/D21). Topics are addressed by
// exact tp_<uuidv7> only; the default view is projected-current, and
// includeSuperseded adds tombstones.
func (r *Reader) Topic(rawID, topicID string, includeSuperseded bool) *Envelope {
	start := r.now()
	id, idErr := ParseID(rawID)
	if idErr != nil {
		return r.finishError(start, idErr, nil)
	}
	if tpErr := ValidateTopicID(topicID); tpErr != nil {
		return r.finishError(start, tpErr, nil)
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

	var topic *format.Topic
	for i := range projected.Topics {
		if projected.Topics[i].ID == topicID {
			topic = &projected.Topics[i]
			break
		}
	}
	if topic == nil {
		return r.finishError(start, newError(ErrCodeTopicNotFound,
			fmt.Sprintf("distillation of %s has no topic %s; copy the exact id from ox conversation topics", id.ConversationID, topicID)), warnings)
	}

	var atoms []AtomView
	current, superseded := 0, 0
	for _, a := range projected.Atoms {
		if a.TopicID != topicID {
			continue
		}
		tombstone := a.ValidTo != nil
		if tombstone {
			superseded++
		} else {
			current++
		}
		if tombstone && !includeSuperseded {
			continue
		}
		atoms = append(atoms, atomView(a, tombstone))
	}

	data := &TopicData{
		Topic:           TopicDetail{ID: topic.ID, Title: topic.Title, Summary: topic.Summary},
		Atoms:           atoms,
		AtomsTotal:      current,
		AtomsSuperseded: superseded,
	}
	guidance := fmt.Sprintf("Follow a citation to its transcript slice: ox conversation transcript '<sageox:// URI>'. Overview: ox conversation topics %s.", id.ConversationID)
	return r.finishSuccess(start, data, guidance, warnings)
}

// atomView projects one atom to its envelope shape. Bi-temporal fields are
// emitted only for tombstones — a current atom's valid_from is bookkeeping,
// not disclosure.
func atomView(a format.Atom, tombstone bool) AtomView {
	v := AtomView{
		ID:         a.ID,
		Kind:       a.Kind,
		Signal:     a.Signal,
		Text:       a.Text,
		Confidence: a.Confidence,
	}
	if a.Quote != nil {
		v.Quote = &AtomQuoteView{CueRef: a.Quote.CueRef, Text: a.Quote.Text}
	}
	if uris := a.Source.CitationURIs(); len(uris) > 0 || a.Source.Speaker != "" {
		v.Source = &AtomSourceView{URIs: uris, Speaker: a.Source.Speaker}
	}
	if tombstone {
		if a.ValidFrom != nil {
			v.ValidFrom = a.ValidFrom.UTC().Format(time.RFC3339)
		}
		if a.ValidTo != nil {
			v.ValidTo = a.ValidTo.UTC().Format(time.RFC3339)
		}
		v.SupersededBy = a.SupersededBy
	}
	return v
}
