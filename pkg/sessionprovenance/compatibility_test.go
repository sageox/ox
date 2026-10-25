package sessionprovenance

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExportCompatibilityFixtures(t *testing.T) {
	for _, name := range []string{"live", "import"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var source Source
			if err = json.Unmarshal(data, &source); err != nil {
				t.Fatal(err)
			}
			if err = source.Validate(); err != nil {
				t.Fatal(err)
			}
			record := Record{Version: 1, Agent: source.Agent, NativeSessionID: source.NativeSessionID, Generation: source.Generation, Coverage: []Coverage{{Start: 0, End: 120, SessionName: "export", RawOID: strings.Repeat("c", 64)}}, UpdatedAt: time.Now()}
			excluded, err := record.CheckCoverage(&source, "export", strings.Repeat("c", 64))
			if err != nil || excluded {
				t.Fatalf("export not covered: %v %v", excluded, err)
			}
			// Decode the serialized CLI value through the public backend alias.
			encoded, err := json.Marshal(source)
			if err != nil {
				t.Fatal(err)
			}
			var backend SessionSource
			if err = json.Unmarshal(encoded, &backend); err != nil {
				t.Fatal(err)
			}
			if err = backend.Validate(); err != nil {
				t.Fatal(err)
			}
			if !source.CapturedAt.Equal(backend.CapturedAt) || backend.CapturedAt.Nanosecond() != 123456789 {
				t.Fatal("capture timestamp changed")
			}
			if (backend.ImportedAt != nil) != (name == "import") {
				t.Fatal("import classification changed")
			}
			if string(backend.Extra["future_source"]) != "{\"retain\":true}" {
				t.Fatalf("lost source extension: %s", encoded)
			}
		})
	}
}

func TestPreCaptureExclusionFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/exclusion.json")
	if err != nil {
		t.Fatal(err)
	}
	var record Record
	if err = json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if err = record.Validate(); err != nil {
		t.Fatal(err)
	}
	source := Source{Version: 1, Agent: record.Agent, NativeSessionID: record.NativeSessionID, Generation: strings.Repeat("a", 64), Ranges: []Range{{Start: 0, End: 1}}, ParserVersion: "codex-jsonl/v1", CapturedAt: time.Now()}
	excluded, err := record.CheckCoverage(&source, "not-published", "")
	if err != nil || !excluded {
		t.Fatalf("pre-capture privacy intent lost: %v %v", excluded, err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var again Record
	if err = json.Unmarshal(encoded, &again); err != nil {
		t.Fatal(err)
	}
	if string(again.Extra["future_record"]) != "true" || len(again.Exclusions[0].Extra["future_exclusion"]) == 0 {
		t.Fatal("lost privacy extensions")
	}
	record.Coverage = []Coverage{{Start: 0, End: 1, SessionName: "export", RawOID: strings.Repeat("b", 64)}}
	if record.Validate() == nil {
		t.Fatal("coverage without generation accepted")
	}
}

func TestRecordRejectsMalformedHashesAndPaths(t *testing.T) {
	valid := Record{Version: 1, Agent: "codex", NativeSessionID: "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04", Generation: strings.Repeat("a", 64)}
	for _, tc := range []struct {
		name   string
		mutate func(*Record)
	}{
		{"uppercase generation", func(r *Record) { r.Generation = strings.Repeat("A", 64) }},
		{"nonhex oid", func(r *Record) {
			r.Coverage = []Coverage{{Start: 0, End: 1, SessionName: "export", RawOID: strings.Repeat("z", 64)}}
		}},
		{"path traversal", func(r *Record) {
			r.Coverage = []Coverage{{Start: 0, End: 1, SessionName: "../export", RawOID: strings.Repeat("a", 64)}}
		}},
		{"negative exclusion", func(r *Record) { r.Exclusions = []Exclusion{{Start: -1, End: -1, Reason: "deleted"}} }},
		{"unsupported version", func(r *Record) { r.Version = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := valid
			tc.mutate(&record)
			if record.Validate() == nil {
				t.Fatal("accepted invalid record")
			}
		})
	}
}

func TestInvalidTimestampRejectedAtDecode(t *testing.T) {
	for _, data := range []string{`{"captured_at":"yesterday"}`, `{"imported_at":"yesterday"}`} {
		var source Source
		if json.Unmarshal([]byte(data), &source) == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}

func TestCoverageFailureAndIdentityBoundaries(t *testing.T) {
	source := Source{Version: 1, Agent: "codex", NativeSessionID: "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04", Generation: strings.Repeat("a", 64), Ranges: []Range{{Start: 1, End: 10}}, ParserVersion: "codex-jsonl/v1", CapturedAt: time.Now()}
	record := Record{Version: 1, Agent: source.Agent, NativeSessionID: source.NativeSessionID, Generation: source.Generation, Coverage: []Coverage{{Start: 0, End: 10, SessionName: "export", RawOID: strings.Repeat("b", 64)}}}
	if _, err := record.CheckCoverage(&source, "wrong-export", strings.Repeat("b", 64)); err == nil {
		t.Fatal("wrong export covered")
	}
	copy := source
	copy.Generation = strings.Repeat("c", 64)
	if record.ValidateSource(&copy) == nil {
		t.Fatal("different generation matched")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Source)
	}{
		{"version", func(s *Source) { s.Version = 2 }},
		{"identity", func(s *Source) { s.NativeSessionID = "../invalid" }},
		{"range", func(s *Source) { s.Ranges = []Range{{Start: 10, End: 1}} }},
		{"digest", func(s *Source) { s.SnapshotDigest = "wrong" }},
		{"import-time", func(s *Source) { zero := time.Time{}; s.ImportedAt = &zero }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := source
			tc.mutate(&copy)
			if _, err := record.CheckCoverage(&copy, "export", strings.Repeat("b", 64)); err == nil {
				t.Fatal("invalid provenance accepted")
			}
		})
	}
	var absent *Source
	if absent.Validate() == nil {
		t.Fatal("nil source accepted")
	}
	var absentRecord *Record
	if absentRecord.Validate() == nil {
		t.Fatal("nil receipt accepted")
	}
	if absentRecord.Covers(&source, "export", "") || absentRecord.Excluded(&source) {
		t.Fatal("absent receipt matches")
	}
	record.Exclusions = []Exclusion{{Start: 10, End: 20, Reason: "deleted"}}
	if record.Excludes(0, 10) || record.Excluded(&source) {
		t.Fatal("adjacent exclusion overlaps")
	}
	if !record.Excludes(19, 21) {
		t.Fatal("overlapping exclusion missed")
	}
	record.Generation = "invalid"
	if record.Exclude(&source, "deleted", time.Now()) == nil {
		t.Fatal("invalid record mutated")
	}
}

