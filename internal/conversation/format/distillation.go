package format

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Distillation file layout under a discussion folder (D7, D10). The base
// episode file is distillation/distillation.jsonl; the three sidecars are
// folded over it by Project.
const (
	DistillationDirName  = "distillation"
	DistillationFileName = "distillation.jsonl"
	EditsFileName        = "edits.jsonl"
	FinalizeFileName     = "finalize.jsonl"
	TTLExtendsFileName   = "ttl_extends.jsonl"
	distillationRelBase  = DistillationDirName + "/" + DistillationFileName
	editsRelBase         = DistillationDirName + "/" + EditsFileName
	finalizeRelBase      = DistillationDirName + "/" + FinalizeFileName
	ttlExtendsRelBase    = DistillationDirName + "/" + TTLExtendsFileName
)

// Episode statuses observed in the wild.
const (
	EpisodeStatusDraft     = "draft"
	EpisodeStatusFinalized = "finalized"
	EpisodeStatusSkipped   = "skipped"
	EpisodeStatusFailed    = "failed"
)

// Provenance is the extraction provenance block of an episode header.
type Provenance struct {
	ExtractedAt      time.Time `json:"extracted_at"`
	ExtractedByRunID string    `json:"extracted_by_run_id"`
	LLMModel         string    `json:"llm_model"`
	PromptVersion    string    `json:"prompt_version"`
	BackstopVersion  int       `json:"backstop_version"`
}

// Episode is the header line of distillation.jsonl. It decodes strictly
// (mirroring the producer): a malformed or absent episode line fails the
// whole distillation load.
//
// TTLExpiresAt is the committed header value — stale by design and ignored by
// projection, which recomputes TTL from provenance.extracted_at and the
// ttl_extends sidecar (D10).
type Episode struct {
	Type          string     `json:"type"`
	SchemaVersion int        `json:"schema_version"`
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	RecordingURI  string     `json:"recording_uri"`
	TTLExpiresAt  *time.Time `json:"ttl_expires_at,omitempty"`
	Provenance    Provenance `json:"provenance"`
	SkippedReason string     `json:"skipped_reason,omitempty"`
}

// Topic is one topic line of distillation.jsonl. Decodes leniently.
type Topic struct {
	Type    string   `json:"type"`
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	CueURIs []string `json:"cue_uris,omitempty"`
}

// AtomQuote is the supporting quote of an atom.
type AtomQuote struct {
	CueRef int    `json:"cue_ref"`
	Text   string `json:"text"`
}

// AtomSource is the source block of an atom. Modern writers emit the plural
// uris array of citation URIs; the legacy singular uri is tolerated as an
// unparsed citation string (never interpreted as the modern grammar).
type AtomSource struct {
	URIs    []string `json:"uris,omitempty"`
	URI     string   `json:"uri,omitempty"`
	Speaker string   `json:"speaker,omitempty"`
}

// CitationURIs returns the source citations, folding the legacy singular
// spelling into the plural view.
func (s AtomSource) CitationURIs() []string {
	if len(s.URIs) > 0 {
		return s.URIs
	}
	if s.URI != "" {
		return []string{s.URI}
	}
	return nil
}

// Atom is one atom line of distillation.jsonl. Decodes leniently. Atoms are
// bi-temporal (D11): ValidTo == nil after the projection fold means current;
// a non-nil ValidTo marks a tombstone, with SupersededBy naming the
// replacement when the atom was superseded rather than rejected.
type Atom struct {
	Type         string     `json:"type"`
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	Signal       string     `json:"signal"`
	Text         string     `json:"text"`
	TopicID      string     `json:"topic_id"`
	Source       AtomSource `json:"source"`
	Quote        *AtomQuote `json:"quote,omitempty"`
	Confidence   float64    `json:"confidence"`
	ValidFrom    *time.Time `json:"valid_from,omitempty"`
	ValidTo      *time.Time `json:"valid_to,omitempty"`
	SupersededBy string     `json:"superseded_by,omitempty"`
}

// Distillation is the decoded base distillation.jsonl of one discussion —
// pre-projection. Invalid surfaces skipped topic/atom lines.
type Distillation struct {
	Episode Episode
	Topics  []Topic
	Atoms   []Atom
	Invalid []InvalidRecord
}

