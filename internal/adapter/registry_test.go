package adapter

import (
	"testing"
)

// --- A. Embedded registry parsing ---

// TestLoadEmbeddedRegistry_Parses verifies the embedded YAML parses without error.
// Failure prevented: malformed registry.yaml breaks all adapter discovery at compile time.
func TestLoadEmbeddedRegistry_Parses(t *testing.T) {
	reg, err := LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("LoadEmbeddedRegistry() error: %v", err)
	}
	if reg == nil {
		t.Fatal("LoadEmbeddedRegistry() returned nil registry")
	}
}

// TestLoadEmbeddedRegistry_SchemaVersion verifies schema_version is set.
// Failure prevented: future schema migrations break silently if version is missing.
func TestLoadEmbeddedRegistry_SchemaVersion(t *testing.T) {
	reg, err := LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("LoadEmbeddedRegistry() error: %v", err)
	}
	if reg.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", reg.SchemaVersion)
	}
}

// --- B. Required fields validation ---

// TestAdapterEntries_RequiredFields verifies every adapter entry has the mandatory
// fields populated. Failure prevented: incomplete entries cause nil dereferences
// or empty output in `ox adapter list`.
func TestAdapterEntries_RequiredFields(t *testing.T) {
	reg, err := LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("LoadEmbeddedRegistry() error: %v", err)
	}

	if len(reg.Adapters) == 0 {
		t.Fatal("registry has no adapters")
	}

	for _, a := range reg.Adapters {
		t.Run(a.Name, func(t *testing.T) {
			if a.Name == "" {
				t.Error("name is empty")
			}
			if a.DisplayName == "" {
				t.Errorf("adapter %q: display_name is empty", a.Name)
			}
			if a.Description == "" {
				t.Errorf("adapter %q: description is empty", a.Name)
			}
			if a.Type == "" {
				t.Errorf("adapter %q: type is empty", a.Name)
			}
			if a.Binary == "" {
				t.Errorf("adapter %q: binary is empty", a.Name)
			}
			if a.Repo == "" {
				t.Errorf("adapter %q: repo is empty", a.Name)
			}
			if len(a.Capabilities) == 0 {
				t.Errorf("adapter %q: capabilities is empty", a.Name)
			}
		})
	}
}

// --- C. Bundled adapter guarantees ---

// TestBundledAdapters_Present verifies the three bundled adapters exist.
// Failure prevented: removing a bundled adapter from the YAML silently breaks
// users who depend on built-in support for these agents.
func TestBundledAdapters_Present(t *testing.T) {
	reg, err := LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("LoadEmbeddedRegistry() error: %v", err)
	}

	expected := []string{"claude-code", "gemini", "codex", "amp", "opencode", "pi", "aider", "droid", "goose"}
	for _, name := range expected {
		entry := reg.Lookup(name)
		if entry == nil {
			t.Errorf("bundled adapter %q not found in registry", name)
			continue
		}
		if !entry.Bundled {
			t.Errorf("adapter %q should be bundled=true", name)
		}
		if entry.Repo != "sageox/ox" {
			t.Errorf("adapter %q repo = %q, want sageox/ox", name, entry.Repo)
		}
	}
}

// TestBundledAdapters_Filter verifies BundledAdapters() returns only bundled entries.
func TestBundledAdapters_Filter(t *testing.T) {
	reg, err := LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("LoadEmbeddedRegistry() error: %v", err)
	}

	bundled := reg.BundledAdapters()
	for _, a := range bundled {
		if !a.Bundled {
			t.Errorf("BundledAdapters() returned non-bundled adapter %q", a.Name)
		}
	}
	if len(bundled) < 3 {
		t.Errorf("expected at least 3 bundled adapters, got %d", len(bundled))
	}
}

// --- D. External adapter guarantees ---

// TestExternalAdapters_Filter verifies ExternalAdapters() returns only non-bundled entries.
func TestExternalAdapters_Filter(t *testing.T) {
	reg, err := LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("LoadEmbeddedRegistry() error: %v", err)
	}

	external := reg.ExternalAdapters()
	for _, a := range external {
		if a.Bundled {
			t.Errorf("ExternalAdapters() returned bundled adapter %q", a.Name)
		}
	}
	if len(external) == 0 {
		t.Error("expected at least one external adapter")
	}
}

// --- E. Lookup ---

// TestLookup_Found verifies Lookup returns the correct entry.
func TestLookup_Found(t *testing.T) {
	reg, err := LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("LoadEmbeddedRegistry() error: %v", err)
	}

	entry := reg.Lookup("claude-code")
	if entry == nil {
		t.Fatal("Lookup(claude-code) returned nil")
	}
	if entry.DisplayName != "Claude Code" {
		t.Errorf("DisplayName = %q, want %q", entry.DisplayName, "Claude Code")
	}
}

