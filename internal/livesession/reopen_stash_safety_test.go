package livesession

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A conversation id read from a user-writable state file must never steer a
// delete outside the stash dir.
func TestStashRefusesNonCanonicalConversationIDs(t *testing.T) {
	t.Parallel()
	ids := []string{
		"../canary",
		"cnv_../../canary",
		"cnv_../canary",
		"/abs/canary",
		"",
		"cnv_",
		"cnv_01A1FA92-DF8F-7305-9236-0B71D90B4D83", // uppercase is not canonical
		"cnv_01a1fa92df8f730592360b71d90b4d83",     // un-hyphenated uuid
		"cnv_01a1fa92-df8f-7305-9236-0b71d90b4d83/../../canary",
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			cfg := Config{DataDir: filepath.Join(root, "data")}
			if err := os.MkdirAll(stashDir(cfg), 0o700); err != nil {
				t.Fatal(err)
			}
			// canaries where a traversal id would land: beside the stash dir
			// and inside it under the raw (unjoined) name
			canaries := []string{
				filepath.Join(cfg.DataDir, "canary.json"),
				filepath.Join(cfg.DataDir, "canary.spool"),
				filepath.Join(root, "canary.json"),
				filepath.Join(stashDir(cfg), "cnv_.json"),
			}
			for _, p := range canaries {
				if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := removeStash(cfg, id); !errors.Is(err, ErrInvalidConversationID) {
				t.Fatalf("removeStash(%q) = %v, want ErrInvalidConversationID", id, err)
			}
			for _, p := range canaries {
				if _, err := os.Stat(p); err != nil {
					t.Fatalf("removeStash(%q) removed %s: %v", id, p, err)
				}
			}
			if _, _, err := safeStashPaths(cfg, id); err == nil {
				t.Fatalf("safeStashPaths(%q) accepted a non-canonical id", id)
			}
		})
	}
}

func TestStashAcceptsCanonicalConversationID(t *testing.T) {
	t.Parallel()
	cfg := Config{DataDir: t.TempDir()}
	meta, spool, err := safeStashPaths(cfg, "cnv_01a1fa92-df8f-7305-9236-0b71d90b4d83")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(meta) != stashDir(cfg) || filepath.Dir(spool) != stashDir(cfg) {
		t.Fatalf("paths %s, %s are not directly in the stash dir", meta, spool)
	}
}

// sweepStash must not delete a temp file another writer is still filling.
func TestSweepStashKeepsFreshTempFilesAndRemovesStaleOnes(t *testing.T) {
	t.Parallel()
	cfg := Config{DataDir: t.TempDir()}
	if err := os.MkdirAll(stashDir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(stashDir(cfg), ".tmp-fresh")
	stale := filepath.Join(stashDir(cfg), ".tmp-stale")
	for _, p := range []string{fresh, stale} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleTempAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	sweepStash(cfg)
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("sweep removed an in-flight temp file: %v", err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("sweep kept an abandoned temp file")
	}
}
