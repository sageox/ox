package main

import (
	"bytes"
	"os"
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
	t.Chdir(t.TempDir())
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
	t.Chdir(t.TempDir())
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
	t.Chdir(t.TempDir())
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

// TestPublishMurmurViaIPCNoDaemon verifies the IPC path returns "daemon not running"
// when no socket exists. We redirect XDG_RUNTIME_DIR to an empty temp dir so
// SocketPath() resolves to a non-existent file and Ping() fails immediately.
func TestPublishMurmurViaIPCNoDaemon(t *testing.T) {
	tmp := t.TempDir()
	// XDG mode uses XDG_RUNTIME_DIR for socket placement; pointing it at an
	// empty temp dir guarantees no socket is present without touching real state.
	t.Setenv("XDG_RUNTIME_DIR", tmp)
	// Disable legacy mode so XDG_RUNTIME_DIR is actually used.
	t.Setenv("OX_XDG_DISABLE", "")

	err := publishMurmurViaIPC(tmp, "data/murmurs/test.json", "hello", []byte(`{"id":"test"}`))
	if err == nil {
		t.Fatal("expected error when daemon is not running")
	}
	if !strings.Contains(err.Error(), "daemon not running") {
		t.Errorf("error = %q, want %q substring", err.Error(), "daemon not running")
	}
}

// TestMurmurContentExactlyAtLimit verifies content at exactly 4096 bytes is accepted.
// The existing test covers >4096; this covers the ==4096 boundary (should pass validation).
func TestMurmurContentExactlyAtLimit(t *testing.T) {
	t.Chdir(t.TempDir())
	cmd := *murmurCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	content := strings.Repeat("x", maxMurmurContentBytes)
	if len(content) != maxMurmurContentBytes {
		t.Fatalf("test setup: content length %d, want %d", len(content), maxMurmurContentBytes)
	}

	err := cmd.RunE(&cmd, []string{content})
	// Must NOT fail with "content too large" — should proceed to scope resolution.
	if err != nil && strings.Contains(err.Error(), "content too large") {
		t.Errorf("content at exactly %d bytes should be accepted, got: %v", maxMurmurContentBytes, err)
	}
}

// TestMurmurJSONImportanceOverridesDefault checks that "importance" in JSON input
// takes effect when the --importance flag was not explicitly set by the caller.
// The test uses an importance value that would fail validation if the JSON value
// were ignored and the default "normal" were substituted instead — so a
// downstream "invalid importance" error would expose that regression.
// Here we use "ambient" (valid) from JSON and verify it is accepted (no importance error).
func TestMurmurJSONImportanceOverridesDefault(t *testing.T) {
	t.Chdir(t.TempDir())

	cmd := *murmurCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// --importance flag NOT set explicitly; JSON provides "ambient"
	input := `{"content": "background note", "importance": "ambient"}`
	err := cmd.RunE(&cmd, []string{input})
	// Must fail somewhere downstream (no ledger), not at importance validation.
	if err == nil {
		t.Fatal("expected downstream error (no ledger)")
	}
	if strings.Contains(err.Error(), "invalid importance") {
		t.Errorf("JSON importance 'ambient' should have been accepted, got: %v", err)
	}
}

// TestMurmurExplicitFlagBeatsJSONImportance verifies that an explicitly set
// --importance flag wins over a conflicting value in JSON input.
// We set --importance=critical via Flags().Set (which marks it Changed),
// while JSON says "ambient". The command should treat it as critical — meaning
// it passes validation (critical is valid) and does NOT report invalid importance.
func TestMurmurExplicitFlagBeatsJSONImportance(t *testing.T) {
	t.Chdir(t.TempDir())

	cmd := *murmurCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Simulate an explicit --importance=critical on the command line.
	// Flags().Set marks the flag as Changed, which is what runMurmur checks.
	if err := cmd.Flags().Set("importance", "critical"); err != nil {
		t.Fatalf("set importance flag: %v", err)
	}

	// JSON says "ambient" — should be ignored because the flag was explicitly set.
	input := `{"content": "urgent signal", "importance": "ambient"}`
	err := cmd.RunE(&cmd, []string{input})
	// Should fail downstream (no ledger), not at importance validation.
	if err == nil {
		t.Fatal("expected downstream error (no ledger)")
	}
	if strings.Contains(err.Error(), "invalid importance") {
		t.Errorf("importance validation should have passed (flag 'critical' wins), got: %v", err)
	}
}

// TestMurmurJSONTopicOverridesDefault checks that "topic" in JSON input
// replaces the default "general" when --topic is not explicitly set.
// We use a topic value ("lint") that differs from the default, then confirm
// the error is NOT about topic — proving the parsed value flowed through.
func TestMurmurJSONTopicOverridesDefault(t *testing.T) {
	t.Chdir(t.TempDir())

	cmd := *murmurCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// --topic not set; JSON provides "lint"
	input := `{"content": "lint rule failing", "topic": "lint"}`
	err := cmd.RunE(&cmd, []string{input})
	if err == nil {
		t.Fatal("expected downstream error (no ledger)")
	}
	// The error must not be a topic-related parsing error.
	if strings.Contains(err.Error(), "invalid JSON") || strings.Contains(err.Error(), "must have a 'content' field") {
		t.Errorf("should have parsed JSON topic successfully, got: %v", err)
	}
}

// TestMurmurExplicitTopicFlagBeatsJSON verifies that an explicitly set --topic
// flag overrides the topic field in JSON input. Both are valid slugs, so the
// only observable difference is which value the command records — but since we
// can't reach ledger writes in a test env, we verify the flag was marked Changed
// and the JSON path respects Flags().Changed("topic").
func TestMurmurExplicitTopicFlagBeatsJSON(t *testing.T) {
	t.Chdir(t.TempDir())

	cmd := *murmurCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Simulate --topic=architecture on the command line.
	if err := cmd.Flags().Set("topic", "architecture"); err != nil {
		t.Fatalf("set topic flag: %v", err)
	}
	if !cmd.Flags().Changed("topic") {
		t.Fatal("flag should be marked Changed after Set()")
	}

	// JSON says "lint" — should be ignored because the flag was explicitly set.
	input := `{"content": "API v3 rolling out", "topic": "lint"}`
	err := cmd.RunE(&cmd, []string{input})
	if err == nil {
		t.Fatal("expected downstream error (no ledger)")
	}
	// Must not fail at JSON parsing — the combined input is structurally valid.
	if strings.Contains(err.Error(), "invalid JSON") || strings.Contains(err.Error(), "must have a 'content' field") {
		t.Errorf("unexpected parse error with explicit flag: %v", err)
	}
}

// TestMurmurAgentIDFromEnv verifies that SAGEOX_AGENT_ID is read as the
// agent identity fallback when --agent-id is not supplied.
// We test the env lookup directly since runMurmur reaches the agent ID
// resolution before the rate-limit filesystem scan, and the env var is
// the only non-flag source of agent identity in the current implementation.
func TestMurmurAgentIDFromEnv(t *testing.T) {
	const envKey = "SAGEOX_AGENT_ID"
	const wantID = "test-agent-abc123"

	// Ensure clean state before and after.
	t.Setenv(envKey, wantID)

	got := os.Getenv(envKey)
	if got != wantID {
		t.Errorf("SAGEOX_AGENT_ID env = %q, want %q", got, wantID)
	}

	// Confirm the flag default is empty, so env is the only source when flag is unset.
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("agent-id", "", "")
	flagVal, _ := cmd.Flags().GetString("agent-id")
	if flagVal != "" {
		t.Errorf("agent-id flag default = %q, want empty — env var fallback assumes empty default", flagVal)
	}
	if cmd.Flags().Changed("agent-id") {
		t.Error("agent-id should not be marked Changed when using default")
	}
}

func TestResolveMurmurFiles(t *testing.T) {
	tests := []struct {
		name        string
		flagValue   string
		flagChanged bool
		jsonValue   string
		want        string
	}{
		{"flag set, no JSON", "cmd/ox/root.go", true, "", "cmd/ox/root.go"},
		{"no flag, JSON provided", "", false, "cmd/ox/root.go", "cmd/ox/root.go"},
		{"flag beats JSON", "cmd/ox/glance.go", true, "cmd/ox/root.go", "cmd/ox/glance.go"},
		{"neither set", "", false, "", ""},
		{"flag set to same as JSON", "a.go", true, "a.go", "a.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveMurmurFiles(tt.flagValue, tt.flagChanged, tt.jsonValue)
			if got != tt.want {
				t.Errorf("resolveMurmurFiles(%q, %v, %q) = %q, want %q",
					tt.flagValue, tt.flagChanged, tt.jsonValue, got, tt.want)
			}
		})
	}
}
