//go:build slow

// TZ-* e2e tests for the team-timezone revert.
//
// These tests assert the target state after units 1-7 of the journal-timezone
// revert have landed:
//   - Unit 4: "timezone" is removed from the ox config settings registry
//   - Unit 7: ox doctor --fix scrubs any stray `timezone` keys from both
//     project config.json and team-context config.toml
//
// The tests exercise the real `ox` binary via testguard and operate against a
// fully isolated XDG environment + per-test workspace, team context, and home
// directory. No daemon is started; no network calls are made. Config files are
// staged as raw bytes (os.WriteFile) — never via `ox config set` — so the
// fixtures still compile after Unit 5 deletes the Timezone struct fields.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tzWorkspace bundles the isolated paths and subprocess env for one TZ-* test.
type tzWorkspace struct {
	workspace string
	teamCtx   string
	home      string
	teamID    string
	endpoint  string
	env       []string
}

// tzProjectRoot walks up from this test file to the ox repo root.
// Using runtime.Caller avoids depending on the developer's cwd at `go test` time.
func tzProjectRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	cmdOxDir := filepath.Dir(thisFile)          // .../cmd/ox
	return filepath.Dir(filepath.Dir(cmdOxDir)) // repo root
}

// tzGit runs a git command inside dir with controlled identity, so tests never
// touch the developer's global git config.
func tzGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.local",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.local",
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v in %s failed: %s", args, dir, out)
}

// setupTZWorkspace builds a fresh workspace + team-context git repos and
// returns the subprocess env that routes ox into a fully isolated XDG home.
// Callers stage config files into w.workspace/.sageox and w.teamCtx
// after this returns.
func setupTZWorkspace(t *testing.T) tzWorkspace {
	t.Helper()

	workspace := t.TempDir()
	teamCtx := t.TempDir()
	home := t.TempDir()

	const teamID = "team_tz_e2e"
	const ep = "https://test.sageox.ai"

	// init workspace git repo with one commit so HEAD exists
	tzGit(t, workspace, "init")
	tzGit(t, workspace, "config", "user.name", "Test")
	tzGit(t, workspace, "config", "user.email", "test@test.local")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# tz e2e\n"), 0o644))
	tzGit(t, workspace, "add", "README.md")
	tzGit(t, workspace, "commit", "-m", "init")

	// .sageox/ must exist before SaveLocalConfig is called
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".sageox"), 0o755))

	// init team context git repo for the doctor configuration checks
	tzGit(t, teamCtx, "init")
	tzGit(t, teamCtx, "config", "user.name", "Test")
	tzGit(t, teamCtx, "config", "user.email", "test@test.local")
	require.NoError(t, os.WriteFile(filepath.Join(teamCtx, ".gitkeep"), []byte{}, 0o644))
	tzGit(t, teamCtx, "add", ".gitkeep")
	tzGit(t, teamCtx, "commit", "-m", "init")

	// register the team context so config.FindRepoTeamContext resolves it.
	// (Without this the fallback path would point into the XDG data dir, which
	// we want to keep empty so nothing ox does leaks onto disk.)
	localCfg := &config.LocalConfig{
		TeamContexts: []config.TeamContext{{
			TeamID:   teamID,
			TeamName: "TZ E2E",
			Path:     teamCtx,
		}},
	}
	require.NoError(t, config.SaveLocalConfig(workspace, localCfg))

	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_RUNTIME_DIR=" + filepath.Join(home, "runtime"),
		"SAGEOX_ENDPOINT=" + ep,
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.local",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.local",
		"GIT_CONFIG_NOSYSTEM=1",
	}

	return tzWorkspace{
		workspace: workspace,
		teamCtx:   teamCtx,
		home:      home,
		teamID:    teamID,
		endpoint:  ep,
		env:       env,
	}
}

// writeTZProjectConfig writes .sageox/config.json with raw bytes so the test
// controls exactly which keys land on disk — including stray keys that no
// longer exist on the ProjectConfig struct after Unit 5.
//
// extras values are interpreted as raw JSON. Pass `"\"Asia/Tokyo\""` to set a
// string field; pass `"42"` for a number; pass a literal plain string for
// convenient string-field setting.
func writeTZProjectConfig(t *testing.T, w tzWorkspace, extras map[string]string) {
	t.Helper()
	cfg := map[string]any{
		"config_version": "2",
		"repo_id":        "tz-e2e-repo",
		"team_id":        w.teamID,
		"team_name":      "TZ E2E",
		"endpoint":       w.endpoint,
	}
	for k, raw := range extras {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			v = raw
		}
		cfg[k] = v
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(w.workspace, ".sageox", "config.json"),
		data, 0o600))
}

