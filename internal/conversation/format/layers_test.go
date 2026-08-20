package format

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverLayersFullFixture(t *testing.T) {
	d, err := DiscoverLayers(fixture(t, "2026-08-11-22-32-full"))
	if err != nil {
		t.Fatalf("DiscoverLayers: %v", err)
	}

	wantLayers := []struct {
		id     string
		kind   string
		layout LayerLayout
	}{
		{"clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca", "transcript", LayoutFolder},
		{"clyr_019ff2f6-029d-7d89-8653-6d8df20ac203", "annotation", LayoutFlat},
		{"clyr_019ff2f6-1627-7f2e-885b-c5f5466546bd", "audio", LayoutFolder}, // folder wins over flat dup
	}
	if len(d.Layers) != len(wantLayers) {
		t.Fatalf("Layers = %d, want %d: %+v", len(d.Layers), len(wantLayers), d.Layers)
	}
	for i, want := range wantLayers {
		got := d.Layers[i]
		if got.Envelope.LayerID != want.id || got.Envelope.Kind != want.kind || got.Layout != want.layout {
			t.Errorf("Layers[%d] = %s/%s/%s, want %s/%s/%s",
				i, got.Envelope.LayerID, got.Envelope.Kind, got.Layout, want.id, want.kind, want.layout)
		}
	}
	if rev := d.Layers[0].Envelope.Revision; rev != 2 {
		t.Errorf("transcript revision = %d, want 2", rev)
	}

	wantInvalid := []struct {
		pathPart string
		reason   string
	}{
		{"audio.clyr_019ff2f6-1627-7f2e-885b-c5f5466546bd.json", "duplicate layer id"},
		{"broken.clyr_019ff2f6-aaaa-7aaa-8aaa-aaaaaaaaaaaa.json", "parse"},
		{"cast-events.clyr_019ff2f5-e661-7ce6-a5ad-0f16d6493fec/layer.json", "without layer.json"},
		{"summary.clyr_019ff2f6-31d2-73fc-b03e-84919ebf3208.json", "layer id mismatch"},
	}
	if len(d.Invalid) != len(wantInvalid) {
		t.Fatalf("Invalid = %d, want %d: %+v", len(d.Invalid), len(wantInvalid), d.Invalid)
	}
	for i, want := range wantInvalid {
		got := d.Invalid[i]
		if !strings.Contains(got.Path, want.pathPart) || !strings.Contains(got.Reason, want.reason) {
			t.Errorf("Invalid[%d] = %s (%s), want path~%q reason~%q", i, got.Path, got.Reason, want.pathPart, want.reason)
		}
	}
}

func TestDiscoverLayersDeterministic(t *testing.T) {
	root := fixture(t, "2026-08-11-22-32-full")
	first, err := DiscoverLayers(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := DiscoverLayers(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(again.Layers) != len(first.Layers) || len(again.Invalid) != len(first.Invalid) {
			t.Fatal("non-deterministic result sizes")
		}
		for j := range first.Layers {
			if again.Layers[j].Path != first.Layers[j].Path {
				t.Fatalf("non-deterministic layer order at %d: %s vs %s", j, again.Layers[j].Path, first.Layers[j].Path)
			}
		}
		for j := range first.Invalid {
			if again.Invalid[j] != first.Invalid[j] {
				t.Fatalf("non-deterministic invalid order at %d", j)
			}
		}
	}
}

func TestDiscoverLayersAbsence(t *testing.T) {
	tests := []struct {
		name string
		root string
	}{
		{"no layers directory (legacy folder)", fixture(t, "2026-08-12-01-00-legacy")},
		{"nonexistent discussion root", filepath.Join(t.TempDir(), "nope")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := DiscoverLayers(tt.root)
			if err != nil {
				t.Fatalf("DiscoverLayers: %v", err)
			}
			if len(d.Layers) != 0 || len(d.Invalid) != 0 {
				t.Errorf("want empty discovery, got %+v", d)
			}
		})
	}
}

func TestParseLayerName(t *testing.T) {
	tests := []struct {
		base     string
		wantOK   bool
		wantKind string
		wantID   string
	}{
		{"transcript.clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca", true, "transcript", "clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca"},
		{"cast-events.clyr_019ff2f5-e661-7ce6-a5ad-0f16d6493fec", true, "cast-events", "clyr_019ff2f5-e661-7ce6-a5ad-0f16d6493fec"},
		{"", false, "", ""},
		{"noseparator", false, "", ""},
		{".clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca", false, "", ""}, // empty kind
		{"kind.clyr_not-a-uuid", false, "", ""},
		{"kind.clyr_019FF2F5-DEB5-77D3-B84B-04DB14C601CA", true, "kind", "clyr_019FF2F5-DEB5-77D3-B84B-04DB14C601CA"},
		{"../evil.clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca", false, "", ""},
		{"a/b.clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca", false, "", ""},
		{`a\b.clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca`, false, "", ""},
		{"kind.clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca-extra", false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			got, ok := parseLayerName(tt.base)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && (got.kind != tt.wantKind || got.id != tt.wantID) {
				t.Errorf("parsed = %+v, want kind %q id %q", got, tt.wantKind, tt.wantID)
			}
		})
	}
}

