package github

import "testing"

func TestParseGitHubRemote(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantOK    bool
	}{
		{
			name:      "https with .git suffix",
			url:       "https://github.com/sageox/ox.git",
			wantOwner: "sageox",
			wantRepo:  "ox",
			wantOK:    true,
		},
		{
			name:      "https without .git suffix",
			url:       "https://github.com/sageox/ox",
			wantOwner: "sageox",
			wantRepo:  "ox",
			wantOK:    true,
		},
		{
			name:      "ssh git@ format",
			url:       "git@github.com:sageox/ox.git",
			wantOwner: "sageox",
			wantRepo:  "ox",
			wantOK:    true,
		},
		{
			name:      "ssh git@ without .git",
			url:       "git@github.com:sageox/ox",
			wantOwner: "sageox",
			wantRepo:  "ox",
			wantOK:    true,
		},
		{
			name:      "ssh:// protocol",
			url:       "ssh://git@github.com/sageox/ox.git",
			wantOwner: "sageox",
			wantRepo:  "ox",
			wantOK:    true,
		},
		{
			name:   "gitlab url returns false",
			url:    "https://gitlab.com/org/repo.git",
			wantOK: false,
		},
		{
			name:   "empty string",
			url:    "",
			wantOK: false,
		},
		{
			name:   "malformed url missing repo",
			url:    "https://github.com/owneronly",
			wantOK: false,
		},
		{
			name:   "non-url string",
			url:    "not-a-url",
			wantOK: false,
		},
		{
			name:      "http protocol",
			url:       "http://github.com/org/repo.git",
			wantOwner: "org",
			wantRepo:  "repo",
			wantOK:    true,
		},
		{
			name:      "whitespace trimmed",
			url:       "  https://github.com/org/repo.git  ",
			wantOwner: "org",
			wantRepo:  "repo",
			wantOK:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, ok := ParseGitHubRemote(tt.url)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}
