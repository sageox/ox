package adapterruntime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateSessionSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "T-current.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		adapter string
		id      string
		path    string
		wantErr bool
	}{
		{"gemini", "", "", true},
		{"gemini", "current", path, true},
		{"amp", "", "", true},
		{"amp", "", path, true},
		{"amp", "T-foreign", path, true},
		{"amp", "../T-current", path, true},
		{"amp", "T-current", path, false},
		{"amp", "https://ampcode.com/threads/T-current?view=full", path, false},
		{"amp", "T-current", "", false},
		{"claude-code", "", path, false},
	} {
		t.Run(tc.adapter+"/"+tc.id+"/"+filepath.Base(tc.path), func(t *testing.T) {
			err := ValidateSessionSource(tc.adapter, tc.path, tc.id)
			if (err != nil) != tc.wantErr || (err != nil && !errors.Is(err, ErrUnboundSource)) {
				t.Fatalf("ValidateSessionSource = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}