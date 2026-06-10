package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// feedback.go is the ledger side of the agent-driven plan review loop. The agent
// renders the plan, a reviewer marks up sections / risks / decisions in the
// HTML, and the marks land here under the plan's ledger dir as immutable review
// ROUNDS. The agent then records what it did about each item as RESOLUTIONS (an
// append log). Re-rendering shows each item's state inline, so the committed
// plan carries the whole review conversation + the agent's response.
//
// Why on-disk in the ledger (not a live socket as the source of truth): a
// machine runs many ox daemons, so a persistent localhost endpoint would be
// ambiguous about WHICH plan it targets. The `ox plan review` server is instead
// ephemeral and agent-owned (it spawns it, owns the port), and it just writes
// these same files. Rounds + resolutions are version-controlled with the plan —
// the human's words are immutable; the agent's responses append. License-clean,
// self-contained; nothing depends on a third-party review tool.

// feedbackSubdir is the per-plan folder holding review rounds + resolutions.
const feedbackSubdir = "feedback"
const resolutionsFile = "resolutions.json"

// FeedbackStatus is the reviewer's verdict on one anchored element.
type FeedbackStatus string

const (
	FeedbackApprove       FeedbackStatus = "approve"
	FeedbackRequestChange FeedbackStatus = "request-change"
	FeedbackFlag          FeedbackStatus = "flag"
	FeedbackComment       FeedbackStatus = "comment"
)

func validStatus(s FeedbackStatus) bool {
	switch s {
	case FeedbackApprove, FeedbackRequestChange, FeedbackFlag, FeedbackComment:
		return true
	}
	return false
}

// ResolutionState is the agent's disposition of a review item.
type ResolutionState string

const (
	ResolutionAddressed ResolutionState = "addressed" // agent made the change
	ResolutionWontfix   ResolutionState = "wontfix"   // agent declined, with reason
	ResolutionVerified  ResolutionState = "verified"  // human confirmed the fix
)

func validResolutionState(s ResolutionState) bool {
	switch s {
	case ResolutionAddressed, ResolutionWontfix, ResolutionVerified:
		return true
	}
	return false
}

// FeedbackItem is one anchored review mark. Anchor is a CONTENT hash of the
// element (section heading + element text), computed page-side, so it survives a
// re-render and only disappears when the agent rewrites that text — which is
// itself the signal the item was addressed. Anchor doubles as the item id used
// by `ox plan feedback resolve`.
type FeedbackItem struct {
	Anchor  string         `json:"anchor"`            // stable content-hash id, e.g. "h3f9a1c2"
	Section string         `json:"section,omitempty"` // section heading the element sits under
	Label   string         `json:"label"`             // short text of the element
	Status  FeedbackStatus `json:"status"`            // approve | request-change | flag | comment
	Note    string         `json:"note,omitempty"`    // the reviewer's comment
}

