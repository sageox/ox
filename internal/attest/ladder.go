package attest

import "sort"

// The tag vocabulary, matched by EXACT equality everywhere it is used.
//
// Exact, never prefix: `@pending-migration` is NOT `@pending` and DOES dispatch.
// Prefix-matching would silently reclassify a whole family of live scenarios as
// switched-off — an over-count of "skipped" that reads as honest pessimism while
// actually being wrong.
const (
	TagValidated   = "@validated"
	TagPending     = "@pending"
	TagWIP         = "@wip"
	TagSpeculative = "@speculative"
)

// ExclusionTags are the tags the Attest compiler treats as "do not dispatch".
// Kept here for callers that need to explain a verdict; the compiled plan
// remains the authority on what actually dispatched.
var ExclusionTags = []string{TagPending, TagWIP, TagSpeculative}

// Verdict is one rung of the honest-state ladder, worst first.
//
// The vocabulary matches the web app's coverage view exactly so the CLI and the
// browser can never tell a reader two different stories about the same
// capability. `stamped` is the rung this work adds, and it exists for one
// reason: a `@validated` tag with no durable record behind it is a CLAIM, and
// letting a claim render the same as a proof is the precise mis-report the
// whole attestation idea was written against.
type Verdict string

const (
	// VerdictUntested — the capability has no scenarios at all.
	VerdictUntested Verdict = "untested"
	// VerdictSkipped — scenarios exist, none of them dispatch (excluded by tag,
	// or the feature has no compiled plan, so nothing can run).
	VerdictSkipped Verdict = "skipped"
	// VerdictUnproven — at least one scenario dispatches, none is stamped.
	VerdictUnproven Verdict = "unproven"
	// VerdictStamped — stamped green by a human, with no attestation record
	// behind it. The stamp names a run id that resolves to nothing anyone else
	// can open.
	VerdictStamped Verdict = "stamped"
	// VerdictAttested — a committed record carrying a clean red-first proof.
	VerdictAttested Verdict = "attested"
)

// VerdictOrder is the ladder, worst first. Also the display order.
var VerdictOrder = []Verdict{
	VerdictUntested,
	VerdictSkipped,
	VerdictUnproven,
	VerdictStamped,
	VerdictAttested,
}

// Meaning is the one-line explanation shown beside a count, so a reader never
// has to guess what a rung asserts.
func (v Verdict) Meaning() string {
	switch v {
	case VerdictUntested:
		return "no scenario covers this capability at all"
	case VerdictSkipped:
		return "scenarios exist but none dispatch"
	case VerdictUnproven:
		return "dispatches, never stamped green"
	case VerdictStamped:
		return "stamped green, but no record backs the claim"
	case VerdictAttested:
		return "a committed record with a red-first proof"
	default:
		return string(v)
	}
}

// Assessment is a capability plus the verdict the corpus supports for it.
type Assessment struct {
	Capability Capability `json:"capability"`
	Verdict    Verdict    `json:"verdict"`
	// Dispatching is how many of its scenarios the compiler actually selected.
	Dispatching int `json:"dispatching"`
	// Stamped is how many scenarios carry the `@validated` tag — the claim.
	Stamped int `json:"stamped"`
	// Unprovenanced is how many of those carry the tag with NO `# validated:`
	// comment: a green assertion with no date and no run id, so it cannot even
	// be aged. Surfaced separately because it is strictly worse than a stale
	// stamp, and averaging the two would hide it.
	Unprovenanced int `json:"unprovenanced"`
	// NewestStamp is the most recent stamp across its scenarios, if any.
	NewestStamp *Stamp `json:"newest_stamp,omitempty"`
	// NoPlan records that the feature has no compiled artifact at all — a
	// materially different cause of `skipped` than "every scenario is tagged
	// off", and one a reader needs in order to know which fix applies.
	NoPlan bool `json:"no_plan"`
	// Record is the attestation backing this capability, when one exists.
	Record *Attestation `json:"record,omitempty"`
}

