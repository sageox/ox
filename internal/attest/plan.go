package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// compiledSubdir holds the committed compiled plans, one per feature file.
const compiledSubdir = "compiled"

// CompiledPlan is the committed artifact the Attest compiler writes per feature.
//
// It is deliberately consulted as the AUTHORITY on whether a scenario
// dispatches, rather than re-deriving that from tags. Two reasons: the compiler
// owns the rule and a second implementation of it here would eventually
// disagree; and a feature with no compiled plan at all cannot dispatch
// regardless of how its tags read, which tag inspection alone can never see.
type CompiledPlan struct {
	SchemaVersion int    `json:"schemaVersion"`
	PlanContract  string `json:"planContract"`
	// Feature is corpus-relative, e.g. "features/channels/connect-slack.feature".
	Feature     string          `json:"feature"`
	FeatureName string          `json:"featureName"`
	Fingerprint PlanFingerprint `json:"fingerprint"`
	Scenarios   []PlanScenario  `json:"scenarios"`
	Excluded    []PlanExcluded  `json:"excluded"`
}

// PlanFingerprint is the compiler's record of exactly which SPEC-side sources
// this plan was built from, each pinned to a git blob OID.
//
// Worth being precise about what it does and does not cover, because it is
// easy to mistake for a freshness answer: these are the feature file and the
// business-action / verification markdown it matched — the SPEC. No product
// source file appears here. So a changed OID proves the spec drifted, while an
// unchanged fingerprint proves nothing at all about whether the product moved
// underneath it. Freshness needs both halves; this is the half that exists.
type PlanFingerprint struct {
	Inputs []FingerprintInput `json:"inputs"`
}

// FingerprintInput is one spec source pinned to its blob OID at compile time.
type FingerprintInput struct {
	Path string `json:"path"`
	OID  string `json:"oid"`
}

// FingerprintDigest collapses a plan fingerprint to one comparable string.
//
// Sorted before hashing so the digest depends on the SET of (path, oid) pairs
// and not on the compiler's emission order — otherwise a cosmetic reordering
// would read as spec drift and send someone re-proving a capability that never
// changed.
func FingerprintDigest(fp PlanFingerprint) string {
	if len(fp.Inputs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fp.Inputs))
	for _, in := range fp.Inputs {
		parts = append(parts, in.Path+"@"+in.OID)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

// PlanScenario is a scenario the compiler selected — it dispatches.
type PlanScenario struct {
	Name  string   `json:"name"`
	Index int      `json:"index"`
	Rule  string   `json:"rule"`
	Tags  []string `json:"tags"`
}

// PlanExcluded is a scenario the compiler refused to dispatch, and why.
//
// Note it carries no Rule, so attribution back to a capability is by scenario
// NAME against the corpus scan — see Plans.Dispatches.
type PlanExcluded struct {
	Name   string   `json:"name"`
	Index  int      `json:"index"`
	Tags   []string `json:"tags"`
	Reason string   `json:"reason"`
}

// Plans indexes every compiled plan in a corpus by its feature path.
type Plans struct {
	// byFeature is keyed by the plan's own `feature` field (corpus-relative,
	// slash-separated), which is the only join key both artifacts share.
	byFeature map[string]*CompiledPlan
	// Count is how many plan files were loaded.
	Count int
}

// LoadPlans reads corpusRoot/compiled/**/*.plan.json.
//
// A missing compiled/ directory is NOT an error: it means nothing has been
// compiled yet, which is a legitimate state that must render as "nothing
// dispatches" rather than as a failure to answer.
func LoadPlans(corpusRoot string) (*Plans, error) {
	plans := &Plans{byFeature: map[string]*CompiledPlan{}}
	dir := filepath.Join(corpusRoot, compiledSubdir)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return plans, nil
		}
		return nil, fmt.Errorf("read compiled plans: %w", err)
	}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".plan.json") {
			return nil
		}
		raw, readErr := os.ReadFile(path) //nolint:gosec // path comes from walking a caller-supplied corpus dir
		if readErr != nil {
			return fmt.Errorf("read plan %s: %w", path, readErr)
		}
		var plan CompiledPlan
		if jsonErr := json.Unmarshal(raw, &plan); jsonErr != nil {
			return fmt.Errorf("parse plan %s: %w", path, jsonErr)
		}
		if plan.Feature == "" {
			// A plan that cannot name its feature cannot be joined to a
			// capability. Skipping it silently would inflate "skipped"; naming
			// it is the honest failure.
			return fmt.Errorf("plan %s has no feature path", path)
		}
		plans.byFeature[filepath.ToSlash(plan.Feature)] = &plan
		plans.Count++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return plans, nil
}

// For returns the compiled plan covering a capability's feature file, and
// whether one exists at all. capabilityPath is repo-relative; the plan's key is
// corpus-relative, so the join is a suffix match on the shared tail.
func (p *Plans) For(capabilityPath string) (*CompiledPlan, bool) {
	if p == nil {
		return nil, false
	}
	want := filepath.ToSlash(capabilityPath)
	for key, plan := range p.byFeature {
		if strings.HasSuffix(want, key) {
			return plan, true
		}
	}
	return nil, false
}

// Dispatches reports whether a named scenario in this plan actually dispatches.
//
// Matched by name because `excluded[]` carries no rule and both arrays index
// independently from zero, so the scenario NAME is the only stable join key
// between the compiled artifact and the corpus scan.
func (plan *CompiledPlan) Dispatches(scenarioName string) bool {
	if plan == nil {
		return false
	}
	for _, s := range plan.Scenarios {
		if s.Name == scenarioName {
			return true
		}
	}
	return false
}

// ExclusionReason returns why a named scenario was excluded ("tag",
// "unmatched-step", ...), or "" when the plan does not mention it.
func (plan *CompiledPlan) ExclusionReason(scenarioName string) string {
	if plan == nil {
		return ""
	}
	for _, e := range plan.Excluded {
		if e.Name == scenarioName {
			return e.Reason
		}
	}
	return ""
}
