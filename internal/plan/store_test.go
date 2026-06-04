package plan

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
)

// withLedger points the package ledger resolver at a temp ledger for the
// duration of a test, restoring the production resolver afterward.
func withLedger(t *testing.T, ledger string) {
	t.Helper()
	prev := ledgerResolver
	ledgerResolver = func(string) string { return ledger }
	t.Cleanup(func() { ledgerResolver = prev })
}

func sampleResult() Result {
	return Result{
		Annotations: []Annotation{
			{Section: "Storage", Kind: BadgeDeterministic, Type: BadgeCollision, Why: "touches open PR #42", Files: []string{"store.go"}},
		},
		Context: []ContextItem{
			{Kind: "adr", Title: "ADR-016", Ref: "docs/adr/016.md", Score: 0.9},
		},
		Signals: SignalSummary{Collisions: 1, PriorArt: 0, ExpertRoutes: 1, Material: true},
	}
}

func TestSaveListLoad_RoundTrip(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	created := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	in := Input{Path: "PLAN.md", Raw: "# Plan capture to ledger\n\nDo the thing."}
	res := sampleResult()
	meta := Meta{
		Topic:     "Plan capture to ledger",
		Authors:   []string{"Person A"},
		CreatedAt: created,
	}

	dir, err := Save("/fake/git/root", in, res, nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// dir is the dated-slug folder under data/plans
	wantDir := filepath.Join(ledger, "data", "plans", "2026-06-03-plan-capture-to-ledger")
	if dir != wantDir {
		t.Fatalf("Save dir = %q, want %q", dir, wantDir)
	}

	// plan.md, annotations.json, meta.json are PLAIN git (not LFS pointers).
	for _, f := range []string{"plan.md", "annotations.json", "meta.json"} {
		p := filepath.Join(dir, f)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s to exist: %v", f, err)
		}
		if lfs.IsPointerFile(p) {
			t.Errorf("%s must be plain git, got an LFS pointer", f)
		}
	}

	// no plan.html when html == nil
	if _, err := os.Stat(filepath.Join(dir, "plan.html")); !os.IsNotExist(err) {
		t.Errorf("plan.html should not exist when html is nil, stat err=%v", err)
	}

	// List sees exactly one plan with the expected fields.
	plans, err := List("/fake/git/root")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("List returned %d plans, want 1", len(plans))
	}
	got := plans[0]
	if got.Slug != "plan-capture-to-ledger" {
		t.Errorf("slug = %q, want plan-capture-to-ledger", got.Slug)
	}
	if got.Topic != "Plan capture to ledger" {
		t.Errorf("topic = %q", got.Topic)
	}
	if got.HasHTML {
		t.Errorf("HasHTML = true, want false")
	}
	if !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, created)
	}

	// Load round-trips the markdown and the Result.
	planMD, loadedRes, info, err := Load("/fake/git/root", "plan-capture-to-ledger")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if planMD != in.Raw {
		t.Errorf("Load markdown = %q, want %q", planMD, in.Raw)
	}
	if loadedRes.Signals != res.Signals {
		t.Errorf("Load signals = %+v, want %+v", loadedRes.Signals, res.Signals)
	}
	if len(loadedRes.Annotations) != 1 || loadedRes.Annotations[0].Type != BadgeCollision {
		t.Errorf("Load annotations = %+v", loadedRes.Annotations)
	}
	if info.Slug != "plan-capture-to-ledger" {
		t.Errorf("Load info slug = %q", info.Slug)
	}

	// SchemaVersion is stamped on both artifacts on write so a future reader
	// can detect/migrate an older layout. Without this, version-bumping the
	// on-disk shape later would be a silent guess.
	if loadedRes.SchemaVersion != SchemaVersion {
		t.Errorf("annotations.json schema_version = %q, want %q", loadedRes.SchemaVersion, SchemaVersion)
	}
	metaBytes, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gotMeta Meta
	if err := json.Unmarshal(metaBytes, &gotMeta); err != nil {
		t.Fatal(err)
	}
	if gotMeta.SchemaVersion != SchemaVersion {
		t.Errorf("meta.json schema_version = %q, want %q", gotMeta.SchemaVersion, SchemaVersion)
	}

	// The dropped approval fields must not reappear in the serialized meta.
	// Their presence would imply an approval flow that does not exist.
	if strings.Contains(string(metaBytes), "approved") || strings.Contains(string(metaBytes), "reviewed_by") {
		t.Errorf("meta.json must not carry approval fields: %s", metaBytes)
	}
}

