package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/carts"
)

// TestCartStartOutputJSON verifies the JSON that runCartsStart emits in its
// --json branch (cartStartOutput) carries a non-empty `guidance` key with the
// floor naming intent AND preserves the embedded issue's fields. This is the
// Layer-1 contract: Codex/Droid agents read only this JSON and must still
// receive the "name this work unit after the cart title" instruction even
// though they install no ox-* command bodies.
func TestCartStartOutputJSON(t *testing.T) {
	issue := &carts.Issue{
		ID:        "ox-test-1",
		Title:     "Auth middleware rate limiting",
		Status:    carts.Status("in_progress"),
		Priority:  2,
		IssueType: carts.IssueType("task"),
		Assignee:  "alice",
	}

	data, err := json.MarshalIndent(cartStartOutput{Issue: issue, Guidance: cartStartGuidance}, "", "  ")
	if err != nil {
		t.Fatalf("marshal cartStartOutput: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal output JSON: %v", err)
	}

	tests := []struct {
		name      string
		key       string
		substring string
	}{
		{name: "guidance present and non-empty", key: "guidance", substring: ""},
		{name: "guidance carries kebab-case naming intent", key: "guidance", substring: "kebab-case"},
		{name: "guidance references the cart title", key: "guidance", substring: "cart title"},
		// embedded issue fields must survive alongside guidance.
		{name: "issue id preserved", key: "id", substring: "ox-test-1"},
		{name: "issue title preserved", key: "title", substring: "Auth middleware rate limiting"},
		{name: "issue status preserved", key: "status", substring: "in_progress"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, ok := out[tc.key]
			if !ok {
				t.Fatalf("missing %q key in start JSON: %s", tc.key, data)
			}
			val, ok := raw.(string)
			if !ok {
				t.Fatalf("%q is not a string: %T", tc.key, raw)
			}
			if val == "" {
				t.Fatalf("%q is empty in start JSON", tc.key)
			}
			if tc.substring != "" && !strings.Contains(val, tc.substring) {
				t.Fatalf("%q = %q, want substring %q", tc.key, val, tc.substring)
			}
		})
	}
}

// TestCartStartGuidancePortable guards against the floor regression: the
// guidance must be host-agnostic. The Claude-specific /rename mechanism is a
// Layer-2 ergonomic note in the command body and must NOT leak into the CLI
// JSON that all agents consume.
func TestCartStartGuidancePortable(t *testing.T) {
	if cartStartGuidance == "" {
		t.Fatal("cartStartGuidance must not be empty")
	}
	for _, banned := range []string{"/rename", "Claude"} {
		if strings.Contains(cartStartGuidance, banned) {
			t.Errorf("cartStartGuidance leaks host-specific token %q: %q", banned, cartStartGuidance)
		}
	}
}
