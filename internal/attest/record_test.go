package attest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validRecord() *Attestation {
	return &Attestation{
		Version:       AttestationVersion,
		AttestationID: GenerateAttestationID(),
		CapabilityID:  "team-management/visibility#a-non-member-is-denied",
		Claim:         "A non-member is denied the restricted sections of a public team",
		RepoKey:       "sageox-monorepo",
		Subject:       TreeRef{Scheme: SchemeGitCommit, Value: "b583e0b56c0ffee0000000000000000000000000"},
		MintedAt:      "2026-08-13T00:00:00Z",
		Proof: Proof{
			Verdict: ProofClean,
			Break:   Break{Description: "team gate inverted"},
			ObservedRed: ObservedRed{
				Verbatim:          "expected Access Required but the section rendered normally",
				StepIndex:         4,
				StepText:          "Then Marcus sees Access Required",
				LandedOnClaimStep: true,
			},
			RedRunID:   "run_red",
			GreenRunID: "run_green",
		},
		ObservedSurface: ObservedSurface{
			Granularity: "route",
			Surfaces:    []Surface{{SurfaceID: "app/team/settings/layout.tsx"}},
		},
	}
}

func mustJSON(t *testing.T, a *Attestation) []byte {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func TestParseAttestation_RoundTrip(t *testing.T) {
	want := validRecord()
	got, err := ParseAttestation(mustJSON(t, want))
	if err != nil {
		t.Fatalf("ParseAttestation: %v", err)
	}
	// Assert on decoded FIELDS, never on the JSON text: a substring check would
	// accept a payload of the wrong shape that merely contains the right words.
	if got.CapabilityID != want.CapabilityID {
		t.Errorf("CapabilityID = %q, want %q", got.CapabilityID, want.CapabilityID)
	}
	if got.Proof.ObservedRed.Verbatim != want.Proof.ObservedRed.Verbatim {
		t.Errorf("Verbatim = %q", got.Proof.ObservedRed.Verbatim)
	}
	if !got.IsProof() {
		t.Error("a clean record must report IsProof")
	}
}

// THE rule. A red that landed away from the step naming the claim is ambiguous
// by construction, and the PARSER enforces it so it cannot be honored
// inconsistently by callers — convention is what lets "4 clean + 1 ambiguous"
// get quietly rounded up to "5 proven".
func TestParseAttestation_LandedOffClaimStepCannotBeClean(t *testing.T) {
	rec := validRecord()
	rec.Proof.ObservedRed.LandedOnClaimStep = false
	rec.Proof.Verdict = ProofClean

	_, err := ParseAttestation(mustJSON(t, rec))
	if err == nil {
		t.Fatal("a clean verdict with landedOnClaimStep=false was accepted — the one rule that must hold")
	}
	if !errors.Is(err, ErrAmbiguousMislabeled) {
		t.Errorf("error = %v, want ErrAmbiguousMislabeled", err)
	}

	// The same record labeled honestly must parse.
	rec.Proof.Verdict = ProofAmbiguous
	got, err := ParseAttestation(mustJSON(t, rec))
	if err != nil {
		t.Fatalf("an honestly-labeled ambiguous record must parse: %v", err)
	}
	if got.IsProof() {
		t.Error("an ambiguous record must NOT count as a proof")
	}
}

// Unknown fields are rejected: a field this record silently ignored is a field
// a future reader will assume was honored.
func TestParseAttestation_UnknownFieldIsRejected(t *testing.T) {
	raw := mustJSON(t, validRecord())
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m["somethingNobodyImplemented"] = true
	polluted, _ := json.Marshal(m)

	if _, err := ParseAttestation(polluted); err == nil {
		t.Fatal("unknown field was accepted; strict decoding is the point")
	}
}

func TestParseAttestation_VersionGate(t *testing.T) {
	rec := validRecord()
	rec.Version = 99
	_, err := ParseAttestation(mustJSON(t, rec))
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("error = %v, want ErrVersionMismatch", err)
	}
}

