package attest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGit scripts git responses by subcommand so freshness can be tested
// without standing up a repository per case.
type fakeGit struct {
	isAncestor  bool
	commitKnown bool
	changed     []string
	untracked   []string
	renameFrom  string
	renameTo    string
	diffFails   bool
	lsFails     bool
}

func (f fakeGit) run(_ string, args ...string) (string, error) {
	switch args[0] {
	case "merge-base":
		if f.isAncestor {
			return "", nil
		}
		return "", errors.New("not an ancestor")
	case "cat-file":
		if f.commitKnown {
			return "", nil
		}
		return "", errors.New("unknown revision")
	case "diff":
		if f.diffFails {
			return "", errors.New("diff failed")
		}
		if f.renameFrom != "" {
			for _, arg := range args {
				if arg == "--no-renames" {
					return f.renameFrom + "\n" + f.renameTo, nil
				}
			}
			return f.renameTo, nil
		}
		return strings.Join(f.changed, "\n"), nil
	case "ls-files":
		if f.lsFails {
			return "", errors.New("ls-files failed")
		}
		return strings.Join(f.untracked, "\n"), nil
	}
	return "", nil
}

func recWithSurface(surfaces ...string) *Attestation {
	a := validRecord()
	a.SpecFingerprint = "spec-abc"
	a.ObservedSurface.Surfaces = nil
	for _, s := range surfaces {
		a.ObservedSurface.Surfaces = append(a.ObservedSurface.Surfaces, Surface{SurfaceID: s})
	}
	return a
}

func TestCheckFreshness_CurrentWhenNothingMoved(t *testing.T) {
	git := fakeGit{isAncestor: true, commitKnown: true, changed: []string{"docs/README.md"}}
	f := checkFreshness("/repo", recWithSurface("app/team/layout.tsx"), "spec-abc", git.run)

	if !f.Current {
		t.Fatalf("expected current, got %+v", f)
	}
	if f.Unknown || f.SpecStale || len(f.ProductDrift) != 0 {
		t.Errorf("unexpected staleness signals: %+v", f)
	}
}

// The load-bearing case: a file the record observed has changed in the WORKING
// TREE. This is the answer a server can never give, because it has no working
// tree — which is why attest is a CLI feature.
func TestCheckFreshness_ProductDriftFromWorkingTree(t *testing.T) {
	git := fakeGit{isAncestor: true, commitKnown: true, changed: []string{"app/team/layout.tsx", "unrelated.go"}}
	f := checkFreshness("/repo", recWithSurface("app/team/layout.tsx"), "spec-abc", git.run)

	if f.Current {
		t.Fatal("a changed observed surface must not read as current")
	}
	if len(f.ProductDrift) != 1 || f.ProductDrift[0] != "app/team/layout.tsx" {
		t.Errorf("ProductDrift = %v, want the observed surface only", f.ProductDrift)
	}
	if !strings.Contains(f.Summary(), "product moved") {
		t.Errorf("Summary = %q", f.Summary())
	}
}

