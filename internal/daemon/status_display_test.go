package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHumanizeError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		expected string
	}{
		{
			name:     "dns timeout from git fetch",
			raw:      "fetch failed: fatal: unable to access 'https://git.sageox.ai/repo.git/': Resolving timed out after 805206 milliseconds (exit status 128)",
			expected: "DNS resolution timed out (network may be offline or DNS is unreachable)",
		},
		{
			name:     "stale project path",
			raw:      "open local repo /Users/ryan/conductor/workspaces/ox/edinburgh-v1: repository does not exist",
			expected: "Project path no longer exists: edinburgh-v1 (workspace may have moved)",
		},
		{
			name:     "could not resolve host",
			raw:      "fetch failed: fatal: unable to access 'https://git.sageox.ai/repo.git/': Could not resolve host: git.sageox.ai",
			expected: "DNS resolution timed out (network may be offline or DNS is unreachable)",
		},
		{
			name:     "connection refused",
			raw:      "fetch failed: fatal: unable to access 'https://git.sageox.ai/repo.git/': Failed to connect: Connection refused",
			expected: "Server unreachable (connection refused)",
		},
		{
			name:     "no such host",
			raw:      "fetch failed: fatal: unable to access 'https://git.sageox.ai/repo.git/': No such host",
			expected: "Server unreachable (DNS lookup failed)",
		},
		{
			name:     "unknown error passes through",
			raw:      "some unknown error happened",
			expected: "some unknown error happened",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := humanizeError(tt.raw)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestGetErrorHint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      string
		wantHint bool
	}{
		{
			name:     "timeout gets retry hint",
			err:      "Git sync timed out (network may be offline)",
			wantHint: true,
		},
		{
			name:     "stale path gets heartbeat hint",
			err:      "Project path no longer exists: edinburgh-v1 (workspace may have moved)",
			wantHint: true,
		},
		{
			name:     "authentication gets credential hint",
			err:      "authentication required",
			wantHint: true,
		},
		{
			name:     "unknown error gets no hint",
			err:      "something else",
			wantHint: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hint := getErrorHint(tt.err)
			if tt.wantHint {
				assert.NotEmpty(t, hint)
			} else {
				assert.Empty(t, hint)
			}
		})
	}
}
