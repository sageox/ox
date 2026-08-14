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

func readManifestAt(t *testing.T, path string) Manifest {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return m
}

func manifestFileOfKind(t *testing.T, manifest Manifest, kind string) ManifestFile {
	t.Helper()
	for _, file := range manifest.Files {
		if file.Kind == kind {
			return file
		}
	}
	t.Fatalf("manifest has no %q file: %#v", kind, manifest.Files)
	return ManifestFile{}
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

func TestWriteBundle_SameInstantCreatesIndependentImmutableSnapshots(t *testing.T) {
	report, recs := publishFixture(t)
	dest := t.TempDir()
	when := time.Date(2026, 8, 2, 10, 0, 0, 123, time.UTC)
	runs := []RunResult{{RunID: "run/red", Source: "orchestrator", Status: "complete"}}

	first, err := WriteBundle(dest, "acme", report, recs, runs, when)
	if err != nil {
		t.Fatalf("first WriteBundle: %v", err)
	}
	firstManifestRaw, err := os.ReadFile(first.ManifestPath)
	if err != nil {
		t.Fatalf("read first manifest: %v", err)
	}
	firstManifest := readManifestAt(t, first.ManifestPath)
	firstAttestation := manifestFileOfKind(t, firstManifest, "attestation")
	firstRun := manifestFileOfKind(t, firstManifest, "run-summary")
	base := filepath.Join(dest, "attest", LayoutVersion)
	firstAttestationRaw, err := os.ReadFile(filepath.Join(base, firstAttestation.Path))
	if err != nil {
		t.Fatalf("read first attestation: %v", err)
	}
	firstRunRaw, err := os.ReadFile(filepath.Join(base, firstRun.Path))
	if err != nil {
		t.Fatalf("read first run: %v", err)
	}

	recs.All()[0].Claim = "a later proof"
	second, err := WriteBundle(dest, "acme", report, recs, runs, when)
	if err != nil {
		t.Fatalf("second WriteBundle: %v", err)
	}
	if first.ManifestPath == second.ManifestPath || first.IndexPath == second.IndexPath {
		t.Fatal("publishes at the same instant collided")
	}
	secondManifest := readManifestAt(t, second.ManifestPath)
	if firstAttestation.Path == manifestFileOfKind(t, secondManifest, "attestation").Path {
		t.Fatal("attestation object path is shared across publishes")
	}
	if firstRun.Path == manifestFileOfKind(t, secondManifest, "run-summary").Path {
		t.Fatal("run summary object path is shared across publishes")
	}

	gotManifestRaw, _ := os.ReadFile(first.ManifestPath)
	gotAttestationRaw, _ := os.ReadFile(filepath.Join(base, firstAttestation.Path))
	gotRunRaw, _ := os.ReadFile(filepath.Join(base, firstRun.Path))
	if string(gotManifestRaw) != string(firstManifestRaw) || string(gotAttestationRaw) != string(firstAttestationRaw) ||
		string(gotRunRaw) != string(firstRunRaw) {
		t.Fatal("later publish changed an object named by the earlier manifest")
	}
}

func TestWriteBundle_RejectsUnsafeRepoKeysBeforeWriting(t *testing.T) {
	report, recs := publishFixture(t)
	for _, key := range []string{"", ".", "..", "../../outside", `..\\outside`, "/absolute", `C:\\absolute`, "trailing."} {
		t.Run(strings.ReplaceAll(key, "/", "_"), func(t *testing.T) {
			dest := t.TempDir()
			if _, err := WriteBundle(dest, key, report, recs, nil, time.Now()); err == nil {
				t.Fatalf("WriteBundle accepted unsafe repo key %q", key)
			}
			if _, err := os.Stat(filepath.Join(dest, "attest")); !os.IsNotExist(err) {
				t.Fatalf("unsafe repo key %q wrote output before validation: %v", key, err)
			}
		})
	}
}

func TestWriteBundle_PublishedReportOmitsLocalPaths(t *testing.T) {
	report, recs := publishFixture(t)
	localRoot := t.TempDir()
	report.Root = localRoot
	badRecord := filepath.Join(localRoot, attestationsSubdir, "broken.v1.json")
	report.InvalidRecords = map[string]string{badRecord: "parse " + badRecord + ": malformed"}
	dest := t.TempDir()

	result, err := WriteBundle(dest, "acme", report, recs, nil, time.Now())
	if err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	manifest := readManifestAt(t, result.ManifestPath)
	reportFile := manifestFileOfKind(t, manifest, "report")
	raw, err := os.ReadFile(filepath.Join(result.Root, reportFile.Path))
	if err != nil {
		t.Fatalf("read published report: %v", err)
	}
	if strings.Contains(string(raw), localRoot) {
		t.Fatalf("published report leaks local root %q:\n%s", localRoot, raw)
	}
	var bundle Bundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatalf("parse published report: %v", err)
	}
	if bundle.Report.Root != "." {
		t.Errorf("published report root = %q, want portable dot", bundle.Report.Root)
	}
	if _, ok := bundle.Report.InvalidRecords["attestations/broken.v1.json"]; !ok {
		t.Errorf("published invalid record keys = %#v, want corpus-relative key", bundle.Report.InvalidRecords)
	}
	if report.Root != localRoot {
		t.Errorf("WriteBundle mutated caller's report root to %q", report.Root)
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
	rule := "Only members can enter"
	runs := []RunResult{{
		RunID: "red-run", Source: "orchestrator", Status: "finalized", FinalizeStatus: "failed",
		ScenarioTotal: 1, ScenarioFailed: 1,
		FailedScenarios: []FailedScenario{{
			ScenarioInstanceID: "scen_red", Feature: "features/auth.feature", Rule: &rule,
			Scenario: "Guest is denied", Outcome: "failed", SessionStatus: "completed",
		}},
	}}
	if _, err := WriteBundle(dest, "acme", report, recs, runs, time.Now()); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	manifest := readManifest(t, dest)
	runFile := manifestFileOfKind(t, manifest, "run-summary")
	base := filepath.Join(dest, "attest", LayoutVersion)
	if _, err := os.Stat(filepath.Join(base, runFile.Path)); err != nil {
		t.Fatalf("normalized run summary missing at %q: %v", runFile.Path, err)
	}
	if !strings.Contains(runFile.Path, "/status/acme/") && !strings.HasPrefix(runFile.Path, "status/acme/") {
		t.Errorf("run summary %q is not scoped to its immutable publish", runFile.Path)
	}
	if want := "/runs/run-" + encodedPathSegment("red-run") + ".json"; !strings.HasSuffix(runFile.Path, want) {
		t.Errorf("run summary path = %q, want encoded suffix %q", runFile.Path, want)
	}
	raw, err := os.ReadFile(filepath.Join(base, runFile.Path))
	if err != nil {
		t.Fatalf("read normalized summary: %v", err)
	}
	var published RunResult
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("decode normalized summary: %v", err)
	}
	if len(published.FailedScenarios) != 1 || published.FailedScenarios[0].Rule == nil ||
		*published.FailedScenarios[0].Rule != rule {
		t.Fatalf("published failed BDD projection = %#v", published.FailedScenarios)
	}
	for _, forbidden := range []string{"artifactsDir", "steps", "envelope", "screenshot", "diagnostics"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("published normalized summary contains raw diagnostic field %q: %s", forbidden, raw)
		}
	}
	if matches, err := filepath.Glob(filepath.Join(base, "**", "*.png")); err != nil || len(matches) != 0 {
		t.Errorf("published screenshots = %v, err %v; summaries must not publish raw diagnostics", matches, err)
	}
}
