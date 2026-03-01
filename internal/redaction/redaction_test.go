package redaction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockLLM struct {
	responses []string
	calls     int
	err       error
}

func (m *mockLLM) Redact(_ context.Context, content string, _ string) (string, error) {
	m.calls++
	if m.err != nil {
		// fail only on first call to test retry
		if m.calls == 1 {
			return "", m.err
		}
	}
	if m.calls <= len(m.responses) {
		return m.responses[m.calls-1], nil
	}
	return "[REDACTED] " + content, nil
}

type alwaysFailLLM struct {
	calls int
}

func (m *alwaysFailLLM) Redact(_ context.Context, _ string, _ string) (string, error) {
	m.calls++
	return "", errors.New("llm unavailable")
}

func TestRedact(t *testing.T) {
	tests := []struct {
		name    string
		content string
		llm     LLMClient
		wantErr bool
		want    string
	}{
		{
			name:    "empty content returns empty",
			content: "",
			llm:     &mockLLM{},
			want:    "",
		},
		{
			name:    "content redacted via LLM",
			content: "send to john@example.com",
			llm:     &mockLLM{responses: []string{"send to [email]"}},
			want:    "send to [email]",
		},
		{
			name:    "retry on first failure",
			content: "test content",
			llm:     &mockLLM{err: errors.New("transient"), responses: []string{"", "redacted content"}},
			want:    "redacted content",
		},
		{
			name:    "permanent failure returns error",
			content: "test content",
			llm:     &alwaysFailLLM{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRedactor(t.TempDir(), tt.llm)
			result, err := r.Redact(context.Background(), tt.content, RedactOpts{})

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.want {
				t.Errorf("got %q, want %q", result, tt.want)
			}
		})
	}
}

func TestRedactBatch(t *testing.T) {
	llm := &mockLLM{responses: []string{"[REDACTED] a" + entrySeparator + "[REDACTED] b"}}
	r := NewRedactor(t.TempDir(), llm)

	results, err := r.RedactBatch(context.Background(), []string{"a", "b"}, RedactOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0] != "[REDACTED] a" {
		t.Errorf("results[0] = %q, want %q", results[0], "[REDACTED] a")
	}
	if results[1] != "[REDACTED] b" {
		t.Errorf("results[1] = %q, want %q", results[1], "[REDACTED] b")
	}
}

func TestRedactBatchEmpty(t *testing.T) {
	llm := &mockLLM{}
	r := NewRedactor(t.TempDir(), llm)

	results, err := r.RedactBatch(context.Background(), []string{}, RedactOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil for empty batch, got %v", results)
	}
}

func TestLoadGuidance(t *testing.T) {
	t.Run("default guidance when no files", func(t *testing.T) {
		r := NewRedactor(t.TempDir(), &mockLLM{})
		guidance, err := r.LoadGuidance("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(guidance, "Always Redact") {
			t.Error("expected default guidance")
		}
	})

	t.Run("team guidance loaded", func(t *testing.T) {
		dir := t.TempDir()
		govDir := filepath.Join(dir, "docs", "governance")
		if err := os.MkdirAll(govDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(govDir, "REDACT.md"), []byte("# Team Rules\nRedact all names"), 0o644); err != nil {
			t.Fatal(err)
		}

		r := NewRedactor(dir, &mockLLM{})
		guidance, err := r.LoadGuidance("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(guidance, "Team Rules") {
			t.Errorf("expected team guidance, got %q", guidance)
		}
	})

	t.Run("integration guidance merged", func(t *testing.T) {
		dir := t.TempDir()

		govDir := filepath.Join(dir, "docs", "governance")
		if err := os.MkdirAll(govDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(govDir, "REDACT.md"), []byte("# Team defaults"), 0o644); err != nil {
			t.Fatal(err)
		}

		slackDir := filepath.Join(dir, "data", "slack")
		if err := os.MkdirAll(slackDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(slackDir, "REDACT.md"), []byte("# Slack overrides"), 0o644); err != nil {
			t.Fatal(err)
		}

		r := NewRedactor(dir, &mockLLM{})
		guidance, err := r.LoadGuidance("slack")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(guidance, "Team defaults") {
			t.Error("expected team defaults in merged guidance")
		}
		if !strings.Contains(guidance, "Slack overrides") {
			t.Error("expected slack overrides in merged guidance")
		}
	})

	t.Run("cache hit avoids re-read", func(t *testing.T) {
		dir := t.TempDir()
		r := NewRedactor(dir, &mockLLM{})

		g1, _ := r.LoadGuidance("")
		g2, _ := r.LoadGuidance("")
		if g1 != g2 {
			t.Error("expected identical cached result")
		}
	})

	t.Run("invalidate cache forces re-read", func(t *testing.T) {
		dir := t.TempDir()
		r := NewRedactor(dir, &mockLLM{})

		_, _ = r.LoadGuidance("")
		r.InvalidateCache()

		// after invalidation, should re-read (still default since no files)
		g, err := r.LoadGuidance("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(g, "Always Redact") {
			t.Error("expected default guidance after cache invalidation")
		}
	})
}

func TestDefaultGuidance(t *testing.T) {
	g := DefaultGuidance()
	if !strings.Contains(g, "REDACTED_SECRET") {
		t.Error("default guidance should mention REDACTED_SECRET")
	}
	if !strings.Contains(g, "Preserve") {
		t.Error("default guidance should have Preserve section")
	}
}