// writeTZProjectConfigRaw writes raw bytes into .sageox/config.json. Use when a
// test needs exact-byte control over the JSON shape (e.g. idempotency checks
// that compare pre/post fix output byte-for-byte).
func writeTZProjectConfigRaw(t *testing.T, w tzWorkspace, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(w.workspace, ".sageox", "config.json"),
		[]byte(content), 0o600))
}

// writeTZTeamConfig writes raw TOML bytes into the team context config.toml.
func writeTZTeamConfig(t *testing.T, w tzWorkspace, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(w.teamCtx, "config.toml"),
		[]byte(content), 0o644))
}

// TestJournalTimezone_TZ02_ConfigSetTimezoneRejected verifies that after the
// revert, `ox config set timezone <value>` is not a valid setting and the
// command fails with an "unknown setting" error instead of silently persisting
// a value that no code path reads.
//
// Failure prevented: the timezone entry is re-introduced to the settings
// registry (cmd/ox/config_settings.go AllSettings) and users can once again
// store a stray timezone that contributes nothing to behavior.
func TestJournalTimezone_TZ02_ConfigSetTimezoneRejected(t *testing.T) {
	t.Parallel()

	oxBin := testguard.BuildOxBinary(t, tzProjectRoot(t))
	w := setupTZWorkspace(t)

	// FreshWorkspace fixture — no stray keys anywhere.
	writeTZProjectConfig(t, w, nil)

	out, code, _ := testguard.RunOx(t, oxBin, w.workspace, w.env,
		"config", "set", "timezone", "UTC")

	assert.NotEqual(t, 0, code, "ox config set timezone should fail; output: %s", out)
	assert.Contains(t, strings.ToLower(out), "unknown setting",
		"expected 'unknown setting' error for timezone; got: %s", out)
}

// --------------------------------------------------------------------------
// TZ-03 — ox config get timezone does not leak stray value
// --------------------------------------------------------------------------

// TestJournalTimezone_TZ03_ConfigGetTimezoneNoLeak verifies that `ox config get
// timezone` returns an "unknown setting" error even when a stray "timezone"
// key is present in the on-disk project config. Critically, the command must
// NOT echo the stray value back — doing so would mislead users into thinking
// the setting is honored.
//
// Failure prevented: the resolver falls through to a generic "read whatever
// key the user asked for from the JSON blob" code path that leaks dead keys
// even after they've been officially removed from the registry.
func TestJournalTimezone_TZ03_ConfigGetTimezoneNoLeak(t *testing.T) {
	t.Parallel()

	oxBin := testguard.BuildOxBinary(t, tzProjectRoot(t))
	w := setupTZWorkspace(t)

	// StrayProjectKey fixture — project config has a stray timezone key.
	writeTZProjectConfig(t, w, map[string]string{"timezone": `"Asia/Tokyo"`})

	out, code, _ := testguard.RunOx(t, oxBin, w.workspace, w.env,
		"config", "get", "timezone")

	assert.NotEqual(t, 0, code, "ox config get timezone should fail; output: %s", out)
	assert.Contains(t, strings.ToLower(out), "unknown setting",
		"expected 'unknown setting' error for timezone; got: %s", out)
	assert.NotContains(t, out, "Asia/Tokyo",
		"ox config get timezone must not leak the stray value; output: %s", out)
}

// --------------------------------------------------------------------------
// TZ-04 — ox doctor scrubs stray timezone keys (idempotent)
// --------------------------------------------------------------------------