func TestCheckFreshness_ProductDriftIncludesRenamesAndUntrackedFiles(t *testing.T) {
	tests := []struct {
		name    string
		surface string
		git     fakeGit
	}{
		{
			name:    "rename reports the observed source path",
			surface: "app/team/layout.tsx",
			git: fakeGit{
				isAncestor:  true,
				commitKnown: true,
				renameFrom:  "app/team/layout.tsx",
				renameTo:    "app/team/new-layout.tsx",
			},
		},
		{
			name:    "untracked observed surface",
			surface: "app/team/new-layout.tsx",
			git: fakeGit{
				isAncestor:  true,
				commitKnown: true,
				untracked:   []string{"app/team/new-layout.tsx"},
			},
		},
		{
			name:    "surface path is normalized",
			surface: "./app/team/layout.tsx",
			git: fakeGit{
				isAncestor:  true,
				commitKnown: true,
				changed:     []string{"app/team/layout.tsx"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := checkFreshness("/repo", recWithSurface(tc.surface), "spec-abc", tc.git.run)
			if f.Current {
				t.Fatal("changed observed surface must not read as current")
			}
			if len(f.ProductDrift) != 1 || f.ProductDrift[0] != tc.surface {
				t.Fatalf("ProductDrift = %v, want %q", f.ProductDrift, tc.surface)
			}
		})
	}
}

func TestCheckFreshness_SpecStale(t *testing.T) {
	git := fakeGit{isAncestor: true, commitKnown: true}
	f := checkFreshness("/repo", recWithSurface("app/team/layout.tsx"), "spec-CHANGED", git.run)

	if f.Current {
		t.Fatal("a moved spec fingerprint must not read as current")
	}
	if !f.SpecStale {
		t.Error("SpecStale should be set")
	}
}

func TestCheckFreshness_NotAnAncestor(t *testing.T) {
	git := fakeGit{isAncestor: false, commitKnown: true}
	f := checkFreshness("/repo", recWithSurface("a.tsx"), "spec-abc", git.run)

	if f.Current || f.Reachable {
		t.Fatalf("expected unreachable, got %+v", f)
	}
	if !strings.Contains(f.Summary(), "did not descend") {
		t.Errorf("Summary = %q", f.Summary())
	}
}

// Ignorance must never render as a clean bill of health. Each of these is a
// question we could not answer, and every one of them returns Unknown rather
// than defaulting either way.
func TestCheckFreshness_UnknownIsNeverFresh(t *testing.T) {
	tests := []struct {
		name string
		rec  *Attestation
		git  fakeGit
		want string
	}{
		{
			name: "commit not in this clone",
			rec:  recWithSurface("a.tsx"),
			git:  fakeGit{isAncestor: false, commitKnown: false},
			want: "not in this clone",
		},
		{
			name: "record has no observed surface",
			rec:  recWithSurface(),
			git:  fakeGit{isAncestor: true, commitKnown: true},
			want: "no observed surface",
		},
		{
			name: "diff could not run",
			rec:  recWithSurface("a.tsx"),
			git:  fakeGit{isAncestor: true, commitKnown: true, diffFails: true},
			want: "could not diff",
		},
		{
			name: "untracked files could not be inspected",
			rec:  recWithSurface("a.tsx"),
			git:  fakeGit{isAncestor: true, commitKnown: true, lsFails: true},
			want: "could not inspect untracked",
		},
		{
			name: "record fingerprint missing",
			rec: func() *Attestation {
				a := recWithSurface("a.tsx")
				a.SpecFingerprint = ""
				return a
			}(),
			git:  fakeGit{isAncestor: true, commitKnown: true},
			want: "record has no spec fingerprint",
		},
		{
			name: "subject scheme git cannot resolve",
			rec: func() *Attestation {
				a := recWithSurface("a.tsx")
				a.Subject.Scheme = SchemeLoreCID
				return a
			}(),
			git:  fakeGit{isAncestor: true, commitKnown: true},
			want: "not resolvable by git",
		},
		{name: "no record at all", rec: nil, git: fakeGit{}, want: "no attestation record"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := checkFreshness("/repo", tc.rec, "spec-abc", tc.git.run)
			if f.Current {
				t.Fatal("an unanswerable freshness question rendered as current")
			}
			if !f.Unknown {
				t.Fatalf("expected Unknown, got %+v", f)
			}
			if !strings.Contains(f.Reason, tc.want) {
				t.Errorf("Reason = %q, want it to mention %q", f.Reason, tc.want)
			}
		})
	}
}

func TestCheckFreshness_MissingCurrentFingerprintIsUnknown(t *testing.T) {
	f := checkFreshness("/repo", recWithSurface("a.tsx"), "", fakeGit{isAncestor: true, commitKnown: true}.run)
	if f.Current || !f.Unknown {
		t.Fatalf("missing current fingerprint must be unknown, got %+v", f)
	}
	if !strings.Contains(f.Reason, "current spec fingerprint is unavailable") {
		t.Errorf("Reason = %q", f.Reason)
	}
}

