package adapterprotocol

import "testing"

// wantBundledAdapterNames is the closed set of registry.yaml `name` values
// this package must resolve a capability set for. It is intentionally
// hand-listed (not derived from BundledAdapterCapabilities' own keys) so a
// typo'd or accidentally-removed map key has something independent to be
// caught against — see TestBundledAdapterCapabilities_AllNonEmptyAndKnown.
var wantBundledAdapterNames = []string{
	"claude-code", "gemini", "codex", "amp", "opencode",
	"pi", "omp", "aider", "droid", "goose",
}

// TestBundledAdapterCapabilities_AllNonEmptyAndKnown is the compile-time
// system's runtime backstop: every bundled adapter name must resolve to a
// non-empty capability set, and the map must carry no unexpected extra or
// missing names. A wrong or missing entry must fail loudly here rather than
// silently resolve to nil at adapter startup.
func TestBundledAdapterCapabilities_AllNonEmptyAndKnown(t *testing.T) {
	if len(BundledAdapterCapabilities) != len(wantBundledAdapterNames) {
		t.Errorf("BundledAdapterCapabilities has %d entries, want %d", len(BundledAdapterCapabilities), len(wantBundledAdapterNames))
	}
	for _, name := range wantBundledAdapterNames {
		caps, ok := BundledAdapterCapabilities[name]
		if !ok {
			t.Errorf("adapter %q has no entry in BundledAdapterCapabilities", name)
			continue
		}
		if len(caps) == 0 {
			t.Errorf("adapter %q resolves to an empty capability set", name)
		}
	}
}

// TestBundledAdapterCapabilities_NoUnexpectedNames catches the inverse drift:
// a name added to BundledAdapterCapabilities that registry.yaml does not
// (yet) know about. internal/adapter's TestBundledAdapters_CapabilitiesMatchBinary
// binds registry.yaml to this map from the other direction; this test keeps
// the map itself from silently growing an orphaned entry.
func TestBundledAdapterCapabilities_NoUnexpectedNames(t *testing.T) {
	want := make(map[string]bool, len(wantBundledAdapterNames))
	for _, name := range wantBundledAdapterNames {
		want[name] = true
	}
	for name := range BundledAdapterCapabilities {
		if !want[name] {
			t.Errorf("unexpected adapter %q in BundledAdapterCapabilities (not in wantBundledAdapterNames)", name)
		}
	}
}