// TestJournalTimezone_TZ04_DoctorScrubsStrayTimezoneKeys verifies that
// `ox doctor --fix --yes`:
//
//  1. Removes a stray "timezone" key from .sageox/config.json while preserving
//     all other fields (team_id, endpoint, badge_status, session_recording...).
//  2. Removes the `timezone = "..."` line from the team context config.toml
//     while preserving comments, other keys, and unrelated TOML sections.
//  3. Is idempotent — running it a second time produces no further mutations.
//
// Failure prevented: the autofix grows a side effect that mutates unrelated
// fields, or it flips between two byte representations on successive runs,
// causing spurious doctor churn in CI.
func TestJournalTimezone_TZ04_DoctorScrubsStrayTimezoneKeys(t *testing.T) {
	t.Parallel()

	oxBin := testguard.BuildOxBinary(t, tzProjectRoot(t))
	w := setupTZWorkspace(t)

	// StrayKeysWithUnrelatedContent fixture — both files carry stray timezone
	// keys AND surrounding content that must survive unchanged.
	projectJSON := `{
  "config_version": "2",
  "repo_id": "tz-e2e-repo",
  "team_id": "team_tz_e2e",
  "team_name": "TZ E2E",
  "endpoint": "https://test.sageox.ai",
  "badge_status": "added",
  "session_recording": "auto",
  "timezone": "Asia/Tokyo"
}
`
	projectJSONPath := filepath.Join(w.workspace, ".sageox", "config.json")
	writeTZProjectConfigRaw(t, w, projectJSON)

	teamTOML := `# top-of-file comment — must be preserved
session_recording = "auto"
timezone = "Europe/Berlin"  # stray — must be removed
session_notification = "whisper"

[owners]
primary = "galex"
# trailing comment — must be preserved
`
	teamTOMLPath := filepath.Join(w.teamCtx, "config.toml")
	writeTZTeamConfig(t, w, teamTOML)

	// first run — must scrub stray timezone keys from both files.
	// Note: the scrub check is FixLevelAuto and runs unconditionally regardless
	// of other check states, so exit code 1 from orthogonal critical failures
	// (login, marker) is accepted. File-state assertions below are the canonical
	// contract per team-lead's option (b) resolution.
	out1, code1, _ := testguard.RunOx(t, oxBin, w.workspace, w.env,
		"doctor", "--fix", "--yes")
	require.Contains(t, []int{0, 1}, code1,
		"ox doctor --fix exit code must be 0 or 1; got %d; output: %s", code1, out1)

	// --- assertions on project config.json after run 1 ---
	projectBytes1, err := os.ReadFile(projectJSONPath)
	require.NoError(t, err)

	var projectCfg1 map[string]any
	require.NoError(t, json.Unmarshal(projectBytes1, &projectCfg1),
		"project config.json must remain valid JSON after fix; got: %s", string(projectBytes1))
	_, hasTZ := projectCfg1["timezone"]
	assert.False(t, hasTZ,
		"project timezone key must be removed; got: %s", string(projectBytes1))
	assert.Equal(t, "team_tz_e2e", projectCfg1["team_id"], "team_id must survive")
	assert.Equal(t, "added", projectCfg1["badge_status"], "badge_status must survive")
	assert.Equal(t, "auto", projectCfg1["session_recording"], "session_recording must survive")
	assert.Equal(t, "tz-e2e-repo", projectCfg1["repo_id"], "repo_id must survive")

	// --- assertions on team context config.toml after run 1 ---
	teamBytes1, err := os.ReadFile(teamTOMLPath)
	require.NoError(t, err)
	teamStr1 := string(teamBytes1)

	tzKeyLine := regexp.MustCompile(`(?m)^\s*timezone\s*=`)
	assert.False(t, tzKeyLine.MatchString(teamStr1),
		"team config top-level timezone key must be removed; got:\n%s", teamStr1)
	assert.NotContains(t, teamStr1, "Europe/Berlin",
		"stray timezone VALUE must not survive; got:\n%s", teamStr1)
	assert.Contains(t, teamStr1, "top-of-file comment",
		"top-of-file comment must survive; got:\n%s", teamStr1)
	assert.Contains(t, teamStr1, "[owners]",
		"[owners] section must survive; got:\n%s", teamStr1)
	assert.Contains(t, teamStr1, `primary = "galex"`,
		"owners.primary must survive; got:\n%s", teamStr1)
	assert.Contains(t, teamStr1, "trailing comment",
		"trailing comment must survive; got:\n%s", teamStr1)
	assert.Contains(t, teamStr1, "session_recording",
		"session_recording key must survive; got:\n%s", teamStr1)
	assert.Contains(t, teamStr1, "session_notification",
		"session_notification key must survive; got:\n%s", teamStr1)

	// second run — must be a strict byte-level no-op on both files.
	// Same exit-code relaxation as run 1: orthogonal failures allowed.
	out2, code2, _ := testguard.RunOx(t, oxBin, w.workspace, w.env,
		"doctor", "--fix", "--yes")
	require.Contains(t, []int{0, 1}, code2,
		"second ox doctor --fix exit code must be 0 or 1; got %d; output: %s", code2, out2)

	projectBytes2, err := os.ReadFile(projectJSONPath)
	require.NoError(t, err)
	assert.Equal(t, string(projectBytes1), string(projectBytes2),
		"ox doctor --fix must be idempotent on project config.json")

	teamBytes2, err := os.ReadFile(teamTOMLPath)
	require.NoError(t, err)
	assert.Equal(t, string(teamBytes1), string(teamBytes2),
		"ox doctor --fix must be idempotent on team config.toml")
}
