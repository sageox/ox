package planreview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// One operation (submit a round, reopen an item, resolve or accept an item)
// writes exactly one immutable record, named by its acceptance time, its
// operation id and a digest of its bytes:
//
//	feedback/round-<time>-<id>.<digest>.json
//	feedback/resolutions/<time>-<id>.<digest>.json
//
// The request is what the client froze; the record adds what the host
// materializes when it accepts it (slug, acceptance time, the caller's
// identity stamped as reviewer). The request digest covers only the frozen
// bytes, so a retry accepted later is the same operation, never a new one.
//
// A submission never writes remaps, lifecycle events or plan files. Remaps are
// written only by the plan's author, one record each (RemapRecord):
//
//	feedback/remaps/<time>-<id>.<digest>.json

// Receipt states.
const (
	ReceiptCreated          = "created"           // the record was written by this call
	ReceiptAlreadyPublished = "already_published" // the operation was already stored, from this reviewer
	ReceiptConflict         = "conflict"          // the operation is stored with another payload or reviewer; nothing written
)

// Receipt is what a write of one operation's record found or did. Path is the
// record that holds the operation.
type Receipt struct {
	State string `json:"state"`
	Path  string `json:"path"`
}

// RoundRequest is a frozen round request, decoded and validated. A host sets
// ID before Record only when the request carried none (ContentRoundID mints
// one for an exported review); everything else comes from the frozen bytes.
type RoundRequest struct {
	Round
	frozen []byte
}

// ParseRoundRequest decodes and validates the exact bytes a client froze for a
// round: at least one item, each with an anchor and a known status, and a
// well-formed id and slug when present. Unknown fields are kept for the record.
func ParseRoundRequest(frozen []byte) (*RoundRequest, error) {
	if _, _, err := roundObjects(frozen); err != nil {
		return nil, err
	}
	var r Round
	if err := json.Unmarshal(frozen, &r); err != nil {
		return nil, fmt.Errorf("parse round request: %w", err)
	}
	if r.Slug != "" {
		if err := validSlug(r.Slug); err != nil {
			return nil, err
		}
	}
	if r.ID != "" && !idPattern.MatchString(r.ID) {
		return nil, fmt.Errorf("invalid round id %q: want 8-64 chars of [A-Za-z0-9_-]", r.ID)
	}
	if len(r.Items) == 0 {
		return nil, errors.New("round request has no items")
	}
	for i := range r.Items {
		if strings.TrimSpace(r.Items[i].Anchor) == "" {
			return nil, fmt.Errorf("item %d has no anchor", i)
		}
		s := Status(strings.ToLower(strings.TrimSpace(string(r.Items[i].Status))))
		switch s {
		case "":
			s = StatusComment
		case StatusApprove, StatusRequestChange, StatusFlag, StatusComment:
		default:
			return nil, fmt.Errorf("item %d: unknown status %q (want approve|request-change|flag|comment)", i, r.Items[i].Status)
		}
		r.Items[i].Status = s
	}
	return &RoundRequest{Round: r, frozen: frozen}, nil
}

// Record materializes the request as its immutable record: accepted at at for
// the plan slug, from reviewer — the host's identity for the caller, stamped
// on the round and on every item.
func (q *RoundRequest) Record(slug, reviewer string, at time.Time) (File, error) {
	if !idPattern.MatchString(q.ID) {
		return File{}, fmt.Errorf("round request needs an id of 8-64 chars of [A-Za-z0-9_-], have %q", q.ID)
	}
	if err := validSlug(slug); err != nil {
		return File{}, err
	}
	top, items, err := roundObjects(q.frozen)
	if err != nil {
		return File{}, err
	}
	if len(items) != len(q.Items) {
		return File{}, fmt.Errorf("round request has %d items, its frozen bytes %d", len(q.Items), len(items))
	}
	for i, it := range items {
		it["status"] = rawJSON(q.Items[i].Status)
		setOrDrop(it, "reviewer", reviewer)
	}
	top["items"] = rawJSON(items)
	top["id"] = rawJSON(q.ID)
	top["slug"] = rawJSON(slug)
	top["created_at"] = rawJSON(at.UTC())
	top["request_sha256"] = rawJSON(sha256Hex(q.frozen))
	setOrDrop(top, "reviewer", reviewer)
	return record("feedback/round-", at, q.ID, top)
}

