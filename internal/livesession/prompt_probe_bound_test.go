package livesession

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/sessionstream"
)

// TestPromptProbeStopsAtAPlantedOverlongLine guards the probe that runs before
// authorize on a checkout-writable spool: a newline-free line longer than any
// batch must not be read whole, and since the spool never holds such a line it
// proves nothing, so the probe says "may hold" and leaves the call to authorize.
// Failure prevented: a planted multi-megabyte spool allocated in full every
// daemon tick.
func TestPromptProbeStopsAtAPlantedOverlongLine(t *testing.T) {
	dir := t.TempDir()
	huge := bytes.Repeat([]byte("x"), 4*sessionstream.MaxBatchBytes)
	if err := os.WriteFile(filepath.Join(dir, spoolFileName), huge, 0o600); err != nil {
		t.Fatal(err)
	}
	if !mayHoldPrompt(dir) {
		t.Fatal("an overlong planted line must not be read as proof of no prompt")
	}
}

// TestPromptProbeStillReadsNormalSpools keeps the bound from changing the
// ordinary answers: a header-only spool holds no prompt, a user line does.
func TestPromptProbeStillReadsNormalSpools(t *testing.T) {
	dir := t.TempDir()
	spool := filepath.Join(dir, spoolFileName)
	if err := os.WriteFile(spool, []byte(`{"type":"header"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if mayHoldPrompt(dir) {
		t.Fatal("a header-only spool holds no prompt")
	}
	if err := os.WriteFile(spool, []byte(`{"type":"header"}`+"\n"+`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !mayHoldPrompt(dir) {
		t.Fatal("a spool with a user line holds a prompt")
	}
}