// TestLookup_NotFound verifies Lookup returns nil for unknown names.
func TestLookup_NotFound(t *testing.T) {
	reg, err := LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("LoadEmbeddedRegistry() error: %v", err)
	}

	if entry := reg.Lookup("nonexistent-adapter"); entry != nil {
		t.Errorf("Lookup(nonexistent-adapter) = %v, want nil", entry)
	}
}

// --- F. GitHubRepo helper ---

// TestGitHubRepo_Format verifies the GitHub URL construction.
func TestGitHubRepo_Format(t *testing.T) {
	entry := &AdapterEntry{Repo: "sageox/ox-adapters"}
	got := entry.GitHubRepo()
	want := "github.com/sageox/ox-adapters"
	if got != want {
		t.Errorf("GitHubRepo() = %q, want %q", got, want)
	}
}

// --- F2. RequireInstallable (curated-path integrity gate, ADR-022/ox-5ihl) ---

// TestRequireInstallable_BundledExempt verifies bundled adapters need no pin.
// Failure prevented: gating bundled adapters (which ship with ox) on a registry
// checksum they will never have.
func TestRequireInstallable_BundledExempt(t *testing.T) {
	e := &AdapterEntry{Name: "claude-code", Bundled: true}
	if err := e.RequireInstallable(); err != nil {
		t.Errorf("bundled adapter should be installable without a pin, got: %v", err)
	}
}

// TestRequireInstallable_MissingTag verifies a non-bundled entry without a tag
// is not installable. Failure prevented: a tagless curated entry installing from
// releases/latest with no provenance (the gap ox-5ihl closes).
func TestRequireInstallable_MissingTag(t *testing.T) {
	e := &AdapterEntry{Name: "cursor", Bundled: false, Checksums: map[string]string{"darwin_arm64": "abc"}}
	if err := e.RequireInstallable(); err == nil {
		t.Fatal("entry without a tag must not be installable")
	}
}

// TestRequireInstallable_MissingChecksums verifies a non-bundled entry without
// checksums is not installable. Failure prevented: installing unverified bytes
// under a SageOx-curated name.
func TestRequireInstallable_MissingChecksums(t *testing.T) {
	e := &AdapterEntry{Name: "cursor", Bundled: false, Tag: "v1.0.0"}
	if err := e.RequireInstallable(); err == nil {
		t.Fatal("entry without checksums must not be installable")
	}
}

// TestRequireInstallable_FullyPinned verifies a non-bundled entry with both a tag
// and checksums is installable.
func TestRequireInstallable_FullyPinned(t *testing.T) {
	e := &AdapterEntry{
		Name: "cursor", Bundled: false, Tag: "v1.0.0",
		Checksums: map[string]string{"darwin_arm64": "deadbeef"},
	}
	if err := e.RequireInstallable(); err != nil {
		t.Errorf("fully pinned entry should be installable, got: %v", err)
	}
}

// TestExternalEntries_CurrentlyUnpinned documents the intended transition state:
// the shipped external entries have no real checksum yet, so RequireInstallable
// fails (install requires --allow-unverified until a maintainer pins them). If
// this test starts failing, real pins were added — update it then.
func TestExternalEntries_CurrentlyUnpinned(t *testing.T) {
	reg, err := LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("LoadEmbeddedRegistry() error: %v", err)
	}
	for _, a := range reg.ExternalAdapters() {
		a := a
		if err := a.RequireInstallable(); err == nil {
			t.Errorf("external adapter %q now has a pin; update the registry-transition test", a.Name)
		}
	}
}

// --- G. Uniqueness constraints ---

// TestAdapterNames_Unique verifies no duplicate adapter names exist.
// Failure prevented: duplicate names cause unpredictable Lookup behavior.
func TestAdapterNames_Unique(t *testing.T) {
	reg, err := LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("LoadEmbeddedRegistry() error: %v", err)
	}

	seen := make(map[string]bool)
	for _, a := range reg.Adapters {
		if seen[a.Name] {
			t.Errorf("duplicate adapter name: %q", a.Name)
		}
		seen[a.Name] = true
	}
}

// TestAdapterBinaries_Unique verifies no duplicate binary names exist.
// Failure prevented: two entries pointing to the same binary cause install conflicts.
func TestAdapterBinaries_Unique(t *testing.T) {
	reg, err := LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("LoadEmbeddedRegistry() error: %v", err)
	}

	seen := make(map[string]bool)
	for _, a := range reg.Adapters {
		if seen[a.Binary] {
			t.Errorf("duplicate binary name: %q (adapter: %s)", a.Binary, a.Name)
		}
		seen[a.Binary] = true
	}
}