// Assess computes the verdict for one capability against the compiled plans and
// the committed attestation records.
//
// Until a record can be pointed at, the strongest thing any corpus can
// truthfully say about a capability is that somebody stamped it — and a record
// whose proof is ambiguous or inconclusive does NOT promote it, because those
// are findings rather than proofs.
func Assess(cap Capability, plans *Plans, records *Records) Assessment {
	a := Assessment{Capability: cap}

	if len(cap.Scenarios) == 0 {
		a.Verdict = VerdictUntested
		return a
	}

	plan, hasPlan := plans.For(cap.Path)
	a.NoPlan = !hasPlan

	for i := range cap.Scenarios {
		s := &cap.Scenarios[i]
		if hasPlan && plan.Dispatches(s.Name) {
			a.Dispatching++
		}
		if s.Validated {
			a.Stamped++
			if s.Stamp == nil {
				a.Unprovenanced++
			}
		}
		if s.Stamp != nil && (a.NewestStamp == nil || s.Stamp.Date > a.NewestStamp.Date) {
			a.NewestStamp = s.Stamp
		}
	}

	if rec, ok := records.For(cap.ID); ok {
		a.Record = rec
	}

	switch {
	case a.Dispatching == 0:
		a.Verdict = VerdictSkipped
	case a.Record != nil && a.Record.IsProof():
		// The only rung that renders as done, and it requires a CLEAN proof.
		// An ambiguous record deliberately leaves the capability at `stamped`:
		// it went red somewhere other than the step naming the claim, so it
		// proves something else.
		a.Verdict = VerdictAttested
	case a.Stamped == 0 && a.Record == nil:
		a.Verdict = VerdictUnproven
	default:
		a.Verdict = VerdictStamped
	}
	return a
}

// Report is a whole corpus assessed.
type Report struct {
	Root         string   `json:"root"`
	Files        int      `json:"files"`
	Plans        int      `json:"plans"`
	Records      int      `json:"records"`
	Capabilities int      `json:"capabilities"`
	Domains      []string `json:"domains"`
	// InvalidRecords maps a path to why it could not be read. Reported rather
	// than swallowed: an unreadable record is a proof that silently vanishes,
	// which looks exactly like never having been proven.
	InvalidRecords map[string]string       `json:"invalid_records,omitempty"`
	Counts         map[Verdict]int         `json:"counts"`
	Scenarios      ScenarioTotals          `json:"scenarios"`
	Assessments    []Assessment            `json:"assessments"`
	ByDomain       map[string][]Assessment `json:"-"`
}

// ScenarioTotals are the corpus-wide scenario numbers, reported alongside the
// capability ladder because they are the figures the corpus already publishes
// about itself and a reader will want to reconcile the two.
type ScenarioTotals struct {
	Authored    int `json:"authored"`
	Dispatching int `json:"dispatching"`
	// Stamped counts `@validated` TAGS on real tag lines only.
	Stamped int `json:"stamped"`
	// Unprovenanced counts stamps with no `# validated:` comment behind them.
	Unprovenanced int `json:"unprovenanced"`
}

// BuildReport assesses every capability in a corpus.
func BuildReport(corpus *Corpus, plans *Plans, records *Records) *Report {
	r := &Report{
		Root:         corpus.Root,
		Files:        corpus.Files,
		Plans:        plans.Count,
		Records:      records.Count,
		InvalidRecords: records.Invalid,
		Capabilities: len(corpus.Capabilities),
		Domains:      corpus.Domains(),
		Counts:       map[Verdict]int{},
		ByDomain:     map[string][]Assessment{},
	}
	for _, v := range VerdictOrder {
		r.Counts[v] = 0
	}
	for _, cap := range corpus.Capabilities {
		a := Assess(cap, plans, records)
		r.Counts[a.Verdict]++
		r.Scenarios.Authored += len(cap.Scenarios)
		r.Scenarios.Dispatching += a.Dispatching
		r.Scenarios.Stamped += a.Stamped
		r.Scenarios.Unprovenanced += a.Unprovenanced
		r.Assessments = append(r.Assessments, a)
		r.ByDomain[cap.Domain] = append(r.ByDomain[cap.Domain], a)
	}
	return r
}

// Weakest returns up to n assessments ordered worst-first — the answer to
// "where does the next hour go?", which is the only question this report is
// actually useful for.
func (r *Report) Weakest(n int) []Assessment {
	out := make([]Assessment, len(r.Assessments))
	copy(out, r.Assessments)
	rank := map[Verdict]int{}
	for i, v := range VerdictOrder {
		rank[v] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Verdict] != rank[out[j].Verdict] {
			return rank[out[i].Verdict] < rank[out[j].Verdict]
		}
		return out[i].Capability.ID < out[j].Capability.ID
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}
