package ephemeral

import (
	"testing"

	"github.com/sageox/ox/internal/runtime"
)

// resetCapsAndUserConfig clears the cached capability probe AND the
// user-config preference between subtests. The runtime cache is the
// process-wide source of truth for IsEphemeral(); without resetting it
// t.Setenv changes are invisible to the cached struct.
func resetCapsAndUserConfig(t *testing.T) {
	t.Helper()
	runtime.SetUserConfigEphemeralPreference(nil)
	runtime.Reset()
	t.Cleanup(func() {
		runtime.SetUserConfigEphemeralPreference(nil)
		runtime.Reset()
	})
}

// clearEnv unsets every env var the runtime probe / venue helpers
// consult, so each subtest starts from a clean baseline. t.Setenv handles
// restoration. CI vars are cleared too — they no longer contribute to
// Reason() but presence here defends against future regressions.
func clearEnv(t *testing.T) {
	t.Helper()
	vars := []string{
		EnvEphemeral,
		"CLAUDE_CODE_REMOTE",
		"DEVIN_TASK_ID",
		"CODESPACES",
		"CI",
		"GITHUB_ACTIONS",
		"GITLAB_CI",
		"JENKINS_URL",
		"BUILDKITE",
		"CODEBUILD_BUILD_ID",
		"OX_PERSIST_DISK",
		"OX_NO_DAEMON",
		"OX_BROWSER",
		"OX_NETWORK",
	}
	for _, v := range vars {
		t.Setenv(v, "")
	}
}

func TestIsEphemeral_CleanEnv(t *testing.T) {
	clearEnv(t)
	resetCapsAndUserConfig(t)
	if IsEphemeral() {
		t.Fatalf("expected IsEphemeral=false on clean env, got true (reason=%q)", Reason())
	}
	if got := Reason(); got != "" {
		t.Fatalf("expected Reason=\"\" on clean env, got %q", got)
	}
}

func TestIsEphemeral_ExplicitOverride(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want bool
	}{
		{"one", "1", true},
		{"true_lower", "true", true},
		{"true_mixed", "True", true},
		{"yes", "yes", true},
		{"on", "on", true},
		{"zero", "0", false},
		{"false", "false", false},
		{"no", "no", false},
		{"empty", "", false},
		{"whitespace_truthy", "  1  ", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			resetCapsAndUserConfig(t)
			t.Setenv(EnvEphemeral, tc.val)
			if got := IsEphemeral(); got != tc.want {
				t.Fatalf("OX_EPHEMERAL=%q: IsEphemeral=%v, want %v (reason=%q)", tc.val, got, tc.want, Reason())
			}
		})
	}
}

func TestIsEphemeral_IndividualSignals(t *testing.T) {
	cases := []struct {
		name   string
		env    string
		val    string
		want   bool
		reason string
	}{
		{"claude_cloud", "CLAUDE_CODE_REMOTE", "1", true, "CLAUDE_CODE_REMOTE"},
		{"claude_cloud_any_value", "CLAUDE_CODE_REMOTE", "task-abc", true, "CLAUDE_CODE_REMOTE"},
		{"devin", "DEVIN_TASK_ID", "t_xyz", true, "DEVIN_TASK_ID"},
		// Codespaces has persistent disk — it behaves like a slow laptop and
		// is no longer in the auto-detect chain. A Codespace user that wants
		// ephemeral behavior sets OX_EPHEMERAL=1 in their devcontainer.json.
		{"codespaces_true_does_not_trigger", "CODESPACES", "true", false, ""},
		{"codespaces_other", "CODESPACES", "1", false, ""},
		// CI signals trigger the derived IsEphemeral() predicate because
		// they collapse DaemonViable (minutes-shaped lifetime) — but they
		// do NOT raise a venue Reason() and they do NOT collapse
		// PersistDisk. Subsystems that historically gated on "FS doesn't
		// persist" (kb merge) should gate on runtime.Caps().PersistDisk
		// directly, not on this composite predicate. CI=true → IsEphemeral
		// is true (no daemon), Reason is "" (no venue marker), kb merge
		// still runs (PersistDisk holds).
		{"ci_generic_is_ephemeral_via_no_daemon", "CI", "true", true, ""},
		{"github_actions_is_ephemeral_via_no_daemon", "GITHUB_ACTIONS", "true", true, ""},
		// GitLab CI / Jenkins / Buildkite / CodeBuild don't set CI or
		// GITHUB_ACTIONS — Probe() doesn't recognize them as a
		// short-lifetime signal, so DaemonViable holds and IsEphemeral
		// stays false. This pins that contract; if these markers ever
		// move into probeLifetime, update the table.
		{"gitlab_ci", "GITLAB_CI", "true", false, ""},
		{"jenkins", "JENKINS_URL", "http://jenkins.example", false, ""},
		{"buildkite", "BUILDKITE", "true", false, ""},
		{"codebuild", "CODEBUILD_BUILD_ID", "build:abc", false, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			resetCapsAndUserConfig(t)
			t.Setenv(tc.env, tc.val)
			if got := IsEphemeral(); got != tc.want {
				t.Fatalf("%s=%q: IsEphemeral=%v, want %v", tc.env, tc.val, got, tc.want)
			}
			if got := Reason(); got != tc.reason {
				t.Fatalf("%s=%q: Reason=%q, want %q", tc.env, tc.val, got, tc.reason)
			}
		})
	}
}

