package planreview

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// captures are testdata/drawn-context/captures.json's records, decoded as
// JSON.parse reads them, in file order.
func captures(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/drawn-context/captures.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Records []map[string]any }
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f.Records
}

// clone deep-copies a decoded JSON value.
func clone(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// step returns the container holding path's last step, and that step.
func step(root any, path []any) (any, any) {
	for _, p := range path[:len(path)-1] {
		switch c := root.(type) {
		case map[string]any:
			root = c[p.(string)]
		case []any:
			root = c[int(p.(float64))]
		}
	}
	return root, path[len(path)-1]
}

// TestRegion_SharedCases runs testdata/drawn-context/regions.json: every
// capture in captures.json is a well-formed region, each refused case is
// refused, and each limit holds at its bound and fails one past it.
// packages/plan-review runs the same file against the browser's check.
// Failure prevented: a host accepts a drawing the server then refuses, so the
// reviewer's draft can never be sent, or the server stores a drawing that
// hosts cannot draw.
func TestRegion_SharedCases(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/drawn-context/regions.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		Limits  map[string]int
		Refused []struct {
			Why, Record string
			Path        []any
			Value       any
			Delete      bool
		}
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	records := captures(t)
	byID := map[string]any{}
	for _, r := range records {
		byID[r["id"].(string)] = r
	}
	check := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		_, err = checkRegion(b)
		return err
	}
	for _, r := range records {
		if err := check(r); err != nil {
			t.Errorf("capture %s refused: %v", r["id"], err)
		}
	}
	for _, c := range cases.Refused {
		base := records[0]
		if c.Record != "" {
			base = byID[c.Record].(map[string]any)
		}
		v := clone(t, base)
		if len(c.Path) == 0 {
			v = c.Value
		} else if parent, last := step(v, c.Path); c.Delete {
			delete(parent.(map[string]any), last.(string))
		} else if m, ok := parent.(map[string]any); ok {
			m[last.(string)] = c.Value
		} else {
			parent.([]any)[int(last.(float64))] = c.Value
		}
		if check(v) == nil {
			t.Errorf("%s: accepted", c.Why)
		}
	}

	want := map[string]int{
		"region_bytes": maxRegionBytes, "context_bytes": maxContextBytes, "path_points": maxPathPoints, "targets": maxTargets,
		"context_items": maxContextItems, "item_labels": maxItemLabels, "target_text": maxTargetText,
		"neighbor_text": maxNeighborText, "label_text": maxLabelText, "layout_keys": maxLayoutKeys,
	}
	if !reflect.DeepEqual(cases.Limits, want) {
		t.Fatalf("regions.json limits %v, this package's %v", cases.Limits, want)
	}
	r1, r7 := "r1-double-fire", "r7-figure"
	// each sets n of something on a copy of a capture; at the limit it is
	// accepted, one past it refused
	bounds := []struct {
		limit  string
		record string
		set    func(r map[string]any, n int)
	}{
		{"path_points", r1, func(r map[string]any, n int) { r["path"] = repeat([]any{1.0, 2.0}, n) }},
		{"targets", r1, func(r map[string]any, n int) { r["targets"] = repeat(r["targets"].([]any)[0], n) }},
		{"context_items", r1, func(r map[string]any, n int) { ctx(r)["items"] = repeat(item(r, 0), n) }},
		{"item_labels", r7, func(r map[string]any, n int) { item(r, 0)["labels"] = repeat(item(r, 0)["labels"].([]any)[0], n) }},
		{"target_text", r1, func(r map[string]any, n int) { item(r, 0)["text"] = strings.Repeat("x", n) }},
		{"neighbor_text", r1, func(r map[string]any, n int) { item(r, 1)["text"] = strings.Repeat("x", n) }},
		{"label_text", r7, func(r map[string]any, n int) {
			item(r, 0)["labels"].([]any)[0].(map[string]any)["text"] = strings.Repeat("x", n)
		}},
		{"layout_keys", r1, func(r map[string]any, n int) {
			r["layout"].(map[string]any)["open"] = repeat("d1a2b3c4d", n)
		}},
		// text is counted in UTF-16 units: n/2 astral characters are n units
		{"target_text", r1, func(r map[string]any, n int) {
			item(r, 0)["text"] = strings.Repeat("😀", n/2) + strings.Repeat("x", n%2)
		}},
		{"context_bytes", r1, func(r map[string]any, n int) { pad(t, ctx(r), n) }},
		{"region_bytes", r1, func(r map[string]any, n int) { pad(t, r, n) }},
	}
	for _, b := range bounds {
		n := cases.Limits[b.limit]
		for _, over := range []bool{false, true} {
			r := clone(t, byID[b.record]).(map[string]any)
			if over {
				b.set(r, n+1)
			} else {
				b.set(r, n)
			}
			if err := check(r); over == (err == nil) {
				t.Errorf("%s of %d (over: %v): %v", b.limit, n, over, err)
			}
		}
	}
}

func ctx(r map[string]any) map[string]any { return r["context"].(map[string]any) }

func repeat(v any, n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func item(r map[string]any, i int) map[string]any { return ctx(r)["items"].([]any)[i].(map[string]any) }

// pad adds a field that makes m exactly n bytes of compact JSON.
func pad(t *testing.T, m map[string]any, n int) {
	t.Helper()
	m["pad"] = ""
	b, _ := json.Marshal(m)
	if len(b) > n {
		t.Fatalf("already %d bytes, past %d", len(b), n)
	}
	m["pad"] = strings.Repeat("x", n-len(b))
}

// TestJSONLen_IsWhatJSONStringifyWrites pins jsonLen to sizes Node's
// JSON.stringify gave for the same values. Failure prevented: the server
// measures a region's limits on other bytes than the browser that checked it,
// so an export the browser kept is refused, or one it refused is stored.
func TestJSONLen_IsWhatJSONStringifyWrites(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int{
		`{"a":"q\"uote\\back\b\f\n\r\t\u0001\u001f\u2028\u2029é😀<>&"}`:                58,
		`[1e21, 1e-7, 0.1, -1.5, 123456789.25, 1e20, 5e-324, 1.7976931348623157e308]`: 87,
		`{"nested":[[],{},[null,true,false],{"k":[0]}]}`:                              46,
		`-0`:             1,
		`"\ud83d\ude00"`: 6,
	} {
		var v any
		if err := json.Unmarshal([]byte(in), &v); err != nil {
			t.Fatal(err)
		}
		if got := jsonLen(v); got != want {
			t.Errorf("jsonLen(%s) = %d, JSON.stringify writes %d bytes", in, got, want)
		}
	}
	// a region's limits hold whatever escapes the request used: encoding/json
	// writes each < as six bytes, and a context of them is still in bounds
	r := clone(t, captures(t)[0]).(map[string]any)
	lt := map[string]any{"anchor": "h21a13288", "tag": "td", "box": []any{0.0, 0.0, 1.0, 1.0}, "text": strings.Repeat("<", 600)}
	ctx(r)["items"] = repeat(lt, 24)
	b, _ := json.Marshal(r) // escapes every <
	if _, err := checkRegion(b); err != nil || len(b) <= maxRegionBytes {
		t.Errorf("a region JSON.stringify writes within bounds was refused (%d bytes as escaped): %v", len(b), err)
	}
}
