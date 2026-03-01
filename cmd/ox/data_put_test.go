package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDomainPattern(t *testing.T) {
	tests := []struct {
		name      string
		domain    string
		wantMatch bool
	}{
		// Valid cases
		{
			name:      "single letter",
			domain:    "a",
			wantMatch: true,
		},
		{
			name:      "simple lowercase",
			domain:    "slack",
			wantMatch: true,
		},
		{
			name:      "with numbers",
			domain:    "a1",
			wantMatch: true,
		},
		{
			name:      "kebab-case",
			domain:    "google-meet",
			wantMatch: true,
		},
		{
			name:      "multiple dashes",
			domain:    "my-app-2",
			wantMatch: true,
		},
		{
			name:      "letter with single digit",
			domain:    "jira2",
			wantMatch: true,
		},
		{
			name:      "complex kebab-case",
			domain:    "slack-api-v2",
			wantMatch: true,
		},

		// Invalid cases
		{
			name:      "uppercase letters",
			domain:    "Slack",
			wantMatch: false,
		},
		{
			name:      "all uppercase",
			domain:    "JIRA",
			wantMatch: false,
		},
		{
			name:      "starts with underscore",
			domain:    "_slack",
			wantMatch: false,
		},
		{
			name:      "contains underscores",
			domain:    "slack_data",
			wantMatch: false,
		},
		{
			name:      "contains dots",
			domain:    "slack..data",
			wantMatch: false,
		},
		{
			name:      "starts with dash",
			domain:    "-slack",
			wantMatch: false,
		},
		{
			name:      "ends with dash",
			domain:    "slack-",
			wantMatch: false,
		},
		{
			name:      "empty string",
			domain:    "",
			wantMatch: false,
		},
		{
			name:      "starts with number",
			domain:    "123",
			wantMatch: false,
		},
		{
			name:      "double dash",
			domain:    "google--meet",
			wantMatch: false,
		},
		{
			name:      "mixed case with dash",
			domain:    "Slack-Data",
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domainPattern.MatchString(tt.domain)
			if got != tt.wantMatch {
				t.Errorf("domainPattern.MatchString(%q) = %v, want %v", tt.domain, got, tt.wantMatch)
			}
		})
	}
}

func TestCheckLFSConfig(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string)
		wantPanic bool
	}{
		{
			name: "with valid LFS config",
			setup: func(t *testing.T, dir string) {
				gitattrs := filepath.Join(dir, ".gitattributes")
				content := "data/** filter=lfs diff=lfs merge=lfs -text\n"
				if err := os.WriteFile(gitattrs, []byte(content), 0o644); err != nil {
					t.Fatalf("failed to write .gitattributes: %v", err)
				}
			},
			wantPanic: false,
		},
		{
			name: "without .gitattributes file",
			setup: func(t *testing.T, dir string) {
				// Do nothing - file doesn't exist
			},
			wantPanic: false,
		},
		{
			name: "with .gitattributes but missing data/** pattern",
			setup: func(t *testing.T, dir string) {
				gitattrs := filepath.Join(dir, ".gitattributes")
				content := "*.md filter=lfs diff=lfs merge=lfs -text\n"
				if err := os.WriteFile(gitattrs, []byte(content), 0o644); err != nil {
					t.Fatalf("failed to write .gitattributes: %v", err)
				}
			},
			wantPanic: false,
		},
		{
			name: "with .gitattributes containing data/** among other rules",
			setup: func(t *testing.T, dir string) {
				gitattrs := filepath.Join(dir, ".gitattributes")
				content := "*.md filter=lfs\ndata/** filter=lfs diff=lfs merge=lfs -text\n"
				if err := os.WriteFile(gitattrs, []byte(content), 0o644); err != nil {
					t.Fatalf("failed to write .gitattributes: %v", err)
				}
			},
			wantPanic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			// Should not panic
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("checkLFSConfig panicked: %v", r)
				}
			}()

			checkLFSConfig(dir)
		})
	}
}
