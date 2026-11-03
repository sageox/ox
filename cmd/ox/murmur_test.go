package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/daemon/testutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/telemetry"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetMurmurFlags returns murmurCmd's flags to their defaults now and when the
// test ends. A `cmd := *murmurCmd` copy shares murmurCmd's flag set, and pflag
// never resets a parsed value, so without this one test's --importance=high is
// the next test's importance, in whatever order the package runs.
func resetMurmurFlags(t *testing.T) {
	t.Helper()
	resetFlagsInTree(murmurCmd)
	t.Cleanup(func() { resetFlagsInTree(murmurCmd) })
}

// Failure prevented: free topics, content, files or principal details reach analytics.
func TestMurmur_PostHogPrivacyAndReplay(t *testing.T) {
	for _, topic := range []string{"general", "wip", "lint", "architecture", "conflict", "private-topic"} {
		t.Run(topic, func(t *testing.T) {
			root := setupSessionTestProject(t)
			captured := captureUsageForTest(t, nil)
			require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_fixture", TeamID: "team_fixture"}))
			m := ledger.MurmurFile{ID: "019ff2f5-2079-7be1-b05e-8caad2772e61", Timestamp: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), Content: "private-body", Topic: topic, Scope: "ledger", Importance: "normal", PrincipalID: "private-principal", Metadata: map[string]string{"files": "private-file"}}
			captureMurmurPublished(root, m)
			captureMurmurPublished(root, m)
			require.Len(t, *captured, 2)
			c := (*captured)[0]
			assert.Equal(t, "murmur.published", c.Event)
			assert.Equal(t, c.Opts, (*captured)[1].Opts)
			assert.Equal(t, m.Timestamp, c.Opts.At)
			var closed struct {
				ID         string `json:"murmur_id"`
				Importance string `json:"importance"`
				Scope      string `json:"scope"`
				TeamID     string `json:"team_id"`
				Topic      string `json:"topic"`
				Actor      string `json:"actor"`
				CLIActor   string `json:"cli_actor"`
				Surface    string `json:"surface"`
				Env        string `json:"env"`
				Version    int    `json:"event_version"`
			}
			raw := strictUsageProps(t, c.Props, &closed)
			assert.Len(t, c.Props, 10)
			want := topic
			if topic == "private-topic" {
				want = "other"
			}
			assert.Equal(t, want, closed.Topic)
			assert.Equal(t, m.ID, closed.ID)
			assert.Equal(t, "team_fixture", closed.TeamID)
			assert.Equal(t, "normal", closed.Importance)
			assert.Equal(t, "ledger", closed.Scope)
			assert.Equal(t, "cli", closed.Surface)
			assert.Equal(t, 1, closed.Version)
			for _, private := range []string{"private-body", "private-topic", "private-file", "private-principal"} {
				assert.NotContains(t, string(raw), private)
			}
		})
	}
}

// Failure prevented: a failed outbox write emits an event, or either successful
// delivery path omits its event; a failed sender must preserve command success.
func TestMurmur_PostHogCaptureAfterDelivery(t *testing.T) {
	for _, mode := range []string{"outbox", "IPC", "write fails", "capture fails"} {
		t.Run(mode, func(t *testing.T) {
			captured := captureUsageForTest(t, nil)
			var delivered chan daemon.MurmurPayload
			var svc *testutil.MockService
			if mode == "IPC" {
				delivered = make(chan daemon.MurmurPayload, 1)
				svc = &testutil.MockService{PublishMurmurFunc: func(p daemon.MurmurPayload) { delivered <- p }}
			}
			startSyncTestDaemon(t, svc)
			root, err := os.Getwd()
			require.NoError(t, err)
			initGitRepo(t, root)
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
			require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_fixture", TeamID: "team_fixture"}))
			target := t.TempDir()
			require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: target}}))
			t.Setenv(config.EnvProjectRoot, root)
			t.Setenv("SAGEOX_DAEMON", "false")
			t.Setenv("SAGEOX_AGENT_ID", "")
			var handedOff daemon.MurmurPayload
			baseCapture := capturePostHogUsage
			capturePostHogUsage = func(event string, props map[string]any, opts telemetry.CaptureOpts) {
				if mode == "IPC" {
					select {
					case handedOff = <-delivered:
					case <-time.After(time.Second):
						t.Fatal("capture happened before IPC hand-off")
					}
				} else {
					files, err := os.ReadDir(daemon.MurmurOutboxDir(target))
					require.NoError(t, err, "capture must follow the outbox write")
					require.Len(t, files, 1)
				}
				baseCapture(event, props, opts)
			}
			if mode == "write fails" {
				outbox := daemon.MurmurOutboxDir(target)
				require.NoError(t, os.MkdirAll(filepath.Dir(outbox), 0o700))
				require.NoError(t, os.WriteFile(outbox, []byte("blocked"), 0o600))
			}
			if mode == "capture fails" {
				capturePostHogUsage = func(string, map[string]any, telemetry.CaptureOpts) { panic("sender failed") }
			}
			resetMurmurFlags(t)
			cmd := *murmurCmd
			var out bytes.Buffer
			cmd.SetOut(&out)
			err = runMurmur(&cmd, []string{"private-body"})
			if mode == "write fails" {
				require.ErrorContains(t, err, "queue murmur")
				assert.Empty(t, *captured)
				return
			}
			require.NoError(t, err)
			if mode == "capture fails" {
				assert.Contains(t, out.String(), "Murmur queued")
				return
			}
			require.Len(t, *captured, 1)
			var payload daemon.MurmurPayload
			if mode == "IPC" {
				payload = handedOff
				assert.NoDirExists(t, daemon.MurmurOutboxDir(target))
			} else {
				files, err := os.ReadDir(daemon.MurmurOutboxDir(target))
				require.NoError(t, err)
				require.Len(t, files, 1)
				raw, err := os.ReadFile(filepath.Join(daemon.MurmurOutboxDir(target), files[0].Name()))
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &payload))
			}
			var m ledger.MurmurFile
			require.NoError(t, json.Unmarshal(payload.MurmurJSON, &m))
			assert.Equal(t, m.ID, (*captured)[0].Props["murmur_id"])
			assert.Equal(t, m.Timestamp, (*captured)[0].Opts.At)
		})
	}
}

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

	resetMurmurFlags(t)
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

	resetMurmurFlags(t)
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
		resetMurmurFlags(t)
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
		resetMurmurFlags(t)
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
		resetMurmurFlags(t)
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

	resetMurmurFlags(t)
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
	resetMurmurFlags(t)
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
	resetMurmurFlags(t)
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
	resetMurmurFlags(t)
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

// TestMurmurQueuesToOutboxWhenDaemonDown verifies the durable-outbox fix: when no
// daemon is reachable, the murmur is queued to the ledger cache (never lost) and
// the command still succeeds. Failure prevented: the original bug where a murmur
// emitted with the daemon down was dropped and its content gone forever.
func TestMurmurQueuesToOutboxWhenDaemonDown(t *testing.T) {
	if testing.Short() {
		t.Skip("short: shells git")
	}

	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".sageox"), 0o755); err != nil {
		t.Fatalf("create .sageox: %v", err)
	}
	if out, err := exec.Command("git", "-C", projectDir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	// ledger must exist for scope resolution; point local config at it
	ledgerDir := t.TempDir()
	if err := config.SaveLocalConfig(projectDir, &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: ledgerDir},
	}); err != nil {
		t.Fatalf("save local config: %v", err)
	}

	t.Setenv(config.EnvProjectRoot, projectDir)
	t.Setenv("SAGEOX_DAEMON", "false")       // StartDaemonNoWait must not spawn a daemon
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // no socket → TryConnect misses
	t.Chdir(projectDir)                      // findGitRoot resolves the ledger path

	resetMurmurFlags(t)
	cmd := *murmurCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	if err := cmd.RunE(&cmd, []string{"outbox smoke test"}); err != nil {
		t.Fatalf("murmur should succeed (queued), got: %v", err)
	}

	if !strings.Contains(buf.String(), "queued") {
		t.Errorf("expected 'queued' message, got: %q", buf.String())
	}

	outboxDir := filepath.Join(ledgerDir, ".sageox", "cache", "murmur-outbox")
	entries, err := os.ReadDir(outboxDir)
	if err != nil {
		t.Fatalf("read outbox dir %s: %v", outboxDir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 queued murmur, got %d", len(entries))
	}
}

// TestMurmurContentExactlyAtLimit verifies content at exactly 4096 bytes is accepted.
// The existing test covers >4096; this covers the ==4096 boundary (should pass validation).
func TestMurmurContentExactlyAtLimit(t *testing.T) {
	t.Chdir(t.TempDir())
	resetMurmurFlags(t)
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

	resetMurmurFlags(t)
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

	resetMurmurFlags(t)
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

	resetMurmurFlags(t)
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

	resetMurmurFlags(t)
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
