//go:build !short

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postHogE2E is a release-shaped ox binary whose PostHog events land on a
// local server, run from an isolated home with no agent in the environment.
type postHogE2E struct {
	bin     string
	env     []string
	workDir string
	home    string
	batches chan map[string]any
}

func newPostHogE2E(t *testing.T) *postHogE2E {
	t.Helper()
	batches := make(chan map[string]any, 8)
	srv := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.URL.Path == "/batch/" && json.NewDecoder(r.Body).Decode(&body) == nil {
			batches <- body
		}
		w.WriteHeader(http.StatusOK)
	}))

	bin := filepath.Join(t.TempDir(), "ox")
	build := exec.Command("go", "build", "-o", bin, "-ldflags",
		"-X github.com/sageox/ox/internal/telemetry.postHogKey=phc_e2e "+
			"-X github.com/sageox/ox/internal/telemetry.postHogHost="+srv.URL,
		"./cmd/ox")
	build.Dir = findModuleRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0") // safe: go build only, no credentials
	out, err := build.CombinedOutput()
	require.NoError(t, err, "%s", out)

	home := t.TempDir()
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		// Every SageOx API call, v1 telemetry included, stays on this server.
		"SAGEOX_ENDPOINT=" + srv.URL,
		// testguard opts every ox run out of telemetry; this test opts back in.
		"DO_NOT_TRACK=",
	}
	workDir := t.TempDir()
	return &postHogE2E{bin: bin, env: env, workDir: workDir, home: home, batches: batches}
}

// nextEvent waits for the next event to reach PostHog.
func (e *postHogE2E) nextEvent(t *testing.T) map[string]any {
	t.Helper()
	select {
	case body := <-e.batches:
		batch, ok := body["batch"].([]any)
		require.True(t, ok, "body: %v", body)
		require.Len(t, batch, 1)
		return batch[0].(map[string]any)
	case <-time.After(15 * time.Second):
		t.Fatal("no event reached PostHog")
		return nil
	}
}

// props waits for the next event and returns its properties.
func (e *postHogE2E) props(t *testing.T) map[string]any {
	t.Helper()
	props, ok := e.nextEvent(t)["properties"].(map[string]any)
	require.True(t, ok)
	return props
}

// TestPostHog_ReleaseBuildReportsCommandsUnderOneInstallID runs a real ox
// binary built with the ldflags goreleaser uses and watches what reaches
// PostHog.
//
// Failure prevented: a release that sends nothing (ldflags naming the wrong
// variables, main not recognizing the sender, the sender never starting),
// that reports one install under a new ID each run, that ignores
// DO_NOT_TRACK, or that reports commands another ox started.
func TestPostHog_ReleaseBuildReportsCommandsUnderOneInstallID(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds and runs a real ox binary")
	}
	e := newPostHogE2E(t)
	bin, env, workDir, home := e.bin, e.env, e.workDir, e.home
	nextEvent := func() map[string]any { return e.nextEvent(t) }
	batches := e.batches

	output, code, _ := testguard.RunOx(t, bin, workDir, env, "version")
	require.Equal(t, 0, code, output)
	first := nextEvent()
	output, code, _ = testguard.RunOx(t, bin, workDir, env, "version", "--json")
	require.Equal(t, 0, code, output)
	second := nextEvent()

	installID, err := os.ReadFile(filepath.Join(home, ".config", "sageox", "client_id"))
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(string(installID)), first["distinct_id"])
	assert.Equal(t, first["distinct_id"], second["distinct_id"], "one install, one ID")
	assert.Equal(t, "ox command run", second["event"])
	props, ok := second["properties"].(map[string]any)
	require.True(t, ok, "event: %v", second)
	assert.Equal(t, "version", props["command"])
	assert.Equal(t, []any{"json"}, props["flags"])
	assert.Equal(t, true, props["success"])
	assert.Equal(t, false, props["$process_person_profile"])
	assert.Equal(t, true, props["$geoip_disable"])

	// A failure reports its kind and which failure it was: here an agent ID
	// with no command after it.
	output, code, _ = testguard.RunOx(t, bin, workDir, env, "agent", "OxAbcd")
	require.Equal(t, 1, code, output)
	failed, ok := nextEvent()["properties"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, failed["success"])
	assert.Equal(t, "usage", failed["error_kind"])
	assert.Equal(t, "missing command after agent_id", failed["error_detail"])

	// Neither an opted-out user nor an ox started by another ox (a hook's
	// prime, sync starting the daemon) reports anything.
	for _, quiet := range []string{"DO_NOT_TRACK=1", envStartedByOx + "=1"} {
		output, code, _ = testguard.RunOx(t, bin, workDir, append(env, quiet), "version")
		require.Equal(t, 0, code, output)
	}
	select {
	case body := <-batches:
		t.Fatalf("an event reached PostHog after DO_NOT_TRACK=1 or %s=1: %v", envStartedByOx, body)
	case <-time.After(3 * time.Second):
	}
}

