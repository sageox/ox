package codexhistory

import "testing"

// TestIsToolError_ExitCodeNormalization guards against misclassifying a
// successful command as failed when its output uses CRLF line endings.
// Without strings.TrimSpace, "Process exited with code 0\r\n" splits into a
// line ending in "\r", so code == "0\r" != "0" and a successful command is
// reported as an error.
func TestIsToolError_ExitCodeNormalization(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"empty output", "", false},
		{"success LF", "Command: echo hi\nProcess exited with code 0\n", false},
		{"success CRLF", "Command: echo hi\r\nProcess exited with code 0\r\n", false},
		{"failure LF", "Command: false\nProcess exited with code 1\n", true},
		{"failure CRLF", "Command: false\r\nProcess exited with code 1\r\n", true},
		{"no exit line", "some unrelated output\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsToolError(tc.output); got != tc.want {
				t.Errorf("IsToolError(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}
