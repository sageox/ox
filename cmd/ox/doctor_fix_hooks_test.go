//go:build !short

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRawSettings writes a raw JSON body to <root>/.claude/settings.json so
// these tests can construct the exact legacy on-disk shapes that the doctor
// repair check needs to handle. We bypass the typed writer (which always
// emits the array form) on purpose — we are testing the repair of files
// that pre-date the typed writer.
func writeRawSettings(t *testing.T, root, body string) string {
	t.Helper()
	dir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCheckClaudeHooksFormat_RepairsStringFormat is the regression test for
// ox-mdrq. Old ox versions and the now-neutered ox-adapter-claude-code
// shipped settings.json files where each hook event value was a bare
// string instead of an array of HookEntry. Current Claude Code rejects
// these wholesale, so the user's hooks silently stop firing.
//
// Failure prevented: a user upgrades ox, never runs `ox doctor --fix`, and
// every Claude Code session in their existing repos runs without any
// SessionStart / PreToolUse / PostToolUse hooks because the settings file
// is in a format Claude Code refuses to parse.
//
// What's special about this case (vs the read-side repair already in
// parseHooksMixed): no other code path *writes* settings.json on a routine
// basis, so files that haven't been touched by ox prime / integrate / etc.
// stay broken indefinitely. This check exists specifically to repair that
// dormant population.
func TestCheckClaudeHooksFormat_RepairsStringFormat(t *testing.T) {
	gitRoot := testGitRepo(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatal(err)
	}

	// The string-format failure mode: the value of "PostToolUse" is a bare
	// command string, not the [{matcher,hooks}] array Claude Code expects.
	legacy := `{
  "hooks": {
    "PostToolUse": "echo legacy",
    "SessionStart": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "ox agent prime"}]}
    ]
  },
  "permissions": {"allow": ["Bash(git:*)"]}
}`
	path := writeRawSettings(t, gitRoot, legacy)

	result := checkClaudeHooksFormat(true)
	if !result.passed {
		t.Fatalf("expected fix to succeed: %+v", result)
	}

	// Verify on-disk shape now satisfies Claude Code's parser: every value
	// under "hooks" must be an array.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("repaired file is not valid JSON: %v\n%s", err, data)
	}
	hooksRaw, ok := raw["hooks"]
	if !ok {
		t.Fatal("repaired file lost the hooks key entirely")
	}
	var events map[string]json.RawMessage
	if err := json.Unmarshal(hooksRaw, &events); err != nil {
		t.Fatal(err)
	}
	for event, val := range events {
		s := strings.TrimSpace(string(val))
		if !strings.HasPrefix(s, "[") {
			t.Errorf("event %q is still in legacy string form: %s", event, s)
		}
	}

	// Lossless: the existing array-form SessionStart entry must survive
	// verbatim, and non-hook keys (permissions) must be preserved.
	if !strings.Contains(string(data), "ox agent prime") {
		t.Error("array-form SessionStart entry was lost during repair")
	}
	if !strings.Contains(string(data), "Bash(git:*)") {
		t.Error("non-hook 'permissions' key was lost during repair")
	}

	// The legacy command must still be present (just wrapped into an array).
	if !strings.Contains(string(data), "echo legacy") {
		t.Error("legacy command body was lost during repair")
	}
}

// TestCheckClaudeHooksFormat_Idempotent guards against the repair check
// rewriting a healthy file on every doctor run — that would create
// unnecessary churn and, if the rewrite ever differs from the input, an
// infinite "fix me again" loop.
//
// Failure prevented: doctor in --fix mode reports "fixed" on a file that
// was already correct, conditioning users to ignore real signals.
func TestCheckClaudeHooksFormat_Idempotent(t *testing.T) {
	gitRoot := testGitRepo(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatal(err)
	}

	// Step 1: legacy file gets repaired into canonical form. Whatever the
	// canonical bytes are, repeated runs must be no-ops.
	legacy := `{"hooks": {"PostToolUse": "echo legacy"}}`
	path := writeRawSettings(t, gitRoot, legacy)

	first := checkClaudeHooksFormat(true)
	if !first.passed {
		t.Fatalf("first pass (repair) failed: %+v", first)
	}
	canonical, _ := os.ReadFile(path)

	// Step 2: re-running on the now-canonical file must report it as
	// already-canonical (the "passed: canonical" branch), and must not
	// rewrite the bytes.
	second := checkClaudeHooksFormat(true)
	if !second.passed {
		t.Fatalf("second pass failed: %+v", second)
	}
	if strings.Contains(second.message, "repaired") {
		t.Errorf("canonical file was unnecessarily rewritten: %q", second.message)
	}
	after, _ := os.ReadFile(path)
	if string(canonical) != string(after) {
		t.Errorf("repair check is not idempotent. before:\n%s\nafter:\n%s", canonical, after)
	}
}