// LoadDistillation reads distillation/distillation.jsonl from a discussion
// folder. A missing file (or missing distillation/ directory) is (nil, nil):
// no distillation is data, not an error (D13).
//
// The episode line decodes strictly — no episode line, an unparseable one, or
// one missing its id or status fails the load. Topic and atom lines decode
// leniently: malformed lines are skipped and surfaced, never fatal (mirroring
// the producer's strict/lenient split).
func LoadDistillation(discussionRoot string) (*Distillation, error) {
	root, err := openOptionalRoot(discussionRoot)
	if err != nil || root == nil {
		return nil, err
	}
	defer root.Close()
	return LoadDistillationIn(root)
}

// LoadDistillationIn is LoadDistillation over an already-open
// discussion-folder root, for callers that hold the folder open (derived from
// a validated discussions root) and must not re-open it by absolute path.
func LoadDistillationIn(root *os.Root) (*Distillation, error) {
	path := filepath.Join(root.Name(), DistillationDirName, DistillationFileName)
	data, err := readOptionalFileIn(root, distillationRelBase)
	if err != nil || data == nil {
		return nil, err
	}

	d := &Distillation{}
	sawEpisode := false
	line := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			d.Invalid = append(d.Invalid, InvalidRecord{Path: distillationRelBase, Line: line, Reason: "malformed line: " + err.Error()})
			continue
		}
		switch probe.Type {
		case "episode":
			var ep Episode
			if err := json.Unmarshal(raw, &ep); err != nil {
				return nil, fmt.Errorf("parse %s:%d: episode line: %w", path, line, err)
			}
			if ep.ID == "" || ep.Status == "" {
				return nil, fmt.Errorf("parse %s:%d: episode line missing id or status", path, line)
			}
			if sawEpisode {
				return nil, fmt.Errorf("parse %s:%d: multiple episode lines", path, line)
			}
			d.Episode = ep
			sawEpisode = true
		case "topic":
			var tp Topic
			if err := json.Unmarshal(raw, &tp); err != nil || tp.ID == "" {
				d.Invalid = append(d.Invalid, InvalidRecord{Path: distillationRelBase, Line: line, Reason: "unusable topic line"})
				continue
			}
			d.Topics = append(d.Topics, tp)
		case "atom":
			var at Atom
			if err := json.Unmarshal(raw, &at); err != nil || at.ID == "" {
				d.Invalid = append(d.Invalid, InvalidRecord{Path: distillationRelBase, Line: line, Reason: "unusable atom line"})
				continue
			}
			d.Atoms = append(d.Atoms, at)
		default:
			d.Invalid = append(d.Invalid, InvalidRecord{Path: distillationRelBase, Line: line, Reason: fmt.Sprintf("unknown record type %q", probe.Type)})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	if !sawEpisode {
		return nil, fmt.Errorf("parse %s: no episode line", path)
	}
	return d, nil
}

// Edit actions folded by Project (D10). Retired redact lines are no-ops.
const (
	EditActionEdit   = "edit"
	EditActionReject = "reject"
	EditActionAdd    = "add"
	EditActionRedact = "redact"
)

// EditRecord is one line of distillation/edits.jsonl. An edit targets either
// an atom (AtomID set) or a topic (TopicID set, no AtomID).
type EditRecord struct {
	EditID  string    `json:"edit_id"`
	Action  string    `json:"action"`
	AtomID  string    `json:"atom_id,omitempty"`
	TopicID string    `json:"topic_id,omitempty"`
	Field   string    `json:"field,omitempty"`
	Value   string    `json:"value,omitempty"`
	Atom    *Atom     `json:"atom,omitempty"`
	Actor   string    `json:"actor,omitempty"`
	At      time.Time `json:"at"`
}

// FinalizeRecord is one line of distillation/finalize.jsonl. The first
// well-formed marker promotes a draft episode to finalized (D10).
type FinalizeRecord struct {
	Actor string    `json:"actor,omitempty"`
	At    time.Time `json:"at"`
}

// TTLExtendRecord is one line of distillation/ttl_extends.jsonl. Each
// well-formed line extends the recomputed TTL by 30 minutes (D10).
type TTLExtendRecord struct {
	Actor string    `json:"actor,omitempty"`
	At    time.Time `json:"at,omitempty"`
}

