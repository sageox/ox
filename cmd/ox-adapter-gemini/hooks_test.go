package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	return settings
}

func installInto(t *testing.T, repoRoot string) string {
	t.Helper()
	resp, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repoRoot, Scope: "project"})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !resp.Installed {
		t.Fatal("install reported Installed=false")
	}
	return filepath.Join(repoRoot, ".gemini", "settings.json")
}

// TestInstallHooks_WritesGeminisRealSchema is the core contract: every event
// value must be an ARRAY of hook definitions, each holding a "hooks" array of
// {type,command} configs. Gemini validates this before it starts and exits with
// "Expected array, received string" on anything else — a bricked CLI, which is
// what ox used to write.
func TestInstallHooks_WritesGeminisRealSchema(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	path := installInto(t, repo)
	settings := readSettings(t, path)

	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatal("settings.hooks is not an object")
	}

	for _, event := range hookEvents {
		groups, ok := hooks[event].([]any)
		if !ok {
			t.Fatalf("hooks.%s is %T, want an array — gemini refuses to start otherwise", event, hooks[event])
		}
		if len(groups) != 1 {
			t.Fatalf("hooks.%s has %d definitions, want 1", event, len(groups))
		}
		group, ok := groups[0].(map[string]any)
		if !ok {
			t.Fatalf("hooks.%s[0] is %T, want an object", event, groups[0])
		}
		configs, ok := group["hooks"].([]any)
		if !ok || len(configs) != 1 {
			t.Fatalf("hooks.%s[0].hooks is %T with %d entries, want 1", event, group["hooks"], len(configs))
		}
		cfg, ok := configs[0].(map[string]any)
		if !ok {
			t.Fatalf("hooks.%s[0].hooks[0] is %T, want an object", event, configs[0])
		}
		if cfg["type"] != "command" {
			t.Errorf("hooks.%s type = %v, want \"command\"", event, cfg["type"])
		}
		cmd, _ := cfg["command"].(string)
		if !strings.Contains(cmd, "ox agent hook "+event) {
			t.Errorf("hooks.%s command = %q, want it to invoke `ox agent hook %s`", event, cmd, event)
		}
	}
}

// TestHookEvents_AreGeminisOwnEventNames pins the event set. These four are
// both real Gemini CLI events (per its bundled docs/hooks/reference.md) and the
// four ox's dispatcher maps to lifecycle phases for AGENT_ENV=gemini. Claude
// Code's names are not Gemini events and never fire.
func TestHookEvents_AreGeminisOwnEventNames(t *testing.T) {
	t.Parallel()

	geminiEvents := map[string]bool{
		"SessionStart": true, "SessionEnd": true, "BeforeAgent": true,
		"AfterAgent": true, "BeforeModel": true, "AfterModel": true,
		"BeforeToolSelection": true, "BeforeTool": true, "AfterTool": true,
		"PreCompress": true, "Notification": true,
	}

	for _, event := range hookEvents {
		if !geminiEvents[event] {
			t.Errorf("%q is not a Gemini CLI hook event — it will never fire", event)
		}
	}

	for _, event := range legacyHookEvents {
		if geminiEvents[event] {
			t.Errorf("%q is a real Gemini event and must not be treated as legacy", event)
		}
	}
}

// TestHookCommand_DoesNotPolluteStdout guards gemini's "silence is mandatory"
// rule: hook stdout is parsed as JSON, so stderr must not be folded into it.
// The old command ended in `2>&1`, which piped every ox log line into the
// channel gemini parses.
func TestHookCommand_DoesNotPolluteStdout(t *testing.T) {
	t.Parallel()

	cmd := hookCommand("SessionStart")
	if strings.Contains(cmd, "ox agent hook SessionStart 2>&1") {
		t.Error("hook command redirects stderr into stdout; gemini parses stdout as JSON")
	}
	if !strings.Contains(cmd, "2>/dev/null") {
		t.Errorf("hook command = %q, want stderr discarded", cmd)
	}
}