// TestCheckClaudeHooksFormat_NoFileSkips confirms the check stays out of
// the way for repos that simply have no Claude settings to repair.
//
// Failure prevented: false-positive failures on perfectly fine
// non-Claude projects.
func TestCheckClaudeHooksFormat_NoFileSkips(t *testing.T) {
	gitRoot := testGitRepo(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatal(err)
	}

	result := checkClaudeHooksFormat(false)
	if !result.skipped {
		t.Errorf("expected skip when settings.json absent, got: %+v", result)
	}
}

// TestCheckClaudeHooksFormat_DetectsWithoutFix verifies the warn-only path:
// when the user runs `ox doctor` (no --fix), a string-format file must be
// flagged with actionable detail, not silently passed.
//
// Failure prevented: silent acceptance of a file Claude Code rejects.
func TestCheckClaudeHooksFormat_DetectsWithoutFix(t *testing.T) {
	gitRoot := testGitRepo(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatal(err)
	}

	legacy := `{"hooks": {"PostToolUse": "echo hi"}}`
	writeRawSettings(t, gitRoot, legacy)

	result := checkClaudeHooksFormat(false)
	if result.passed && !result.warning {
		t.Errorf("expected warning on string-format file without --fix, got passed: %+v", result)
	}
	if result.detail == "" {
		t.Error("expected actionable detail pointing the user at `ox doctor --fix`")
	}
}

// TestCheckClaudeHooksFormat_LeavesUserAuthoredFileUntouched is the regression
// test for the "settings.json rewritten on every Claude Code session" bug
// (ox-647t). Prior to the fix, MarshalSettings used encoding/json defaults
// that HTML-escape <, >, & and strip trailing newlines, and both no-op
// guards (here and in internal/doctor/autofix/default_checks.go) compared
// bytes after re-marshaling. Any user-authored file that contained those
// characters in a permission rule, or simply ended with a newline, would
// be silently rewritten on every doctor / autofix pass — the rewrite
// itself produced encoder output that drifted on the next pass too, so
// the file churned forever even though no real content had changed.
//
// Failure prevented: doctor mutating a user's hand-written settings.json
// on every session. Concretely: a file containing `<`, `>`, `&` literals
// in a Bash() permission rule must remain byte-identical across two
// consecutive checkClaudeHooksFormat(true) calls.
//
// This is the adversarial-input test that was missing — the existing
// idempotency test seeded `{"hooks": {"PostToolUse": "echo legacy"}}`,
// which has none of the bytes that exposed the bug. A test that only
// round-trips the encoder's own output can never catch an encoder that
// mistreats user input.
func TestCheckClaudeHooksFormat_LeavesUserAuthoredFileUntouched(t *testing.T) {
	gitRoot := testGitRepo(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatal(err)
	}

	// User-authored settings.json: array-form hooks, literal HTML
	// characters in a permission rule, hand-written \uXXXX escape,
	// trailing newline, four-space indentation, top-level keys in
	// insertion order (permissions before hooks). All of these are
	// shapes the previous byte-equal guard would have rewritten.
	userAuthored := "{\n" +
		"    \"permissions\": {\n" +
		"        \"allow\": [\n" +
		"            \"Bash(grep '</script>':*)\",\n" +
		"            \"Bash(curl 'https://example.com/?a=1\\u0026b=2':*)\"\n" +
		"        ]\n" +
		"    },\n" +
		"    \"hooks\": {\n" +
		"        \"SessionStart\": [\n" +
		"            {\n" +
		"                \"matcher\": \"\",\n" +
		"                \"hooks\": [\n" +
		"                    {\"command\": \"ox agent hook SessionStart\", \"type\": \"command\"}\n" +
		"                ]\n" +
		"            }\n" +
		"        ]\n" +
		"    }\n" +
		"}\n"
	path := writeRawSettings(t, gitRoot, userAuthored)

	first := checkClaudeHooksFormat(true)
	if !first.passed {
		t.Fatalf("first pass should pass on a well-formed user file: %+v", first)
	}
	if strings.Contains(first.message, "repaired") {
		t.Errorf("well-formed user file was rewritten on first pass: %q\n"+
			"this is the regression — user content must not be mutated by the no-op path",
			first.message)
	}
	afterFirst, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFirst) != userAuthored {
		t.Errorf("on-disk bytes changed after first checkClaudeHooksFormat(true)\nwant:\n%s\ngot:\n%s",
			userAuthored, string(afterFirst))
	}

	second := checkClaudeHooksFormat(true)
	if strings.Contains(second.message, "repaired") {
		t.Errorf("idempotency broken — second pass rewrote: %q", second.message)
	}
	afterSecond, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterSecond) != string(afterFirst) {
		t.Errorf("file mutated between consecutive passes\nfirst:\n%s\nsecond:\n%s",
			string(afterFirst), string(afterSecond))
	}
}

