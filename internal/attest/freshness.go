package attest

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Freshness is what a record can still be trusted to say about HEAD.
//
// Deliberately NOT a boolean, and deliberately two independent axes. Merging
// them would produce a single "fresh" that is sometimes a lie, and the whole
// point of the record is that a false green is the worst output this system can
// produce. A false STALE costs one re-run; a false FRESH is the failure mode.
type Freshness struct {
	// Current is true only when the subject commit is an ancestor of HEAD AND
	// nothing observed has moved. The conjunction is the answer; the fields
	// below say which half failed.
	Current bool `json:"current"`
	// Reachable is false when the subject commit is not an ancestor of HEAD —
	// the proof was made on a tree this one did not descend from.
	Reachable bool `json:"reachable"`
	// SpecStale is true when the compiled plan's fingerprint has moved since
	// the record was minted: the specification itself changed.
	SpecStale bool `json:"spec_stale"`
	// ProductDrift lists observed surfaces whose files changed between the
	// subject commit and the working tree (INCLUDING uncommitted edits).
	ProductDrift []string `json:"product_drift,omitempty"`
	// Unknown is the honest third answer: we could not determine freshness —
	// no git, a commit that is not in this clone, or a record minted with no
	// observed surface at all. Never silently reported as fresh.
	Unknown bool   `json:"unknown"`
	Reason  string `json:"reason,omitempty"`
}

// gitRunner is the seam tests use to avoid needing a real repository.
type gitRunner func(repoRoot string, args ...string) (string, error)

func runGit(repoRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...) //nolint:gosec // args are constructed here, never user input
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// HeadCommit resolves the tree a new record should be bound to.
//
// Domain logic rather than CLI plumbing: "which tree is this proof valid for?"
// is the attestation's third required property, and answering it belongs beside
// the code that later checks whether that tree is still current.
func HeadCommit(repoRoot string) (string, error) {
	sha, err := runGit(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve HEAD in %s: %w", repoRoot, err)
	}
	return sha, nil
}

// CheckFreshness answers "can this record still be trusted about the current
// working tree?"
//
// This is the operation that makes the CLI the only honest home for attest:
// it compares against the WORKING TREE, uncommitted edits included. A server
// has no working tree, so the same question asked over an API could only ever
// be answered about the last pushed commit — a different and weaker claim.
func CheckFreshness(repoRoot string, a *Attestation, currentSpecFingerprint string) Freshness {
	return checkFreshness(repoRoot, a, currentSpecFingerprint, runGit)
}

func checkFreshness(repoRoot string, a *Attestation, currentSpecFingerprint string, git gitRunner) Freshness {
	f := Freshness{}
	if a == nil {
		return Freshness{Unknown: true, Reason: "no attestation record"}
	}
	if a.Subject.Scheme != SchemeGitCommit {
		// A lore-cid subject is legitimate; this checker simply cannot resolve
		// it. Saying so is the correct answer — guessing "fresh" is not.
		return Freshness{Unknown: true, Reason: "subject scheme " + a.Subject.Scheme + " is not resolvable by git"}
	}
	if a.SpecFingerprint == "" {
		return Freshness{Unknown: true, Reason: "record has no spec fingerprint — specification drift cannot be ruled out"}
	}
	if currentSpecFingerprint == "" {
		return Freshness{Unknown: true, Reason: "current spec fingerprint is unavailable — specification drift cannot be ruled out"}
	}

	// Half 1: is the proof's tree an ancestor of where we are now?
	if _, err := git(repoRoot, "merge-base", "--is-ancestor", a.Subject.Value, "HEAD"); err != nil {
		// Distinguish "not an ancestor" from "commit not in this clone": the
		// first is a real staleness verdict, the second is ignorance.
		if _, catErr := git(repoRoot, "cat-file", "-e", a.Subject.Value+"^{commit}"); catErr != nil {
			return Freshness{Unknown: true, Reason: "subject commit " + short(a.Subject.Value) + " is not in this clone"}
		}
		f.Reachable = false
	} else {
		f.Reachable = true
	}

	// Half 2a: did the specification itself move?
	f.SpecStale = a.SpecFingerprint != currentSpecFingerprint

	// Half 2b: did the product move underneath it?
	//
	// `git diff --name-only <subject>` with no second ref compares against the
	// WORKING TREE, so an uncommitted edit counts. That is the point.
	// Disable rename detection so both the old and new path are emitted. A
	// record observes the old path; accepting only a rename destination would
	// let that observed surface disappear without registering as drift.
	changed, err := git(repoRoot, "diff", "--name-only", "--no-renames", a.Subject.Value)
	if err != nil {
		return Freshness{
			Reachable: f.Reachable, SpecStale: f.SpecStale,
			Unknown: true, Reason: "could not diff against " + short(a.Subject.Value),
		}
	}
	changedSet := map[string]struct{}{}
	for _, p := range strings.Split(changed, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			changedSet[normalizeRepoPath(p)] = struct{}{}
		}
	}
	// `git diff` intentionally omits untracked files. They still matter when a
	// dirty-tree record names one as an observed surface, so fold them into the
	// same changed set rather than silently declaring the proof current.
	untracked, err := git(repoRoot, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return Freshness{
			Reachable: f.Reachable, SpecStale: f.SpecStale,
			Unknown: true, Reason: "could not inspect untracked files",
		}
	}
	for _, p := range strings.Split(untracked, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			changedSet[normalizeRepoPath(p)] = struct{}{}
		}
	}
	for _, s := range a.ObservedSurface.Surfaces {
		if _, hit := changedSet[normalizeRepoPath(s.SurfaceID)]; hit {
			f.ProductDrift = append(f.ProductDrift, s.SurfaceID)
		}
	}

	// A record with no observed surface cannot rule product drift in or out.
	// It is NOT fresh — it is unknown, and conflating the two is exactly how a
	// stale proof keeps rendering green.
	if len(a.ObservedSurface.Surfaces) == 0 {
		return Freshness{
			Reachable: f.Reachable, SpecStale: f.SpecStale,
			Unknown: true, Reason: "record has no observed surface — product drift cannot be ruled out",
		}
	}

	f.Current = f.Reachable && !f.SpecStale && len(f.ProductDrift) == 0
	return f
}

func normalizeRepoPath(path string) string {
	path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	return strings.TrimPrefix(path, "./")
}

// Summary is a one-line human verdict.
func (f Freshness) Summary() string {
	switch {
	case f.Unknown:
		return "unknown — " + f.Reason
	case f.Current:
		return "current"
	case !f.Reachable:
		return "proof was made on a tree this one did not descend from"
	case f.SpecStale && len(f.ProductDrift) > 0:
		return "spec changed and the product moved underneath it"
	case f.SpecStale:
		return "spec changed since the proof"
	default:
		return "product moved underneath the proof"
	}
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