// TestPostHog_InvocationMistakesAndWhoMadeThem runs a real ox binary the way
// a person at a terminal would, in a repository set up for Codex, and makes
// the mistakes people make at a command line.
//
// Failure prevented: typos and bad flags that never reach PostHog, because
// cobra rejects them before ox builds its command context; and every person
// working in a repository that holds a .codex/ directory (ox writes
// .codex/hooks.json there) counted as an AI coworker.
func TestPostHog_InvocationMistakesAndWhoMadeThem(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds and runs a real ox binary")
	}
	e := newPostHogE2E(t)
	require.NoError(t, os.MkdirAll(filepath.Join(e.workDir, ".codex"), 0o755))

	output, code, _ := testguard.RunOx(t, e.bin, e.workDir, e.env, "version")
	require.Equal(t, 0, code, output)
	props := e.props(t)
	assert.Equal(t, "human", props["actor"], "a person in a repo with .codex/ is not an AI coworker")
	assert.Equal(t, "", props["agent_type"])

	mistakes := []struct {
		args    []string
		command string
		detail  string
	}{
		{[]string{"status", "--no-such-flag"}, "status", "unknown flag"},
		{[]string{"status", "--config"}, "status", "flag needs an argument"},
		{[]string{"no-such-command"}, "ox", "unknown command"},
		// Cobra checks required flags and flag groups after PersistentPreRunE,
		// once ox has built its context.
		{[]string{"session", "push-summary"}, "session push-summary", "required flag missing"},
		{[]string{"session", "redact", "--all", "--session", "x"}, "session redact", "conflicting flags"},
	}
	for _, m := range mistakes {
		output, code, _ := testguard.RunOx(t, e.bin, e.workDir, e.env, m.args...)
		require.NotEqual(t, 0, code, output)
		props := e.props(t)
		assert.Equal(t, m.command, props["command"], "%v", m.args)
		assert.Equal(t, "usage", props["error_kind"], "%v", m.args)
		assert.Equal(t, m.detail, props["error_detail"], "%v", m.args)
		assert.Equal(t, false, props["success"], "%v", m.args)
		assert.Equal(t, "human", props["actor"], "%v", m.args)
	}
}

// TestPostHog_EverydayFailuresSayWhatWentWrong runs a real ox binary into the
// failures people hit most often before a repository is fully set up, and
// checks each reaches PostHog with a kind and a fixed detail.
//
// Failure prevented: the commonest failures filed as error_kind=other with no
// error_detail, which made the usage dashboard unable to say why ox failed.
func TestPostHog_EverydayFailuresSayWhatWentWrong(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds and runs a real ox binary")
	}
	e := newPostHogE2E(t)
	repo := filepath.Join(e.workDir, "repo") // a git repository not set up for SageOx
	require.NoError(t, os.MkdirAll(repo, 0o755))
	gitInit := exec.Command("git", "init", "-q", repo)
	require.NoError(t, gitInit.Run())
	setUp := filepath.Join(e.workDir, "set-up") // set up for SageOx, Ledger not cloned yet
	require.NoError(t, os.MkdirAll(filepath.Join(setUp, ".sageox"), 0o755))
	require.NoError(t, exec.Command("git", "init", "-q", setUp).Run())

	failures := []struct {
		dir     string
		args    []string
		command string
		kind    string
		detail  string
	}{
		{repo, []string{"conversation", "list"}, "conversation list", "not_initialized", "no_team_context"},
		{repo, []string{"murmur", "hello"}, "murmur", "not_initialized", "no ledger found — run 'ox doctor --fix' or wait for daemon to clone"},
		{setUp, []string{"session", "download", "abc"}, "session download", "not_initialized", "no ledger path found (run 'ox doctor --fix' or wait for daemon to clone)"},
		{e.workDir, []string{"session", "upload", "abc"}, "session upload", "not_initialized", "not in a SageOx project (no .sageox directory found)"},
		{repo, []string{"session", "score", "0.5"}, "session score", "other", "SAGEOX_AGENT_ID not set -- run 'ox agent prime' first"},
		{repo, []string{"agent", "OxAbcd", "session", "start"}, "agent session start", "other", "instance not found: %s"},
		{repo, []string{"sync"}, "sync", "daemon", "daemon start disabled: OX_NO_DAEMON=1"},
		// How AI coworkers run it: the failure is printed as JSON, not as an error.
		{repo, []string{"sync", "--json"}, "sync", "daemon", "daemon start disabled: OX_NO_DAEMON=1"},
		// Last: a regression that let it through would initialize repo.
		{repo, []string{"init"}, "init", "not_logged_in", "ox init requires authentication"},
	}
	for _, f := range failures {
		output, code, _ := testguard.RunOx(t, e.bin, f.dir, e.env, f.args...)
		require.NotEqual(t, 0, code, output)
		props := e.props(t)
		assert.Equal(t, f.command, props["command"], "%v", f.args)
		assert.Equal(t, f.kind, props["error_kind"], "%v", f.args)
		assert.Equal(t, f.detail, props["error_detail"], "%v", f.args)
		if slices.Contains(f.args, "--json") {
			assert.Contains(t, output, `"success": false`, "the failure is reported as JSON")
			assert.NotContains(t, output, "Error:", "and not printed a second time as an error")
		}
	}
}
