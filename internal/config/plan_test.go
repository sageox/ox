package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateUserConfig points OX_USER_CONFIG at a nonexistent file so the
// real user config on the test machine can't leak plan settings into a
// resolver under test. Tests that want explicit user config write their own
// YAML and re-point OX_USER_CONFIG at it.
func isolateUserConfig(t *testing.T) {
	t.Helper()
	t.Setenv("OX_USER_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
}

// clearPlanHTMLEnv ensures SAGEOX_PLAN_HTML is not inherited from the ambient
// environment for tests that assert config/default resolution.
func clearPlanHTMLEnv(t *testing.T) {
	t.Helper()
	require.NoError(t, os.Unsetenv(EnvPlanHTML))
}

// --- A. Documented defaults hold when nothing is configured ---
// Failure prevented: a defaulted key silently returning the Go zero value
// (false / "") because the resolver forgot the pointer-vs-default distinction.

func TestPlanSave_DefaultsTrue(t *testing.T) {
	isolateUserConfig(t)
	// empty project root and a fresh temp project must both resolve to the
	// documented default (true).
	assert.Equal(t, DefaultPlanSave, PlanSave(""), "empty project root")
	assert.Equal(t, DefaultPlanSave, PlanSave(t.TempDir()), "uninitialized temp project")
	assert.True(t, DefaultPlanSave, "guard: plan.save default must be true")
}

func TestPlanHTML_DefaultsRecommend(t *testing.T) {
	isolateUserConfig(t)
	clearPlanHTMLEnv(t)
	assert.Equal(t, PlanHTMLRecommend, PlanHTML(""), "empty project root")
	assert.Equal(t, PlanHTMLRecommend, PlanHTML(t.TempDir()), "uninitialized temp project")
	assert.Equal(t, PlanHTMLRecommend, DefaultPlanHTML, "guard: default enum is recommend")
}

// --- B. Explicit project config overrides the default ---
// Failure prevented: explicit-false on the true-by-default save key being
// ignored (the entire reason PlanConfig.Save is *bool, not bool); and an
// explicit html enum value not taking effect.

func TestPlanSave_ProjectOverride(t *testing.T) {
	tests := []struct {
		name string
		save *bool
		want bool
	}{
		{"explicit false sticks against true default", boolPtr(false), false},
		{"explicit true", boolPtr(true), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateUserConfig(t)
			dir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
				ProjectID:   "test_project",
				WorkspaceID: "test_workspace",
				Plan:        &PlanConfig{Save: tt.save},
			})
			assert.Equal(t, tt.want, PlanSave(dir))
		})
	}
}

func TestPlanHTML_ProjectOverride(t *testing.T) {
	for _, val := range []string{PlanHTMLOff, PlanHTMLRecommend, PlanHTMLAlways} {
		t.Run(val, func(t *testing.T) {
			isolateUserConfig(t)
			clearPlanHTMLEnv(t)
			dir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
				ProjectID:   "test_project",
				WorkspaceID: "test_workspace",
				Plan:        &PlanConfig{HTML: StringPtr(val)},
			})
			assert.Equal(t, val, PlanHTML(dir))
		})
	}
}

// off must truly silence — proves the off value round-trips through config and
// is returned verbatim (the renderer/prime guidance keys off this exact value).
func TestPlanHTML_OffSilences(t *testing.T) {
	isolateUserConfig(t)
	clearPlanHTMLEnv(t)
	dir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		ProjectID:   "test_project",
		WorkspaceID: "test_workspace",
		Plan:        &PlanConfig{HTML: StringPtr(PlanHTMLOff)},
	})
	assert.Equal(t, PlanHTMLOff, PlanHTML(dir), "plan.html=off must resolve to off, not the recommend default")
}

// --- C. Precedence: user config > project config > default ---
// Failure prevented: a project-level value masking a user-level preference,
// or the precedence chain short-circuiting at the wrong rung.

func TestPlanSave_UserBeatsProject(t *testing.T) {
	// user=false, project=true → user wins (false). Proves the user rung is
	// consulted first AND that explicit-false on the user side is honored.
	userDir := t.TempDir()
	userCfgPath := filepath.Join(userDir, "config.yaml")
	require.NoError(t, os.WriteFile(userCfgPath, []byte("plan:\n  save: false\n"), 0644))
	t.Setenv("OX_USER_CONFIG", userCfgPath)

	dir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		ProjectID:   "test_project",
		WorkspaceID: "test_workspace",
		Plan:        &PlanConfig{Save: boolPtr(true)},
	})

	assert.False(t, PlanSave(dir), "user explicit-false must override project explicit-true")
}

func TestPlanSave_ProjectUsedWhenUserUnset(t *testing.T) {
	// user unset, project=false → project value (false) is used, not default.
	isolateUserConfig(t)
	dir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		ProjectID:   "test_project",
		WorkspaceID: "test_workspace",
		Plan:        &PlanConfig{Save: boolPtr(false)},
	})
	assert.False(t, PlanSave(dir), "project explicit-false used when user is unset")
}

