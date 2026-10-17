// Package planreview is the persistence contract for plan review records in a
// SageOx Ledger: their schemas and file names, the assembler that turns a
// capture of a plan's review files into the merged review, the immutable
// records an operation writes, and confined reads and writes of a local
// worktree. docs/specs/plan-review-ledger-contract.md in the ox repository is
// the contract it implements.
//
// A host owns its transport, identity and authorization. It captures a plan's
// feedback/ subtree as an Inventory (Capture does this for a local worktree),
// assembles it, and writes the one record an operation produces (Publish does
// this for a local worktree).
package planreview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Status is a reviewer's verdict on one anchored element.
type Status string

const (
	StatusApprove       Status = "approve"
	StatusRequestChange Status = "request-change"
	StatusFlag          Status = "flag"
	StatusComment       Status = "comment"
)

// State is the disposition a resolution records.
type State string

const (
	StateAddressed State = "addressed" // the author made the change
	StateWontfix   State = "wontfix"   // the author declined, with a reason
	StateVerified  State = "verified"  // a reviewer confirmed the change
)

// Item is one anchored review mark. Anchor is the content hash the review page
// computes (see AnchorFor): "h…" for an element, "q…" for a highlight; or
// WholePlanAnchor.
//
// The field order and tags are ContentRoundID's input; changing them changes
// every content-derived round id.
type Item struct {
	Anchor   string `json:"anchor"`
	Section  string `json:"section,omitempty"`
	Label    string `json:"label"`
	Quote    string `json:"quote,omitempty"`
	Status   Status `json:"status"`
	Note     string `json:"note,omitempty"`
	Reviewer string `json:"reviewer,omitempty"`
}

// WholePlanAnchor is the anchor of a mark on the plan as a whole, such as its
// direction, scope or readiness. It names no element, so Rebind never moves
// it. Writers label it "Whole plan", which readers that predate it print.
const WholePlanAnchor = "plan"