// TestCheckClaudeHooksFormat_RepairsThenStays verifies the two-pass invariant
// for files that *do* need repair: the first pass rewrites into canonical
// form, the second pass leaves the rewritten file alone. This is the
// property the old TestCheckClaudeHooksFormat_Idempotent test attempted to
// prove, but with an adversarial input (HTML chars in permissions) that
// actually exercises the encoder's behavior on user content.
//
// Failure prevented: the daemon autofix scheduler rewriting a freshly
// repaired file on the next tick because canonical form ≠ canonical form
// when read back through the round-trip.
func TestCheckClaudeHooksFormat_RepairsThenStays(t *testing.T) {
	gitRoot := testGitRepo(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(gitRoot); err != nil {
		t.Fatal(err)
	}

	// Legacy string-form hooks alongside HTML-bearing permissions: the
	// file needs migration AND its content has the bytes the old encoder
	// mishandled.
	legacy := `{
  "permissions": {"allow": ["Bash(grep '</style>':*)"]},
  "hooks": {"PostToolUse": "ox agent hook PostToolUse"}
}`
	path := writeRawSettings(t, gitRoot, legacy)

	first := checkClaudeHooksFormat(true)
	if !first.passed {
		t.Fatalf("first pass (repair) failed: %+v", first)
	}
	if !strings.Contains(first.message, "repaired") {
		t.Errorf("expected repair message on legacy file, got: %q", first.message)
	}
	afterFirst, _ := os.ReadFile(path)

	// Migrated file must still contain the literal HTML chars (not <,
	// >, & escapes) and must parse as array-shape hooks.
	if !strings.Contains(string(afterFirst), `</style>`) {
		t.Errorf("repair re-escaped user content (<, >, & should stay literal):\n%s", afterFirst)
	}
	var probe struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Type    string `json:"type"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(afterFirst, &probe); err != nil {
		t.Fatalf("repaired file does not parse as array-shape hooks: %v\n%s", err, afterFirst)
	}
	if len(probe.Hooks["PostToolUse"]) != 1 {
		t.Errorf("PostToolUse not in array shape after repair:\n%s", afterFirst)
	}

	second := checkClaudeHooksFormat(true)
	if strings.Contains(second.message, "repaired") {
		t.Errorf("second pass rewrote a freshly canonical file: %q", second.message)
	}
	afterSecond, _ := os.ReadFile(path)
	if string(afterFirst) != string(afterSecond) {
		t.Errorf("file churned between consecutive passes:\nfirst:\n%s\nsecond:\n%s",
			afterFirst, afterSecond)
	}
}
