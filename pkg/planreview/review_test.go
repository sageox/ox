package planreview

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

// fixture is one testdata/review-contract/<name>/expect.json: what assembling
// the fixture's plan/ directory yields. Items are projected to the fields
// every host must agree on; failures to path and reason.
type fixture struct {
	Summary string `json:"summary"`
	Limits  struct {
		Records      int   `json:"records"`
		RecordBytes  int64 `json:"record_bytes"`
		CaptureBytes int64 `json:"capture_bytes"`
	} `json:"limits"`
	Items     []fixtureItem `json:"items"`
	Failures  []Failure     `json:"failures"`
	Conflicts []Conflict    `json:"conflicts"`
}

type fixtureItem struct {
	Anchor       string `json:"anchor"`
	Reviewer     string `json:"reviewer"`
	Status       string `json:"status"`
	Label        string `json:"label"`
	Note         string `json:"note"`
	Open         bool   `json:"open"`
	Resolution   string `json:"resolution,omitempty"`
	RemappedFrom string `json:"remapped_from,omitempty"`
}

// Every transport must yield expect.json's items, failures and conflicts,
// regardless of listing order or whether plan metadata is present.
// Failure prevented: transport differences hide or reinterpret stored feedback.
func TestContractFixtures(t *testing.T) {
	t.Parallel()
	const root = "testdata/review-contract"
	dirs, err := os.ReadDir(root)
	if err != nil || len(dirs) == 0 {
		t.Fatalf("read fixtures: %v (%d found)", err, len(dirs))
	}
	for _, d := range dirs {
		t.Run(d.Name(), func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join(root, d.Name(), "expect.json"))
			if err != nil {
				t.Fatal(err)
			}
			var want fixture
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&want); err != nil {
				t.Fatalf("decode expect.json: %v", err)
			}
			limits := Limits{Records: want.Limits.Records, RecordBytes: want.Limits.RecordBytes, CaptureBytes: want.Limits.CaptureBytes}
			plan := filepath.Join(root, d.Name(), "plan")

			plain := readAll(t, plan)
			reordered := Inventory{Files: slices.Clone(plain.Files)}
			slices.Reverse(reordered.Files)
			retained := t.TempDir()
			for _, f := range plain.Files {
				write(t, filepath.Join(retained, filepath.FromSlash(f.Path)), string(f.Data))
			}
			captured := Capture(plan, "", limits)
			for _, f := range captured.Files {
				disk, err := os.ReadFile(filepath.Join(plan, filepath.FromSlash(f.Path)))
				if err != nil || !bytes.Equal(disk, f.Data) {
					t.Errorf("Capture: %s is not byte-exact (%v)", f.Path, err)
				}
			}
			for name, inv := range map[string]Inventory{
				"Capture": captured, "plain read": plain, "reordered": reordered,
				"retained feedback only": Capture(retained, "", limits),
			} {
				rev := Assemble(inv, limits)
				var items []fixtureItem
				for _, it := range rev.Items {
					g := fixtureItem{Anchor: it.Anchor, Reviewer: it.Reviewer, Status: string(it.Status), Label: it.Label, Note: it.Note, Open: it.Open, RemappedFrom: it.RemappedFrom}
					if it.Resolution != nil {
						g.Resolution = string(it.Resolution.State)
					}
					items = append(items, g)
				}
				if !slices.Equal(items, want.Items) {
					t.Errorf("%s: items:\n got %+v\nwant %+v", name, items, want.Items)
				}
				if got := pathsAndReasons(rev.Failures); !slices.Equal(got, pathsAndReasons(want.Failures)) {
					t.Errorf("%s: failures = %v, want %v", name, got, pathsAndReasons(want.Failures))
				}
				if !slices.EqualFunc(rev.Conflicts, want.Conflicts, func(a, b Conflict) bool {
					return a.Kind == b.Kind && a.ID == b.ID && slices.Equal(a.Paths, b.Paths)
				}) {
					t.Errorf("%s: conflicts = %+v, want %+v", name, rev.Conflicts, want.Conflicts)
				}
				if rev.Complete() != (len(want.Failures) == 0) {
					t.Errorf("%s: Complete() = %v with failures %v", name, rev.Complete(), rev.Failures)
				}
			}

			// Retained records still assemble when a fetch fails, but the
			// review must remain incomplete.
			if len(plain.Files) == 0 {
				t.Fatal("fixture has no retained review files")
			}
			missing := plain.Files[len(plain.Files)-1]
			partial := Inventory{Files: plain.Files[:len(plain.Files)-1]}
			withoutFailure := Assemble(partial, limits)
			failure := Failure{Path: missing.Path, Reason: ReasonUnreadable, Detail: "transport failed to fetch retained record"}
			partial.Failures = []Failure{failure}
			rev := Assemble(partial, limits)
			if rev.Complete() || !slices.Contains(rev.Failures, failure) {
				t.Errorf("incomplete transport lost its failure: %+v", rev)
			}
			if !reflect.DeepEqual(rev.Items, withoutFailure.Items) ||
				!reflect.DeepEqual(rev.Rounds, withoutFailure.Rounds) ||
				!reflect.DeepEqual(rev.Conflicts, withoutFailure.Conflicts) {
				t.Error("incomplete transport discarded or reinterpreted retained records")
			}
		})
	}
}

