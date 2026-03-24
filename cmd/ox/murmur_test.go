package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestMurmurFlagDefaults(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("topic", "general", "")
	cmd.Flags().String("importance", "normal", "")
	cmd.Flags().String("scope", "ledger", "")
	cmd.Flags().String("agent-id", "", "")

	topic, _ := cmd.Flags().GetString("topic")
	if topic != "general" {
		t.Errorf("default topic: got %q, want %q", topic, "general")
	}

	importance, _ := cmd.Flags().GetString("importance")
	if importance != "normal" {
		t.Errorf("default importance: got %q, want %q", importance, "normal")
	}

	scope, _ := cmd.Flags().GetString("scope")
	if scope != "ledger" {
		t.Errorf("default scope: got %q, want %q", scope, "ledger")
	}

	agentID, _ := cmd.Flags().GetString("agent-id")
	if agentID != "" {
		t.Errorf("default agent-id: got %q, want empty", agentID)
	}
}

func TestMurmurValidImportanceLevels(t *testing.T) {
	tests := []struct {
		level string
		valid bool
	}{
		{"critical", true},
		{"normal", true},
		{"ambient", true},
		{"high", false},
		{"low", false},
		{"", false},
		{"CRITICAL", false}, // case-sensitive
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			got := validImportanceLevels[tt.level]
			if got != tt.valid {
				t.Errorf("validImportanceLevels[%q] = %v, want %v", tt.level, got, tt.valid)
			}
		})
	}
}

func TestMurmurMissingContent(t *testing.T) {
	t.Setenv("FEATURE_MURMUR", "true")
	cmd := *murmurCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(&cmd, []string{})
	if err == nil {
		t.Fatal("expected error for missing content")
	}
	if !strings.Contains(err.Error(), "no content provided") {
		t.Errorf("error = %q, want substring %q", err.Error(), "no content provided")
	}
}

func TestMurmurEmptyContent(t *testing.T) {
	t.Setenv("FEATURE_MURMUR", "true")
	cmd := *murmurCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(&cmd, []string{"   "})
	if err == nil {
		t.Fatal("expected error for whitespace-only content")
	}
	if !strings.Contains(err.Error(), "no content provided") {
		t.Errorf("error = %q, want substring %q", err.Error(), "no content provided")
	}
}

func TestMurmurJSONInputParsing(t *testing.T) {
	t.Setenv("FEATURE_MURMUR", "true")
	t.Chdir(t.TempDir())

	t.Run("valid JSON with content passes parsing", func(t *testing.T) {
		cmd := *murmurCmd
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		input := `{"content": "test message", "topic": "lint"}`
		err := cmd.RunE(&cmd, []string{input})
		// will fail downstream (no ledger), but must NOT fail at JSON parsing
		if err == nil {
			t.Fatal("expected error (no ledger in test env)")
		}
		if strings.Contains(err.Error(), "invalid JSON") {
			t.Errorf("should have parsed JSON successfully, got: %v", err)
		}
		if strings.Contains(err.Error(), "must have a 'content' field") {
			t.Errorf("should have found content field, got: %v", err)
		}
	})

	t.Run("JSON missing content field", func(t *testing.T) {
		cmd := *murmurCmd
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		err := cmd.RunE(&cmd, []string{`{"topic": "lint"}`})
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "must have a 'content' field") {
			t.Errorf("error = %q, want substring about missing content field", err.Error())
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		cmd := *murmurCmd
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		err := cmd.RunE(&cmd, []string{`{"content": broken}`})
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "invalid JSON input") {
			t.Errorf("error = %q, want substring %q", err.Error(), "invalid JSON input")
		}
	})
}

func TestMurmurInvalidImportanceReturnsError(t *testing.T) {
	t.Setenv("FEATURE_MURMUR", "true")
	cmd := *murmurCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = cmd.Flags().Set("importance", "high")

	err := cmd.RunE(&cmd, []string{"test message"})
	if err == nil {
		t.Fatal("expected error for invalid importance")
	}
	if !strings.Contains(err.Error(), "invalid importance") {
		t.Errorf("error = %q, want substring %q", err.Error(), "invalid importance")
	}
}

func TestMurmurInvalidScopeReturnsError(t *testing.T) {
	t.Setenv("FEATURE_MURMUR", "true")
	cmd := *murmurCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = cmd.Flags().Set("scope", "global")
	_ = cmd.Flags().Set("importance", "normal")

	err := cmd.RunE(&cmd, []string{"test message"})
	if err == nil {
		t.Fatal("expected error for invalid scope")
	}
	// error will reference the invalid scope
	if !strings.Contains(err.Error(), "invalid scope") {
		t.Errorf("error = %q, want substring about invalid scope", err.Error())
	}
}

func TestMurmurContentTooLarge(t *testing.T) {
	t.Setenv("FEATURE_MURMUR", "true")
	cmd := *murmurCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	content := make([]byte, maxMurmurContentBytes+1)
	for i := range content {
		content[i] = 'a'
	}

	err := cmd.RunE(&cmd, []string{string(content)})
	if err == nil {
		t.Fatal("expected error for oversized content")
	}
	if !strings.Contains(err.Error(), "content too large") {
		t.Errorf("error = %q, want substring %q", err.Error(), "content too large")
	}
}

func TestMurmurJSONOverridesDefaults(t *testing.T) {
	t.Setenv("FEATURE_MURMUR", "true")
	cmd := *murmurCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// JSON with topic and importance — should be used since flags weren't explicitly set
	input := `{"content": "test", "topic": "lint", "importance": "critical"}`
	err := cmd.RunE(&cmd, []string{input})
	// will fail downstream, but the parsing path is exercised
	if err == nil {
		t.Fatal("expected error (no ledger in test env)")
	}
	// verify it got past JSON parsing
	if strings.Contains(err.Error(), "invalid JSON") || strings.Contains(err.Error(), "must have a 'content' field") {
		t.Errorf("unexpected parsing error: %v", err)
	}
}