// TestInstallHooks_MigratesTheBrickingLegacyConfig is the repair path for
// anyone who already ran an older ox: the Claude-named string values must be
// gone after a reinstall, because their mere presence stops gemini booting.
func TestInstallHooks_MigratesTheBrickingLegacyConfig(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	settingsPath := filepath.Join(repo, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{
  "hooks": {
    "PostToolUse": "if command -v ox >/dev/null 2>&1; then AGENT_ENV=gemini ox agent hook PostToolUse 2>&1 || true; fi",
    "PreToolUse": "if command -v ox >/dev/null 2>&1; then AGENT_ENV=gemini ox agent hook PreToolUse 2>&1 || true; fi",
    "Stop": "if command -v ox >/dev/null 2>&1; then AGENT_ENV=gemini ox agent hook Stop 2>&1 || true; fi"
  }
}`
	if err := os.WriteFile(settingsPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	installInto(t, repo)
	hooks := readSettings(t, settingsPath)["hooks"].(map[string]any)

	for _, event := range legacyHookEvents {
		if _, present := hooks[event]; present {
			t.Errorf("hooks.%s survived the migration; gemini still refuses to start", event)
		}
	}
	for _, event := range hookEvents {
		if _, ok := hooks[event].([]any); !ok {
			t.Errorf("hooks.%s missing after migration", event)
		}
	}
}

// TestCheckHooks_ReportsLegacyConfigAsNotInstalled makes the migration
// reachable from doctor: a settings file written by an older ox must not read
// as healthy just because it mentions `ox agent hook`.
func TestCheckHooks_ReportsLegacyConfigAsNotInstalled(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	settingsPath := filepath.Join(repo, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"hooks":{"PostToolUse":"AGENT_ENV=gemini ox agent hook PostToolUse"}}`
	if err := os.WriteFile(settingsPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	resp, err := handleCheckHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if resp.Installed {
		t.Error("legacy Claude-named hooks must report Installed=false so doctor migrates them")
	}
}

// TestDiagnoseHooks_FlagsTheInvalidShapeAsAnError makes the bricked state
// visible instead of merely absent.
func TestDiagnoseHooks_FlagsTheInvalidShapeAsAnError(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	settingsPath := filepath.Join(repo, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"hooks":{"PostToolUse":"AGENT_ENV=gemini ox agent hook PostToolUse"}}`
	if err := os.WriteFile(settingsPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	issues := diagnoseHooks(repo)

	var found bool
	for _, issue := range issues {
		if issue.Slug == "gemini:hooks-invalid-shape" {
			found = true
			if issue.Severity != "error" {
				t.Errorf("severity = %q, want error — this stops the gemini CLI from starting", issue.Severity)
			}
		}
	}
	if !found {
		t.Errorf("want a gemini:hooks-invalid-shape issue, got %+v", issues)
	}
}

// TestDiagnoseHooks_OffersSafeFixViaOx verifies both repairable gemini hook
// issues (invalid shape and missing) are repairable via the "ox" dispatch
// path, not the external adapter binary — ox-adapter-gemini as argv[0] is
// rejected by adapterFixArgvAllowlist in cmd/ox/doctor_adapters.go, so a
// FixArgv naming it would silently downgrade to display-only under
// `ox doctor --fix`.
// Failure prevented: FixSafe=true with an argv[0] the auto-fix path refuses,
// making the "safe, automatic" repair never actually run.
func TestDiagnoseHooks_OffersSafeFixViaOx(t *testing.T) {
	t.Parallel()

	wantArgv := []string{"ox", "integrate", "install", "--gemini"}

	t.Run("hooks-missing", func(t *testing.T) {
		repo := t.TempDir()

		issues := diagnoseHooks(repo)

		issue := findIssueBySlug(issues, "gemini:hooks-missing")
		if issue == nil {
			t.Fatalf("expected gemini:hooks-missing, got %+v", issues)
		}
		if !issue.FixSafe {
			t.Error("FixSafe = false, want true")
		}
		if !slicesEqual(issue.FixArgv, wantArgv) {
			t.Errorf("FixArgv = %v, want %v (argv[0] must be \"ox\" — "+
				"ox-adapter-gemini is not in the auto-fix allowlist)", issue.FixArgv, wantArgv)
		}
	})

	t.Run("hooks-invalid-shape", func(t *testing.T) {
		repo := t.TempDir()
		settingsPath := filepath.Join(repo, ".gemini", "settings.json")
		if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
			t.Fatal(err)
		}
		legacy := `{"hooks":{"PostToolUse":"AGENT_ENV=gemini ox agent hook PostToolUse"}}`
		if err := os.WriteFile(settingsPath, []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}

		issues := diagnoseHooks(repo)

		issue := findIssueBySlug(issues, "gemini:hooks-invalid-shape")
		if issue == nil {
			t.Fatalf("expected gemini:hooks-invalid-shape, got %+v", issues)
		}
		if !issue.FixSafe {
			t.Error("FixSafe = false, want true")
		}
		if !slicesEqual(issue.FixArgv, wantArgv) {
			t.Errorf("FixArgv = %v, want %v (argv[0] must be \"ox\" — "+
				"ox-adapter-gemini is not in the auto-fix allowlist)", issue.FixArgv, wantArgv)
		}
	})
}

func findIssueBySlug(issues []adapterprotocol.DiagnoseIssue, slug string) *adapterprotocol.DiagnoseIssue {
	for i := range issues {
		if issues[i].Slug == slug {
			return &issues[i]
		}
	}
	return nil
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestInstallHooks_IsIdempotent keeps repeated installs (doctor auto-fix runs
// on every invocation) from stacking duplicate hook definitions.
func TestInstallHooks_IsIdempotent(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	path := installInto(t, repo)
	installInto(t, repo)
	installInto(t, repo)

	hooks := readSettings(t, path)["hooks"].(map[string]any)
	for _, event := range hookEvents {
		groups := hooks[event].([]any)
		if len(groups) != 1 {
			t.Errorf("hooks.%s has %d definitions after 3 installs, want 1", event, len(groups))
		}
	}
}

// TestInstallHooks_PreservesForeignHooks proves ox only owns its own entry: a
// hook someone else configured on the same event must survive install and
// uninstall.
func TestInstallHooks_PreservesForeignHooks(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	settingsPath := filepath.Join(repo, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := `{
  "hooks": {
    "AfterTool": [
      {"matcher": "write_.*", "hooks": [{"type": "command", "command": "/usr/local/bin/audit.sh"}]}
    ]
  },
  "theme": "GitHub"
}`
	if err := os.WriteFile(settingsPath, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}

	installInto(t, repo)
	hooks := readSettings(t, settingsPath)["hooks"].(map[string]any)
	if groups := hooks["AfterTool"].([]any); len(groups) != 2 {
		t.Fatalf("AfterTool has %d definitions, want 2 (foreign + ox)", len(groups))
	}

	if _, err := handleUninstallHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	settings := readSettings(t, settingsPath)
	if settings["theme"] != "GitHub" {
		t.Error("unrelated settings must survive uninstall")
	}
	hooks, _ = settings["hooks"].(map[string]any)
	groups, ok := hooks["AfterTool"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("AfterTool = %v, want the foreign hook to survive uninstall", hooks["AfterTool"])
	}
	group := groups[0].(map[string]any)
	if group["matcher"] != "write_.*" {
		t.Errorf("surviving hook = %+v, want the foreign audit hook", group)
	}
}

// TestUninstallHooks_RemovesEverythingOxWrote covers the round trip, including
// dropping the now-empty hooks object.
func TestUninstallHooks_RemovesEverythingOxWrote(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	path := installInto(t, repo)

	if _, err := handleUninstallHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	settings := readSettings(t, path)
	if _, present := settings["hooks"]; present {
		t.Errorf("hooks key survived a full uninstall: %+v", settings["hooks"])
	}

	resp, err := handleCheckHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if resp.Installed {
		t.Error("check reports installed after uninstall")
	}
}

// TestInstallHooks_RefusesToClobberAForeignNonArrayValue keeps the migration
// from destroying a value ox did not write.
func TestInstallHooks_RefusesToClobberAForeignNonArrayValue(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	settingsPath := filepath.Join(repo, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"hooks":{"Stop":"someone-elses-script.sh"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"}); err == nil {
		t.Fatal("want an error rather than silently discarding a foreign value")
	}

	settings := readSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]any)
	if hooks["Stop"] != "someone-elses-script.sh" {
		t.Errorf("foreign value was modified: %v", hooks["Stop"])
	}
}