func TestSave_HTMLSizeGating(t *testing.T) {
	tests := []struct {
		name        string
		size        int
		wantPointer bool
	}{
		{"small html stays plain git", 1024, false},
		{"large html becomes LFS pointer", htmlLFSThreshold + 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ledger := t.TempDir()
			withLedger(t, ledger)

			html := bytes.Repeat([]byte("a"), tc.size)
			in := Input{Raw: "# H\n"}
			meta := Meta{Topic: "Size gate", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

			dir, err := Save("/g", in, sampleResult(), html, meta)
			if err != nil {
				t.Fatalf("Save: %v", err)
			}

			htmlPath := filepath.Join(dir, "plan.html")
			isPointer := lfs.IsPointerFile(htmlPath)
			if isPointer != tc.wantPointer {
				t.Fatalf("plan.html IsPointer = %v, want %v (size=%d)", isPointer, tc.wantPointer, tc.size)
			}

			if tc.wantPointer {
				// pointer references the correct OID/size
				ref, err := lfs.ReadPointerFile(htmlPath)
				if err != nil {
					t.Fatalf("ReadPointerFile: %v", err)
				}
				if ref.Size != int64(len(html)) {
					t.Errorf("pointer size = %d, want %d", ref.Size, len(html))
				}
				if ref.OID != "sha256:"+lfs.ComputeOID(html) {
					t.Errorf("pointer OID = %q, want sha256:%s", ref.OID, lfs.ComputeOID(html))
				}
			} else {
				// plain bytes survive verbatim
				got, err := os.ReadFile(htmlPath)
				if err != nil {
					t.Fatalf("read plain html: %v", err)
				}
				if !bytes.Equal(got, html) {
					t.Errorf("plain html bytes differ from input")
				}
			}

			// List reflects HasHTML in both cases.
			plans, err := List("/g")
			if err != nil || len(plans) != 1 {
				t.Fatalf("List: %v (n=%d)", err, len(plans))
			}
			if !plans[0].HasHTML {
				t.Errorf("HasHTML = false, want true")
			}
		})
	}
}

func TestList_FailOpen(t *testing.T) {
	t.Run("no ledger configured", func(t *testing.T) {
		withLedger(t, "")
		plans, err := List("/g")
		if err != nil {
			t.Fatalf("List err = %v, want nil (fail-open)", err)
		}
		if plans != nil {
			t.Errorf("List = %v, want nil", plans)
		}
	})

	t.Run("ledger exists but no plans dir", func(t *testing.T) {
		withLedger(t, t.TempDir())
		plans, err := List("/g")
		if err != nil {
			t.Fatalf("List err = %v, want nil", err)
		}
		if len(plans) != 0 {
			t.Errorf("List = %v, want empty", plans)
		}
	})

	t.Run("skips malformed plan dir", func(t *testing.T) {
		ledger := t.TempDir()
		withLedger(t, ledger)
		// a dir with no meta.json must be skipped, not error.
		bad := filepath.Join(ledger, "data", "plans", "2026-01-01-broken")
		if err := os.MkdirAll(bad, 0o755); err != nil {
			t.Fatal(err)
		}
		plans, err := List("/g")
		if err != nil {
			t.Fatalf("List err = %v", err)
		}
		if len(plans) != 0 {
			t.Errorf("List = %v, want empty (malformed skipped)", plans)
		}
	})
}

func TestSave_NoLedgerErrors(t *testing.T) {
	withLedger(t, "")
	_, err := Save("/g", Input{Raw: "# x"}, sampleResult(), nil, Meta{Topic: "x"})
	if err == nil {
		t.Fatal("Save with no ledger should error so the porcelain path can treat it as 'nothing to save'")
	}
}

func TestLoad_NotFound(t *testing.T) {
	withLedger(t, t.TempDir())
	_, _, _, err := Load("/g", "does-not-exist")
	if err == nil {
		t.Fatal("Load of missing slug should error")
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Plan capture to ledger", "plan-capture-to-ledger"},
		{"One Two", "one-two"},
		{"  Trim   Me  ", "trim-me"},
		{"A/B: C.d, E!", "a-b-c-d"},
		{"Five Six Seven Eight Nine", "five-six-seven-eight"},
		{"Single", "single"},
		{"", "untitled-plan"},
		{"!!!", "untitled-plan"},
		{"CamelCase Words", "camelcase-words"},
	}
	for _, tc := range tests {
		if got := Slugify(tc.in); got != tc.want {
			t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSave_DefaultsSlugAndTimestamp(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	// empty Slug and zero CreatedAt should be derived/defaulted.
	dir, err := Save("/g", Input{Raw: "# x"}, sampleResult(), nil, Meta{Topic: "Derive My Slug"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if base := filepath.Base(dir); !strings.HasSuffix(base, "-derive-my-slug") {
		t.Errorf("dir base = %q, want suffix -derive-my-slug", base)
	}

	// meta.json carries a non-zero created_at and the derived slug.
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Slug != "derive-my-slug" {
		t.Errorf("meta.Slug = %q", meta.Slug)
	}
	if meta.CreatedAt.IsZero() {
		t.Errorf("meta.CreatedAt is zero, want defaulted")
	}
}
