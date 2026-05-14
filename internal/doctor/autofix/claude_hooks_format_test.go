package autofix

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestCheckClaudeHooksFormat_NoChurnOnUserAuthored is the daemon-side
// regression for ox-647t. The autofix scheduler runs every 30 minutes,
// and the previous byte-equal no-op guard treated encoder cosmetics
// (HTML-escaping of <, >, &; missing trailing newline; \uXXXX vs literal
// rune; non-canonical indentation inside opaque permissions) as real
// changes. The result was a perpetual rewrite loop on any user-authored
// settings.json containing those bytes.
//
// Failure prevented: the daemon mutating the user's settings.json every
// 30 minutes for the life of the session.
//
// The test seeds a file that exercises every drift pattern the broken
// encoder produced, runs the check twice (simulating two scheduler
// ticks), and asserts the on-disk bytes are byte-identical across both
// runs. The cmd/ox-side analog lives in doctor_fix_hooks_test.go;
// both must pass because the daemon and CLI share the underlying
// MarshalSettings + SettingsSemanticallyEqual primitives but live in
// independent call paths.
func TestCheckClaudeHooksFormat_NoChurnOnUserAuthored(t *testing.T) {
	repo := t.TempDir()
	claudeDir := filepath.Join(repo, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")

	// Adversarial input: literal `<`, `>`, `&`; permissions value uses
	// tab indentation; top-level keys in insertion order with permissions
	// before hooks; trailing newline. Every one of these defeated the
	// pre-fix byte-equal guard.
	userAuthored := "{\n" +
		"\t\"permissions\": {\n" +
		"\t\t\"allow\": [\n" +
		"\t\t\t\"Bash(grep '</body>':*)\",\n" +
		"\t\t\t\"Bash(curl 'https://x?a=1&b=2':*)\"\n" +
		"\t\t]\n" +
		"\t},\n" +
		"\t\"hooks\": {\n" +
		"\t\t\"SessionStart\": [\n" +
		"\t\t\t{\n" +
		"\t\t\t\t\"matcher\": \"\",\n" +
		"\t\t\t\t\"hooks\": [\n" +
		"\t\t\t\t\t{\"command\": \"ox agent hook SessionStart\", \"type\": \"command\"}\n" +
		"\t\t\t\t]\n" +
		"\t\t\t}\n" +
		"\t\t]\n" +
		"\t}\n" +
		"}\n"
	if err := os.WriteFile(settingsPath, []byte(userAuthored), 0o600); err != nil {
		t.Fatal(err)
	}

	first := checkClaudeHooksFormat(context.Background(), repo)
	if first.Status != StatusClean {
		t.Errorf("first tick: status=%v summary=%q; well-formed user file must be reported clean (this is the regression)",
			first.Status, first.Summary)
	}
	afterFirst, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFirst) != userAuthored {
		t.Errorf("autofix mutated user-authored file on first tick.\nwant:\n%s\ngot:\n%s",
			userAuthored, string(afterFirst))
	}

	second := checkClaudeHooksFormat(context.Background(), repo)
	if second.Status != StatusClean {
		t.Errorf("second tick: status=%v summary=%q; should still be clean", second.Status, second.Summary)
	}
	afterSecond, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFirst) != string(afterSecond) {
		t.Errorf("autofix is not idempotent on user-authored file (would churn every 30 min).\nfirst tick wrote:\n%s\nsecond tick wrote:\n%s",
			string(afterFirst), string(afterSecond))
	}
}

// TestCheckClaudeHooksFormat_MigratesLegacyOnce verifies the other half of
// the contract: when the on-disk shape is legacy (bare-string hook values
// that Claude Code rejects), the autofix DOES rewrite it — but only once.
// The repaired file must then be stable across subsequent ticks.
//
// Failure prevented: either (a) the autofix refuses to migrate broken
// files because semantic equality says "they parse the same in memory"
// (over-permissive no-op guard), or (b) the autofix migrates correctly
// but the canonical output is itself non-idempotent (the original ox-647t
// bug, just shifted to post-migration state).
func TestCheckClaudeHooksFormat_MigratesLegacyOnce(t *testing.T) {
	repo := t.TempDir()
	claudeDir := filepath.Join(repo, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")

	legacy := `{
  "permissions": {"allow": ["Bash(echo '<x>&y':*)"]},
  "hooks": {"PostToolUse": "ox agent hook PostToolUse"}
}
`
	if err := os.WriteFile(settingsPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	first := checkClaudeHooksFormat(context.Background(), repo)
	if first.Status != StatusFixed {
		t.Errorf("first tick: status=%v summary=%q; legacy string-form file must be repaired",
			first.Status, first.Summary)
	}
	afterFirst, _ := os.ReadFile(settingsPath)
	if string(afterFirst) == legacy {
		t.Error("autofix reported Fixed but bytes are unchanged")
	}
	// HTML chars must survive the rewrite as literals.
	for _, lit := range []string{"<x>", "&y"} {
		if !contains(string(afterFirst), lit) {
			t.Errorf("expected literal %q in repaired file (encoder must not HTML-escape user content):\n%s",
				lit, afterFirst)
		}
	}

	second := checkClaudeHooksFormat(context.Background(), repo)
	if second.Status != StatusClean {
		t.Errorf("second tick: status=%v summary=%q; freshly-repaired file must be Clean",
			second.Status, second.Summary)
	}
	afterSecond, _ := os.ReadFile(settingsPath)
	if string(afterFirst) != string(afterSecond) {
		t.Errorf("post-migration file churned between ticks.\nfirst:\n%s\nsecond:\n%s",
			afterFirst, afterSecond)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