func TestExcludeRejectsEmptyReason(t *testing.T) {
	source := Source{Version: 1, Agent: "codex", NativeSessionID: "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04", Generation: strings.Repeat("a", 64), Ranges: []Range{{Start: 0, End: 1}}, ParserVersion: "codex-jsonl/v1", CapturedAt: time.Now()}
	record := Record{Version: 1, Agent: source.Agent, NativeSessionID: source.NativeSessionID, Generation: source.Generation}
	if err := record.Exclude(&source, "", time.Now()); err == nil {
		t.Fatal("empty exclusion reason accepted")
	}
	if len(record.Exclusions) != 0 {
		t.Fatal("record mutated despite rejected exclusion")
	}
}

func TestExcludeRejectsZeroTimestamp(t *testing.T) {
	source := Source{Version: 1, Agent: "codex", NativeSessionID: "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04", Generation: strings.Repeat("a", 64), Ranges: []Range{{Start: 0, End: 1}}, ParserVersion: "codex-jsonl/v1", CapturedAt: time.Now()}
	record := Record{Version: 1, Agent: source.Agent, NativeSessionID: source.NativeSessionID, Generation: source.Generation}
	if err := record.Exclude(&source, "deleted", time.Time{}); err == nil {
		t.Fatal("zero exclusion timestamp accepted")
	}
	if len(record.Exclusions) != 0 || !record.UpdatedAt.IsZero() {
		t.Fatal("record mutated despite rejected exclusion")
	}
}

func TestValidateRequiresUpdatedAtForContentBearingRecords(t *testing.T) {
	base := Record{Version: 1, Agent: "codex", NativeSessionID: "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04"}
	if err := base.Validate(); err != nil {
		t.Fatalf("blank record should validate without updated_at: %v", err)
	}
	withCoverage := base
	withCoverage.Generation = strings.Repeat("a", 64)
	withCoverage.Coverage = []Coverage{{Start: 0, End: 1, SessionName: "export", RawOID: strings.Repeat("b", 64)}}
	if withCoverage.Validate() == nil {
		t.Fatal("coverage-bearing record without updated_at accepted")
	}
	withCoverage.UpdatedAt = time.Now()
	if err := withCoverage.Validate(); err != nil {
		t.Fatalf("coverage-bearing record with updated_at should validate: %v", err)
	}
}

func TestValidateRejectsZeroExclusionTimestamp(t *testing.T) {
	record := Record{
		Version:         1,
		Agent:           "codex",
		NativeSessionID: "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04",
		Exclusions:      []Exclusion{{Start: 0, End: -1, Reason: "deleted"}},
		UpdatedAt:       time.Now(),
	}
	if record.Validate() == nil {
		t.Fatal("exclusion with zero created_at accepted")
	}
	record.Exclusions[0].CreatedAt = time.Now()
	if err := record.Validate(); err != nil {
		t.Fatalf("exclusion with valid created_at should validate: %v", err)
	}
}

func TestValidateRequiresGenerationForProjections(t *testing.T) {
	record := Record{
		Version:         1,
		Agent:           "codex",
		NativeSessionID: "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04",
		Projections:     map[string]Projection{"export": {RawOID: strings.Repeat("b", 64), LayerID: "layer"}},
	}
	if record.Validate() == nil {
		t.Fatal("projection-bearing record without generation accepted")
	}
	record.Projections = nil
	record.ProjectionRevision = "rev-1"
	if record.Validate() == nil {
		t.Fatal("projection revision without generation accepted")
	}
}

func TestRangeRoundTripPreservesUnknownFields(t *testing.T) {
	data := []byte(`{"start":0,"end":1,"future_range":true}`)
	var r Range
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if string(r.Extra["future_range"]) != "true" {
		t.Fatal("lost range extension on decode")
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var again Range
	if err = json.Unmarshal(encoded, &again); err != nil {
		t.Fatal(err)
	}
	if string(again.Extra["future_range"]) != "true" {
		t.Fatalf("lost range extension after round trip: %s", encoded)
	}
}

// Publication receipts compare decoded provenance with the value just uploaded.
// Decoding known-only fields must not invent empty extension maps and reject
// an otherwise identical freshly published receipt.
func TestKnownOnlySourceRoundTripPreservesValue(t *testing.T) {
	source := Source{Version: 1, Agent: "codex", NativeSessionID: "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04", Generation: strings.Repeat("a", 64), Ranges: []Range{{Start: 0, End: 1}}, ParserVersion: "codex-jsonl/v1", CapturedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Source
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(source, decoded) {
		t.Fatalf("round trip changed provenance: %#v != %#v", source, decoded)
	}
}
