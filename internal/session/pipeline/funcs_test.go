package pipeline

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
)

func TestFilterEntriesAfterStart(t *testing.T) {
	base := time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC)

	entries := []adapters.RawEntry{
		{Timestamp: base.Add(-5 * time.Minute), Role: "user", Content: "before"},
		{Timestamp: base.Add(-1 * time.Minute), Role: "user", Content: "also before"},
		{Timestamp: base, Role: "user", Content: "at start"},
		{Timestamp: base.Add(1 * time.Minute), Role: "assistant", Content: "after"},
		{Timestamp: time.Time{}, Role: "system", Content: "no timestamp"},
	}

	filtered := FilterEntriesAfterStart(entries, base)

	if len(filtered) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(filtered))
	}
	if filtered[0].Content != "at start" {
		t.Errorf("first entry: got %q, want 'at start'", filtered[0].Content)
	}
	if filtered[1].Content != "after" {
		t.Errorf("second entry: got %q, want 'after'", filtered[1].Content)
	}
	if filtered[2].Content != "no timestamp" {
		t.Errorf("third entry: got %q, want 'no timestamp'", filtered[2].Content)
	}
}

func TestFilterEntriesAfterStartAllBefore(t *testing.T) {
	base := time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC)
	entries := []adapters.RawEntry{
		{Timestamp: base.Add(-5 * time.Minute), Role: "user", Content: "old"},
	}
	filtered := FilterEntriesAfterStart(entries, base)
	if len(filtered) != 0 {
		t.Errorf("expected 0 entries, got %d", len(filtered))
	}
}

func TestFilterEntriesAfterStartEmpty(t *testing.T) {
	filtered := FilterEntriesAfterStart(nil, time.Now())
	if len(filtered) != 0 {
		t.Errorf("expected 0 entries, got %d", len(filtered))
	}
}

func TestIsAuthRelatedError(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"authentication required", true},
		{"HTTP 401 Unauthorized", true},
		{"HTTP 403 Forbidden", true},
		{"no git credentials found", true},
		{"empty token", true},
		{"run 'ox login' first", true},
		{"PAT rejected by server", true},
		{"credential expired", true},
		{"random network error", false},
		{"timeout connecting to server", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			got := IsAuthRelatedError(tt.msg)
			if got != tt.want {
				t.Errorf("IsAuthRelatedError(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestIsGenericAdapter(t *testing.T) {
	if !IsGenericAdapter("generic") {
		t.Error("expected true for 'generic'")
	}
	if IsGenericAdapter("claude-code") {
		t.Error("expected false for 'claude-code'")
	}
	if IsGenericAdapter("") {
		t.Error("expected false for empty string")
	}
}
