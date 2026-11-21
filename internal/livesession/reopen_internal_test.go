package livesession

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A fallback interrupted between the spool rewrite and the state write comes
// back with the old seed length; cutting again would remove the recording's
// own lines.
func TestDropSeedTwiceCutsOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	header := `{"type":"header","metadata":{"session_id":"own"}}` + "\n"
	seed := `{"type":"header","metadata":{"session_id":"earlier"}}` + "\n" + `{"type":"user","content":"earlier work"}` + "\n"
	tail := `{"type":"user","content":"new work"}` + "\n" + `{"type":"assistant","content":"done"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, spoolFileName), []byte(seed+tail), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := dropSeed(t.Context(), dir, int64(len(seed)), []byte(header)); err != nil {
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

// One activity time covers the pair: a stash a resume just retargeted keeps its
// spool even though the spool file itself is old.
func TestSweepStashKeepsAFreshStashesOldSpool(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	cnv := "cnv_01a1fa92-df8f-7305-9236-0b71d90b4d83"
	if err := os.MkdirAll(stashDir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	meta, spool := stashPaths(cfg, cnv)
	for _, p := range []string{meta, spool} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * reopenRetention)
	if err := os.Chtimes(spool, old, old); err != nil {
		t.Fatal(err)
	}
	sweepStash(cfg)
	for _, p := range []string{meta, spool} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("sweep removed %s of a fresh stash: %v", filepath.Base(p), err)
		}
	}
	if err := os.Chtimes(meta, old, old); err != nil {
		t.Fatal(err)
	}
	sweepStash(cfg)
	for _, p := range []string{meta, spool} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("sweep kept %s of an expired stash", filepath.Base(p))
		}
	}
}