// Round is one review submission. RequestSHA256 is set on immutable records
// only: the SHA-256 of the request bytes the client froze.
type Round struct {
	ID            string    `json:"id,omitempty"`
	Slug          string    `json:"slug"`
	Reviewer      string    `json:"reviewer,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	Items         []Item    `json:"items"`
	RequestSHA256 string    `json:"request_sha256,omitempty"`
}

// Resolution is one disposition of a review item. ID, Reviewer and
// RequestSHA256 are set on immutable records only.
type Resolution struct {
	ID            string    `json:"id,omitempty"`
	Anchor        string    `json:"anchor"`
	State         State     `json:"state"`
	Commit        string    `json:"commit,omitempty"`
	Note          string    `json:"note,omitempty"`
	At            time.Time `json:"at"`
	Reviewer      string    `json:"reviewer,omitempty"`
	RequestSHA256 string    `json:"request_sha256,omitempty"`
}

// Remap records that the element a mark was anchored to moved: whatever lived
// at From now lives at To.
type Remap struct {
	From    string    `json:"from"`
	To      string    `json:"to"`
	Section string    `json:"section,omitempty"`
	Label   string    `json:"label,omitempty"`
	Method  string    `json:"method"` // label-exact | label-fuzzy
	Score   float64   `json:"score"`
	At      time.Time `json:"at"`
}

// File is one file of a plan's review subtree: its slash-separated path
// relative to the plan directory, and its exact bytes.
type File struct {
	Path string
	Data []byte
}

// Inventory is one capture of a plan's feedback/ subtree: every file the host
// read, byte-exact, and every entry it could not read.
type Inventory struct {
	Files    []File
	Failures []Failure
}

// Failure is an entry a capture could not use. Path is "" for a failure of the
// capture as a whole.
type Failure struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Failure reasons this package reports. A host's transport may add its own.
const (
	ReasonUnrecognized    = "unrecognized"       // not a record name this package knows
	ReasonUndecodable     = "undecodable"        // not valid JSON for its kind
	ReasonAltered         = "altered"            // bytes no longer hash to the name; still assembled
	ReasonTooLarge        = "too_large"          // over Limits.RecordBytes; not read
	ReasonTooManyRecords  = "too_many_records"   // over Limits.Records; no later record read
	ReasonCaptureTooLarge = "capture_too_large"  // over Limits.CaptureBytes; no later record read
	ReasonNotRegular      = "not_regular"        // a symlink or other non-regular entry; not followed
	ReasonUnreadable      = "unreadable"         // an I/O error
	ReasonChanged         = "changed"            // the subtree changed while it was read
	ReasonReplay          = "replay_in_progress" // a rebase or merge was in progress
)

// Conflict is one operation stored with differing payloads, or under another
// reviewer. Every copy is kept; the first path is the copy assembled.
type Conflict struct {
	Kind  string   `json:"kind"` // "round" or "resolution"
	ID    string   `json:"id"`
	Paths []string `json:"paths"`
}

// Merged is one review item: the latest mark per (anchor, reviewer), joined
// with the latest resolution of its anchor.
type Merged struct {
	Item
	RaisedAt   time.Time   `json:"raised_at"`
	Resolution *Resolution `json:"resolution,omitempty"`
	// Open is true with no resolution, or when the mark was raised after it.
	Open bool `json:"open"`
	// RemappedFrom is the mark's original anchor when a remap moved it; Anchor
	// is always the current one.
	RemappedFrom string `json:"remapped_from,omitempty"`
}

// Review is one assembled capture. Rounds are the rounds assembled, oldest
// first, each operation once.
type Review struct {
	Items     []Merged
	Rounds    []Round
	Failures  []Failure
	Conflicts []Conflict
}

// Complete reports whether the review holds every record of its capture. An
// incomplete review is never authoritative: a consumer must not report "no
// open feedback", show an item resolved, or approve on one.
func (r Review) Complete() bool { return len(r.Failures) == 0 }

// Limits bound one capture. A zero field takes its DefaultLimits value.
type Limits struct {
	Records      int   // records read
	RecordBytes  int64 // one record
	CaptureBytes int64 // every record together
}

// MaxRecordBytes is the largest record a writer here stores. A reader whose
// RecordBytes is lower can refuse records a writer stored.
const MaxRecordBytes = 8 << 20

// DefaultLimits bound a local capture. A host that pays per record read may
// pass lower Records and CaptureBytes.
var DefaultLimits = Limits{Records: 10_000, RecordBytes: MaxRecordBytes, CaptureBytes: 64 << 20}

func (l Limits) orDefaults() Limits {
	if l.Records <= 0 {
		l.Records = DefaultLimits.Records
	}
	if l.RecordBytes <= 0 {
		l.RecordBytes = DefaultLimits.RecordBytes
	}
	if l.CaptureBytes <= 0 {
		l.CaptureBytes = DefaultLimits.CaptureBytes
	}
	return l
}

// budget applies Limits to records taken in path order. Once the record or
// capture bound is passed, no later record is read.
type budget struct {
	Limits
	n       int
	total   int64
	stopped bool
}

// admit reports whether a record of size bytes at p may be read, appending the
// failure to fails when it may not.
func (b *budget) admit(fails *[]Failure, p string, size int64) bool {
	switch {
	case b.stopped:
		return false
	case b.n == b.Records:
		b.stopped = true
		*fails = append(*fails, Failure{Reason: ReasonTooManyRecords, Detail: fmt.Sprintf("more than %d records", b.Records)})
		return false
	}
	b.n++
	switch {
	case size > b.RecordBytes:
		*fails = append(*fails, Failure{Path: p, Reason: ReasonTooLarge, Detail: fmt.Sprintf("%d bytes, limit %d", size, b.RecordBytes)})
		return false
	case b.total+size > b.CaptureBytes:
		b.stopped = true
		*fails = append(*fails, Failure{Reason: ReasonCaptureTooLarge, Detail: fmt.Sprintf("more than %d bytes", b.CaptureBytes)})
		return false
	}
	b.total += size
	return true
}

// timeLayout names a record by its acceptance time, UTC and fixed width, so
// copies of one operation sort in acceptance order.
const timeLayout = "20060102-150405.000000000"

// idPattern bounds an operation id. The id is part of a file name, so this is
// a traversal guard as much as a format.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// immutableName matches an immutable record's name after its kind prefix:
// "<time>-<id>.<digest>.json", digest being the first 16 hex characters of
// the SHA-256 of the file's bytes. An id cannot contain '.', so the split is
// unambiguous.
var immutableName = regexp.MustCompile(`^\d{8}-\d{6}\.\d{9}-[A-Za-z0-9_-]{8,64}\.([0-9a-f]{16})\.json$`)

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func recordName(prefix string, at time.Time, id string, data []byte) string {
	return prefix + at.UTC().Format(timeLayout) + "-" + id + "." + digest(data) + ".json"
}

type kind int

const (
	notReview kind = iota // outside feedback/, or a writer's dot-prefixed temporary
	unrecognized
	roundFile
	resolutionFile
	remapFile
	legacyResolutions // feedback/resolutions.json, written by v0.20.0 and earlier
	legacyRemaps      // feedback/remaps.json
)

// classify returns the kind of the file at p and, for an immutable name, the
// digest its bytes must have. Every name a release ever wrote is a record:
// round-<time>.json, round-<time>-<rand8>.json and round-<time>-<id>.json
// are rounds too.
func classify(p string) (kind, string) {
	rest, ok := strings.CutPrefix(p, "feedback/")
	if !ok {
		return notReview, ""
	}
	dir, name := path.Split(rest)
	if strings.HasPrefix(name, ".") || strings.Contains("/"+dir, "/.") {
		return notReview, ""
	}
	switch {
	case dir == "" && name == "resolutions.json":
		return legacyResolutions, ""
	case dir == "" && name == "remaps.json":
		return legacyRemaps, ""
	case dir == "" && strings.HasPrefix(name, "round-") && strings.HasSuffix(name, ".json"):
		return roundFile, nameDigest(strings.TrimPrefix(name, "round-"))
	case dir == "resolutions/" && strings.HasSuffix(name, ".json"):
		return resolutionFile, nameDigest(name)
	case dir == "remaps/" && strings.HasSuffix(name, ".json"):
		return remapFile, nameDigest(name)
	}
	return unrecognized, ""
}

func nameDigest(name string) string {
	if m := immutableName.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// Assemble merges the review a capture holds. It decodes every record name
// this package knows, reports every failure and conflicting operation, and
// keeps the assembly every ox release has used:
//
//   - rounds in path order; an id seen again assembles once, as its earliest
//     copy, so a retried request never reopens an item;
//   - the latest mark per (canonical anchor, reviewer);
//   - the latest resolution per canonical anchor, by its at; a resolution id
//     seen again counts once, as its earliest;
//   - an item is open when it was raised after that resolution;
//   - remaps are applied at read time; nothing on disk is rewritten.
//
// limits bound the records decoded, in path order, whatever the transport
// already enforced.
func Assemble(inv Inventory, limits Limits) Review {
	rev := Review{Failures: slices.Clone(inv.Failures)}
	files := slices.Clone(inv.Files)
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })

	// Path order puts each legacy array before its per-entry directory
	// ('.' sorts before '/'), the order releases have always merged them in.
	type roundCopy struct {
		path  string
		round Round
	}
	type resolutionCopy struct {
		path string
		res  Resolution
	}
	var rounds []roundCopy
	var resolutions []resolutionCopy
	var remaps []Remap
	b := budget{Limits: limits.orDefaults()}
	for _, f := range files {
		k, want := classify(f.Path)
		switch k {
		case notReview:
			continue
		case unrecognized:
			rev.Failures = append(rev.Failures, Failure{Path: f.Path, Reason: ReasonUnrecognized})
			continue
		}
		if !b.admit(&rev.Failures, f.Path, int64(len(f.Data))) {
			continue
		}
		if want != "" && digest(f.Data) != want {
			rev.Failures = append(rev.Failures, Failure{Path: f.Path, Reason: ReasonAltered, Detail: "bytes do not hash to the name's digest"})
		}
		var err error
		switch k {
		case roundFile:
			var r Round
			if err = json.Unmarshal(f.Data, &r); err == nil {
				rounds = append(rounds, roundCopy{f.Path, r})
			}
		case resolutionFile:
			var r Resolution
			if err = json.Unmarshal(f.Data, &r); err == nil {
				resolutions = append(resolutions, resolutionCopy{f.Path, r})
			}
		case legacyResolutions:
			var rs []Resolution
			if err = json.Unmarshal(f.Data, &rs); err == nil {
				for _, r := range rs {
					resolutions = append(resolutions, resolutionCopy{f.Path, r})
				}
			}
		case remapFile:
			var m Remap
			if err = json.Unmarshal(f.Data, &m); err == nil {
				remaps = append(remaps, m)
			}
		case legacyRemaps:
			var ms []Remap
			if err = json.Unmarshal(f.Data, &ms); err == nil {
				remaps = append(remaps, ms...)
			}
		}
		if err != nil {
			rev.Failures = append(rev.Failures, Failure{Path: f.Path, Reason: ReasonUndecodable, Detail: err.Error()})
		}
	}

	keptRounds, conflicts := firstCopies("round", rounds,
		func(c roundCopy) (string, string) { return c.path, c.round.ID },
		func(a, b roundCopy) bool { return sameRound(a.round, b.round) })
	rev.Conflicts = conflicts
	for _, c := range keptRounds {
		rev.Rounds = append(rev.Rounds, c.round)
	}

	// An id-less entry carried forward into both resolution layouts counts
	// once, as releases before ids did.
	type entryKey struct {
		anchor, commit, note string
		state                State
		at                   int64
	}
	seen := map[entryKey]bool{}
	var entries []resolutionCopy
	for _, c := range resolutions {
		if c.res.ID == "" {
			k := entryKey{c.res.Anchor, c.res.Commit, c.res.Note, c.res.State, c.res.At.UnixNano()}
			if seen[k] {
				continue
			}
			seen[k] = true
		}
		entries = append(entries, c)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].res.At.Before(entries[j].res.At) })
	entries, conflicts = firstCopies("resolution", entries,
		func(c resolutionCopy) (string, string) { return c.path, c.res.ID },
		func(a, b resolutionCopy) bool { return sameResolution(a.res, b.res) })
	rev.Conflicts = append(rev.Conflicts, conflicts...)

	var anchors []string
	for _, r := range rev.Rounds {
		for _, it := range r.Items {
			anchors = append(anchors, it.Anchor)
		}
	}
	for _, c := range entries {
		anchors = append(anchors, c.res.Anchor)
	}
	canon := canonical(anchors, remaps)
	type markKey struct{ anchor, reviewer string }
	latest := map[markKey]Merged{}
	var order []markKey
	for _, r := range rev.Rounds {
		for _, it := range r.Items {
			if it.Reviewer == "" {
				it.Reviewer = r.Reviewer // rounds saved before items were stamped
			}
			orig := it.Anchor
			it.Anchor = canon[orig]
			k := markKey{it.Anchor, it.Reviewer}
			if _, ok := latest[k]; !ok {
				order = append(order, k)
			}
			m := Merged{Item: it, RaisedAt: r.CreatedAt, Open: true}
			if it.Anchor != orig {
				m.RemappedFrom = orig
			}
			latest[k] = m
		}
	}
	// A resolution closes every reviewer's mark on its anchor, unless a later
	// round raised it again. One written before a remap follows its item.
	latestRes := map[string]Resolution{}
	for _, c := range entries {
		a := canon[c.res.Anchor]
		if cur, ok := latestRes[a]; !ok || c.res.At.After(cur.At) {
			latestRes[a] = c.res
		}
	}
	for _, k := range order {
		m := latest[k]
		if r, ok := latestRes[k.anchor]; ok {
			m.Resolution = &r
			m.Open = m.RaisedAt.After(r.At)
		}
		rev.Items = append(rev.Items, m)
	}
	return rev
}

// firstCopies keeps the first copy of each operation id in order; copies
// without an id are separate operations. An id with a copy that is not the
// same operation as its first is a Conflict naming every copy.
func firstCopies[T any](kind string, copies []T, op func(T) (path, id string), same func(a, b T) bool) ([]T, []Conflict) {
	groups := map[string][]T{}
	var ids []string
	var kept []T
	for _, c := range copies {
		if _, id := op(c); id != "" {
			if g, ok := groups[id]; ok {
				groups[id] = append(g, c)
				continue
			}
			groups[id] = []T{c}
			ids = append(ids, id)
		}
		kept = append(kept, c)
	}
	var conflicts []Conflict
	for _, id := range ids {
		g := groups[id]
		if !slices.ContainsFunc(g[1:], func(c T) bool { return !same(g[0], c) }) {
			continue
		}
		cf := Conflict{Kind: kind, ID: id}
		for _, c := range g {
			p, _ := op(c)
			cf.Paths = append(cf.Paths, p)
		}
		conflicts = append(conflicts, cf)
	}
	return kept, conflicts
}

// sameRound reports whether two copies of one round id are the same
// operation: the same payload from the same reviewer. Copies compare who said
// what as ContentRoundID canonicalizes it (reviewer and items), after filling
// each item's empty reviewer from the round's as writers store it; two
// immutable records must also carry the same request digest. A digest copied
// onto edited marks is therefore a conflict, not a duplicate.
func sameRound(a, b Round) bool {
	if a.RequestSHA256 != "" && b.RequestSHA256 != "" && a.RequestSHA256 != b.RequestSHA256 {
		return false
	}
	return contentDigest(a.Reviewer, stamped(a)) == contentDigest(b.Reviewer, stamped(b))
}

// sameResolution reports whether two copies of one resolution id are the same
// operation: the same request from the same reviewer. Only immutable records
// carry a resolution id, and with it the request digest.
func sameResolution(a, b Resolution) bool {
	return a.Reviewer == b.Reviewer && a.RequestSHA256 == b.RequestSHA256 &&
		a.Anchor == b.Anchor && a.State == b.State && a.Commit == b.Commit && a.Note == b.Note
}

func stamped(r Round) []Item {
	items := slices.Clone(r.Items)
	for i := range items {
		if items[i].Reviewer == "" {
			items[i].Reviewer = r.Reviewer
		}
	}
	return items
}

func contentDigest(reviewer string, items []Item) [32]byte {
	canon, _ := json.Marshal(struct {
		Reviewer string `json:"reviewer"`
		Items    []Item `json:"items"`
	}{reviewer, items}) // strings only: Marshal cannot fail
	return sha256.Sum256(canon)
}

// ContentRoundID derives a round id from what the reviewer said (reviewer and
// items), for a request that carries none, such as an exported review applied
// by hand: applying the same export twice yields the same id. It is the id
// `ox plan feedback apply` mints for such an export.
func ContentRoundID(r Round) string {
	sum := contentDigest(r.Reviewer, r.Items)
	return "c" + hex.EncodeToString(sum[:16])
}