func TestValidate_RejectsIncompleteRecords(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Attestation)
	}{
		{"wrong version", func(a *Attestation) { a.Version = 0 }},
		{"no attestation id", func(a *Attestation) { a.AttestationID = "" }},
		{"no capability", func(a *Attestation) { a.CapabilityID = "" }},
		{"no claim", func(a *Attestation) { a.Claim = "" }},
		{"no repo key", func(a *Attestation) { a.RepoKey = "" }},
		{"unsafe repo key", func(a *Attestation) { a.RepoKey = "../../outside" }},
		{"no subject", func(a *Attestation) { a.Subject.Value = "" }},
		{"unknown subject scheme", func(a *Attestation) { a.Subject.Scheme = "svn-rev" }},
		{"no minted time", func(a *Attestation) { a.MintedAt = "" }},
		{"malformed minted time", func(a *Attestation) { a.MintedAt = "yesterday" }},
		{"unknown verdict", func(a *Attestation) { a.Proof.Verdict = "probably-fine" }},
		{"no break description", func(a *Attestation) { a.Proof.Break.Description = "" }},
		{"no red run", func(a *Attestation) { a.Proof.RedRunID = "" }},
		{"red run with whitespace", func(a *Attestation) { a.Proof.RedRunID = " run_red" }},
		{"no green run", func(a *Attestation) { a.Proof.GreenRunID = "" }},
		{"green run with whitespace", func(a *Attestation) { a.Proof.GreenRunID = "run_green " }},
		{"same red and green run", func(a *Attestation) { a.Proof.GreenRunID = a.Proof.RedRunID }},
		{"red verdict with no verbatim failure", func(a *Attestation) { a.Proof.ObservedRed.Verbatim = "" }},
		{"red verdict with no failure step", func(a *Attestation) { a.Proof.ObservedRed.StepIndex = 0 }},
		{"red verdict with no failure step text", func(a *Attestation) { a.Proof.ObservedRed.StepText = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := validRecord()
			tc.mutate(rec)
			if err := rec.Validate(); err == nil {
				t.Fatalf("Validate accepted a record with: %s", tc.name)
			}
		})
	}
}

func TestValidate_InconclusiveProofKeepsHonestUnknownRepresentable(t *testing.T) {
	rec := validRecord()
	rec.Proof.Verdict = ProofInconclusive
	rec.Proof.ObservedRed = ObservedRed{}
	if err := rec.Validate(); err != nil {
		t.Fatalf("inconclusive proof with a run pair but no observed failure must remain valid: %v", err)
	}
}

func TestWriteRecord_RefusesToWriteAnInvalidRecord(t *testing.T) {
	root := t.TempDir()
	rec := validRecord()
	rec.Proof.ObservedRed.LandedOnClaimStep = false // clean + not-landed is illegal

	if _, err := WriteRecord(root, rec); err == nil {
		t.Fatal("WriteRecord wrote an invalid record")
	}
	// Validation happens BEFORE the write, so nothing may land on disk.
	if entries, _ := os.ReadDir(filepath.Join(root, attestationsSubdir)); len(entries) != 0 {
		t.Errorf("an invalid record left %d file(s) on disk", len(entries))
	}
}

func TestWriteAndLoadRecords(t *testing.T) {
	root := t.TempDir()
	rec := validRecord()
	path, err := WriteRecord(root, rec)
	if err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	// One file per capability, current record only — the path carries no
	// attestation id, because duplicating git's history job is what makes the
	// tree grow without bound.
	if strings.Contains(filepath.Base(path), rec.AttestationID) {
		t.Errorf("record path %q embeds the attestation id", path)
	}

	recs, err := LoadRecords(root)
	if err != nil {
		t.Fatalf("LoadRecords: %v", err)
	}
	if recs.Count != 1 {
		t.Fatalf("Count = %d, want 1", recs.Count)
	}
	got, ok := recs.For(rec.CapabilityID)
	if !ok {
		t.Fatalf("record not indexed under %q", rec.CapabilityID)
	}
	if got.Claim != rec.Claim {
		t.Errorf("Claim = %q", got.Claim)
	}
}

func TestRecordPath_IsStableReversibleAndCollisionFree(t *testing.T) {
	root := t.TempDir()
	ids := []string{"foo/bar#baz", "foo-bar#baz"}
	paths := make(map[string]string, len(ids))
	for _, id := range ids {
		path := RecordPath(root, id)
		if path != RecordPath(root, id) {
			t.Fatalf("RecordPath(%q) is not stable", id)
		}
		got, ok := capabilityIDFromRecordFilename(filepath.Base(path))
		if !ok || got != id {
			t.Fatalf("record filename %q decoded as %q, %v; want %q", filepath.Base(path), got, ok, id)
		}
		paths[id] = path
	}
	if paths[ids[0]] == paths[ids[1]] {
		t.Fatalf("distinct capability ids collide at %q", paths[ids[0]])
	}
}

