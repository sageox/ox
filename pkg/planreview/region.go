package planreview

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"unicode/utf16"
	"unicode/utf8"
)

// A drawn region is a reviewer's freehand ellipse around part of a plan,
// stored on its round item under "region" beside the item's legacy fields
// (anchor "q-<region id>", label "Drawing: …"), so readers that predate
// regions still list it. docs/specs/plan-review-drawn-context.md is its
// format; packages/plan-review/src/region.ts checks the same rules in the
// browser, and testdata/regions.json holds the cases both must agree on.
//
// A region is checked as JSON.parse reads it: exact keys, the last of a
// duplicated key. Fields it does not name are kept, as everywhere in a record.

// The limits on one region (spec §5). Text lengths are in UTF-16 code units
// and sizes in UTF-8 bytes of JSON.stringify's output, as the browser that
// captured it counts them (jsonLen).
const (
	maxRegionBytes  = 64 << 10 // the region
	maxContextBytes = 16 << 10 // its context
	maxPathPoints   = 512
	maxTargets      = 16
	maxContextItems = 48
	maxItemLabels   = 32
	maxTargetText   = 2000 // an enclosed element's text
	maxNeighborText = 600  // any other context item's text
	maxLabelText    = 80   // a diagram label's text
	maxLayoutKeys   = 128  // open <details> and checked inputs, each
)