func TestPlanHTML_UserBeatsProject(t *testing.T) {
	// user=always, project=off → user wins (always).
	userDir := t.TempDir()
	userCfgPath := filepath.Join(userDir, "config.yaml")
	require.NoError(t, os.WriteFile(userCfgPath, []byte("plan:\n  html: always\n"), 0644))
	t.Setenv("OX_USER_CONFIG", userCfgPath)
	clearPlanHTMLEnv(t)

	dir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		ProjectID:   "test_project",
		WorkspaceID: "test_workspace",
		Plan:        &PlanConfig{HTML: StringPtr(PlanHTMLOff)},
	})

	assert.Equal(t, PlanHTMLAlways, PlanHTML(dir), "user plan.html must override project plan.html")
}

func TestPlanHTML_ProjectUsedWhenUserUnset(t *testing.T) {
	isolateUserConfig(t)
	clearPlanHTMLEnv(t)
	dir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		ProjectID:   "test_project",
		WorkspaceID: "test_workspace",
		Plan:        &PlanConfig{HTML: StringPtr(PlanHTMLOff)},
	})
	assert.Equal(t, PlanHTMLOff, PlanHTML(dir), "project plan.html used when user is unset")
}

// --- D. Env override SAGEOX_PLAN_HTML is a direct enum override ---
// Failure prevented: the env not winning over config; or the env being treated
// as a magic-string special case instead of a plain enum value.

func TestPlanHTML_EnvDirectOverride(t *testing.T) {
	// Each valid enum value, set via env, wins over a contradicting config.
	for _, envVal := range []string{PlanHTMLOff, PlanHTMLRecommend, PlanHTMLAlways} {
		t.Run("env="+envVal+" beats project", func(t *testing.T) {
			isolateUserConfig(t)
			t.Setenv(EnvPlanHTML, envVal)
			// project says off; env should win regardless of what it says.
			dir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
				ProjectID:   "test_project",
				WorkspaceID: "test_workspace",
				Plan:        &PlanConfig{HTML: StringPtr(PlanHTMLOff)},
			})
			assert.Equal(t, envVal, PlanHTML(dir), "env enum value must win over project config")
		})
	}
}

func TestPlanHTML_EnvBeatsUser(t *testing.T) {
	userDir := t.TempDir()
	userCfgPath := filepath.Join(userDir, "config.yaml")
	require.NoError(t, os.WriteFile(userCfgPath, []byte("plan:\n  html: off\n"), 0644))
	t.Setenv("OX_USER_CONFIG", userCfgPath)
	t.Setenv(EnvPlanHTML, PlanHTMLAlways)
	assert.Equal(t, PlanHTMLAlways, PlanHTML(""), "env must override user config")
}

func TestPlanHTML_EnvCaseInsensitive(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv(EnvPlanHTML, "ALWAYS")
	assert.Equal(t, PlanHTMLAlways, PlanHTML(""), "env override should normalize case")
}

func TestPlanHTML_EnvUnknownFallsThrough(t *testing.T) {
	// An unknown or empty env value must NOT force anything — it falls through
	// to the configured value, then the default. Proves env is validated, not
	// blindly trusted.
	tests := []struct {
		name   string
		envVal string
		setEnv bool
		// project config html (empty = leave Plan nil → default applies)
		projectHTML string
		want        string
	}{
		{"unset env → default", "", false, "", PlanHTMLRecommend},
		{"empty env → default", "", true, "", PlanHTMLRecommend},
		{"junk env → default", "yes-please", true, "", PlanHTMLRecommend},
		{"junk env respects config", "yes-please", true, PlanHTMLAlways, PlanHTMLAlways},
		{"junk env does not suppress off config", "true", true, PlanHTMLOff, PlanHTMLOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateUserConfig(t)
			if tt.setEnv {
				t.Setenv(EnvPlanHTML, tt.envVal)
			} else {
				require.NoError(t, os.Unsetenv(EnvPlanHTML))
			}
			var plan *PlanConfig
			if tt.projectHTML != "" {
				plan = &PlanConfig{HTML: StringPtr(tt.projectHTML)}
			}
			dir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
				ProjectID:   "test_project",
				WorkspaceID: "test_workspace",
				Plan:        plan,
			})
			assert.Equal(t, tt.want, PlanHTML(dir),
				"unknown env value must fall through, never force a value")
		})
	}
}

// --- E. PlanConfig helpers ---

func TestPlanConfig_IsEmpty(t *testing.T) {
	assert.True(t, (*PlanConfig)(nil).IsEmpty(), "nil config is empty")
	assert.True(t, (&PlanConfig{}).IsEmpty(), "zero-value config is empty")
	assert.False(t, (&PlanConfig{Save: boolPtr(false)}).IsEmpty(), "save set → not empty")
	assert.False(t, (&PlanConfig{HTML: StringPtr(PlanHTMLOff)}).IsEmpty(), "html set → not empty")
}
