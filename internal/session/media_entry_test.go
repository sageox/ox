package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// A media entry written through the real raw writer keeps its digest (the
// redactor must not mistake 64 hex characters for a secret) and carries no
// image bytes; a skipped image is written as a declared gap.
func TestMediaEntriesSurviveRawWriterAsMediaAndGapLines(t *testing.T) {
	const sha = "b16a348ab644ef7715b4b6dd35bcdc72a91a43565dcc8bd733337b1df768bc85"
	entries := ConvertRawEntries([]adapters.RawEntry{
		{Role: "user", Content: "look"},
		{Role: adapterprotocol.RoleMedia, Media: &adapterprotocol.MediaRef{SHA256: sha, MIME: "image/png", Size: 72, Position: 1}},
		{Role: adapterprotocol.RoleMedia, Media: &adapterprotocol.MediaRef{MIME: "image/svg+xml", Skipped: adapterprotocol.MediaSkippedUnsupportedType}},
	})
	raw := filepath.Join(t.TempDir(), "raw.jsonl")
	w, err := NewRawWriter(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteEntries(entries); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d: %s", len(lines), b)
	}
	for _, want := range []string{`"type":"media"`, `"sha256":"` + sha + `"`, `"mime":"image/png"`, `"size":72`, `"position":1`} {
		if !strings.Contains(lines[1], want) {
			t.Fatalf("media line missing %s: %s", want, lines[1])
		}
	}
	for _, want := range []string{`"type":"gap"`, `"reason":"media_unsupported_type"`, `"lost_entries":1`} {
		if !strings.Contains(lines[2], want) {
			t.Fatalf("gap line missing %s: %s", want, lines[2])
		}
	}
}
