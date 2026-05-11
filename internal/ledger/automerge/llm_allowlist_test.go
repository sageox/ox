package automerge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsAllowedLLMBinary covers the ox-7esb allowlist semantics.
func TestIsAllowedLLMBinary(t *testing.T) {
	cases := map[string]bool{
		"":                 false,
		"claude":           true,
		"gemini":           true,
		"codex":            true,
		"CLAUDE":           true, // case-insensitive bare name
		"evil":             false,
		"/usr/local/bin/claude": true, // absolute path passes through
		"/tmp/evil":             true, // operator-chosen absolute paths trusted
		"./evil":           false, // relative with separator refused
		"bin/claude":       false, // any path component refused
		"claude/../evil":   false, // path traversal style refused
	}
	for binary, want := range cases {
		t.Run(binary, func(t *testing.T) {
			assert.Equal(t, want, isAllowedLLMBinary(binary))
		})
	}
}
