package main

import "testing"

func TestExtractLatestVersionSkipsUnreleased(t *testing.T) {
	content := `# Changelog

## [Unreleased]

## [0.14.0] - 2026-08-15

New release notes.

## [0.13.0] - 2026-08-11

Previous release notes.`

	want := "## [0.14.0] - 2026-08-15\n\nNew release notes."
	if got := extractLatestVersion(content); got != want {
		t.Errorf("extractLatestVersion() = %q, want %q", got, want)
	}
}