// The fingerprint digest must depend on the SET of inputs, not their order —
// otherwise a cosmetic reordering by the compiler reads as spec drift and sends
// somebody re-proving a capability that never changed.
func TestFingerprintDigest_OrderIndependent(t *testing.T) {
	a := PlanFingerprint{Inputs: []FingerprintInput{
		{Path: "features/x.feature", OID: "aaa"},
		{Path: "business-actions/y.md", OID: "bbb"},
	}}
	b := PlanFingerprint{Inputs: []FingerprintInput{
		{Path: "business-actions/y.md", OID: "bbb"},
		{Path: "features/x.feature", OID: "aaa"},
	}}
	if FingerprintDigest(a) != FingerprintDigest(b) {
		t.Error("digest changed when only the input order changed")
	}

	c := PlanFingerprint{Inputs: []FingerprintInput{
		{Path: "features/x.feature", OID: "CHANGED"},
		{Path: "business-actions/y.md", OID: "bbb"},
	}}
	if FingerprintDigest(a) == FingerprintDigest(c) {
		t.Error("digest did not change when an input OID changed")
	}
	if FingerprintDigest(PlanFingerprint{}) != "" {
		t.Error("an empty fingerprint must digest to empty, not to a hash of nothing")
	}
}

func TestLiveFingerprint_UsesCurrentSourceBytes(t *testing.T) {
	root := writeCorpus(t, map[string]string{"d/live.feature": "hello\n"})
	fp := PlanFingerprint{Inputs: []FingerprintInput{
		// Known `git hash-object` result for "hello\n". This also locks the
		// in-process implementation to the compiler's Git blob formula.
		{Path: "features/d/live.feature", OID: "ce013625030ba8dba906f756967f9e9ca394464a"},
	}}

	before, err := LiveFingerprint(root, fp)
	if err != nil {
		t.Fatalf("LiveFingerprint before edit: %v", err)
	}
	if want := FingerprintDigest(fp); before != want {
		t.Fatalf("live fingerprint = %q, want compiler-compatible digest %q", before, want)
	}
	if err := os.WriteFile(filepath.Join(root, "features", "d", "live.feature"), []byte("Feature: After\n"), 0o600); err != nil {
		t.Fatalf("edit feature: %v", err)
	}
	after, err := LiveFingerprint(root, fp)
	if err != nil {
		t.Fatalf("LiveFingerprint after edit: %v", err)
	}
	if before == after {
		t.Fatal("live fingerprint did not change when a compiler input changed")
	}
	if got := FingerprintDigest(fp); after == got {
		t.Fatal("live fingerprint reused the OID stored in the compiled plan")
	}
}

func TestLiveFingerprint_RejectsInputOutsideCorpus(t *testing.T) {
	root := t.TempDir()
	_, err := LiveFingerprint(root, PlanFingerprint{Inputs: []FingerprintInput{{Path: "../secret", OID: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "escapes corpus root") {
		t.Fatalf("LiveFingerprint path traversal error = %v", err)
	}
}

func TestCheckFreshness_RealGitWorkingTree(t *testing.T) {
	repo := t.TempDir()
	runFreshnessGit(t, repo, "init", "--quiet")
	runFreshnessGit(t, repo, "config", "user.name", "Attest Test")
	runFreshnessGit(t, repo, "config", "user.email", "attest@example.com")
	path := filepath.Join(repo, "observed.go")
	if err := os.WriteFile(path, []byte("package observed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFreshnessGit(t, repo, "add", "observed.go")
	runFreshnessGit(t, repo, "commit", "--quiet", "-m", "initial")

	head, err := HeadCommit(repo)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	rec := recWithSurface("observed.go")
	rec.Subject = TreeRef{Scheme: SchemeGitCommit, Value: head}
	if got := CheckFreshness(repo, rec, "spec-abc"); !got.Current {
		t.Fatalf("clean real repository is not current: %+v", got)
	}

	if err := os.WriteFile(path, []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := CheckFreshness(repo, rec, "spec-abc")
	if got.Current || len(got.ProductDrift) != 1 || got.ProductDrift[0] != "observed.go" {
		t.Fatalf("real working-tree edit was not detected: %+v", got)
	}
}

func runFreshnessGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