// readAll is the simplest transport: every regular file under feedback/.
func readAll(t *testing.T, plan string) Inventory {
	t.Helper()
	var inv Inventory
	err := filepath.WalkDir(filepath.Join(plan, "feedback"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(plan, p)
		inv.Files = append(inv.Files, File{Path: filepath.ToSlash(rel), Data: data})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func pathsAndReasons(fs []Failure) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Path+" "+f.Reason)
	}
	slices.Sort(out)
	return out
}

// TestClassify_OnlyKnownNamesAreRecords pins the names the fixtures do not
// carry. Failure prevented: a writer's temporary is read as a record, an
// unknown file passes unreported, or a name merely shaped like an immutable
// one is checked against a digest and reported altered.
func TestClassify_OnlyKnownNamesAreRecords(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		path   string
		kind   kind
		digest string
	}{
		{"feedback/resolutions/20261010-020000.000000000-acc-1.0123456789abcdef.json", resolutionFile, ""}, // id too short: not an immutable name
		{"feedback/.round-x.json.1a2b.tmp", notReview, ""},
		{"feedback/.git/round-x.json", notReview, ""},
		{"plan.md", notReview, ""},
		{"feedback/resolutions/old.txt", unrecognized, ""},
		{"feedback/attachments/round-x.json", unrecognized, ""},
	} {
		if k, d := classify(c.path); k != c.kind || d != c.digest {
			t.Errorf("classify(%q) = %v %q, want %v %q", c.path, k, d, c.kind, c.digest)
		}
	}
}

func TestAssemble_LimitsCutOffInPathOrder(t *testing.T) {
	t.Parallel()
	files := []File{
		{Path: "feedback/round-3.json", Data: []byte(`{}`)},
		{Path: "feedback/round-1.json", Data: []byte(`{}`)},
		{Path: "feedback/round-2.json", Data: []byte(`{"items":[]}`)},
	}
	for _, c := range []struct {
		limits Limits
		want   []string
		rounds int
	}{
		{Limits{Records: 2}, []string{" too_many_records"}, 2},
		{Limits{CaptureBytes: 13}, []string{" capture_too_large"}, 1},
	} {
		rev := Assemble(Inventory{Files: files}, c.limits)
		if got := pathsAndReasons(rev.Failures); !slices.Equal(got, c.want) || len(rev.Rounds) != c.rounds {
			t.Errorf("limits %+v: failures %v and %d rounds, want %v and %d", c.limits, got, len(rev.Rounds), c.want, c.rounds)
		}
	}
}

// TestContentRoundID_MatchesCLI pins the id to the one `ox plan feedback
// apply` mints (internal/plan.ContentRoundID). Failure prevented: an export
// applied by the CLI and by another host lands as two operations.
func TestContentRoundID_MatchesCLI(t *testing.T) {
	t.Parallel()
	r := Round{Reviewer: "Ana", Items: []Item{{Anchor: "h1", Section: "Risks", Label: "The first risk", Status: StatusRequestChange, Note: "Name the owner."}}}
	if got, want := ContentRoundID(r), "ca1d14a93b529a8f4869de2469605eed1"; got != want {
		t.Errorf("ContentRoundID = %s, want %s", got, want)
	}
}

func at(hour int) time.Time { return time.Date(2026, 10, 1, hour, 0, 0, 0, time.UTC) }
