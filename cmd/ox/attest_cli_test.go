package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/attest"
	"github.com/spf13/cobra"
)

func TestReadVerbatimPreservesExactFileBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "red.txt")
	want := "assertion failed\n\n"
	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.Flags().String("red-verbatim-file", path, "")
	cmd.Flags().String("red-verbatim", "", "")
	got, err := readVerbatim(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("readVerbatim() = %q, want exact bytes %q", got, want)
	}
}

func TestDiffDigestRejectsUnreadableFile(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("break-diff-file", filepath.Join(t.TempDir(), "missing.patch"), "")

	if _, err := diffDigest(cmd); err == nil || !strings.Contains(err.Error(), "read --break-diff-file") {
		t.Fatalf("diffDigest() error = %v, want an actionable read error", err)
	}
}

func TestValidateAttestRunIDsRequiresBothRuns(t *testing.T) {
	tests := []struct {
		name  string
		red   string
		green string
		want  string
	}{
		{name: "missing red", green: "run_green", want: "--red-run is required"},
		{name: "missing green", red: "run_red", want: "--green-run is required"},
		{name: "red whitespace", red: " run_red", green: "run_green", want: "--red-run cannot"},
		{name: "green whitespace", red: "run_red", green: "run_green ", want: "--green-run cannot"},
		{name: "same run", red: "run_same", green: "run_same", want: "must identify different runs"},
		{name: "both", red: "run_red", green: "run_green"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAttestRunIDs(tt.red, tt.green)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("validateAttestRunIDs() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateAttestRunIDs() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateAttestProofInputsRequiresRedFailureStep(t *testing.T) {
	if err := validateAttestProofInputs("invalid", "run_red", "run_green", 2, "Then it fails"); err == nil || !strings.Contains(err.Error(), "--verdict") {
		t.Fatalf("invalid verdict error = %v", err)
	}
	if err := validateAttestProofInputs(attest.ProofClean, "run_red", "run_green", 0, "Then it fails"); err == nil || !strings.Contains(err.Error(), "--step") {
		t.Fatalf("missing step error = %v", err)
	}
	if err := validateAttestProofInputs(attest.ProofAmbiguous, "run_red", "run_green", 2, ""); err == nil || !strings.Contains(err.Error(), "--step-text") {
		t.Fatalf("missing step text error = %v", err)
	}
	if err := validateAttestProofInputs(attest.ProofInconclusive, "run_red", "run_green", 0, ""); err != nil {
		t.Fatalf("inconclusive proof unexpectedly required a failure step: %v", err)
	}
}

func TestVerdictGlyphIncludesStaleProof(t *testing.T) {
	if got := verdictGlyph(attest.VerdictStale); got != "◑" {
		t.Fatalf("verdictGlyph(stale) = %q, want stale marker", got)
	}
}

func TestNormalizeAttestSurfacesCanonicalizesFilesAndPreservesRoutes(t *testing.T) {
	repo := t.TempDir()
	file := filepath.Join(repo, "app", "page.go")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, files, err := normalizeAttestSurfaces(repo, []string{
		"./app/page.go",
		"app\\page.go",
		file,
		"/teams/:team/settings",
		"/api/settings.json",
		"route:admin.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"app/page.go",
		"app/page.go",
		"app/page.go",
		"/teams/:team/settings",
		"/api/settings.json",
		"admin.json",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("normalized surfaces = %#v, want %#v", got, want)
	}
	if len(files) != 3 {
		t.Fatalf("file surfaces = %#v, want three file paths", files)
	}
}

func TestNormalizeAttestSurfaceRejectsFileOutsideRepo(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := normalizeAttestSurface(repo, "file:"+outside); err == nil {
		t.Fatal("normalizeAttestSurface() accepted a file outside the repository")
	}
}

func TestRequireCleanAttestTreeRejectsTrackedAndUntrackedChanges(t *testing.T) {
	repo := t.TempDir()
	runAttestTestGit(t, repo, "init", "--quiet")
	runAttestTestGit(t, repo, "config", "user.name", "Attest Test")
	runAttestTestGit(t, repo, "config", "user.email", "attest@example.com")
	tracked := filepath.Join(repo, "observed.go")
	if err := os.WriteFile(tracked, []byte("package observed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runAttestTestGit(t, repo, "add", "observed.go")
	runAttestTestGit(t, repo, "commit", "--quiet", "-m", "initial")

	if err := requireCleanAttestTree(repo); err != nil {
		t.Fatalf("clean input rejected: %v", err)
	}
	if err := os.WriteFile(tracked, []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireCleanAttestTree(repo); err == nil {
		t.Fatal("dirty tracked input was accepted")
	}
	if err := os.WriteFile(tracked, []byte("package observed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(repo, "new.go")
	if err := os.WriteFile(untracked, []byte("package observed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireCleanAttestTree(repo); err == nil {
		t.Fatal("untracked input was accepted")
	}
}

func TestValidateRepoKeyRejectsPathComponents(t *testing.T) {
	invalid := []string{"", ".", "..", "../escape", `folder\\escape`, "bad:key", "trailing.", " padded "}
	for _, key := range invalid {
		if err := validateRepoKey(key); err == nil {
			t.Errorf("validateRepoKey(%q) accepted an unsafe key", key)
		}
	}
	if err := validateRepoKey("sageox-ox"); err != nil {
		t.Fatalf("validateRepoKey() rejected a portable key: %v", err)
	}
}

func TestRepoKeyFromRootUsesPlatformPathRules(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work", "sageox", "ox") + string(filepath.Separator)
	if got := repoKeyFromRoot(root); got != "ox" {
		t.Fatalf("repoKeyFromRoot(%q) = %q, want ox", root, got)
	}
}

func TestEmitHonorsQuiet(t *testing.T) {
	oldCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = oldCfg })

	cmd := &cobra.Command{}
	cmd.Flags().Bool("quiet", false, "")
	if err := cmd.Flags().Set("quiet", "true"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := emit(cmd, false, "", map[string]string{"result": "hidden"}, "hidden", "test"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("quiet output = %q, want none", out.String())
	}
}

func TestFilterAttestReportAppliesDomainAndWeakest(t *testing.T) {
	first := attest.Assessment{Capability: attest.Capability{ID: "alpha/one#rule", Domain: "alpha"}, Verdict: attest.VerdictUntested}
	second := attest.Assessment{Capability: attest.Capability{ID: "beta/two#rule", Domain: "beta"}, Verdict: attest.VerdictAttested}
	report := &attest.Report{
		Assessments: []attest.Assessment{first, second},
		ByDomain: map[string][]attest.Assessment{
			"alpha": {first},
			"beta":  {second},
		},
	}

	weakest := filterAttestReport(report, "", 1)
	if len(weakest.Assessments) != 1 || weakest.Assessments[0].Capability.ID != first.Capability.ID {
		t.Fatalf("weakest JSON assessments = %#v", weakest.Assessments)
	}
	domain := filterAttestReport(report, "beta", 1)
	if len(domain.Assessments) != 1 || domain.Assessments[0].Capability.ID != second.Capability.ID {
		t.Fatalf("domain JSON assessments = %#v", domain.Assessments)
	}
	if len(report.Assessments) != 2 {
		t.Fatal("filterAttestReport mutated the source report")
	}
}

func TestRenderAttestStatusWarnsOnlyWhenNoRecordsExist(t *testing.T) {
	report := &attest.Report{
		Records: 1,
		Counts:  map[attest.Verdict]int{},
	}
	got := renderAttestStatus(report, "", 0)
	if strings.Contains(got, "no attestation records yet") {
		t.Fatalf("status warned that no records exist despite Records=1: %q", got)
	}
}

func TestRenderAttestStatusDoesNotCallUnreadableRecordAbsent(t *testing.T) {
	report := &attest.Report{
		Counts:         map[attest.Verdict]int{},
		InvalidRecords: map[string]string{"bad.v1.json": "invalid JSON"},
	}
	got := renderAttestStatus(report, "", 0)
	if strings.Contains(got, "no attestation records yet") {
		t.Fatalf("status called an unreadable record absent: %q", got)
	}
}

func TestRenderProofUsesStoredVerdictForConclusion(t *testing.T) {
	record := &attest.Attestation{
		Proof: attest.Proof{
			Verdict: attest.ProofInconclusive,
			ObservedRed: attest.ObservedRed{
				LandedOnClaimStep: true,
			},
		},
	}
	got := renderProof(attest.Assessment{Record: record}, nil)
	if !strings.Contains(got, "inconclusive") {
		t.Fatalf("proof conclusion did not reflect inconclusive verdict: %q", got)
	}
}

func TestRenderAttestCheckSurfacesInvalidRecords(t *testing.T) {
	got := renderAttestCheck(attestCheckResult{
		InvalidRecords: map[string]string{"/tmp/bad.v1.json": "invalid JSON"},
	})
	if !strings.Contains(got, "could not be read") || !strings.Contains(got, "bad.v1.json") {
		t.Fatalf("check output hid invalid record: %q", got)
	}
	if strings.Contains(got, "no attestation records in this repo") {
		t.Fatalf("check output contradicted invalid record presence: %q", got)
	}
}

func TestRenderAttestCheckSurfacesOrphanedRecords(t *testing.T) {
	got := renderAttestCheck(attestCheckResult{
		RecordCount: 1,
		Orphaned: []attestCheckOrphan{{
			CapabilityID: "removed/feature#old-rule",
			Claim:        "An old capability",
		}},
	})
	if !strings.Contains(got, "no longer match") || !strings.Contains(got, "removed/feature#old-rule") {
		t.Fatalf("check output hid orphaned record: %q", got)
	}
}

func runAttestTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