// ResolutionRequest is a frozen resolution request, decoded and validated. A
// host sets ID before Record only when the request carried none.
type ResolutionRequest struct {
	Resolution
	frozen []byte
}

// ParseResolutionRequest decodes and validates the exact bytes a client froze
// for a resolution: an anchor, a known state, and a well-formed id when
// present. Unknown fields are kept for the record.
func ParseResolutionRequest(frozen []byte) (*ResolutionRequest, error) {
	if _, err := object(frozen, resolutionFields); err != nil {
		return nil, fmt.Errorf("parse resolution request: %w", err)
	}
	var r Resolution
	if err := json.Unmarshal(frozen, &r); err != nil {
		return nil, fmt.Errorf("parse resolution request: %w", err)
	}
	if a := strings.TrimSpace(r.Anchor); a == "" || strings.ContainsAny(a, `/\`) || strings.Contains(a, "..") {
		return nil, fmt.Errorf("invalid anchor %q", r.Anchor)
	}
	switch r.State {
	case StateAddressed, StateWontfix, StateVerified:
	default:
		return nil, fmt.Errorf("invalid resolution state %q (want addressed|wontfix|verified)", r.State)
	}
	if r.ID != "" && !idPattern.MatchString(r.ID) {
		return nil, fmt.Errorf("invalid resolution id %q: want 8-64 chars of [A-Za-z0-9_-]", r.ID)
	}
	return &ResolutionRequest{Resolution: r, frozen: frozen}, nil
}

// Record materializes the request as its immutable record, accepted at at from
// reviewer, the host's identity for the caller.
func (q *ResolutionRequest) Record(reviewer string, at time.Time) (File, error) {
	if !idPattern.MatchString(q.ID) {
		return File{}, fmt.Errorf("resolution request needs an id of 8-64 chars of [A-Za-z0-9_-], have %q", q.ID)
	}
	top, err := object(q.frozen, resolutionFields)
	if err != nil {
		return File{}, fmt.Errorf("parse resolution request: %w", err)
	}
	top["id"] = rawJSON(q.ID)
	top["at"] = rawJSON(at.UTC())
	top["request_sha256"] = rawJSON(sha256Hex(q.frozen))
	setOrDrop(top, "reviewer", reviewer)
	return record("feedback/resolutions/", at, q.ID, top)
}

// RemapRecord is the immutable record of one remap. Its id is derived from the
// move, so the same move computed by two clones shares one.
func RemapRecord(m Remap) (File, error) {
	if m.From == "" || m.To == "" || m.At.IsZero() {
		return File{}, fmt.Errorf("remap needs from, to and at: %+v", m)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return File{}, fmt.Errorf("encode remap: %w", err)
	}
	id := "m" + sha256Hex([]byte(m.From + "\x00" + m.To))[:16]
	return File{Path: recordName("feedback/remaps/", m.At, id, data), Data: data}, nil
}

// Prior looks rec's operation up in a capture before rec is written, so a
// restarted request finds what an earlier attempt stored instead of writing
// another copy. It returns the receipt for the operation's earliest copy:
// ReceiptAlreadyPublished when that copy carries rec's payload from rec's
// reviewer, else ReceiptConflict. ok is false when the capture holds no copy,
// and rec should be written. Prior sees only what the capture read: a copy
// behind one of its failures is not found, and writing rec then stores a
// readable copy.
func Prior(inv Inventory, rec File) (r Receipt, ok bool) {
	k, _ := classify(rec.Path)
	if k != roundFile && k != resolutionFile {
		return Receipt{}, false
	}
	files := slices.Clone(inv.Files)
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	// same decodes a stored copy of rec's kind and reports whether it holds
	// rec's operation (found) and is the same operation (equal).
	var same func(data []byte) (found, equal bool)
	switch k {
	case roundFile:
		var want Round
		if json.Unmarshal(rec.Data, &want) != nil || want.ID == "" {
			return Receipt{}, false
		}
		same = func(data []byte) (bool, bool) {
			var got Round
			if json.Unmarshal(data, &got) != nil || got.ID != want.ID {
				return false, false
			}
			return true, sameRound(got, want)
		}
	default:
		var want Resolution
		if json.Unmarshal(rec.Data, &want) != nil || want.ID == "" {
			return Receipt{}, false
		}
		same = func(data []byte) (bool, bool) {
			var got Resolution
			if json.Unmarshal(data, &got) != nil || got.ID != want.ID {
				return false, false
			}
			return true, sameResolution(got, want)
		}
	}
	for _, f := range files {
		if fk, _ := classify(f.Path); fk != k {
			continue
		}
		if found, equal := same(f.Data); found {
			if equal {
				return Receipt{State: ReceiptAlreadyPublished, Path: f.Path}, true
			}
			return Receipt{State: ReceiptConflict, Path: f.Path}, true
		}
	}
	return Receipt{}, false
}

// The known fields of each request object. A request is decoded twice: into
// the typed fields that are validated, and into raw objects that carry its
// unknown fields into the record. encoding/json matches a field name
// case-insensitively, so a key differing from a known field only in case
// would be read by the first and not the second; object refuses one.
var (
	roundFields      = []string{"id", "slug", "reviewer", "created_at", "items", "request_sha256"}
	itemFields       = []string{"anchor", "section", "label", "quote", "status", "note", "reviewer"}
	resolutionFields = []string{"id", "anchor", "state", "commit", "note", "at", "reviewer", "request_sha256"}
)

// object decodes one JSON object of a frozen request, keeping every field.
func object(raw []byte, known []string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("not a JSON object")
	}
	for k := range m {
		for _, f := range known {
			if k != f && strings.EqualFold(k, f) {
				return nil, fmt.Errorf("field %q differs from %q only in case", k, f)
			}
		}
	}
	return m, nil
}

// roundObjects decodes a frozen round request and each of its items as raw
// objects.
func roundObjects(frozen []byte) (map[string]json.RawMessage, []map[string]json.RawMessage, error) {
	top, err := object(frozen, roundFields)
	if err != nil {
		return nil, nil, fmt.Errorf("parse round request: %w", err)
	}
	var raws []json.RawMessage
	if raw, ok := top["items"]; !ok {
		return nil, nil, errors.New("round request has no items")
	} else if err := json.Unmarshal(raw, &raws); err != nil {
		return nil, nil, fmt.Errorf("parse round request items: %w", err)
	}
	items := make([]map[string]json.RawMessage, len(raws))
	for i, raw := range raws {
		if items[i], err = object(raw, itemFields); err != nil {
			return nil, nil, fmt.Errorf("parse round request item %d: %w", i, err)
		}
	}
	return top, items, nil
}

func record(prefix string, at time.Time, id string, fields map[string]json.RawMessage) (File, error) {
	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return File{}, fmt.Errorf("encode record: %w", err)
	}
	if len(data) > MaxRecordBytes {
		return File{}, fmt.Errorf("record is %d bytes, over the %d-byte limit", len(data), MaxRecordBytes)
	}
	return File{Path: recordName(prefix, at, id, data), Data: data}, nil
}

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v) // strings, times and decoded JSON only: Marshal cannot fail
	return b
}

// setOrDrop sets a string field, or drops it when empty, as omitempty would.
func setOrDrop(m map[string]json.RawMessage, key, v string) {
	if v == "" {
		delete(m, key)
		return
	}
	m[key] = rawJSON(v)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// validSlug rejects a slug that is not a simple name: the slug is
// client-supplied and travels toward file paths.
func validSlug(slug string) error {
	s := strings.TrimSpace(slug)
	if s == "" {
		return errors.New("empty slug")
	}
	if strings.ContainsAny(s, `/\`) || strings.Contains(s, "..") {
		return fmt.Errorf("invalid slug %q: must be a simple name, not a path", slug)
	}
	return nil
}
