package plan

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
)

// HasLargePlainHTML is what keeps a failed upload's multi-megabyte page out of a
// commit, so each state it must tell apart is pinned.
func TestHasLargePlainHTML(t *testing.T) {
	large := bytes.Repeat([]byte("x"), htmlLFSThreshold+1)
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  bool
	}{
		{"no plan.html", func(t *testing.T, dir string) {}, false},
		{"small plain", func(t *testing.T, dir string) { writePlanHTMLPlain(t, dir, []byte("<html></html>")) }, false},
		{"large plain", func(t *testing.T, dir string) { writePlanHTMLPlain(t, dir, large) }, true},
		{"large pointer", func(t *testing.T, dir string) {
			ref := lfs.NewFileRef(large)
			if err := os.WriteFile(filepath.Join(dir, planHTMLFile), []byte(lfs.FormatPointer(ref.OID, ref.Size)), 0o644); err != nil {
				t.Fatal(err)
			}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			if got := HasLargePlainHTML(dir); got != tt.want {
				t.Errorf("HasLargePlainHTML = %v, want %v", got, tt.want)
			}
		})
	}
}
