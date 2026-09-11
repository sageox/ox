package main

import (
	"errors"
	"strings"
	"testing"
)

// TestKBAutoFixHint covers the two shapes that previously produced unusable
// doctor output: an error that formats to nothing (rendered as a bare
// "Auto-fix hint: " — a label with no content, which reads as a bug in ox), and
// a raw transport error like "i/o timeout" on a socket path, which is true but
// tells the reader nothing they can act on.
func TestKBAutoFixHint(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "empty error still yields an actionable line",
			err:         errors.New(""),
			wantContain: []string{"daemon did not complete", "ox daemon status"},
			// the old bug: a label with nothing after it
			wantAbsent: []string{"Auto-fix hint:"},
		},
		{
			name:        "nil error still yields an actionable line",
			err:         nil,
			wantContain: []string{"ox daemon status"},
			wantAbsent:  []string{"Auto-fix hint:"},
		},
		{
			name: "transport timeout is translated, and the raw text kept for support",
			err:  errors.New("dial unix /tmp/ox.sock: i/o timeout"),
			wantContain: []string{
				"daemon did not complete", "ox daemon status", "i/o timeout",
			},
		},
		{
			name:        "connection refused is a daemon-liveness problem",
			err:         errors.New("connection refused"),
			wantContain: []string{"ox daemon status"},
		},
		{
			name: "a real, actionable error is passed through as the hint",
			err:  errors.New("bubble kb_abc has no configured remote"),
			wantContain: []string{
				"Auto-fix hint:", "no configured remote",
			},
			wantAbsent: []string{"ox daemon status"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := kbAutoFixHint(tc.err)
			if strings.TrimSpace(got) == "" {
				t.Fatal("hint must never be empty — an empty detail reads as a broken check")
			}
			for _, want := range tc.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q\n--- got ---\n%s", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("should not contain %q\n--- got ---\n%s", absent, got)
				}
			}
		})
	}
}