func TestReason_Precedence(t *testing.T) {
	// when multiple signals fire, Reason returns the highest-precedence one:
	// OX_EPHEMERAL > CLAUDE_CODE_REMOTE > DEVIN_TASK_ID > user-config
	clearEnv(t)
	resetCapsAndUserConfig(t)
	t.Setenv(EnvEphemeral, "1")
	t.Setenv("CLAUDE_CODE_REMOTE", "1")
	t.Setenv("DEVIN_TASK_ID", "x")
	if got := Reason(); got != EnvEphemeral {
		t.Fatalf("expected OX_EPHEMERAL to win precedence, got %q", got)
	}

	clearEnv(t)
	resetCapsAndUserConfig(t)
	t.Setenv("CLAUDE_CODE_REMOTE", "1")
	t.Setenv("DEVIN_TASK_ID", "x")
	if got := Reason(); got != "CLAUDE_CODE_REMOTE" {
		t.Fatalf("expected CLAUDE_CODE_REMOTE to beat DEVIN, got %q", got)
	}

	clearEnv(t)
	resetCapsAndUserConfig(t)
	t.Setenv("DEVIN_TASK_ID", "x")
	if got := Reason(); got != "DEVIN_TASK_ID" {
		t.Fatalf("expected DEVIN_TASK_ID to win when set alone, got %q", got)
	}
}

// TestCodespacesDoesNotTriggerEphemeral defends the design decision that
// Codespaces, which ships persistent disk, is treated like a slow laptop
// rather than a sandbox. Regression: if a future refactor re-adds the
// CODESPACES branch to Reason(), every Codespace user loses the on-disk
// state they paid for.
func TestCodespacesDoesNotTriggerEphemeral(t *testing.T) {
	clearEnv(t)
	resetCapsAndUserConfig(t)
	t.Setenv("CODESPACES", "true")
	if IsEphemeral() {
		t.Fatalf("CODESPACES=true must not enable ephemeral mode (reason=%q)", Reason())
	}

	// explicit opt-in still works — need to reset caps because IsEphemeral
	// reads from the cached probe
	runtime.Reset()
	t.Setenv(EnvEphemeral, "1")
	if !IsEphemeral() {
		t.Fatalf("Codespace user with OX_EPHEMERAL=1 must still be ephemeral")
	}
}

// TestCISignalsKeepPersistDisk pins the invariant that originally lived
// as "CI doesn't trigger ephemeral": kb merge and any other PersistDisk-
// gated subsystem must continue to work inside a CI job. The composite
// IsEphemeral() predicate now fires under CI=true (DaemonViable
// collapses), but PersistDisk holds so the subsystems that actually care
// about FS persistence are unaffected. Regression: if a future refactor
// collapses PersistDisk on CI=true, every kb merge test breaks again.
func TestCISignalsKeepPersistDisk(t *testing.T) {
	ciVars := []string{
		"CI", "GITHUB_ACTIONS", "GITLAB_CI",
		"JENKINS_URL", "BUILDKITE", "CODEBUILD_BUILD_ID",
	}
	for _, v := range ciVars {
		t.Run(v, func(t *testing.T) {
			clearEnv(t)
			resetCapsAndUserConfig(t)
			t.Setenv(v, "true")
			c := runtime.Caps()
			if !c.PersistDisk {
				t.Fatalf("%s=true must not collapse PersistDisk (caps=%+v)", v, c)
			}
			if Reason() != "" {
				t.Fatalf("%s=true must not raise a venue Reason(), got %q", v, Reason())
			}
		})
	}
}

// TestIsEphemeral_UserConfigOnly verifies that a user-config opt-in flips
// IsEphemeral() to true even when no env var or venue marker is set.
func TestIsEphemeral_UserConfigOnly(t *testing.T) {
	clearEnv(t)
	resetCapsAndUserConfig(t)

	if IsEphemeral() {
		t.Fatalf("clean baseline must be non-ephemeral, got reason=%q", Reason())
	}

	on := true
	SetUserConfigPreference(&on)
	runtime.Reset() // user-config feeds into the cached probe; reprime

	if !IsEphemeral() {
		t.Fatalf("user-config=true must flip IsEphemeral() on")
	}
	if got := Reason(); got != runtime.ReasonUserConfig {
		t.Fatalf("expected Reason=%q, got %q", runtime.ReasonUserConfig, got)
	}
}

// TestIsEphemeral_EnvOverridesUserConfigDisable verifies that
// OX_EPHEMERAL=1 wins even when the user has persisted ephemeral=false.
func TestIsEphemeral_EnvOverridesUserConfigDisable(t *testing.T) {
	clearEnv(t)
	resetCapsAndUserConfig(t)

	off := false
	SetUserConfigPreference(&off)
	t.Setenv(EnvEphemeral, "1")
	runtime.Reset()

	if !IsEphemeral() {
		t.Fatalf("OX_EPHEMERAL=1 must override user-config=false, got reason=%q", Reason())
	}
	if got := Reason(); got != EnvEphemeral {
		t.Fatalf("expected Reason=%q, got %q", EnvEphemeral, got)
	}
}
