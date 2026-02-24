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
			expected: "https://user:password@host.com/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, SanitizeOutput(tt.input))
		})
	}
}