func TestLoadRecords_ReadsLegacyFilenameAndPrefersCanonicalDuplicate(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, attestationsSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	legacy := validRecord()
	legacy.Claim = "legacy copy"
	if err := os.WriteFile(filepath.Join(dir, slugify(legacy.CapabilityID)+".v1.json"), mustJSON(t, legacy), 0o600); err != nil {
		t.Fatalf("write legacy record: %v", err)
	}
	canonical := validRecord()
	canonical.Claim = "canonical copy"
	if err := os.WriteFile(RecordPath(root, canonical.CapabilityID), mustJSON(t, canonical), 0o600); err != nil {
		t.Fatalf("write canonical record: %v", err)
	}

	recs, err := LoadRecords(root)
	if err != nil {
		t.Fatalf("LoadRecords: %v", err)
	}
	if recs.Count != 1 {
		t.Fatalf("Count = %d, want one unique capability", recs.Count)
	}
	got, ok := recs.For(canonical.CapabilityID)
	if !ok || got.Claim != canonical.Claim {
		t.Fatalf("loaded record = %#v, %v; want canonical copy", got, ok)
	}
}

func TestRecordsAll_IsSortedAndIncludesOrphans(t *testing.T) {
	a := validRecord()
	a.CapabilityID = "z/orphan#last"
	b := validRecord()
	b.CapabilityID = "a/current#first"
	recs := &Records{byCapability: map[string]*Attestation{a.CapabilityID: a, b.CapabilityID: b}, Count: 2}

	got := recs.All()
	if len(got) != 2 || got[0] != b || got[1] != a {
		t.Fatalf("All() = %#v, want records sorted by capability id", got)
	}
	if got := (*Records)(nil).All(); got != nil {
		t.Fatalf("nil Records.All() = %#v, want nil", got)
	}
}

// An unreadable record is a proof that silently vanishes, which reads exactly
// like never having been proven. It must be reported, not swallowed.
func TestLoadRecords_InvalidRecordIsReportedNotSwallowed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, attestationsSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.v1.json"), []byte(`{"version":1,"nope":true}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	recs, err := LoadRecords(root)
	if err != nil {
		t.Fatalf("LoadRecords must not fail the whole scan for one bad file: %v", err)
	}
	if recs.Count != 0 {
		t.Errorf("Count = %d, want 0", recs.Count)
	}
	if len(recs.Invalid) != 1 {
		t.Fatalf("Invalid = %d entries, want 1 — the bad record was swallowed", len(recs.Invalid))
	}
}

func TestLoadRecords_MissingDirIsNotAnError(t *testing.T) {
	recs, err := LoadRecords(t.TempDir())
	if err != nil {
		t.Fatalf("a repo with no attestations yet must not error: %v", err)
	}
	if recs.Count != 0 {
		t.Errorf("Count = %d, want 0", recs.Count)
	}
}

// An ambiguous record must NOT promote a capability to attested: it went red
// somewhere other than the step naming the claim, so it proves something else.
func TestAssess_AmbiguousRecordDoesNotAttest(t *testing.T) {
	root := writeCorpus(t, map[string]string{"d/ladder.feature": ladderFeature})
	writePlan(t, root, CompiledPlan{
		SchemaVersion: 1,
		Feature:       "features/d/ladder.feature",
		Scenarios:     []PlanScenario{{Name: "Stamped one"}},
	})
	corpus, _ := ScanCorpus(root, root)
	plans, _ := LoadPlans(root)

	target := corpus.Capabilities[0]
	for _, verdict := range []string{ProofAmbiguous, ProofClean} {
		rec := validRecord()
		rec.CapabilityID = target.ID
		rec.Proof.Verdict = verdict
		rec.Proof.ObservedRed.LandedOnClaimStep = verdict == ProofClean

		recs := &Records{byCapability: map[string]*Attestation{target.ID: rec}, Count: 1}
		got := Assess(target, plans, recs).Verdict

		want := VerdictStamped
		if verdict == ProofClean {
			want = VerdictAttested
		}
		if got != want {
			t.Errorf("verdict %q record → %q, want %q", verdict, got, want)
		}
	}
}
