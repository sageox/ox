package hooks_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/daemon/hooks"
)

func TestLoadHooksFromEmpty(t *testing.T) {
	t.Parallel()

	cfgs, err := hooks.LoadHooksFrom("/nonexistent/path/hooks.yaml")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if cfgs != nil {
		t.Fatalf("expected nil hooks for missing file, got: %v", cfgs)
	}
}

func TestLoadHooksFromValid(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	content := `hooks:
  - event: murmur.received
    command: echo hello
  - event: session.uploaded
    command: notify-send "uploaded"
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := hooks.LoadHooksFrom(path)
	if err != nil {
		t.Fatalf("LoadHooksFrom error: %v", err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(cfgs))
	}
	if cfgs[0].Event != "murmur.received" || cfgs[0].Command != "echo hello" {
		t.Errorf("hook[0] = %+v", cfgs[0])
	}
}

func TestLoadHooksDedup(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	content := `hooks:
  - event: daemon.started
    command: echo test
  - event: daemon.started
    command: echo test
  - event: daemon.started
    command: echo different
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := hooks.LoadHooksFrom(path)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("expected 2 hooks after dedup, got %d", len(cfgs))
	}
}

func TestLoadHooksSkipInvalidEvent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	content := `hooks:
  - event: invalid.event.name
    command: echo test
  - event: daemon.started
    command: echo valid
  - event: "*"
    command: echo wildcard
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := hooks.LoadHooksFrom(path)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// invalid event skipped, valid + wildcard kept
	if len(cfgs) != 2 {
		t.Fatalf("expected 2 hooks (invalid skipped), got %d: %+v", len(cfgs), cfgs)
	}
}

func TestLoadHooksSkipEmptyCommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	content := `hooks:
  - event: daemon.started
    command: ""
  - event: daemon.started
    command: echo valid
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := hooks.LoadHooksFrom(path)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("expected 1 hook (empty command skipped), got %d", len(cfgs))
	}
}

// --- Malformed YAML ---

// TestLoadHooksMalformedYAML verifies invalid YAML content returns an error.
// Failure prevented: unparseable config silently produces empty hook list.
func TestLoadHooksMalformedYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	content := `hooks:
  - event: [invalid yaml
    command: broken
  unterminated: {
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := hooks.LoadHooksFrom(path)
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
}

// --- Empty YAML file ---

// TestLoadHooksEmptyFile verifies an empty file returns empty slice, not error.
// Failure prevented: empty config file treated as corruption.
func TestLoadHooksEmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := hooks.LoadHooksFrom(path)
	if err != nil {
		t.Fatalf("unexpected error for empty file: %v", err)
	}
	if len(cfgs) != 0 {
		t.Fatalf("expected 0 hooks for empty file, got %d", len(cfgs))
	}
}

// --- YAML with empty hooks list ---

// TestLoadHooksEmptyList verifies `hooks: []` returns empty slice.
// Failure prevented: explicit empty list treated differently from missing file.
func TestLoadHooksEmptyList(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	if err := os.WriteFile(path, []byte("hooks: []\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := hooks.LoadHooksFrom(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfgs) != 0 {
		t.Fatalf("expected 0 hooks, got %d", len(cfgs))
	}
}

// --- YAML with extra/unknown fields ---

// TestLoadHooksExtraFields verifies unknown YAML fields are silently ignored.
// Failure prevented: strict parsing rejects valid configs with forward-compatible fields.
func TestLoadHooksExtraFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	content := `hooks:
  - event: daemon.started
    command: echo test
    description: this is an extra field
    timeout: 5000
    unknown_field: should be ignored
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := hooks.LoadHooksFrom(path)
	if err != nil {
		t.Fatalf("unexpected error with extra fields: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(cfgs))
	}
	if cfgs[0].Event != "daemon.started" {
		t.Errorf("event = %q, want daemon.started", cfgs[0].Event)
	}
}

// --- Large hooks file ---

// TestLoadHooksLargeFile verifies loading 1000+ hooks completes in reasonable time.
// Failure prevented: O(n^2) dedup or validation causes timeout on large configs.
func TestLoadHooksLargeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("short: large file performance")
	}
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")

	var b strings.Builder
	b.WriteString("hooks:\n")
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&b, "  - event: daemon.started\n    command: echo hook-%d\n", i)
	}

	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := hooks.LoadHooksFrom(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// all 1000 have unique commands, so none should be deduped
	if len(cfgs) != 1000 {
		t.Fatalf("expected 1000 hooks, got %d", len(cfgs))
	}
}

// --- Wildcard with valid events in same file ---

// TestLoadHooksWildcardPreserved verifies wildcard "*" event passes validation.
// Failure prevented: wildcard incorrectly rejected by ValidEvent check.
func TestLoadHooksWildcardPreserved(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	content := `hooks:
  - event: "*"
    command: echo all-events
  - event: daemon.started
    command: echo specific
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := hooks.LoadHooksFrom(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("expected 2 hooks (wildcard + specific), got %d", len(cfgs))
	}
	if cfgs[0].Event != "*" {
		t.Errorf("first hook event = %q, want '*'", cfgs[0].Event)
	}
}

// --- Dedup with different events same command ---

// TestLoadHooksDedupSameCommandDifferentEvents verifies that the same command
// with different events is NOT deduped.
// Failure prevented: dedup key ignores event name, collapsing distinct hooks.
func TestLoadHooksDedupSameCommandDifferentEvents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	content := `hooks:
  - event: daemon.started
    command: echo test
  - event: daemon.stopped
    command: echo test
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := hooks.LoadHooksFrom(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("expected 2 hooks (same command, different events), got %d", len(cfgs))
	}
}

// --- Permission error ---

// TestLoadHooksPermissionError verifies an unreadable file returns an error.
// Failure prevented: permission error silently returns empty hooks.
func TestLoadHooksPermissionError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	if err := os.WriteFile(path, []byte("hooks:\n  - event: daemon.started\n    command: echo test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// remove read permission
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0600) }) // restore for cleanup

	_, err := hooks.LoadHooksFrom(path)
	if err == nil {
		t.Fatal("expected error for unreadable file, got nil")
	}
}

// --- YAML with only hooks key but no entries ---

// TestLoadHooksNullHooksKey verifies `hooks:` with no value returns empty slice.
// Failure prevented: null YAML value causes nil pointer dereference.
func TestLoadHooksNullHooksKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.yaml")
	if err := os.WriteFile(path, []byte("hooks:\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := hooks.LoadHooksFrom(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfgs) != 0 {
		t.Fatalf("expected 0 hooks, got %d", len(cfgs))
	}
}