// FeedbackSet is one review round (one submit from the page).
type FeedbackSet struct {
	Slug      string         `json:"slug"`
	Reviewer  string         `json:"reviewer,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	Items     []FeedbackItem `json:"items"`
}

// Resolution is one agent disposition of a review item, append-logged.
type Resolution struct {
	Anchor string          `json:"anchor"`           // the item it resolves
	State  ResolutionState `json:"state"`            // addressed | wontfix | verified
	Commit string          `json:"commit,omitempty"` // commit SHA that made the change
	Note   string          `json:"note,omitempty"`   // what the agent did / why wontfix
	At     time.Time       `json:"at"`
}

// MergedItem is a review item joined with its latest resolution and a computed
// open/closed state. Open = no resolution, or the item was re-raised after the
// last resolution (CreatedAt newer than the resolution's At).
type MergedItem struct {
	FeedbackItem
	RaisedAt   time.Time
	Resolution *Resolution
	Open       bool
}

// validateSlug rejects a slug that is not a simple ledger name. Feedback JSON is
// the one externally-authored input that flows toward filesystem paths, so its
// slug must never contain a path separator or traversal — otherwise a crafted
// export could steer SaveFeedback/Load outside the plans tree.
func validateSlug(slug string) error {
	s := strings.TrimSpace(slug)
	if s == "" {
		return fmt.Errorf("empty slug")
	}
	if s != filepath.Base(s) || strings.ContainsAny(s, `/\`) || strings.Contains(s, "..") {
		return fmt.Errorf("invalid slug %q: must be a simple name, not a path", slug)
	}
	return nil
}

// ParseFeedback decodes and validates a review-round JSON (the page submit/export).
// Fail-loud on malformed input, an unknown status, or an unsafe slug.
func ParseFeedback(raw []byte) (FeedbackSet, error) {
	var set FeedbackSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return FeedbackSet{}, fmt.Errorf("parse feedback json: %w", err)
	}
	if set.Slug != "" {
		if err := validateSlug(set.Slug); err != nil {
			return FeedbackSet{}, err
		}
	}
	if len(set.Items) == 0 {
		return FeedbackSet{}, fmt.Errorf("feedback has no items")
	}
	for i := range set.Items {
		set.Items[i].Status = FeedbackStatus(strings.ToLower(strings.TrimSpace(string(set.Items[i].Status))))
		if set.Items[i].Status == "" {
			set.Items[i].Status = FeedbackComment
		}
		if !validStatus(set.Items[i].Status) {
			return FeedbackSet{}, fmt.Errorf("item %d: unknown status %q (want approve|request-change|flag|comment)", i, set.Items[i].Status)
		}
	}
	return set, nil
}

// SaveFeedback writes a review round under <planDir>/feedback/. now controls the
// timestamp (tests stay deterministic). Returns the written path.
func SaveFeedback(planDir string, set FeedbackSet, now time.Time) (string, error) {
	if set.CreatedAt.IsZero() {
		set.CreatedAt = now.UTC()
	}
	dir := filepath.Join(planDir, feedbackSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create feedback dir: %w", err)
	}
	name := "round-" + now.UTC().Format("20060102-150405.000000000") + ".json"
	path := filepath.Join(dir, name)
	b, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode feedback: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", fmt.Errorf("write feedback: %w", err)
	}
	return path, nil
}

// LoadAllFeedback reads every review round under a plan dir, oldest first. A
// missing feedback/ dir is not an error. resolutions.json is skipped (it is not
// a round).
func LoadAllFeedback(planDir string) ([]FeedbackSet, error) {
	dir := filepath.Join(planDir, feedbackSubdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read feedback dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == resolutionsFile || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names) // timestamped names sort chronologically
	var sets []FeedbackSet
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		var set FeedbackSet
		if err := json.Unmarshal(b, &set); err != nil {
			continue
		}
		sets = append(sets, set)
	}
	return sets, nil
}

// AppendResolution adds one agent disposition to the append log.
func AppendResolution(planDir string, r Resolution, now time.Time) error {
	if err := validateAnchor(r.Anchor); err != nil {
		return err
	}
	if !validResolutionState(r.State) {
		return fmt.Errorf("invalid resolution state %q (want addressed|wontfix|verified)", r.State)
	}
	if r.At.IsZero() {
		r.At = now.UTC()
	}
	existing, err := LoadResolutions(planDir)
	if err != nil {
		return err
	}
	existing = append(existing, r)
	dir := filepath.Join(planDir, feedbackSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create feedback dir: %w", err)
	}
	b, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("encode resolutions: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, resolutionsFile), b, 0o644)
}

// validateAnchor keeps anchors to the page-emitted shape (no separators) so a
// resolution can never be keyed to a path.
func validateAnchor(a string) error {
	a = strings.TrimSpace(a)
	if a == "" {
		return fmt.Errorf("empty anchor")
	}
	if strings.ContainsAny(a, `/\`) || strings.Contains(a, "..") {
		return fmt.Errorf("invalid anchor %q", a)
	}
	return nil
}

// LoadResolutions reads the append log (latest entries last). Missing is empty.
func LoadResolutions(planDir string) ([]Resolution, error) {
	b, err := os.ReadFile(filepath.Join(planDir, feedbackSubdir, resolutionsFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read resolutions: %w", err)
	}
	var rs []Resolution
	if err := json.Unmarshal(b, &rs); err != nil {
		return nil, fmt.Errorf("parse resolutions: %w", err)
	}
	return rs, nil
}

// AssembleReview joins every review item (latest mark per anchor across rounds)
// with its latest resolution, computing open/closed. This is the single source
// the digest and the render read. An item is OPEN when it has no resolution, or
// when it was re-raised after the latest resolution (supporting the verify loop).
func AssembleReview(planDir string) ([]MergedItem, error) {
	sets, err := LoadAllFeedback(planDir)
	if err != nil {
		return nil, err
	}
	resolutions, err := LoadResolutions(planDir)
	if err != nil {
		return nil, err
	}

	// latest mark per anchor (rounds are chronological)
	latestItem := map[string]FeedbackItem{}
	raisedAt := map[string]time.Time{}
	var order []string
	for _, set := range sets {
		for _, it := range set.Items {
			if _, seen := latestItem[it.Anchor]; !seen {
				order = append(order, it.Anchor)
			}
			latestItem[it.Anchor] = it
			raisedAt[it.Anchor] = set.CreatedAt
		}
	}

	// latest resolution per anchor
	latestRes := map[string]Resolution{}
	for _, r := range resolutions {
		if cur, ok := latestRes[r.Anchor]; !ok || r.At.After(cur.At) {
			latestRes[r.Anchor] = r
		}
	}

	out := make([]MergedItem, 0, len(order))
	for _, anchor := range order {
		mi := MergedItem{FeedbackItem: latestItem[anchor], RaisedAt: raisedAt[anchor], Open: true}
		if r, ok := latestRes[anchor]; ok {
			rc := r
			mi.Resolution = &rc
			// re-raised after resolution → open again
			mi.Open = mi.RaisedAt.After(r.At)
		}
		out = append(out, mi)
	}
	return out, nil
}

// FeedbackDigest renders a compact, agent-readable summary from the merged view:
// open/addressed/verified/wontfix counts, then every OPEN actionable item with
// its anchor (the id to resolve), section, label, and note. Returns "" when
// there is no feedback.
func FeedbackDigest(items []MergedItem) string {
	if len(items) == 0 {
		return ""
	}
	var open, addressed, verified, wontfix, approvals int
	var openItems []MergedItem
	for _, it := range items {
		if it.Status == FeedbackApprove && it.Open {
			approvals++
			continue
		}
		if it.Open {
			open++
			openItems = append(openItems, it)
			continue
		}
		switch it.Resolution.State {
		case ResolutionAddressed:
			addressed++
		case ResolutionVerified:
			verified++
		case ResolutionWontfix:
			wontfix++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Review: %d open · %d addressed · %d verified · %d wontfix · %d approvals\n",
		open, addressed, verified, wontfix, approvals)
	for _, it := range openItems {
		label := it.Label
		if label == "" {
			label = it.Anchor
		}
		fmt.Fprintf(&b, "  [%s] (%s) %s", it.Status, it.Anchor, label)
		if it.Section != "" {
			fmt.Fprintf(&b, " · §%s", it.Section)
		}
		if it.Note != "" {
			fmt.Fprintf(&b, " — %s", it.Note)
		}
		b.WriteByte('\n')
	}
	if open > 0 {
		b.WriteString("\nResolve each: ox plan feedback resolve <slug> <anchor> --state addressed --commit <sha> --note \"…\"\n")
	}
	return b.String()
}
