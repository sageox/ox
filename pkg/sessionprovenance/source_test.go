package sessionprovenance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSourceExclusionsSurviveReencoding(t *testing.T) {
	id := "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04"
	s := &SessionSource{ParserVersion: "codex-jsonl/v1", CapturedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), Version: 1, Agent: "codex", NativeSessionID: id, Generation: strings.Repeat("a", 64), Ranges: []SessionSourceRange{{Start: 10, End: 20}}}
	r := SessionSourceRecord{Version: 1, Agent: "codex", NativeSessionID: id, Generation: s.Generation}
	if err := r.Exclude(s, "deleted", time.Now()); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	fields["future_exclusion_policy"] = json.RawMessage(`{"deny":true}`)
	b, _ = json.Marshal(fields)
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	r.Coverage = nil // An index rebuild cannot erase recording intent.
	excluded, err := r.CheckCoverage(s, "missing-content", "")
	if err != nil || !excluded {
		t.Fatalf("exclusion must win over missing coverage: %v, %v", excluded, err)
	}
	b, _ = json.Marshal(r)
	fields = nil
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Deny bool `json:"deny"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(fields["future_exclusion_policy"])))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		t.Fatal(err)
	}
	if !policy.Deny || !r.Excluded(s) {
		t.Fatal("excluded history resurrected")
	}
	if _, err := SessionSourcePath("codex", "../../other"); err == nil {
		t.Fatal("unsafe native path accepted")
	}
}

// TestSourcePathIsAgentNamespacedAndTraversalSafe pins the contract's one
// agent-dependent behavior before v0.1.0 freezes it. Codex paths and the
// Path wrapper stay byte-identical, so no existing caller moves; any other
// well-formed agent gets its own sibling directory rather than a rejection;
// and because the agent becomes a path element, anything that is not one
// flat segment is refused -- that is the sole traversal surface, the native
// ID being pinned to canonical UUID form.
// Failure prevented: publishing a "shared" contract only Codex can use, or
// one that lets an agent value escape data/session-sources/.
func TestSourcePathIsAgentNamespacedAndTraversalSafe(t *testing.T) {
	id := "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04"
	codex, err := SessionSourcePath("codex", id)
	if err != nil || codex != "data/session-sources/codex/"+id+".json" {
		t.Fatalf("codex path changed: %q, %v", codex, err)
	}
	if legacy, err := Path(id); err != nil || legacy != codex {
		t.Fatalf("Path wrapper must stay byte-identical to the codex path: %q, %v", legacy, err)
	}
	if claude, err := SessionSourcePath("claude-code", id); err != nil || claude != "data/session-sources/claude-code/"+id+".json" {
		t.Fatalf("another agent must get its own namespace, not a rejection: %q, %v", claude, err)
	}
	for _, agent := range []string{"", ".", "..", "../../etc", "a/b", "a\\b"} {
		if _, err := SessionSourcePath(agent, id); err == nil {
			t.Fatalf("agent %q must be rejected as a path element", agent)
		}
	}
	if err := (&SessionSourceRecord{Version: 1, Agent: "claude-code", NativeSessionID: id}).Validate(); err != nil {
		t.Fatalf("blank record for another agent must validate: %v", err)
	}
	if err := (&SessionSourceRecord{Version: 1, Agent: "../x", NativeSessionID: id}).Validate(); err == nil {
		t.Fatal("record with a path-unsafe agent must be rejected")
	}
}

func TestSourceClearedProjectionStaysCleared(t *testing.T) {
	var record SessionSourceRecord
	if err := json.Unmarshal([]byte(`{"version":1,"projections":{"session":{"raw_oid":"old"}},"projection_revision":"old","future":true}`), &record); err != nil {
		t.Fatal(err)
	}
	record.Projections = nil
	record.ProjectionRevision = ""
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version         int                      `json:"version"`
		Agent           string                   `json:"agent"`
		NativeSessionID string                   `json:"native_session_id"`
		Generation      string                   `json:"generation"`
		Coverage        []SessionSourceCoverage  `json:"coverage"`
		Exclusions      []SessionSourceExclusion `json:"exclusions"`
		UpdatedAt       string                   `json:"updated_at"`
		Future          bool                     `json:"future"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Future {
		t.Fatal("lost unknown field")
	}
}

func TestSourceRejectsInvalidCoverage(t *testing.T) {
	s := &SessionSource{ParserVersion: "codex-jsonl/v1", CapturedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), Version: 1, Agent: "codex", NativeSessionID: "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04", Generation: strings.Repeat("a", 64), Ranges: []SessionSourceRange{{Start: 0, End: 10}}}
	for _, span := range []SessionSourceCoverage{{Start: -1, End: 10}, {Start: 10, End: 10}, {Start: 10, End: 5}} {
		r := SessionSourceRecord{Version: 1, Agent: s.Agent, NativeSessionID: s.NativeSessionID, Generation: s.Generation, Coverage: []SessionSourceCoverage{span}}
		if err := r.ValidateSource(s); err == nil {
			t.Fatalf("accepted invalid coverage %+v", span)
		}
	}
}

func TestNestedReceiptFieldsSurviveUpdates(t *testing.T) {
	var record SessionSourceRecord
	err := json.Unmarshal([]byte(`{"coverage":[{"start":0,"end":1,"future":{"keep":true}}],"exclusions":[{"start":0,"end":1,"future":{"keep":true}}],"projections":{"session":{"raw_oid":"old","future":{"keep":true}}}}`), &record)
	if err != nil {
		t.Fatal(err)
	}
	record.Coverage[0].End = 2
	record.Exclusions[0].Reason = "deleted"
	projection := record.Projections["session"]
	projection.RawOID = "new"
	record.Projections["session"] = projection
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err = json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	// Plain aliases remove the extensible decoders so this assertion checks the wire shape.
	type coverageFields SessionSourceCoverage
	type exclusionFields SessionSourceExclusion
	type projectionFields SessionSourceProjection
	type futureField struct {
		Keep bool `json:"keep"`
	}
	var coverage []struct {
		coverageFields
		Future futureField `json:"future"`
	}
	var exclusions []struct {
		exclusionFields
		Future futureField `json:"future"`
	}
	var projections map[string]struct {
		projectionFields
		Future futureField `json:"future"`
	}
	for key, target := range map[string]any{"coverage": &coverage, "exclusions": &exclusions, "projections": &projections} {
		decoder := json.NewDecoder(strings.NewReader(string(wire[key])))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(target); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	if len(coverage) != 1 || len(exclusions) != 1 || len(projections) != 1 {
		t.Fatal("receipt collections changed shape")
	}
	if !coverage[0].Future.Keep || !exclusions[0].Future.Keep || !projections["session"].Future.Keep {
		t.Fatal("lost nested extension")
	}
	if coverage[0].End != 2 || exclusions[0].Reason != "deleted" || projections["session"].RawOID != "new" {
		t.Fatal("known fields were not updated")
	}
}

func TestSourceRequiresParserAndCaptureProvenance(t *testing.T) {
	base := SessionSource{Version: 1, Agent: "codex", NativeSessionID: "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04", Generation: strings.Repeat("a", 64), Ranges: []SessionSourceRange{{Start: 0, End: 1}}, ParserVersion: "codex-jsonl/v1", CapturedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"parser", "capture", "invalid-time"} {
		source := base
		switch field {
		case "parser":
			source.ParserVersion = " "
		case "capture":
			source.CapturedAt = time.Time{}
		case "invalid-time":
			source.CapturedAt = time.Time{}
		}
		if source.Validate() == nil {
			t.Fatalf("accepted missing/invalid %s", field)
		}
	}
}
