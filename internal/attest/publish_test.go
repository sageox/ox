package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func publishFixture(t *testing.T) (*Report, *Records) {
	t.Helper()
	root := writeCorpus(t, map[string]string{"d/ladder.feature": ladderFeature})
	writePlan(t, root, CompiledPlan{
		SchemaVersion: 1,
		Feature:       "features/d/ladder.feature",
		Scenarios:     []PlanScenario{{Name: "Stamped one"}},
	})
	corpus, _ := ScanCorpus(root, root)
	plans, _ := LoadPlans(root)

	rec := validRecord()
	rec.CapabilityID = corpus.Capabilities[0].ID
	recs := &Records{byCapability: map[string]*Attestation{rec.CapabilityID: rec}, Count: 1}

	return BuildReport(corpus, plans, recs), recs
}

func readManifest(t *testing.T, dest string) Manifest {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dest, "attest", LayoutVersion, "status", "*", "*", "*", "*", "*", ManifestName))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly one manifest, got %v (err %v)", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return m
}

// Object-store keys are LITERAL — there is no path traversal — so a manifest
// path containing `..` is not resolved to a parent prefix. It becomes part of
// the key and the fetch 404s. Every path must be bundle-root relative.
func TestWriteBundle_ManifestPathsAreValidObjectKeys(t *testing.T) {
	report, recs := publishFixture(t)
	dest := t.TempDir()

	if _, err := WriteBundle(dest, "acme", report, recs, nil, time.Now()); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	m := readManifest(t, dest)
	if len(m.Files) == 0 {
		t.Fatal("manifest lists no files")
	}
	base := filepath.Join(dest, "attest", LayoutVersion)
	for _, f := range m.Files {
		if strings.Contains(f.Path, "..") {
			t.Errorf("manifest path %q contains \"..\" — not a fetchable object key", f.Path)
		}
		if strings.HasPrefix(f.Path, "/") {
			t.Errorf("manifest path %q is absolute", f.Path)
		}
		// Every listed path must resolve from the bundle root.
		if _, err := os.Stat(filepath.Join(base, f.Path)); err != nil {
			t.Errorf("manifest path %q does not resolve from the bundle root: %v", f.Path, err)
		}
	}
}

// The digest is the only integrity check a reader gets once these bytes leave
// our machine, so it has to actually match the file.
func TestWriteBundle_ManifestDigestsMatchTheBytes(t *testing.T) {
	report, recs := publishFixture(t)
	dest := t.TempDir()
	if _, err := WriteBundle(dest, "acme", report, recs, nil, time.Now()); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	base := filepath.Join(dest, "attest", LayoutVersion)
	m := readManifest(t, dest)

	for _, f := range m.Files {
		raw, err := os.ReadFile(filepath.Join(base, f.Path))
		if err != nil {
			t.Fatalf("read %s: %v", f.Path, err)
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != f.SHA256 {
			t.Errorf("%s digest = %s, manifest says %s", f.Path, got, f.SHA256)
		}
		if int64(len(raw)) != f.Bytes {
			t.Errorf("%s bytes = %d, manifest says %d", f.Path, len(raw), f.Bytes)
		}
	}
}

// The date partition is UTC, stated rather than implied: a partition boundary
// is immutable once written, and a local-time publisher would scatter one day
// across two prefixes depending on who ran it.
func TestWriteBundle_PartitionsByUTC(t *testing.T) {
	report, recs := publishFixture(t)
	dest := t.TempDir()

	// 01:30 on the 2nd in UTC is still the 1st in a US timezone. The published
	// prefix must follow UTC regardless of the caller's clock.
	zone := time.FixedZone("UTC-8", -8*3600)
	local := time.Date(2026, 8, 2, 1, 30, 0, 0, time.UTC).In(zone)

	if _, err := WriteBundle(dest, "acme", report, recs, nil, local); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	want := filepath.Join(dest, "attest", LayoutVersion, "status", "acme", "2026", "08", "02")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected UTC partition %s: %v", want, err)
	}
}

// One immutable object per publish, never an appended file: object storage has
// no append, so a shared index would be a read-modify-write race between two
// publishers.
func TestWriteBundle_IndexIsOneObjectPerPublish(t *testing.T) {
	report, recs := publishFixture(t)
	dest := t.TempDir()

	first := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	for _, when := range []time.Time{first, second} {
		if _, err := WriteBundle(dest, "acme", report, recs, nil, when); err != nil {
			t.Fatalf("WriteBundle: %v", err)
		}
	}

	entries, err := filepath.Glob(filepath.Join(dest, "attest", LayoutVersion, "index", "acme", "*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("index entries = %d, want 2 — publishes must not overwrite each other", len(entries))
	}
}

func TestWriteBundle_ManifestCarriesTotals(t *testing.T) {
	report, recs := publishFixture(t)
	dest := t.TempDir()
	if _, err := WriteBundle(dest, "acme", report, recs, nil, time.Now()); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	m := readManifest(t, dest)
	if m.LayoutVersion != LayoutVersion {
		t.Errorf("LayoutVersion = %q, want %q", m.LayoutVersion, LayoutVersion)
	}
	if m.Capabilities != report.Capabilities {
		t.Errorf("Capabilities = %d, want %d", m.Capabilities, report.Capabilities)
	}
	if m.Attestations != recs.Count {
		t.Errorf("Attestations = %d, want %d", m.Attestations, recs.Count)
	}
}

func TestWriteBundle_PublishesOnlyNormalizedReferencedRunSummaries(t *testing.T) {
	report, recs := publishFixture(t)
	dest := t.TempDir()
	runs := []RunResult{{RunID: "red-run", Source: "orchestrator", Status: "complete", FinalizeStatus: "failed"}}
	if _, err := WriteBundle(dest, "acme", report, recs, runs, time.Now()); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	base := filepath.Join(dest, "attest", LayoutVersion)
	if _, err := os.Stat(filepath.Join(base, "runs", "acme", "red-run.json")); err != nil {
		t.Fatalf("normalized run summary missing: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(base, "**", "*.png")); err != nil || len(matches) != 0 {
		t.Errorf("published screenshots = %v, err %v; summaries must not publish raw diagnostics", matches, err)
	}
}
