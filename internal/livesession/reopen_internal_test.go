package livesession

import (
	"os"
	"path/filepath"
	"testing"
)

// A fallback interrupted between the spool rewrite and the state write comes
// back with the old seed length; cutting again would remove the recording's
// own lines.
func TestDropSeedTwiceCutsOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	header := `{"type":"header","metadata":{"session_id":"own"}}` + "\n"
	seed := `{"type":"header","metadata":{"session_id":"earlier"}}` + "\n" + `{"type":"user","content":"earlier work"}` + "\n"
	tail := `{"type":"user","content":"new work"}` + "\n" + `{"type":"assistant","content":"done"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(header), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, spoolFileName), []byte(seed+tail), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := dropSeed(t.Context(), dir, int64(len(seed))); err != nil {
			t.Fatalf("drop %d: %v", i+1, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, spoolFileName))
	if err != nil {
		t.Fatal(err)
	}
	if want := header + tail; string(got) != want {
		t.Fatalf("spool after two drops = %q, want %q", got, want)
	}
}
