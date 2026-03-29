package index

import "testing"

func TestNormalizeRepoURL(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{"https with trailing slash", "https://github.com/org/repo/", "github.com/org/repo", false},
		{"https without trailing slash", "https://github.com/org/repo", "github.com/org/repo", false},
		{"https with .git suffix", "https://github.com/org/repo.git", "github.com/org/repo", false},
		{"https with .git and trailing slash", "https://github.com/org/repo.git/", "github.com/org/repo", false},
		{"http scheme", "http://gitlab.com/org/repo", "gitlab.com/org/repo", false},
		{"git scheme", "git://github.com/org/repo", "github.com/org/repo", false},
		{"no scheme", "github.com/org/repo", "github.com/org/repo", false},
		{"no scheme with .git", "github.com/org/repo.git", "github.com/org/repo", false},
		{"empty after strip", "https://", "", true},
		{"empty string", "", "", true},
		{"only slashes after scheme", "https:///", "", true},
		{"only .git after scheme", "http://.git", "", true},
		{"multiple trailing slashes", "https://github.com/org/repo///", "github.com/org/repo", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeRepoURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeRepoURL(%q): expected error, got %q", tt.url, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeRepoURL(%q): unexpected error: %v", tt.url, err)
			}
			if got != tt.want {
				t.Errorf("normalizeRepoURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestRepoDirFromURL_Schemes(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tests := []struct {
		name string
		url  string
		want string
	}{
		{"http scheme", "http://github.com/org/repo", "github.com/org/repo.git"},
		{"no scheme", "github.com/org/repo", "github.com/org/repo.git"},
		{"git scheme with .git", "git://github.com/org/repo.git", "github.com/org/repo.git"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RepoDirFromURL(tt.url)
			if err != nil {
				t.Fatalf("RepoDirFromURL(%q): %v", tt.url, err)
			}
			if got != tt.want {
				t.Errorf("RepoDirFromURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestRepoDirFromURL_ErrorCases(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	errCases := []string{
		"https://",
		"",
		"https:///",
		"http://.git",
	}

	for _, url := range errCases {
		t.Run(url, func(t *testing.T) {
			_, err := RepoDirFromURL(url)
			if err == nil {
				t.Errorf("RepoDirFromURL(%q): expected error", url)
			}
		})
	}
}
