package adapters

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoverExternalAdapters_FindsBinaries(t *testing.T) {
	dir := t.TempDir()
	// create a valid adapter binary
	script := `#!/bin/sh
echo '{"protocol_version":1,"name":"test-discover","display_name":"Test","version":"0.1.0","type":"session","capabilities":["session_reader"]}'`
	binary := filepath.Join(dir, "ox-adapter-test-discover")
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OX_ADAPTER_PATH", dir)

	adapters := DiscoverExternalAdapters()
	found := false
	for _, a := range adapters {
		if a.Name() == "test-discover" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to discover test-discover adapter")
	}
}

func TestDiscoverExternalAdapters_SkipsNonExecutable(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
echo '{"protocol_version":1,"name":"noexec","version":"0.1.0","type":"session"}'`
	binary := filepath.Join(dir, "ox-adapter-noexec")
	if err := os.WriteFile(binary, []byte(script), 0644); err != nil { // no exec bit
		t.Fatal(err)
	}

	t.Setenv("OX_ADAPTER_PATH", dir)

	adapters := DiscoverExternalAdapters()
	for _, a := range adapters {
		if a.Name() == "noexec" {
			t.Error("should not discover non-executable adapter")
		}
	}
}

func TestDiscoverExternalAdapters_SkipsOldProtocol(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
echo '{"protocol_version":0,"name":"old-proto","version":"0.1.0","type":"session"}'`
	binary := filepath.Join(dir, "ox-adapter-old-proto")
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OX_ADAPTER_PATH", dir)

	adapters := DiscoverExternalAdapters()
	for _, a := range adapters {
		if a.Name() == "old-proto" {
			t.Error("should not discover adapter with old protocol version")
		}
	}
}

func TestDiscoverExternalAdapters_PriorityOrder(t *testing.T) {
	// higher-priority dir has adapter v1.0, lower-priority dir has v2.0
	highDir := t.TempDir()
	lowDir := t.TempDir()

	scriptHigh := `#!/bin/sh
echo '{"protocol_version":1,"name":"priority-test","version":"1.0.0","type":"session"}'`
	if err := os.WriteFile(filepath.Join(highDir, "ox-adapter-priority-test"), []byte(scriptHigh), 0755); err != nil {
		t.Fatal(err)
	}

	scriptLow := `#!/bin/sh
echo '{"protocol_version":1,"name":"priority-test","version":"2.0.0","type":"session"}'`
	if err := os.WriteFile(filepath.Join(lowDir, "ox-adapter-priority-test"), []byte(scriptLow), 0755); err != nil {
		t.Fatal(err)
	}

	// highDir listed first = higher priority
	t.Setenv("OX_ADAPTER_PATH", highDir+string(os.PathListSeparator)+lowDir)

	adapters := DiscoverExternalAdapters()
	for _, a := range adapters {
		if a.Name() == "priority-test" {
			if a.Info().Version != "1.0.0" {
				t.Errorf("expected high-priority version 1.0.0, got %s", a.Info().Version)
			}
			return
		}
	}
	t.Error("priority-test adapter not found")
}

func TestDiscoverExternalAdapters_IgnoresNonAdapterBinaries(t *testing.T) {
	dir := t.TempDir()
	// create a binary that doesn't match the prefix
	if err := os.WriteFile(filepath.Join(dir, "some-other-tool"), []byte("#!/bin/sh\necho hi"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OX_ADAPTER_PATH", dir)

	adapters := DiscoverExternalAdapters()
	if len(adapters) != 0 {
		t.Errorf("expected 0 adapters, got %d", len(adapters))
	}
}

func TestDiscoverExternalAdapters_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OX_ADAPTER_PATH", dir)

	adapters := DiscoverExternalAdapters()
	if len(adapters) != 0 {
		t.Errorf("expected 0 adapters from empty dir, got %d", len(adapters))
	}
}

func TestDiscoverExternalAdapters_NonexistentDir(t *testing.T) {
	t.Setenv("OX_ADAPTER_PATH", "/nonexistent/path/that/should/not/exist")

	adapters := DiscoverExternalAdapters()
	if len(adapters) != 0 {
		t.Errorf("expected 0 adapters from nonexistent dir, got %d", len(adapters))
	}
}

func TestRegisterExternalAdapters_SupersedesBuiltIn(t *testing.T) {
	// register a fake built-in
	ResetRegistry()
	defer ResetRegistry()

	// register a simple built-in that returns the name "test-replace"
	Register(&discoveryMockAdapter{name: "test-replace"})

	// create an external adapter with the same name
	dir := t.TempDir()
	script := `#!/bin/sh
echo '{"protocol_version":1,"name":"test-replace","display_name":"External","version":"2.0.0","type":"session","capabilities":["session_reader"]}'`
	if err := os.WriteFile(filepath.Join(dir, "ox-adapter-test-replace"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OX_ADAPTER_PATH", dir)

	if err := RegisterExternalAdapters(); err != nil {
		t.Fatalf("RegisterExternalAdapters: %v", err)
	}

	// the external adapter should have replaced the built-in
	adapter, err := GetAdapter("test-replace")
	if err != nil {
		t.Fatalf("GetAdapter: %v", err)
	}

	ea, ok := adapter.(*ExternalAdapter)
	if !ok {
		t.Fatal("expected ExternalAdapter, got built-in")
	}
	if ea.Info().Version != "2.0.0" {
		t.Errorf("Version = %q, want 2.0.0", ea.Info().Version)
	}
}

func TestAdapterDirs_IncludesEnvPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OX_ADAPTER_PATH", dir)

	dirs := AdapterDirs()
	if len(dirs) == 0 {
		t.Fatal("expected at least one dir")
	}
	if dirs[0] != dir {
		t.Errorf("first dir = %q, want %q", dirs[0], dir)
	}
}

// discoveryMockAdapter is a minimal Adapter for testing registry priority.
// Named differently from mockAdapter in adapter_test.go to avoid redeclaration.
type discoveryMockAdapter struct {
	name string
}

func (m *discoveryMockAdapter) Name() string                                              { return m.name }
func (m *discoveryMockAdapter) Detect() bool                                              { return false }
func (m *discoveryMockAdapter) FindSessionFile(_ string, _ time.Time) (string, error)     { return "", nil }
func (m *discoveryMockAdapter) Read(_ string) ([]RawEntry, error)                         { return nil, nil }
func (m *discoveryMockAdapter) ReadMetadata(_ string) (*SessionMetadata, error)            { return nil, nil }
func (m *discoveryMockAdapter) Watch(_ context.Context, _ string) (<-chan RawEntry, error) { return nil, nil }
