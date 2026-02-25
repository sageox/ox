package agentinstance

import (
	"strings"
	"testing"
)

func TestLooksLikeUUID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"019568a5-e1e2-7cd1-8da8-b2d7440e3aab", true},
		{"00000000-0000-0000-0000-000000000000", true},
		{"ffffffff-ffff-ffff-ffff-ffffffffffff", true},
		{"019568a5e1e27cd18da8b2d7440e3aab", false},  // no hyphens
		{"019568a5-e1e2-7cd1-8da8-b2d7440e3aa", false}, // too short (35)
		{"019568a5-e1e2-7cd1-8da8-b2d7440e3aabb", false}, // too long (37)
		{"019568a5-e1e27cd1-8da8-b2d7440e3aab", false}, // wrong hyphen positions (missing at 13)
		{"OxA1b2", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := looksLikeUUID(tt.input); got != tt.want {
				t.Errorf("looksLikeUUID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestClassifyBadID(t *testing.T) {
	tests := []struct {
		input       string
		wantSubstr  string
		wantEmpty   bool
	}{
		// valid agent ID — returns empty
		{"OxA1b2", "", true},

		// UUID — targeted message
		{"019568a5-e1e2-7cd1-8da8-b2d7440e3aab", "looks like a UUID", false},

		// server session ID
		{"oxsid_01KCJECKEGETGX6HC80NRYVZ3P", "server session ID", false},

		// wrong Ox format (wrong case)
		{"ox1234", "invalid agent ID format", false},
		{"OX1234", "invalid agent ID format", false},

		// wrong Ox format (wrong length)
		{"Ox12345", "invalid agent ID format", false},
		{"Ox123", "invalid agent ID format", false},

		// truly unknown — returns empty (caller uses generic message)
		{"foobar", "", true},
		{"session", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ClassifyBadID(tt.input)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("ClassifyBadID(%q) = %q, want empty", tt.input, got)
				}
				return
			}
			if !strings.Contains(got, tt.wantSubstr) {
				t.Errorf("ClassifyBadID(%q) = %q, want substring %q", tt.input, got, tt.wantSubstr)
			}
			// all non-empty messages should mention ox agent prime
			if !strings.Contains(got, "ox agent prime") {
				t.Errorf("ClassifyBadID(%q) = %q, should mention 'ox agent prime'", tt.input, got)
			}
		})
	}
}
