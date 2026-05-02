package gitutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "standard oauth2 token",
			input:    "https://oauth2:glpat-xxxx@gitlab.com/org/repo.git",
			expected: "https://oauth2:***@gitlab.com/org/repo.git",
		},
		{
			name:     "multiple tokens in one string",
			input:    "remote: https://oauth2:tok1@host1.com failed, fallback https://oauth2:tok2@host2.com",
			expected: "remote: https://oauth2:***@host1.com failed, fallback https://oauth2:***@host2.com",
		},
		{
			name:     "no credentials",
			input:    "fatal: repository 'https://github.com/org/repo.git' not found",
			expected: "fatal: repository 'https://github.com/org/repo.git' not found",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "long PAT",
			input:    "https://oauth2:glpat-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx@gitlab.com/repo.git",
			expected: "https://oauth2:***@gitlab.com/repo.git",
		},
		{
			name:     "token with special characters",
			input:    "https://oauth2:abc123!#$%^&*()_+-=xyz@host.com/repo.git",
			expected: "https://oauth2:***@host.com/repo.git",
		},
		{
			name:     "non-oauth2 URL unchanged",
			input:    "https://user:password@host.com/repo.git",
			expected: "https://***:***@host.com/repo.git",
		},
		{
			name:     "keychain noise only",
			input:    "fatal: failed to store: -25300\n",
			expected: "",
		},
		{
			name:     "keychain noise mixed with real errors",
			input:    "fatal: failed to store: -25300\nfatal: repository not found\n",
			expected: "fatal: repository not found",
		},
		{
			name:     "keychain noise surrounded by real output",
			input:    "Enumerating objects: 5, done.\nfatal: failed to store: -25300\nWriting objects: 100%\n",
			expected: "Enumerating objects: 5, done.\nWriting objects: 100%",
		},
		{
			name:     "credential and keychain noise both cleaned",
			input:    "fatal: failed to store: -25300\nhttps://oauth2:glpat-xxxx@gitlab.com/org/repo.git\n",
			expected: "https://oauth2:***@gitlab.com/org/repo.git",
		},
		{
			name:     "bearer token",
			input:    "Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig",
			expected: "Authorization: Bearer ***",
		},
		{
			name:     "bearer token lowercase",
			input:    "header: bearer glpat-xxxxxxxxxxxx",
			expected: "header: Bearer ***",
		},
		{
			name:     "x-access-token in URL",
			input:    "https://x-access-token:ghs_xxxxxxxxxxxx@github.com/org/repo.git",
			expected: "https://x-access-token:***@github.com/org/repo.git",
		},
		{
			name:     "generic user:pass URL",
			input:    "https://user:s3cretP4ss@host.example.com/repo.git",
			expected: "https://***:***@host.example.com/repo.git",
		},
		{
			name:     "oauth2 still works with new patterns",
			input:    "https://oauth2:glpat-xxxx@gitlab.com/org/repo.git",
			expected: "https://oauth2:***@gitlab.com/org/repo.git",
		},
		{
			name:     "multiple credential types in one string",
			input:    "remote https://oauth2:tok@host.com auth: Bearer eyJ123 fallback https://x-access-token:ghs@gh.com",
			expected: "remote https://oauth2:***@host.com auth: Bearer *** fallback https://x-access-token:***@gh.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, SanitizeOutput(tt.input))
		})
	}
}
