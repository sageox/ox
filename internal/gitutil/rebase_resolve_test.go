package gitutil

import "testing"

func TestMatchesSafePrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		path     string
		prefixes []string
		want     bool
	}{
		{"exact match", "data/github/", []string{"data/github/"}, true},
		{"prefix match", "data/github/issues.json", []string{"data/github/"}, true},
		{"no match", "sessions/abc.jsonl", []string{"data/github/"}, false},
		{"multiple prefixes first matches", "data/github/prs.json", []string{"data/github/", "data/linear/"}, true},
		{"multiple prefixes second matches", "data/linear/issues.json", []string{"data/github/", "data/linear/"}, true},
		{"empty prefixes", "data/github/x.json", nil, false},
		{"empty path", "", []string{"data/"}, false},
		{"root file not safe", "AGENTS.md", []string{"data/github/"}, false},
		{"partial prefix no match", "data-backup/file.txt", []string{"data/"}, false},
		{"nested safe", "data/github/comments/pr_1.json", []string{"data/github/"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := matchesSafePrefix(tt.path, tt.prefixes)
			if got != tt.want {
				t.Errorf("matchesSafePrefix(%q, %v) = %v, want %v", tt.path, tt.prefixes, got, tt.want)
			}
		})
	}
}
