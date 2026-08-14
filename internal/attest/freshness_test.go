package attest

import (
	"errors"
	"strings"
	"testing"
)

// fakeGit scripts git responses by subcommand so freshness can be tested
// without standing up a repository per case.
type fakeGit struct {
	isAncestor  bool
	commitKnown bool
	changed     []string
	diffFails   bool
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
		return strings.Join(f.changed, "\n"), nil
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
