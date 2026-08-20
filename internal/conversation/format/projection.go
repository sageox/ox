package format

import (
	"os"
	"time"
)

// TTL recomputation constants (D10): the projected TTL is
// extracted_at + ttlBase + n×ttlExtendStep, capped at extracted_at + ttlCap.
// The committed episode-header ttl_expires_at is stale by design and ignored.
const (
	ttlBase       = time.Hour
	ttlExtendStep = 30 * time.Minute
	ttlCap        = 4 * time.Hour
)

// Projected is the served view of one distillation: base state folded with
// the edits / finalize / ttl_extends sidecars (D10) and bi-temporal atom
// bookkeeping resolved (D11).
type Projected struct {
	// Status is the projected episode status. Draft and finalized are served
	// identically — status is point-in-time metadata, with no gating.
	Status        string
	ExtractedAt   time.Time
	TTLExpiresAt  time.Time
	SkippedReason string
	Topics        []Topic
	// Atoms is every atom post-fold, tombstones included; filter with
	// CurrentAtoms / SupersededAtoms.
	Atoms []Atom
	// Invalid aggregates the surfaced defects from the base file and every
	// sidecar, in load order.
	Invalid []InvalidRecord
}

// AtomCounts reports the projected-current and superseded totals the
// envelopes always carry (D11).
func (p *Projected) AtomCounts() (current, superseded int) {
	for _, a := range p.Atoms {
		if a.ValidTo == nil {
			current++
		} else {
			superseded++
		}
	}
	return current, superseded
}

// CurrentAtoms returns the projected-current view: atoms whose ValidTo is
// nil after the edits fold. An atom rejected by an edit is excluded even if
// its base line looks current (D11).
func CurrentAtoms(atoms []Atom) []Atom {
	var out []Atom
	for _, a := range atoms {
		if a.ValidTo == nil {
			out = append(out, a)
		}
	}
	return out
}

// SupersededAtoms returns the tombstones: atoms with a non-nil ValidTo,
// carrying valid_from / valid_to / superseded_by so succession chains are
// auditable (D11).
func SupersededAtoms(atoms []Atom) []Atom {
	var out []Atom
	for _, a := range atoms {
		if a.ValidTo != nil {
			out = append(out, a)
		}
	}
	return out
}

// Project folds the three sidecars over a base distillation per D10:
//
//   - edits.jsonl: action=edit applies known fields to its atom or topic;
//     action=reject tombstones its atom at the edit time; action=add appends
//     the embedded atom; retired redact lines are no-ops. Edits naming
//     unknown atoms, topics, or fields are ignored (the sidecar may reference
//     state ox has not modeled).
//   - finalize.jsonl: the first well-formed marker promotes draft → finalized.
//   - ttl_extends.jsonl: TTL is recomputed as extracted_at + 1h + n×30m,
//     capped at 4h; the stale header value is ignored.
//
// skipped/failed episodes project their header unchanged (status +
// skipped_reason); the fold still runs so counts stay consistent.
// The base Distillation is not mutated.
func Project(d *Distillation, edits []EditRecord, finalize []FinalizeRecord, ttlExtends []TTLExtendRecord) *Projected {
	if d == nil {
		return nil
	}
	p := &Projected{
		Status:        d.Episode.Status,
		ExtractedAt:   d.Episode.Provenance.ExtractedAt,
		SkippedReason: d.Episode.SkippedReason,
		Topics:        append([]Topic(nil), d.Topics...),
		Atoms:         append([]Atom(nil), d.Atoms...),
		Invalid:       append([]InvalidRecord(nil), d.Invalid...),
	}

	atomIdx := make(map[string]int, len(p.Atoms))
	for i, a := range p.Atoms {
		atomIdx[a.ID] = i
	}
	topicIdx := make(map[string]int, len(p.Topics))
	for i, t := range p.Topics {
		topicIdx[t.ID] = i
	}

	for _, e := range edits {
		switch e.Action {
		case EditActionRedact:
			// Retired action: no-op by contract.
		case EditActionReject:
			if i, ok := atomIdx[e.AtomID]; ok && p.Atoms[i].ValidTo == nil {
				at := e.At
				p.Atoms[i].ValidTo = &at
			}
		case EditActionAdd:
			if e.Atom != nil && e.Atom.ID != "" {
				atom := *e.Atom
				if atom.ValidFrom == nil && !e.At.IsZero() {
					at := e.At
					atom.ValidFrom = &at
				}
				if _, dup := atomIdx[atom.ID]; !dup {
					atomIdx[atom.ID] = len(p.Atoms)
					p.Atoms = append(p.Atoms, atom)
				}
			}
		case EditActionEdit:
			if e.AtomID != "" {
				if i, ok := atomIdx[e.AtomID]; ok {
					applyAtomEdit(&p.Atoms[i], e.Field, e.Value)
				}
			} else if e.TopicID != "" {
				if i, ok := topicIdx[e.TopicID]; ok {
					applyTopicEdit(&p.Topics[i], e.Field, e.Value)
				}
			}
		}
	}

	if len(finalize) > 0 && p.Status == EpisodeStatusDraft {
		p.Status = EpisodeStatusFinalized
	}

	if !p.ExtractedAt.IsZero() {
		extension := ttlBase + time.Duration(len(ttlExtends))*ttlExtendStep
		if extension > ttlCap {
			extension = ttlCap
		}
		p.TTLExpiresAt = p.ExtractedAt.Add(extension)
	}
	return p
}

// applyAtomEdit applies one known atom field edit; unknown fields are
// ignored (lenient — the editor UI may grow fields ox has not modeled).
func applyAtomEdit(a *Atom, field, value string) {
	switch field {
	case "text":
		a.Text = value
	case "kind":
		a.Kind = value
	case "signal":
		a.Signal = value
	}
}

// applyTopicEdit applies one known topic field edit. The editor writes the
// topic summary under the field name "takeaway" (observed in production
// sidecars); both spellings land on Summary.
func applyTopicEdit(t *Topic, field, value string) {
	switch field {
	case "title":
		t.Title = value
	case "summary", "takeaway":
		t.Summary = value
	}
}

// LoadProjectedDistillation is the one-call read path: it loads the base
// distillation and all three sidecars from a discussion folder and returns
// the projected view. A folder with no distillation at all returns
// (nil, nil). Sidecar files are each optional; their surfaced invalid lines
// are aggregated into Projected.Invalid.
func LoadProjectedDistillation(discussionRoot string) (*Projected, error) {
	root, err := openOptionalRoot(discussionRoot)
	if err != nil || root == nil {
		return nil, err
	}
	defer root.Close()
	return LoadProjectedDistillationIn(root)
}

// LoadProjectedDistillationIn is LoadProjectedDistillation over an
// already-open discussion-folder root, for callers that hold the folder open
// (derived from a validated discussions root) and must not re-open it by
// absolute path.
func LoadProjectedDistillationIn(root *os.Root) (*Projected, error) {
	d, err := LoadDistillationIn(root)
	if err != nil || d == nil {
		return nil, err
	}
	edits, editsInvalid, err := LoadEditsIn(root)
	if err != nil {
		return nil, err
	}
	finalize, finalizeInvalid, err := LoadFinalizeIn(root)
	if err != nil {
		return nil, err
	}
	extends, extendsInvalid, err := LoadTTLExtendsIn(root)
	if err != nil {
		return nil, err
	}
	p := Project(d, edits, finalize, extends)
	p.Invalid = append(p.Invalid, editsInvalid...)
	p.Invalid = append(p.Invalid, finalizeInvalid...)
	p.Invalid = append(p.Invalid, extendsInvalid...)
	return p, nil
}