// LoadEdits reads distillation/edits.jsonl leniently. Missing file is
// (nil, nil, nil); malformed or unrecognized lines are skipped and surfaced.
func LoadEdits(discussionRoot string) ([]EditRecord, []InvalidRecord, error) {
	root, err := openOptionalRoot(discussionRoot)
	if err != nil || root == nil {
		return nil, nil, err
	}
	defer root.Close()
	return LoadEditsIn(root)
}

// LoadEditsIn is LoadEdits over an already-open discussion-folder root.
func LoadEditsIn(root *os.Root) ([]EditRecord, []InvalidRecord, error) {
	var edits []EditRecord
	invalid, err := loadJSONLines(root, EditsFileName, editsRelBase, func(raw []byte) error {
		var e EditRecord
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		switch e.Action {
		case EditActionEdit, EditActionReject, EditActionAdd, EditActionRedact:
		default:
			return fmt.Errorf("unknown edit action %q", e.Action)
		}
		// A live edit without a timestamp cannot be folded: a zero-time
		// reject would tombstone its atom at year 0001, hiding it from the
		// current view and emitting an invalid valid_to under
		// --include-superseded. Retired redact lines stay accepted — they
		// are no-ops by contract and legacy data omits at.
		if e.Action != EditActionRedact && e.At.IsZero() {
			return fmt.Errorf("edit record missing at timestamp")
		}
		edits = append(edits, e)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return edits, invalid, nil
}

// LoadFinalize reads distillation/finalize.jsonl leniently. A well-formed
// marker is a JSON object with a parseable, non-zero at timestamp.
func LoadFinalize(discussionRoot string) ([]FinalizeRecord, []InvalidRecord, error) {
	root, err := openOptionalRoot(discussionRoot)
	if err != nil || root == nil {
		return nil, nil, err
	}
	defer root.Close()
	return LoadFinalizeIn(root)
}

// LoadFinalizeIn is LoadFinalize over an already-open discussion-folder root.
func LoadFinalizeIn(root *os.Root) ([]FinalizeRecord, []InvalidRecord, error) {
	var records []FinalizeRecord
	invalid, err := loadJSONLines(root, FinalizeFileName, finalizeRelBase, func(raw []byte) error {
		var f FinalizeRecord
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		if f.At.IsZero() {
			return fmt.Errorf("finalize marker missing at timestamp")
		}
		records = append(records, f)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return records, invalid, nil
}

// LoadTTLExtends reads distillation/ttl_extends.jsonl leniently. Every
// well-formed JSON-object line counts as one 30-minute extension.
func LoadTTLExtends(discussionRoot string) ([]TTLExtendRecord, []InvalidRecord, error) {
	root, err := openOptionalRoot(discussionRoot)
	if err != nil || root == nil {
		return nil, nil, err
	}
	defer root.Close()
	return LoadTTLExtendsIn(root)
}

// LoadTTLExtendsIn is LoadTTLExtends over an already-open discussion-folder
// root.
func LoadTTLExtendsIn(root *os.Root) ([]TTLExtendRecord, []InvalidRecord, error) {
	var records []TTLExtendRecord
	invalid, err := loadJSONLines(root, TTLExtendsFileName, ttlExtendsRelBase, func(raw []byte) error {
		// json.Unmarshal accepts a literal null into a struct without
		// error; a null line must not count as a +30m extension. Only a
		// JSON object is a well-formed marker.
		if len(raw) == 0 || raw[0] != '{' {
			return fmt.Errorf("ttl extend record is not a JSON object")
		}
		var r TTLExtendRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		records = append(records, r)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return records, invalid, nil
}

// loadJSONLines is the shared lenient JSONL sidecar reader: missing file is
// no records, blank lines skipped, each non-blank line handed to decode, and
// any decode error surfaced as an InvalidRecord rather than failing the load.
func loadJSONLines(root *os.Root, fileName, relPath string, decode func(raw []byte) error) ([]InvalidRecord, error) {
	path := filepath.Join(root.Name(), DistillationDirName, fileName)
	data, err := readOptionalFileIn(root, relPath)
	if err != nil || data == nil {
		return nil, err
	}
	var invalid []InvalidRecord
	line := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		if err := decode(raw); err != nil {
			reason := err.Error()
			if !strings.Contains(reason, "unknown") && !strings.Contains(reason, "missing") {
				reason = "malformed line: " + reason
			}
			invalid = append(invalid, InvalidRecord{Path: relPath, Line: line, Reason: reason})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return invalid, nil
}
