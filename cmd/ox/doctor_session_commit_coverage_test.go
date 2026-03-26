package main

import (
	"strings"
	"testing"
	"time"
)

func TestExtractSessionIDs_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		files    []string
		expected []string
	}{
		{
			name: "flat jsonl session files",
			// extractSessionIDs uses filepath.Base, so these are flat files under sessions/
			files:    []string{"sessions/2026-03-01-10-30-alice-Oxa7b3.jsonl"},
			expected: []string{"Oxa7b3"},
		},
		{
			name: "subdirectory files are not matched",
			// raw.jsonl inside a session dir: Base="raw.jsonl", splits to ["raw"], len<7
			files:    []string{"sessions/2026-03-01-10-30-alice-Oxa7b3/raw.jsonl"},
			expected: nil,
		},
		{
			name: "deduplication across sessions",
			files: []string{
				"sessions/2026-03-01-10-30-alice-abc123.jsonl",
				"sessions/2026-03-02-11-45-bob-abc123.jsonl",
			},
			expected: []string{"abc123"},
		},
		{
			name: "multiple distinct sessions",
			files: []string{
				"sessions/2026-03-01-10-30-alice-sess1.jsonl",
				"sessions/2026-03-02-11-45-bob-sess2.jsonl",
			},
			expected: []string{"sess1", "sess2"},
		},
		{
			name:     "non-jsonl files ignored",
			files:    []string{"sessions/2026-03-01-10-30-alice-abc.md"},
			expected: nil,
		},
		{
			name:     "short filename without enough parts",
			files:    []string{"short.jsonl"},
			expected: nil,
		},
		{
			name:     "empty input",
			files:    nil,
			expected: nil,
		},
		{
			name:     "six-part filename (just under threshold)",
			files:    []string{"2026-03-01-10-30-sess1.jsonl"},
			expected: nil,
		},
		{
			name:     "seven-part filename (at threshold)",
			files:    []string{"2026-03-01-10-30-alice-sess1.jsonl"},
			expected: []string{"sess1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractSessionIDs(tt.files)
			if len(got) != len(tt.expected) {
				t.Errorf("expected %d IDs, got %d: %v", len(tt.expected), len(got), got)
				return
			}
			for i, id := range got {
				if id != tt.expected[i] {
					t.Errorf("expected ID[%d]=%q, got %q", i, tt.expected[i], id)
				}
			}
		})
	}
}

func TestBuildSessionCommitMessage_EdgeCases(t *testing.T) {
	t.Parallel()

	today := time.Now().Format("2006-01-02")

	tests := []struct {
		name       string
		sessionIDs []string
		contains   string
	}{
		{
			name:       "single session",
			sessionIDs: []string{"abc123"},
			contains:   "Add session " + today + "-abc123",
		},
		{
			name:       "multiple sessions",
			sessionIDs: []string{"abc", "def", "ghi"},
			contains:   "Add 3 sessions " + today,
		},
		{
			name:       "no sessions (empty)",
			sessionIDs: nil,
			contains:   "Update sessions " + today,
		},
		{
			name:       "empty slice",
			sessionIDs: []string{},
			contains:   "Update sessions " + today,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildSessionCommitMessage(tt.sessionIDs)
			if !strings.Contains(got, tt.contains) {
				t.Errorf("expected message containing %q, got %q", tt.contains, got)
			}
		})
	}
}
