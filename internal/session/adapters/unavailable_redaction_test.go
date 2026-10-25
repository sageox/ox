package adapters

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	adapterregistry "github.com/sageox/ox/internal/adapter"
)

// Registry placeholders without an installed implementation must not silently
// select generic JSONL recording for a vendor's unsupported native format.
func TestUnavailableRegisteredSessionAdaptersCannotCapture(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"home", "bundled", "external", "local"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("USERPROFILE", filepath.Join(root, "home"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "local"))
	t.Setenv("OX_ADAPTER_PATH", filepath.Join(root, "external"))
	oldExecutable := executableFunc
	executableFunc = func() (string, error) { return filepath.Join(root, "bundled", "ox"), nil }
	t.Cleanup(func() { executableFunc = oldExecutable })
	registryMu.Lock()
	oldRegistry := registry
	registry = map[string]Adapter{"generic": &GenericJSONLAdapter{}}
	registryMu.Unlock()
	t.Cleanup(func() { registryMu.Lock(); registry = oldRegistry; registryMu.Unlock() })
	entries, err := adapterregistry.LoadEmbeddedRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries.Adapters {
		if entry.Type != "session" || entry.Bundled {
			continue
		}
		t.Run(entry.Name, func(t *testing.T) {
			got, err := GetAdapter(entry.Name)
			if !errors.Is(err, ErrAdapterNotFound) || got != nil {
				t.Error("unavailable adapter did not fail closed before capture")
			}
		})
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Error("unavailable-adapter lookup created an artifact")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
