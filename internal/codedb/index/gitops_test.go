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
	got, err := RepoNameFromURL("https://github.com/ylow/SFrameRust/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "github.com/ylow/SFrameRust" {
		t.Errorf("got %q", got)
	}
}