// FuzzParseLayerName is the confinement fuzz target: any accepted name must be
// join-safe — no separators, no parent references, and the joined path must
// stay strictly inside the root the discovery walk hands it.
func FuzzParseLayerName(f *testing.F) {
	seeds := []string{
		"transcript.clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca",
		"../../etc/passwd.clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca",
		"..\\..\\evil.clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca",
		"a/../b.clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca",
		"..", ".", "", "kind.clyr_", "kind.clyr_..",
		"kind.clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca/..",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, base string) {
		parsed, ok := parseLayerName(base)
		if !ok {
			return
		}
		if strings.ContainsAny(base, `/\`) || strings.Contains(base, "..") {
			t.Fatalf("accepted traversal-capable name %q", base)
		}
		root := filepath.Join("some", "root")
		joined := filepath.Clean(filepath.Join(root, LayersDirName, base))
		prefix := filepath.Clean(filepath.Join(root, LayersDirName)) + string(filepath.Separator)
		if !strings.HasPrefix(joined, prefix) {
			t.Fatalf("name %q (id %s) escapes root: %s", base, parsed.id, joined)
		}
	})
}

// mustSymlink creates a symlink or skips the test on platforms without
// symlink support.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}
}

// TestDiscoverLayersNeverFollowsEscapingSymlinks verifies every read of the
// discovery walk happens through the discussion root with no-follow
// semantics. Failure prevented: a symlink committed into the
// customer-writable, git-synced team context — a symlinked layers/
// directory, flat envelope file, or layer.json — passes the lexical name
// checks and reads layer metadata from outside the discussion root
// (read-escape / exfiltration).
func TestDiscoverLayersNeverFollowsEscapingSymlinks(t *testing.T) {
	const layerID = "clyr_019ff500-0000-7000-8000-000000000001"
	envelope := `{"layer_id":"` + layerID + `","kind":"transcript","revision":3}`

	// Outside tree the symlinks point at: real, well-formed layer content
	// that must never be served.
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "layers", "transcript."+layerID), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(outside, "layers", "transcript."+layerID, "layer.json"),
		filepath.Join(outside, "layers", "transcript."+layerID+".json"),
		filepath.Join(outside, "layer.json"),
		filepath.Join(outside, "flat.json"),
	} {
		if err := os.WriteFile(p, []byte(envelope), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("symlinked layers dir", func(t *testing.T) {
		root := t.TempDir()
		mustSymlink(t, filepath.Join(outside, "layers"), filepath.Join(root, LayersDirName))
		d, err := DiscoverLayers(root)
		if err == nil {
			if len(d.Layers) != 0 {
				t.Fatalf("layers served through a symlinked layers/ dir: %+v", d.Layers)
			}
			t.Fatal("symlinked layers/ dir silently ignored, want an error or invalid record")
		}
	})

	t.Run("symlinked flat envelope", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, LayersDirName), 0o755); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, filepath.Join(outside, "flat.json"),
			filepath.Join(root, LayersDirName, "transcript."+layerID+".json"))
		d, err := DiscoverLayers(root)
		if err != nil {
			t.Fatalf("DiscoverLayers: %v", err)
		}
		if len(d.Layers) != 0 {
			t.Fatalf("layers served through a symlinked flat envelope: %+v", d.Layers)
		}
		if len(d.Invalid) != 1 {
			t.Fatalf("Invalid = %+v, want the symlinked envelope surfaced", d.Invalid)
		}
	})

	t.Run("symlinked layer.json", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, LayersDirName, "transcript."+layerID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, filepath.Join(outside, "layer.json"), filepath.Join(dir, layerFileName))
		d, err := DiscoverLayers(root)
		if err != nil {
			t.Fatalf("DiscoverLayers: %v", err)
		}
		if len(d.Layers) != 0 {
			t.Fatalf("layers served through a symlinked layer.json: %+v", d.Layers)
		}
		if len(d.Invalid) != 1 {
			t.Fatalf("Invalid = %+v, want the symlinked layer.json surfaced", d.Invalid)
		}
	})
}