var (
	elementAnchor = regexp.MustCompile(`^h[0-9a-f]{8}$`)
	planRevision  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	isoTime       = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$`)
)

// checkRegion reports why raw, one value of a decoded request, is not a
// well-formed region, and its id.
func checkRegion(raw json.RawMessage) (string, error) {
	var v any
	_ = json.Unmarshal(raw, &v) // decoded from the request: valid JSON
	if n := jsonLen(v); n > maxRegionBytes {
		return "", fmt.Errorf("region is %d bytes, over the %d-byte limit", n, maxRegionBytes)
	}
	r, ok := v.(map[string]any)
	if !ok {
		return "", errors.New("region is not an object")
	}
	id, _ := r["id"].(string)
	switch {
	case r["v"] != float64(1):
		return "", errors.New("region: v must be 1")
	case r["kind"] != "region":
		return "", errors.New(`region: kind must be "region"`)
	case r["frame"] != "document":
		return "", errors.New(`region: frame must be "document"`)
	case !idPattern.MatchString(id):
		return "", errors.New("region: id must be 8-64 chars of [A-Za-z0-9_-]")
	case !matches(planRevision, r["plan_revision"]):
		return "", errors.New(`region: plan_revision must be "sha256:" and 64 hex digits`)
	case !matches(isoTime, r["created_at"]):
		return "", errors.New("region: created_at must be a UTC time like 2026-10-10T20:00:00Z")
	}
	// checkContext reads the targets checkTargets has checked
	for _, check := range []func(map[string]any) error{checkStroke, checkLayout, checkTargets, checkContext} {
		if err := check(r); err != nil {
			return "", fmt.Errorf("region: %w", err)
		}
	}
	if n := jsonLen(r["context"]); n > maxContextBytes {
		return "", fmt.Errorf("region: context is %d bytes, over the %d-byte limit", n, maxContextBytes)
	}
	return id, nil
}

// jsonLen is the size of v, a decoded JSON value, as JavaScript's
// JSON.stringify writes it in UTF-8. A request's own bytes would not do: Go's
// encoder writes < > & as six bytes each, and a number can be spelled
// shorter (1e20) or longer than JavaScript writes it.
func jsonLen(v any) int {
	switch x := v.(type) {
	case bool:
		if x {
			return 4
		}
		return 5
	case float64:
		if x == 0 {
			return 1 // JavaScript writes -0 as 0
		}
		b, _ := json.Marshal(x) // a decoded number is finite; encoding/json writes it as ECMAScript does
		return len(b)
	case string:
		n := 2
		for _, r := range x {
			switch {
			case r == '"' || r == '\\' || r == '\b' || r == '\f' || r == '\n' || r == '\r' || r == '\t':
				n += 2
			case r < 0x20:
				n += 6
			default:
				n += utf8.RuneLen(r)
			}
		}
		return n
	case []any:
		n := 1 + max(len(x), 1) // brackets and commas
		for _, e := range x {
			n += jsonLen(e)
		}
		return n
	case map[string]any:
		n := 1 + max(len(x), 1)
		for k, e := range x {
			n += jsonLen(k) + 1 + jsonLen(e)
		}
		return n
	}
	return 4 // null
}

// checkStroke checks what is drawn: the stroke, the ellipse fitted to it and
// that ellipse's box.
func checkStroke(r map[string]any) error {
	path, ok := r["path"].([]any)
	if !ok || len(path) < 3 || len(path) > maxPathPoints {
		return fmt.Errorf("path must hold 3-%d points", maxPathPoints)
	}
	for _, p := range path {
		if !numbers(p, 2) {
			return errors.New("each path point must be [x, y]")
		}
	}
	e, ok := r["ellipse"].(map[string]any)
	if !ok || !numbers(e["center"], 2) || !numbers(e["radii"], 2) || !finite(e["rotation"]) || !finite(e["start"]) ||
		(e["dir"] != float64(1) && e["dir"] != float64(-1)) {
		return errors.New("ellipse must have center, radii, rotation, start and dir 1 or -1")
	}
	if radii := e["radii"].([]any); radii[0].(float64) <= 0 || radii[1].(float64) <= 0 {
		return errors.New("ellipse radii must be positive")
	}
	if !box(r["bbox"]) {
		return errors.New("bbox must be [x, y, width, height]")
	}
	return nil
}

// checkLayout checks the conditions the geometry was captured under.
func checkLayout(r map[string]any) error {
	l, ok := r["layout"].(map[string]any)
	if !ok || !numbers(l["viewport"], 2) || !numbers(l["document"], 2) || !numbers(l["scroll"], 2) ||
		!finite(l["dpr"]) || !finite(l["scale"]) || !isString(l["theme"]) || !isString(l["fonts"]) ||
		!stringList(l["open"], maxLayoutKeys) || !stringList(l["checked"], maxLayoutKeys) {
		return errors.New("layout must have viewport, document, scroll, dpr, scale, theme, fonts, open and checked")
	}
	return nil
}

// checkTargets checks the elements the ellipse encloses.
func checkTargets(r map[string]any) error {
	targets, ok := r["targets"].([]any)
	if !ok || len(targets) == 0 || len(targets) > maxTargets {
		return fmt.Errorf("targets must hold 1-%d elements", maxTargets)
	}
	for i, x := range targets {
		t, ok := x.(map[string]any)
		if !ok || !matches(elementAnchor, t["anchor"]) || !isString(t["tag"]) || !isString(t["section"]) || !box(t["box"]) {
			return fmt.Errorf("target %d must have an element anchor, tag, section and box", i)
		}
		if c, ok := t["cover"].(float64); !ok || c < 0 || c > 1 {
			return fmt.Errorf("target %d: cover must be 0-1", i)
		}
		nth, hasNth := t["nth"]
		of, hasOf := t["of"]
		if hasNth != hasOf || hasNth && !(count(nth) && count(of) && of.(float64) >= 2 && nth.(float64) < of.(float64)) {
			return fmt.Errorf("target %d: nth and of must name one of two or more occurrences", i)
		}
	}
	return nil
}

// checkContext checks what lay under and around the drawing: each item's
// anchor, box and text, within its limit, and its diagram labels.
func checkContext(r map[string]any) error {
	ctx, ok := r["context"].(map[string]any)
	if !ok {
		return errors.New("context must be an object")
	}
	items, ok := ctx["items"].([]any)
	if _, isBool := ctx["truncated"].(bool); !ok || len(items) > maxContextItems || !isBool {
		return fmt.Errorf("context must have at most %d items and truncated", maxContextItems)
	}
	var targets []any
	for _, t := range r["targets"].([]any) {
		targets = append(targets, t.(map[string]any)["anchor"])
	}
	for i, x := range items {
		it, ok := x.(map[string]any)
		if !ok || !matches(elementAnchor, it["anchor"]) || !isString(it["tag"]) || !box(it["box"]) || !isString(it["text"]) {
			return fmt.Errorf("context item %d must have an element anchor, tag, box and text", i)
		}
		limit := maxNeighborText
		if slices.Contains(targets, it["anchor"]) {
			limit = maxTargetText
		}
		if utf16Len(it["text"].(string)) > limit {
			return fmt.Errorf("context item %d: text over %d characters", i, limit)
		}
		labels, has := it["labels"]
		if !has {
			continue
		}
		list, ok := labels.([]any)
		if !ok || len(list) == 0 || len(list) > maxItemLabels {
			return fmt.Errorf("context item %d: labels must hold 1-%d labels", i, maxItemLabels)
		}
		for _, y := range list {
			l, ok := y.(map[string]any)
			if !ok || !box(l["box"]) || !isString(l["text"]) || utf16Len(l["text"].(string)) > maxLabelText {
				return fmt.Errorf("context item %d: each label must have a box and text of at most %d characters", i, maxLabelText)
			}
		}
	}
	return nil
}

func isString(v any) bool { _, ok := v.(string); return ok }

func matches(re *regexp.Regexp, v any) bool {
	s, ok := v.(string)
	return ok && re.MatchString(s)
}

func finite(v any) bool {
	f, ok := v.(float64)
	return ok && !math.IsInf(f, 0) && !math.IsNaN(f)
}

// count reports whether v is a whole number of zero or more.
func count(v any) bool {
	f, ok := v.(float64)
	return ok && f >= 0 && f == math.Trunc(f)
}

// numbers reports whether v is an array of exactly n finite numbers.
func numbers(v any, n int) bool {
	a, ok := v.([]any)
	return ok && len(a) == n && !slices.ContainsFunc(a, func(x any) bool { return !finite(x) })
}

// box reports whether v is [x, y, width, height] with no negative size.
func box(v any) bool {
	if !numbers(v, 4) {
		return false
	}
	b := v.([]any)
	return b[2].(float64) >= 0 && b[3].(float64) >= 0
}

// stringList reports whether v is an array of at most n strings.
func stringList(v any, n int) bool {
	a, ok := v.([]any)
	return ok && len(a) <= n && !slices.ContainsFunc(a, func(x any) bool { return !isString(x) })
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }
