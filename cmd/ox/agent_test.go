package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/spf13/cobra"
)

// TestReinjectSessionFlags verifies the dispatcher reinjects cobra-parsed
// values for --title, --file, --session-id, and --adapter back into
// sessionArgs as positional tokens.
//
// Failure prevented: cobra strips flags it has registered when parsing
// agentCmd. Without reinjection, the manual parsers (parseTitle,
// parseCapturePriorFile, parseSessionID, parseAdapter) used by every
// session subcommand handler would see empty args and silently behave as
// if no flag was passed. The original bug was that --session-id, --title,
// and --file were rejected entirely with "unknown flag"; the FParseErrWhitelist
// fix appears to make them work but actually drops them — this test pins the
// reinjection behavior so a future "simplification" can't quietly regress.
func TestReinjectSessionFlags(t *testing.T) {
	t.Parallel()

	// Use local flags (not persistent) so Set() and GetString() target the
	// same flagset without going through cobra's Execute()/merge step.
	// In production, agentCmd registers these as persistent flags and
	// cobra merges them into cmd.Flags() during Execute() — so the
	// helper sees the values via Flags().GetString(). The behavior under
	// test is the lookup-and-append, which is identical regardless of
	// where the flag was originally declared.
	makeCmd := func() *cobra.Command {
		cmd := &cobra.Command{Use: "agent"}
		cmd.Flags().String("title", "", "")
		cmd.Flags().String("file", "", "")
		cmd.Flags().String("session-id", "", "")
		cmd.Flags().String("adapter", "", "")
		return cmd
	}

	tests := []struct {
		name     string
		setFlags map[string]string
		input    []string
		want     []string
	}{
		{
			name:     "no flags set leaves args unchanged",
			setFlags: nil,
			input:    []string{"existing", "positional"},
			want:     []string{"existing", "positional"},
		},
		{
			name:     "session-id only",
			setFlags: map[string]string{"session-id": "abc-123"},
			input:    []string{},
			want:     []string{"--session-id", "abc-123"},
		},
		{
			name: "all four flags reinjected in stable order",
			setFlags: map[string]string{
				"title":      "My Plan",
				"file":       "/tmp/data.jsonl",
				"session-id": "sess-xyz",
				"adapter":    "claude",
			},
			input: []string{},
			want: []string{
				"--title", "My Plan",
				"--file", "/tmp/data.jsonl",
				"--session-id", "sess-xyz",
				"--adapter", "claude",
			},
		},
		{
			name:     "appends after existing positional args",
			setFlags: map[string]string{"adapter": "codex"},
			input:    []string{"capture-prior-positional"},
			want:     []string{"capture-prior-positional", "--adapter", "codex"},
		},
		{
			name:     "empty string flag not injected",
			setFlags: map[string]string{"title": "", "adapter": "claude"},
			input:    []string{},
			want:     []string{"--adapter", "claude"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := makeCmd()
			for k, v := range tt.setFlags {
				if err := cmd.Flags().Set(k, v); err != nil {
					t.Fatalf("set %s: %v", k, err)
				}
			}
			got := reinjectSessionFlags(cmd, tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("reinjectSessionFlags() = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("nil cmd is a no-op", func(t *testing.T) {
		t.Parallel()
		input := []string{"a", "b"}
		got := reinjectSessionFlags(nil, input)
		if !reflect.DeepEqual(got, input) {
			t.Errorf("reinjectSessionFlags(nil) = %v, want %v", got, input)
		}
	})
}

// TestDispatchedCommandPath verifies that the dispatcher's span-naming
// helper produces the right token sequence for each invocation shape.
//
// Why this matters: dispatchedCommandPath is what determines how a
// dispatcher hit gets bucketed in trace backends. A regression that
// silently returns the wrong tokens (or always returns the same single
// token) re-creates the very "one giant ox agent bucket" problem the
// dispatcher rename was added to fix.
func TestDispatchedCommandPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// what real-world bucketing failure does this prevent
		prevents string
		in       []string
		want     []string
	}{
		{
			name:     "empty",
			prevents: "panic on no-args dispatcher invocation",
			in:       nil,
			want:     nil,
		},
		{
			name:     "doctor_alone",
			prevents: "doctor calls collapsing into a single bucket with other commands",
			in:       []string{"doctor"},
			want:     []string{"doctor"},
		},
		{
			name:     "heartbeat_alone",
			prevents: "heartbeats hiding in the generic bucket — they are the highest-volume call",
			in:       []string{"heartbeat"},
			want:     []string{"heartbeat"},
		},
		{
			name:     "session_with_subsubcommand",
			prevents: "session start / stop / capture-prior all collapsing into one `session` bucket",
			in:       []string{"session", "start"},
			want:     []string{"session", "start"},
		},
		{
			name:     "session_capture_prior",
			prevents: "capture-prior latency / errors hiding inside the `session` bucket",
			in:       []string{"session", "capture-prior", "--adapter=claude"},
			want:     []string{"session", "capture-prior"},
		},
		{
			name:     "session_alone",
			prevents: "panic on bare `session` with no sub-subcommand (validation error path)",
			in:       []string{"session"},
			want:     []string{"session"},
		},
		{
			name:     "session_unknown_subsubcommand_collapses",
			prevents: "an unknown / typo / attacker-controlled session sub-subcommand minting an arbitrary span name (CodeRabbit PR488). Without the allowlist, `ox agent <id> session <typo>` would create a per-typo bucket.",
			in:       []string{"session", "totally-bogus-subcommand"},
			want:     []string{"session"},
		},
		{
			name:     "session_recover_in_allowlist",
			prevents: "regression that drops `recover` from validSessionSubcommands when adding new entries — keep this case in lock-step with runWithAgentID's case statement",
			in:       []string{"session", "recover"},
			want:     []string{"session", "recover"},
		},
		{
			name:     "session_subagent_complete_in_allowlist",
			prevents: "subagent-complete (hyphenated) accidentally getting filtered by an over-eager allowlist",
			in:       []string{"session", "subagent-complete"},
			want:     []string{"session", "subagent-complete"},
		},
		{
			name:     "query_does_not_include_query_text",
			prevents: "high-cardinality query string blowing up span-name cardinality",
			// query has its arg as the search text — must NOT be included
			in:   []string{"query", "how do we handle auth"},
			want: []string{"query"},
		},
		{
			name:     "whisper_alone",
			prevents: "bare whisper getting bucketed with whisper history despite being a different RunE path",
			in:       []string{"whisper"},
			want:     []string{"whisper"},
		},
		{
			name:     "whisper_history_expanded",
			prevents: "merging `whisper history` (its own RunE in runWithAgentID) into the bare `whisper` bucket and losing the observability split (CodeRabbit PR488)",
			in:       []string{"whisper", "history"},
			want:     []string{"whisper", "history"},
		},
		{
			name:     "whisper_unknown_collapses",
			prevents: "an unknown whisper sub-subcommand minting a per-typo span-name bucket — only `history` is a real branch",
			in:       []string{"whisper", "totally-bogus"},
			want:     []string{"whisper"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := dispatchedCommandPath(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("dispatchedCommandPath(%q) = %q, want %q\n(prevents: %s)",
					tt.in, got, tt.want, tt.prevents)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("dispatchedCommandPath(%q)[%d] = %q, want %q\n(prevents: %s)",
						tt.in, i, got[i], tt.want[i], tt.prevents)
				}
			}
		})
	}
}

// TestIsKnownHookEventName covers the dispatcher's hook-phase clamping
// rule. The dispatcher passes the second token of `ox agent hook <X>`
// into the span name only when isKnownHookEventName(X) is true; an
// invalid token collapses to just "ox agent hook".
//
// Failure prevented: a regression that loosens the regex (e.g.
// removes the start anchor or allows whitespace) lets attacker-
// controlled tokens flow into span names. Each test case names the
// shape of input it defends against.
func TestIsKnownHookEventName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		prevents string
		in       string
		want     bool
	}{
		// happy paths — all real adapter event names follow strict PascalCase
		{"PostToolUse", "Claude Code post-tool event", "PostToolUse", true},
		{"PreToolUse", "Claude Code pre-tool event", "PreToolUse", true},
		{"SessionStart", "common across adapters", "SessionStart", true},
		{"SessionEnd", "common lifecycle event", "SessionEnd", true},
		{"BeforeAgent", "gemini-style event name", "BeforeAgent", true},
		{"UserPromptSubmit", "Claude Code prompt event", "UserPromptSubmit", true},
		{"AfterTool", "compact event name", "AfterTool", true},
		{"single_uppercase", "minimum valid PascalCase input", "A", true},
		{"single_segment", "one PascalCase segment", "Session", true},
		{"with_digits_in_segment", "digits allowed inside a segment (e.g. event versions)", "Tool2Use", true},

		// rejection paths — each prevents a different cardinality leak
		{"empty_string", "empty token must not be a valid name", "", false},
		{"lowercase_start", "lowercase-first input lets arbitrary tokens through (CodeRabbit PR488 — strict PascalCase rejects this)", "postToolUse", false},
		{"all_lowercase", "single lowercase word reaches the regex but not PascalCase shape", "foo", false},
		{"lowercase_with_digits", "alphanumeric lowercase still rejected", "abc123", false},
		{"with_space", "spaces let attacker-controlled strings into span names", "Post ToolUse", false},
		{"with_slash", "path-like input must be rejected", "Post/Tool", false},
		{"with_dash", "dashes are not PascalCase", "Post-Tool-Use", false},
		{"with_underscore", "snake_case is not PascalCase", "Post_Tool_Use", false},
		{"with_quote", "shell-quoted input must be rejected", "Post\"Tool", false},
		{"with_semicolon", "shell metacharacters must be rejected", "Post;Tool", false},
		{"with_newline", "newlines must be rejected", "Post\nTool", false},
		{"starts_with_digit", "must start with uppercase letter", "1ToolUse", false},
		{"too_long", "length cap prevents pathologically long span names", strings.Repeat("A", 65), false},
		{"too_many_segments", "16-segment cap on the regex prevents 64-uppercase-letter pathological input", strings.Repeat("A", 17), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isKnownHookEventName(tt.in)
			if got != tt.want {
				t.Errorf("isKnownHookEventName(%q) = %v, want %v\n(prevents: %s)",
					tt.in, got, tt.want, tt.prevents)
			}
		})
	}
}

func TestGenerateAgentID(t *testing.T) {
	t.Run("generates valid agent ID with no existing IDs", func(t *testing.T) {
		agentID, err := agentinstance.GenerateAgentID([]string{})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if !agentinstance.IsValidAgentID(agentID) {
			t.Errorf("generated invalid agent ID: %s", agentID)
		}

		if !strings.HasPrefix(agentID, "Ox") {
			t.Errorf("agent ID should start with 'Ox', got %s", agentID)
		}

		if len(agentID) != 6 {
			t.Errorf("agent ID should be 6 chars, got %d: %s", len(agentID), agentID)
		}
	})

	t.Run("generates unique agent ID avoiding collisions", func(t *testing.T) {
		existing := []string{"OxA1b2", "OxC3d4", "OxE5f6"}
		agentID, err := agentinstance.GenerateAgentID(existing)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		for _, existingID := range existing {
			if agentID == existingID {
				t.Errorf("generated ID %s collides with existing ID", agentID)
			}
		}
	})

	t.Run("generates multiple unique IDs", func(t *testing.T) {
		seen := make(map[string]bool)
		iterations := 100

		for i := 0; i < iterations; i++ {
			agentID, err := agentinstance.GenerateAgentID([]string{})
			if err != nil {
				t.Fatalf("iteration %d: expected no error, got %v", i, err)
			}

			if seen[agentID] {
				t.Errorf("iteration %d: duplicate agent ID generated: %s", i, agentID)
			}
			seen[agentID] = true
		}
	})
}

func TestIsValidAgentID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "valid alphanumeric", input: "OxA1b2", expected: true},
		{name: "valid all numbers", input: "Ox1234", expected: true},
		{name: "valid all letters", input: "OxZzZz", expected: true},
		{name: "valid mixed case", input: "OxAaBb", expected: true},
		{name: "invalid lowercase prefix", input: "ox1234", expected: false},
		{name: "invalid uppercase prefix", input: "OX1234", expected: false},
		{name: "invalid too short", input: "Ox123", expected: false},
		{name: "invalid too long", input: "Ox12345", expected: false},
		{name: "invalid special char", input: "Ox12#4", expected: false},
		{name: "invalid empty", input: "", expected: false},
		{name: "invalid no prefix", input: "123456", expected: false},
		{name: "invalid wrong prefix", input: "Ab1234", expected: false},
		{name: "invalid spaces", input: "Ox12 4", expected: false},
		{name: "invalid underscore", input: "Ox12_4", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := agentinstance.IsValidAgentID(tt.input)
			if result != tt.expected {
				t.Errorf("IsValidAgentID(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestFindProjectRoot(t *testing.T) {
	t.Run("finds project root with .sageox directory", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "ox-test-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		sageoxDir := filepath.Join(tmpDir, ".sageox")
		if err := os.MkdirAll(sageoxDir, 0755); err != nil {
			t.Fatalf("failed to create .sageox dir: %v", err)
		}

		subDir := filepath.Join(tmpDir, "sub", "nested")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("failed to create nested dir: %v", err)
		}

		originalCwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer os.Chdir(originalCwd)

		if err := os.Chdir(subDir); err != nil {
			t.Fatalf("failed to change dir: %v", err)
		}

		root, err := findProjectRoot()
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		// resolve both paths to handle symlinks on macOS
		expectedRoot, _ := filepath.EvalSymlinks(tmpDir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("expected root %s, got %s", expectedRoot, actualRoot)
		}
	})

	t.Run("returns cwd when no .sageox directory found", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "ox-test-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		originalCwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer os.Chdir(originalCwd)

		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("failed to change dir: %v", err)
		}

		root, err := findProjectRoot()
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		// resolve both paths to handle symlinks on macOS
		expectedRoot, _ := filepath.EvalSymlinks(tmpDir)
		actualRoot, _ := filepath.EvalSymlinks(root)

		if actualRoot != expectedRoot {
			t.Errorf("expected root %s, got %s", expectedRoot, actualRoot)
		}
	})
}

func TestFindProjectRoot_OxProjectRootEnv(t *testing.T) {
	t.Run("OX_PROJECT_ROOT overrides cwd discovery", func(t *testing.T) {
		// create a fully initialized project dir (.sageox/config.json)
		projectDir := t.TempDir()
		sageoxDir := filepath.Join(projectDir, ".sageox")
		if err := os.MkdirAll(sageoxDir, 0755); err != nil {
			t.Fatalf("failed to create .sageox dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(`{"config_version":"2"}`), 0644); err != nil {
			t.Fatalf("failed to create config.json: %v", err)
		}

		// cwd is somewhere else entirely
		otherDir := t.TempDir()
		originalCwd, _ := os.Getwd()
		defer os.Chdir(originalCwd)
		if err := os.Chdir(otherDir); err != nil {
			t.Fatalf("failed to change dir: %v", err)
		}

		t.Setenv("OX_PROJECT_ROOT", projectDir)

		root, err := findProjectRoot()
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		expectedRoot, _ := filepath.EvalSymlinks(projectDir)
		actualRoot, _ := filepath.EvalSymlinks(root)
		if actualRoot != expectedRoot {
			t.Errorf("expected root %s, got %s", expectedRoot, actualRoot)
		}
	})

	t.Run("invalid OX_PROJECT_ROOT falls back to cwd discovery", func(t *testing.T) {
		// create a project dir with .sageox in cwd
		projectDir := t.TempDir()
		sageoxDir := filepath.Join(projectDir, ".sageox")
		if err := os.MkdirAll(sageoxDir, 0755); err != nil {
			t.Fatalf("failed to create .sageox dir: %v", err)
		}

		originalCwd, _ := os.Getwd()
		defer os.Chdir(originalCwd)
		if err := os.Chdir(projectDir); err != nil {
			t.Fatalf("failed to change dir: %v", err)
		}

		// point env var at a dir without .sageox
		t.Setenv("OX_PROJECT_ROOT", t.TempDir())

		root, err := findProjectRoot()
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		// should fall back to cwd-based discovery
		expectedRoot, _ := filepath.EvalSymlinks(projectDir)
		actualRoot, _ := filepath.EvalSymlinks(root)
		if actualRoot != expectedRoot {
			t.Errorf("expected root %s, got %s", expectedRoot, actualRoot)
		}
	})
}

func TestGetUserSlug(t *testing.T) {
	slug := getUserSlug()

	if slug == "" {
		t.Error("getUserSlug should not return empty string")
	}

	if strings.Contains(slug, " ") {
		t.Errorf("slug should not contain spaces: %s", slug)
	}

	if strings.Contains(slug, "@") {
		t.Errorf("slug should not contain @: %s", slug)
	}

	if slug != strings.ToLower(slug) {
		t.Errorf("slug should be lowercase: %s", slug)
	}
}

// SageOx is multiplayer - offline mode is not supported.
// See internal/auth/feature.go for philosophy.

func TestAgentPrimeOutputFormatting(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	defer os.Chdir(originalCwd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change dir: %v", err)
	}

	t.Run("json format output", func(t *testing.T) {
		output := agentPrimeOutput{
			Status:  "fresh",
			AgentID: "OxA1b2",
			Content: "test guidance content",
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("failed to marshal output: %v", err)
		}

		var decoded agentPrimeOutput
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal output: %v", err)
		}

		if decoded.Status != output.Status {
			t.Errorf("status mismatch: got %s, want %s", decoded.Status, output.Status)
		}

		if decoded.AgentID != output.AgentID {
			t.Errorf("agent_id mismatch: got %s, want %s", decoded.AgentID, output.AgentID)
		}
	})
}

func TestOutputAgentPrimeTextFormat(t *testing.T) {
	t.Skip("skipping output format test - requires cobra command setup")
}

func TestInstanceExpiry(t *testing.T) {
	t.Run("expired instance detected", func(t *testing.T) {
		inst := &agentinstance.Instance{
			AgentID:         "OxA1b2",
			ServerSessionID: "oxsid_01KCJECKEGETGX6HC80NRYVZ3P",
			CreatedAt:       time.Now().Add(-25 * time.Hour),
			ExpiresAt:       time.Now().Add(-1 * time.Hour),
		}

		if !inst.IsExpired() {
			t.Error("expected instance to be expired")
		}
	})

	t.Run("active instance not expired", func(t *testing.T) {
		inst := &agentinstance.Instance{
			AgentID:         "OxA1b2",
			ServerSessionID: "oxsid_01KCJECKEGETGX6HC80NRYVZ3P",
			CreatedAt:       time.Now(),
			ExpiresAt:       time.Now().Add(23 * time.Hour),
		}

		if inst.IsExpired() {
			t.Error("expected instance to not be expired")
		}
	})
}

func TestAgentPrimeOutputStatuses(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		message       string
		expectMessage bool
	}{
		{
			name:          "fresh status no message",
			status:        "fresh",
			message:       "",
			expectMessage: false,
		},
		{
			name:          "stale status with message",
			status:        "stale",
			message:       "Using cached guidance",
			expectMessage: true,
		},
		{
			name:          "offline status with message",
			status:        "offline",
			message:       "Offline mode",
			expectMessage: true,
		},
		{
			name:          "unavailable status with message",
			status:        "unavailable",
			message:       "API unavailable",
			expectMessage: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := agentPrimeOutput{
				Status:  tt.status,
				AgentID: "OxTest",
				Message: tt.message,
			}

			if tt.status != output.Status {
				t.Errorf("status mismatch: got %s, want %s", output.Status, tt.status)
			}

			if tt.expectMessage && output.Message == "" {
				t.Error("expected message to be set for non-fresh status")
			}
		})
	}
}

func TestIsAgentSubcommand(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "session is subcommand", input: "session", expected: true},
		{name: "doctor is subcommand", input: "doctor", expected: true},
		{name: "prime is NOT subcommand", input: "prime", expected: false},
		{name: "list is NOT subcommand", input: "list", expected: false},
		{name: "unknown is NOT subcommand", input: "foobar", expected: false},
		{name: "empty is NOT subcommand", input: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isAgentSubcommand(tt.input)
			if result != tt.expected {
				t.Errorf("isAgentSubcommand(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// --- Symlink resolution ---

// TestFindProjectRoot_ResolvesSymlinks verifies that findProjectRoot returns
// the real (symlink-resolved) path, not the symlinked path.
// Failure prevented: session file lookups fail when cwd traverses a symlink.
func TestFindProjectRoot_ResolvesSymlinks(t *testing.T) {
	realDir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(realDir) // normalize macOS /tmp -> /private/tmp
	if err != nil {
		t.Fatalf("failed to eval symlinks on temp dir: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(realDir, ".sageox"), 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	linkParent := t.TempDir()
	linkParent, _ = filepath.EvalSymlinks(linkParent)
	linkPath := filepath.Join(linkParent, "linked-project")
	if err := os.Symlink(realDir, linkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	originalCwd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(originalCwd) })

	if err := os.Chdir(linkPath); err != nil {
		t.Fatalf("failed to chdir to symlink: %v", err)
	}

	root, err := findProjectRoot()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if root != realDir {
		t.Errorf("expected resolved real path %s, got %s", realDir, root)
	}
}

// TestFindProjectRoot_SymlinkAndReal_ReturnSamePath verifies that navigating
// via symlink or real path produces the same findProjectRoot result.
// Failure prevented: same project gets different root paths depending on how user cd'd in.
func TestFindProjectRoot_SymlinkAndReal_ReturnSamePath(t *testing.T) {
	realDir := t.TempDir()
	realDir, _ = filepath.EvalSymlinks(realDir)

	if err := os.MkdirAll(filepath.Join(realDir, ".sageox"), 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	linkParent := t.TempDir()
	linkParent, _ = filepath.EvalSymlinks(linkParent)
	linkPath := filepath.Join(linkParent, "linked-project")
	if err := os.Symlink(realDir, linkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	originalCwd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(originalCwd) })

	// get root via real path
	if err := os.Chdir(realDir); err != nil {
		t.Fatalf("failed to chdir to real dir: %v", err)
	}
	rootA, err := findProjectRoot()
	if err != nil {
		t.Fatalf("findProjectRoot from real dir: %v", err)
	}

	// get root via symlink
	if err := os.Chdir(linkPath); err != nil {
		t.Fatalf("failed to chdir to symlink: %v", err)
	}
	rootB, err := findProjectRoot()
	if err != nil {
		t.Fatalf("findProjectRoot from symlink: %v", err)
	}

	if rootA != rootB {
		t.Errorf("real path root %s != symlink root %s", rootA, rootB)
	}
}

// TestFindProjectRoot_SymlinkInParentDir verifies symlink resolution when the
// symlink is in a parent directory and cwd is a child of the symlinked tree.
// Failure prevented: walk-up discovery returns unresolved path when symlink is above cwd.
func TestFindProjectRoot_SymlinkInParentDir(t *testing.T) {
	realBase := t.TempDir()
	realBase, _ = filepath.EvalSymlinks(realBase)

	parentDir := filepath.Join(realBase, "parent")
	childDir := filepath.Join(parentDir, "child")
	if err := os.MkdirAll(filepath.Join(parentDir, ".sageox"), 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}
	if err := os.MkdirAll(childDir, 0755); err != nil {
		t.Fatalf("failed to create child dir: %v", err)
	}

	linkBase := t.TempDir()
	linkBase, _ = filepath.EvalSymlinks(linkBase)
	linkPath := filepath.Join(linkBase, "linked")
	if err := os.Symlink(parentDir, linkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	originalCwd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(originalCwd) })

	if err := os.Chdir(filepath.Join(linkPath, "child")); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}

	root, err := findProjectRoot()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if root != parentDir {
		t.Errorf("expected resolved parent %s, got %s", parentDir, root)
	}
}

// TestFindProjectRoot_OverrideEnvResolvesSymlinks verifies that OX_PROJECT_ROOT
// env var values are symlink-resolved before being returned.
// Failure prevented: override path contains symlink, causing path mismatches downstream.
func TestFindProjectRoot_OverrideEnvResolvesSymlinks(t *testing.T) {
	realDir := t.TempDir()
	realDir, _ = filepath.EvalSymlinks(realDir)

	// IsInitialized checks for .sageox/config.json
	sageoxDir := filepath.Join(realDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(`{}`), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	linkParent := t.TempDir()
	linkParent, _ = filepath.EvalSymlinks(linkParent)
	linkPath := filepath.Join(linkParent, "linked-project")
	if err := os.Symlink(realDir, linkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	// cwd is somewhere unrelated
	otherDir := t.TempDir()
	originalCwd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(originalCwd) })
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}

	t.Setenv("OX_PROJECT_ROOT", linkPath)

	root, err := findProjectRoot()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if root != realDir {
		t.Errorf("expected resolved real path %s, got %s", realDir, root)
	}
}

func TestAgentDispatcherEnvVarFallback(t *testing.T) {
	t.Run("env var with valid agent ID resolves subcommand", func(t *testing.T) {
		// isAgentSubcommand + valid env var = would attempt runWithAgentID
		// we test the logic components since full dispatch needs cobra wiring
		if !isAgentSubcommand("session") {
			t.Fatal("session should be a known agent subcommand")
		}

		validID := "OxT3st"
		if !agentinstance.IsValidAgentID(validID) {
			t.Fatalf("%s should be a valid agent ID", validID)
		}
	})

	t.Run("env var with invalid agent ID does not resolve", func(t *testing.T) {
		invalidIDs := []string{"", "bad", "ox1234", "OX1234", "Ox12"}
		for _, id := range invalidIDs {
			if agentinstance.IsValidAgentID(id) {
				t.Errorf("expected %q to be invalid agent ID", id)
			}
		}
	})

	t.Run("non-subcommand first arg does not trigger env fallback", func(t *testing.T) {
		nonSubcommands := []string{"prime", "list", "foobar", ""}
		for _, arg := range nonSubcommands {
			if isAgentSubcommand(arg) {
				t.Errorf("expected %q to NOT be a subcommand", arg)
			}
		}
	})
}
