package planreview

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestRoundRecord_KeepsTheRequestAndStampsTheHost pins materialization: every
// field the client froze survives into the record, unknown ones included; the
// host's fields replace the client's; and the name carries the id and the
// digest of the record's bytes. Failure prevented: a future field is dropped
// on write, or a request names its own reviewer or acceptance time.
func TestRoundRecord_KeepsTheRequestAndStampsTheHost(t *testing.T) {
	t.Parallel()
	frozen := []byte(`{"id":"r-0001-abcd","slug":"spoofed","reviewer":"Mallory","created_at":"2020-01-01T00:00:00Z","x_capture":{"rev":2},` +
		`"items":[{"anchor":"h1","label":"One","status":" Flag ","reviewer":"Mallory","x_region":"M0 0"},{"anchor":"h2","label":"Two"}]}`)
	q, err := ParseRoundRequest(frozen)
	if err != nil {
		t.Fatal(err)
	}
	f, err := q.Record("plan-a", "Ana", at(1))
	if err != nil {
		t.Fatal(err)
	}
	if want := "feedback/round-20261001-010000.000000000-r-0001-abcd." + digest(f.Data) + ".json"; f.Path != want {
		t.Errorf("path = %s, want %s", f.Path, want)
	}
	var got map[string]any
	if err := json.Unmarshal(f.Data, &got); err != nil {
		t.Fatal(err)
	}
	items := got["items"].([]any)
	first, second := items[0].(map[string]any), items[1].(map[string]any)
	for field, want := range map[string]any{
		"slug": "plan-a", "reviewer": "Ana", "created_at": "2026-10-01T01:00:00Z", "request_sha256": sha256Hex(frozen),
		"x_capture.rev": float64(2), "0.status": "flag", "0.reviewer": "Ana", "0.x_region": "M0 0", "1.status": "comment", "1.reviewer": "Ana",
	} {
		var v any
		switch {
		case field == "x_capture.rev":
			capture, _ := got["x_capture"].(map[string]any)
			v = capture["rev"]
		case strings.HasPrefix(field, "0."):
			v = first[strings.TrimPrefix(field, "0.")]
		case strings.HasPrefix(field, "1."):
			v = second[strings.TrimPrefix(field, "1.")]
		default:
			v = got[field]
		}
		if v != want {
			t.Errorf("%s = %v, want %v", field, v, want)
		}
	}
	again, _ := q.Record("plan-a", "Ana", at(1))
	if again.Path != f.Path || string(again.Data) != string(f.Data) {
		t.Error("the same request and materialization must yield the same record")
	}
	// an anonymous caller: the request's own names go too, never kept as the reviewer's
	anon, err := q.Record("plan-a", "", at(1))
	if err != nil || strings.Contains(string(anon.Data), "Mallory") || strings.Contains(string(anon.Data), `"reviewer"`) {
		t.Errorf("anonymous record kept a reviewer: %s (%v)", anon.Data, err)
	}
}

