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
			got := matchesSafePrefix(tt.path, tt.prefixes, nil)
			if got != tt.want {
				t.Errorf("matchesSafePrefix(%q, %v, nil) = %v, want %v", tt.path, tt.prefixes, got, tt.want)
			}
		})
	}
}

func TestMatchesSafePrefix_WithDenies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		path     string
		prefixes []string
		denies   []string
		want     bool
	}{
		{
			"denied subdir blocked",
			"data/proprietary/secrets.json",
			[]string{"data/"},
			[]string{"data/proprietary/"},
			false,
		},
		{
			"non-denied subdir allowed",
			"data/github/prs.json",
			[]string{"data/"},
			[]string{"data/proprietary/"},
			true,
		},
		{
			"deny exact file",
			"data/special.json",
			[]string{"data/"},
			[]string{"data/special.json"},
			false,
		},
		{
			"empty denies = no effect",
			"data/github/prs.json",
			[]string{"data/"},
			nil,
			true,
		},
		{
			"deny wins over prefix at same specificity",
			"data/proprietary/report.pdf",
			[]string{"data/", "data/proprietary/"},
			[]string{"data/proprietary/"},
			false,
		},
		{
			"3-level nesting: more specific allow overrides deny",
			"data/proprietary/public/readme.md",
			[]string{"data/", "data/proprietary/public/"},
			[]string{"data/proprietary/"},
			true,
		},
		{
			"3-level nesting: deny still blocks non-public",
			"data/proprietary/secrets.json",
			[]string{"data/", "data/proprietary/public/"},
			[]string{"data/proprietary/"},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := matchesSafePrefix(tt.path, tt.prefixes, tt.denies)
			if got != tt.want {
				t.Errorf("matchesSafePrefix(%q, %v, %v) = %v, want %v",
					tt.path, tt.prefixes, tt.denies, got, tt.want)
			}
		})
	}
}
