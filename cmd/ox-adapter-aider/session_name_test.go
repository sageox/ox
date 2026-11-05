package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The read protocol carries only a path, so a caller-supplied file outside
// Aider's history name must never become a session source.
func TestReadRefusesFilesNotNamedAsAiderHistory(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "credentials.md")
	if err := os.WriteFile(outside, []byte("#### user\nsecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if entries, err := readAiderFile(outside); err == nil || len(entries) != 0 {
		t.Fatalf("readAiderFile(%q) = %d entries, err %v; want refusal", outside, len(entries), err)
	}
	if _, _, err := readAiderFromOffset(outside, 0); err == nil {
		t.Fatalf("readAiderFromOffset(%q) succeeded; want refusal", outside)
	}
	if _, err := statAiderFile(outside); err == nil {
		t.Fatalf("statAiderFile(%q) succeeded; want refusal", outside)
	}
}
