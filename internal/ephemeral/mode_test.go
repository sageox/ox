package ephemeral

import (
	"testing"
)

// resetUserConfig clears the user-config preference between subtests so
// each one starts from a known nil state. The package-level var is
// process-global; tests that set it must restore it.
func resetUserConfig(t *testing.T) {
	t.Helper()
	prev := userConfigEphemeral.Load()
	t.Cleanup(func() { userConfigEphemeral.Store(prev) })
	userConfigEphemeral.Store(nil)
}

// clearEnv unsets every env var that IsEphemeral consults, so each subtest
// starts from a clean baseline. t.Setenv handles per-test isolation and
// restoration; we just need to zero-out the inherited shell environment.
//
// CI-related vars (CI, GITHUB_ACTIONS, ...) are also cleared so the test
// suite is deterministic on CI runners; they no longer contribute to
// IsEphemeral but presence here defends against future regressions.
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
	}
	for _, v := range vars {
		t.Setenv(v, "")
	}
}

func TestIsEphemeral_CleanEnv(t *testing.T) {
	clearEnv(t)
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
		{"codespaces_true", "CODESPACES", "true", true, "CODESPACES"},
		{"codespaces_other", "CODESPACES", "1", false, ""},
		// CI signals deliberately do NOT trigger ephemeral mode — CI runners
		// have writable filesystems and within a job their state persists.
		// They only drive non-interactive UX, which is handled separately by
		// internal/config.IsCI. See package doc.
		{"ci_generic", "CI", "true", false, ""},
		{"github_actions", "GITHUB_ACTIONS", "true", false, ""},
		{"gitlab_ci", "GITLAB_CI", "true", false, ""},
		{"jenkins", "JENKINS_URL", "http://jenkins.example", false, ""},
		{"buildkite", "BUILDKITE", "true", false, ""},
		{"codebuild", "CODEBUILD_BUILD_ID", "build:abc", false, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
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
	// OX_EPHEMERAL > CLAUDE_CODE_REMOTE > DEVIN_TASK_ID > CODESPACES > user-config
	clearEnv(t)
	t.Setenv(EnvEphemeral, "1")
	t.Setenv("CLAUDE_CODE_REMOTE", "1")
	t.Setenv("DEVIN_TASK_ID", "x")
	t.Setenv("CODESPACES", "true")
	if got := Reason(); got != EnvEphemeral {
		t.Fatalf("expected OX_EPHEMERAL to win precedence, got %q", got)
	}

	clearEnv(t)
	t.Setenv("CLAUDE_CODE_REMOTE", "1")
	t.Setenv("DEVIN_TASK_ID", "x")
	t.Setenv("CODESPACES", "true")
	if got := Reason(); got != "CLAUDE_CODE_REMOTE" {
		t.Fatalf("expected CLAUDE_CODE_REMOTE to beat DEVIN/CODESPACES, got %q", got)
	}

	clearEnv(t)
	t.Setenv("DEVIN_TASK_ID", "x")
	t.Setenv("CODESPACES", "true")
	if got := Reason(); got != "DEVIN_TASK_ID" {
		t.Fatalf("expected DEVIN_TASK_ID to beat CODESPACES, got %q", got)
	}

	clearEnv(t)
	t.Setenv("CODESPACES", "true")
	if got := Reason(); got != "CODESPACES" {
		t.Fatalf("expected CODESPACES to win when set alone, got %q", got)
	}
}

// TestCISignalsDoNotTriggerEphemeral defends the invariant that CI=true
// does NOT enable ephemeral mode. Regression: when this list was wired in,
// every kb merge test failed in CI runs because kb sync was incorrectly
// disabled. CI affects interactivity, not filesystem persistence.
func TestCISignalsDoNotTriggerEphemeral(t *testing.T) {
	ciVars := []string{
		"CI", "GITHUB_ACTIONS", "GITLAB_CI",
		"JENKINS_URL", "BUILDKITE", "CODEBUILD_BUILD_ID",
	}
	for _, v := range ciVars {
		t.Run(v, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(v, "true")
			if IsEphemeral() {
				t.Fatalf("%s=true must not enable ephemeral mode (reason=%q)", v, Reason())
			}
		})
	}
}

func TestIndividualHelpers(t *testing.T) {
	clearEnv(t)
	if isClaudeCloud() || isDevin() || isCodespaces() {
		t.Fatalf("expected all individual helpers false on clean env")
	}

	t.Setenv("CLAUDE_CODE_REMOTE", "1")
	if !isClaudeCloud() {
		t.Fatalf("isClaudeCloud should be true")
	}

	clearEnv(t)
	t.Setenv("DEVIN_TASK_ID", "t1")
	if !isDevin() {
		t.Fatalf("isDevin should be true")
	}

	clearEnv(t)
	t.Setenv("CODESPACES", "true")
	if !isCodespaces() {
		t.Fatalf("isCodespaces should be true")
	}
	// only the literal "true" string activates Codespaces
	t.Setenv("CODESPACES", "yes")
	if isCodespaces() {
		t.Fatalf("isCodespaces should require exact \"true\"")
	}
}

// TestIsEphemeral_UserConfigOnly verifies that a user-config opt-in flips
// IsEphemeral() to true even when no env var or venue marker is set.
// Failure prevented: regression where the user-config layer is plumbed
// in but Reason() forgets to consult it, so the persisted preference
// silently does nothing.
func TestIsEphemeral_UserConfigOnly(t *testing.T) {
	clearEnv(t)
	resetUserConfig(t)

	if IsEphemeral() {
		t.Fatalf("clean baseline must be non-ephemeral, got reason=%q", Reason())
	}

	on := true
	SetUserConfigPreference(&on)

	if !IsEphemeral() {
		t.Fatalf("user-config=true must flip IsEphemeral() on")
	}
	if got := Reason(); got != reasonUserConfig {
		t.Fatalf("expected Reason=%q, got %q", reasonUserConfig, got)
	}
}

// TestIsEphemeral_EnvOverridesUserConfigDisable verifies that
// OX_EPHEMERAL=1 wins even when the user has persisted ephemeral=false.
// Failure prevented: a future refactor accidentally inverts precedence so
// a stale user-config setting suppresses an explicit per-invocation
// env override.
func TestIsEphemeral_EnvOverridesUserConfigDisable(t *testing.T) {
	clearEnv(t)
	resetUserConfig(t)

	off := false
	SetUserConfigPreference(&off)
	t.Setenv(EnvEphemeral, "1")

	if !IsEphemeral() {
		t.Fatalf("OX_EPHEMERAL=1 must override user-config=false, got reason=%q", Reason())
	}
	if got := Reason(); got != EnvEphemeral {
		t.Fatalf("expected Reason=%q, got %q", EnvEphemeral, got)
	}
}
