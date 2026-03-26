package main

import (
	"strings"
	"testing"
)

func TestBuildIncompleteSessionHumanResult_SingleSession(t *testing.T) {
	t.Parallel()
	incomplete := []IncompleteSessionInfo{
		{SessionID: "2026-03-01-abc", Missing: []string{"summary.md", "session.html"}},
	}
	result := buildIncompleteSessionHumanResult(incomplete)

	if result.passed != true {
		t.Error("expected passed=true for warning check")
	}
	if result.warning != true {
		t.Error("expected warning=true")
	}
	if !result.requiresAgent {
		t.Error("expected requiresAgent=true")
	}
	if result.message != "1 found" {
		t.Errorf("expected message '1 found', got %q", result.message)
	}
	if !strings.Contains(result.detail, "2026-03-01-abc") {
		t.Errorf("expected detail to contain session ID, got %q", result.detail)
	}
	if !strings.Contains(result.detail, "summary.md, session.html") {
		t.Errorf("expected detail to list missing files, got %q", result.detail)
	}
}

func TestBuildIncompleteSessionHumanResult_MoreThanFive(t *testing.T) {
	t.Parallel()
	var incomplete []IncompleteSessionInfo
	for i := 0; i < 8; i++ {
		incomplete = append(incomplete, IncompleteSessionInfo{
			SessionID: strings.Repeat("s", i+1),
			Missing:   []string{"summary.md"},
		})
	}
	result := buildIncompleteSessionHumanResult(incomplete)

	if result.message != "8 found" {
		t.Errorf("expected '8 found', got %q", result.message)
	}
	// should show "... and 3 more"
	if !strings.Contains(result.detail, "and 3 more") {
		t.Errorf("expected truncation message, got %q", result.detail)
	}
	// only 5 sessions should be listed (not all 8)
	lines := strings.Split(result.detail, "\n")
	sessionLines := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			sessionLines++
		}
	}
	if sessionLines != 5 {
		t.Errorf("expected 5 session lines, got %d", sessionLines)
	}
}

func TestBuildIncompleteSessionHumanResult_ExactlyFive(t *testing.T) {
	t.Parallel()
	var incomplete []IncompleteSessionInfo
	for i := 0; i < 5; i++ {
		incomplete = append(incomplete, IncompleteSessionInfo{
			SessionID: "session-" + string(rune('a'+i)),
			Missing:   []string{"session.html"},
		})
	}
	result := buildIncompleteSessionHumanResult(incomplete)

	if strings.Contains(result.detail, "more") {
		t.Error("should not show 'more' when exactly 5 sessions")
	}
}

func TestBuildIncompleteSessionAgentResult_SingleSession(t *testing.T) {
	t.Parallel()
	incomplete := []IncompleteSessionInfo{
		{SessionID: "test-session", Missing: []string{"summary.md"}},
	}
	result := buildIncompleteSessionAgentResult(incomplete)

	if result.message != "1 found" {
		t.Errorf("expected '1 found', got %q", result.message)
	}
	if !result.requiresAgent {
		t.Error("expected requiresAgent=true")
	}
	if !result.detailRaw {
		t.Error("expected detailRaw=true for agent result")
	}
	// agent result should contain command suggestions
	if !strings.Contains(result.detail, "ox agent") {
		t.Errorf("expected detail to contain 'ox agent' command, got %q", result.detail)
	}
}

func TestBuildIncompleteSessionAgentResult_MoreThanFive(t *testing.T) {
	t.Parallel()
	var incomplete []IncompleteSessionInfo
	for i := 0; i < 7; i++ {
		incomplete = append(incomplete, IncompleteSessionInfo{
			SessionID: "s" + string(rune('0'+i)),
			Missing:   []string{"session.html"},
		})
	}
	result := buildIncompleteSessionAgentResult(incomplete)

	if result.message != "7 found" {
		t.Errorf("expected '7 found', got %q", result.message)
	}
	if !strings.Contains(result.detail, "and 2 more") {
		t.Errorf("expected truncation message in agent result, got %q", result.detail)
	}
}
