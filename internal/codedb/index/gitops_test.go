package index

import "testing"

func TestRepoDirFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/ylow/SFrameRust/", "github.com/ylow/SFrameRust.git"},
		{"https://github.com/ylow/SFrameRust.git", "github.com/ylow/SFrameRust.git"},
		{"https://github.com/ylow/SFrameRust", "github.com/ylow/SFrameRust.git"},
		{"git://github.com/ylow/SFrameRust", "github.com/ylow/SFrameRust.git"},
	}
	for _, tt := range tests {
		got, err := RepoDirFromURL(tt.url)
		if err != nil {
			t.Fatalf("RepoDirFromURL(%q): %v", tt.url, err)
		}
		if got != tt.want {
			t.Errorf("RepoDirFromURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestRepoDirFromURLInvalid(t *testing.T) {
	_, err := RepoDirFromURL("https://")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestRepoNameFromURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{name: "valid https", url: "https://github.com/ylow/SFrameRust/", want: "github.com/ylow/SFrameRust"},
		{name: "valid git", url: "git://github.com/ylow/SFrameRust", want: "github.com/ylow/SFrameRust"},
		{name: "strips .git suffix", url: "https://github.com/ylow/SFrameRust.git", want: "github.com/ylow/SFrameRust"},
		{name: "empty after scheme strip", url: "https://", wantErr: true},
		{name: "empty string", url: "", wantErr: true},
		{name: "only slashes", url: "https:///", wantErr: true},
		{name: "scheme with .git only", url: "http://.git", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RepoNameFromURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("RepoNameFromURL(%q): expected error", tt.url)
				}
				return
			}
			if err != nil {
				t.Fatalf("RepoNameFromURL(%q): %v", tt.url, err)
			}
			if got != tt.want {
				t.Fatalf("RepoNameFromURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}
