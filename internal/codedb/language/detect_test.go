package language

import "testing"

func TestDetect(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"src/main.rs", "rust"},
		{"lib.py", "python"},
		{"Cargo.toml", "toml"},
		{"README.md", "markdown"},
		{"Makefile", ""},
		{"foo.bar.rs", "rust"},
		{"app.tsx", "tsx"},
		{"main.go", "go"},
		{"style.css", "css"},
		{"test.cpp", "cpp"},
		{"header.hpp", "cpp"},
		{"main.c", "c"},
		{"header.h", "c"},
	}
	for _, tt := range tests {
		got := Detect(tt.path)
		if got != tt.want {
			t.Errorf("Detect(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
