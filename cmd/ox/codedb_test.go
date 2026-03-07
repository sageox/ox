package main

import (
	"strings"
	"testing"
)

func TestFindCodeDB_NotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	_, err := findCodeDB()
	if err == nil {
		t.Fatal("expected error when codedb not in PATH")
	}
	if got := err.Error(); !strings.Contains(got, "not found") {
		t.Errorf("error should mention 'not found', got: %s", got)
	}
}

func TestFindCodeDB_InPath(t *testing.T) {
	path, err := findCodeDB()
	if err != nil {
		t.Skip("codedb not installed, skipping")
	}
	if path == "" {
		t.Fatal("expected non-empty path")
	}
}