func TestParseRequests_RefuseMalformedInput(t *testing.T) {
	t.Parallel()
	for _, c := range []string{
		`not json`,
		`{"items":[]}`,
		`{"id":"short","items":[{"anchor":"h1","status":"flag"}]}`,
		`{"slug":"../x","items":[{"anchor":"h1","status":"flag"}]}`,
		`{"items":[{"anchor":"h1","status":"approveee"}]}`,
		`{"items":[null]}`,
		`{"items":[{"note":"a mark on nothing"}]}`,
		`{"items":[{"anchor":"  ","status":"flag"}]}`,
		// keys encoding/json matches to a known field (Unicode folding: U+017F
		// is a long s): validation and the record would see different fields
		`{"id":"r-0001-abcd","items":[{"anchor":"h1"},{"anchor":"h2"}],"Items":[{"anchor":"h3"}]}`,
		`{"id":"r-0001-abcd","items":[{"anchor":"h1","status":"flag","\u017ftatus":"approve"}]}`,
		// a duplicate key: the last value is the one stored, so it is the one validated
		`{"id":"r-0001-abcd","items":[{"anchor":"h1","anchor":null,"status":"flag"}]}`,
	} {
		if _, err := ParseRoundRequest([]byte(c)); err == nil {
			t.Errorf("ParseRoundRequest(%s) accepted", c)
		}
	}
	for _, c := range []string{
		`[]`,
		`{"anchor":"../x","state":"addressed"}`,
		`{"anchor":"h1","state":"done"}`,
		`{"id":"bad/id-0001","anchor":"h1","state":"addressed"}`,
		`{"id":"accept-0001","anchor":"h1","state":"addressed","State":"wontfix"}`,
		`{"id":"accept-0001","anchor":"h1","anchor":null,"state":"verified"}`,
	} {
		if _, err := ParseResolutionRequest([]byte(c)); err == nil {
			t.Errorf("ParseResolutionRequest(%s) accepted", c)
		}
	}
	q, err := ParseRoundRequest([]byte(`{"items":[{"anchor":"h1"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Record("p", "Ana", at(1)); err == nil {
		t.Error("a request without an id became a record")
	}
	q.ID = ContentRoundID(q.Round)
	extra := *q
	extra.Items = append(slices.Clone(q.Items), Item{Anchor: "h2"})
	if _, err := extra.Record("p", "Ana", at(1)); err == nil {
		t.Error("items added after parsing became a record")
	}
	for _, slug := range []string{"a/b", " "} {
		if _, err := q.Record(slug, "Ana", at(1)); err == nil {
			t.Errorf("slug %q became a record", slug)
		}
	}
	if r, err := ParseResolutionRequest([]byte(`{"anchor":"h1","state":"addressed"}`)); err != nil {
		t.Fatal(err)
	} else if _, err := r.Record("Ana", at(1)); err == nil {
		t.Error("a resolution request without an id became a record")
	}
	// an unknown field is carried, whatever it is named
	if r, err := ParseResolutionRequest([]byte(`{"id":"accept-0001","anchor":"h1","state":"verified","items":5}`)); err != nil {
		t.Errorf("unknown field refused: %v", err)
	} else if f, err := r.Record("Ana", at(1)); err != nil || !strings.Contains(string(f.Data), `"items": 5`) {
		t.Errorf("unknown field not carried into the record: %s (%v)", f.Data, err)
	}
	big := `{"id":"r-big-00001","items":[{"anchor":"h1","note":"` + strings.Repeat("x", MaxRecordBytes) + `"}]}`
	if q, err := ParseRoundRequest([]byte(big)); err != nil {
		t.Fatal(err)
	} else if _, err := q.Record("p", "Ana", at(1)); err == nil {
		t.Error("a record over MaxRecordBytes was built")
	}
}

// TestPrior_AnswersARetryFromWhatIsStored pins the writer's rules for a
// restarted request: the operation is looked up by id before anything is
// written; the same payload from the same reviewer is already published, and
// anything else is a conflict, so the client keeps its draft. Failure
// prevented: a retry under another account is told someone else's record is
// its saved review, or a changed payload is answered "duplicate" and lost.
func TestPrior_AnswersARetryFromWhatIsStored(t *testing.T) {
	t.Parallel()
	frozen := []byte(`{"id":"r-0001-abcd","items":[{"anchor":"h1","status":"flag","note":"Why?"}]}`)
	q, _ := ParseRoundRequest(frozen)
	stored, _ := q.Record("p", "Ana", at(1))
	inv := Inventory{Files: []File{stored}}

	retry, _ := q.Record("p", "Ana", at(3))
	otherReviewer, _ := q.Record("p", "Ben", at(3))
	q2, _ := ParseRoundRequest([]byte(strings.Replace(string(frozen), "Why?", "Why not?", 1)))
	otherPayload, _ := q2.Record("p", "Ana", at(3))
	q3, _ := ParseRoundRequest([]byte(`{"id":"r-0002-abcd","items":[{"anchor":"h1"}]}`))
	otherOp, _ := q3.Record("p", "Ana", at(3))
	// a copy whose marks were edited but whose request digest and reviewer
	// were copied from the original
	forged := File{Path: stored.Path, Data: []byte(strings.Replace(string(stored.Data), "Why?", "LGTM", 1))}
	// a copy an older writer stored: no request digest, the same marks
	old := File{Path: "feedback/round-20261001-010000.000000000-r-0001-abcd.json", Data: []byte(
		`{"id":"r-0001-abcd","slug":"p","reviewer":"Ana","created_at":"2026-10-01T01:00:00Z","items":[{"anchor":"h1","label":"","status":"flag","note":"Why?","reviewer":"Ana"}]}`)}

	for _, c := range []struct {
		name  string
		inv   Inventory
		rec   File
		state string
		found bool
	}{
		{"retry accepted later", inv, retry, ReceiptAlreadyPublished, true},
		{"same bytes, another reviewer", inv, otherReviewer, ReceiptConflict, true},
		{"same id, another payload", inv, otherPayload, ReceiptConflict, true},
		{"another operation", inv, otherOp, "", false},
		{"older writer's copy", Inventory{Files: []File{old}}, retry, ReceiptAlreadyPublished, true},
		{"edited copy under the same digest", Inventory{Files: []File{forged}}, retry, ReceiptConflict, true},
	} {
		r, found := Prior(c.inv, c.rec)
		if found != c.found || r.State != c.state {
			t.Errorf("%s: Prior = %+v %v, want %q %v", c.name, r, found, c.state, c.found)
		}
		if found && r.Path != c.inv.Files[0].Path {
			t.Errorf("%s: receipt path %s, want the stored copy", c.name, r.Path)
		}
	}

	res, _ := ParseResolutionRequest([]byte(`{"id":"accept-0001","anchor":"h1","state":"verified"}`))
	accepted, _ := res.Record("Ana", at(2))
	again, _ := res.Record("Ana", at(5))
	if r, found := Prior(Inventory{Files: []File{stored, accepted}}, again); !found || r.State != ReceiptAlreadyPublished || r.Path != accepted.Path {
		t.Errorf("resolution retry: Prior = %+v %v, want already_published at %s", r, found, accepted.Path)
	}
	if _, found := Prior(inv, File{Path: "feedback/remaps.json"}); found {
		t.Error("Prior looked up a file that is no operation's record")
	}
}

func TestRemapRecord(t *testing.T) {
	t.Parallel()
	m := Remap{From: "hold", To: "hnew", Method: "label-exact", Score: 1, At: at(1)}
	f, err := RemapRecord(m)
	if err != nil {
		t.Fatal(err)
	}
	if k, want := classify(f.Path); k != remapFile || want != digest(f.Data) {
		t.Errorf("RemapRecord path %s is not an immutable remap name", f.Path)
	}
	if _, err := RemapRecord(Remap{From: "hold", To: "hnew"}); err == nil {
		t.Error("a remap without a time became a record")
	}
	if _, err := RemapRecord(Remap{From: "hold", To: "hnew", At: at(1), Label: strings.Repeat("x", MaxRecordBytes)}); err == nil {
		t.Error("a remap over MaxRecordBytes became a record that readers refuse")
	}
}

// drawingItem is a round item for a drawing of rec, a captures.json record:
// anchored on its id, labeled as hosts label drawings.
func drawingItem(t *testing.T, rec map[string]any, status, note string) string {
	t.Helper()
	region, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return `{"anchor":"q-` + rec["id"].(string) + `","section":"Risks","label":"Drawing: The retry path can double-fire under load when the queue is …",` +
		`"status":"` + status + `","note":"` + note + `","region":` + string(region) + `}`
}

// TestRoundRequest_DrawingsTravelUnderMarks pins the request a round with
// drawings sends (drawn-context spec §7): its items under marks, items empty,
// a shape releases before drawings refuse; each drawing a well-formed region
// on its own q-<id> anchor. The record stores the items under items, every
// region the same JSON value it was sent as. Failure prevented: an older
// importer stores a review without its drawings, a malformed or misattributed
// drawing reaches the Ledger, or the writer changes what a reviewer drew.
func TestRoundRequest_DrawingsTravelUnderMarks(t *testing.T) {
	t.Parallel()
	recs := captures(t)
	// text a writer escapes: the stored region is the same value, not the same bytes
	item(recs[0], 1)["text"] = "Risk <Severity> & Mitigation"
	element := `{"anchor":"h59e210da","section":"Risks","label":"The retry path can double-fire under load when the queue is saturated.","status":"request-change","note":"Bound the retries."}`
	one, two := drawingItem(t, recs[0], "request-change", "Both retries fire here."), drawingItem(t, recs[1], "flag", "And again on requeue.")
	frozen := []byte(`{"id":"r-0008-regions","items":[],"marks":[` + element + `,` + one + `,` + two + `]}`)
	q, err := ParseRoundRequest(frozen)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := q.Record("rc-region-items", "Devon", at(1))
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Items         []map[string]any `json:"items"`
		Marks         any              `json:"marks"`
		RequestSHA256 string           `json:"request_sha256"`
	}
	if err := json.Unmarshal(rec.Data, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Items) != 3 || stored.Marks != nil || stored.RequestSHA256 != sha256Hex(frozen) {
		t.Fatalf("record: %d items, marks kept %v, digest %s; want the 3 marks stored as items, digest over the frozen bytes",
			len(stored.Items), stored.Marks != nil, stored.RequestSHA256)
	}
	for i, want := range []map[string]any{recs[0], recs[1]} {
		if got := stored.Items[i+1]["region"]; !reflect.DeepEqual(got, clone(t, want)) {
			t.Errorf("drawing %d stored as %v, sent as %v", i, got, want)
		}
	}
	// encoding/json writes < > & as < > &, as the spec says
	if escaped, _ := json.Marshal("Risk <Severity> & Mitigation"); !strings.Contains(string(rec.Data), string(escaped)) {
		t.Errorf("the record does not write the text as %s", escaped)
	}
	rev := Assemble(Inventory{Files: []File{rec}}, Limits{})
	if len(rev.Items) != 3 || !rev.Complete() {
		t.Fatalf("assembled %d items, failures %v", len(rev.Items), rev.Failures)
	}
	for i, id := range []string{"", "r1-double-fire", "r2-double-fire-again"} {
		var region struct{ ID string }
		if len(rev.Items[i].Region) > 0 {
			_ = json.Unmarshal(rev.Items[i].Region, &region)
		}
		if region.ID != id {
			t.Errorf("item %s assembled with region %q, want %q", rev.Items[i].Anchor, region.ID, id)
		}
	}

	misnamed := strings.Replace(one, `"anchor":"q-r1-double-fire"`, `"anchor":"q-r2-double-fire-again"`, 1)
	onElement := strings.Replace(one, `"anchor":"q-r1-double-fire"`, `"anchor":"h59e210da"`, 1)
	for name, body := range map[string]string{
		"a drawing under items":          `{"id":"r-0009-regions","items":[` + one + `]}`,
		"items under both keys":          `{"id":"r-0009-regions","items":[` + element + `],"marks":[` + one + `]}`,
		"a drawing on another's anchor":  `{"id":"r-0009-regions","items":[],"marks":[` + misnamed + `]}`,
		"a drawing on an element anchor": `{"id":"r-0009-regions","items":[],"marks":[` + onElement + `]}`,
		"a malformed region":             `{"id":"r-0009-regions","items":[],"marks":[` + strings.Replace(one, `"v":1`, `"v":2`, 1) + `]}`,
		"a null region":                  `{"id":"r-0009-regions","items":[],"marks":[{"anchor":"q-r1-double-fire","status":"flag","region":null}]}`,
		"a region keyed apart by case":   `{"id":"r-0009-regions","items":[],"marks":[` + strings.Replace(one, `"region":`, `"Region":`, 1) + `]}`,
		"marks that are not a list":      `{"id":"r-0009-regions","items":[],"marks":{"0":` + one + `}}`,
		"items that are not a list":      `{"id":"r-0009-regions","items":{"0":` + element + `}}`,
		"empty items and marks":          `{"id":"r-0009-regions","items":[],"marks":[]}`,
	} {
		if _, err := ParseRoundRequest([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestAssemble_DrawingsStayIndependent pins that two drawings around one
// paragraph are two review items through resolve and reopen: a resolution
// closes only the drawing it names, a reopen raises that drawing again with
// what was drawn, and the element mark on the same paragraph is untouched.
// Failure prevented: resolving one drawing closes another drawing of the same
// text, or a reopened drawing loses its region.
func TestAssemble_DrawingsStayIndependent(t *testing.T) {
	t.Parallel()
	recs := captures(t)
	round := func(id string, hour int, items ...string) File {
		t.Helper()
		q, err := ParseRoundRequest([]byte(`{"id":"` + id + `","items":[],"marks":[` + strings.Join(items, ",") + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		f, err := q.Record("p", "Devon", at(hour))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	resolve := func(id, anchor string, hour int) File {
		t.Helper()
		q, err := ParseResolutionRequest([]byte(`{"id":"` + id + `","anchor":"` + anchor + `","state":"addressed"}`))
		if err != nil {
			t.Fatal(err)
		}
		f, err := q.Record("Author", at(hour))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	element := `{"anchor":"h59e210da","label":"The retry path","status":"request-change"}`
	files := []File{
		round("r-0001-draws", 1, element, drawingItem(t, recs[0], "request-change", "Both fire."), drawingItem(t, recs[1], "flag", "Again.")),
		resolve("res-0001-r1", "q-r1-double-fire", 2),
		round("r-0002-reopen", 3, drawingItem(t, recs[0], "request-change", "Still fires twice.")),
		resolve("res-0002-r2", "q-r2-double-fire-again", 4),
	}
	rev := Assemble(Inventory{Files: files}, Limits{})
	type got struct {
		anchor, note, region string
		open                 bool
	}
	var items []got
	for _, it := range rev.Items {
		var region struct{ ID string }
		if len(it.Region) > 0 {
			_ = json.Unmarshal(it.Region, &region)
		}
		items = append(items, got{it.Anchor, it.Note, region.ID, it.Open})
	}
	want := []got{
		{"h59e210da", "", "", true},
		{"q-r1-double-fire", "Still fires twice.", "r1-double-fire", true},
		{"q-r2-double-fire-again", "Again.", "r2-double-fire-again", false},
	}
	if !reflect.DeepEqual(items, want) || !rev.Complete() {
		t.Errorf("assembled %+v (failures %v), want %+v", items, rev.Failures, want)
	}
}
