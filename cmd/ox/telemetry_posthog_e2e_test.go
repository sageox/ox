//go:build !short

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/endpoint"
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

// Failure prevented: a release-shaped signed-in command drops its person, install or occurrence identity.
func TestPostHog_ReleaseBuildSignedInPerson(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds and runs a real ox binary")
	}
	e := newPostHogE2E(t)
	// Exercise the default production identity policy while every HTTPS request
	// is rejected locally. No connection or credential reaches production.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	env := make([]string, 0, len(e.env)+2)
	for _, kv := range e.env {
		if !strings.HasPrefix(kv, "SAGEOX_ENDPOINT=") {
			env = append(env, kv)
		}
	}
	env = append(env, "HTTPS_PROXY="+proxy.URL, "HTTP_PROXY="+proxy.URL)
	authDir := filepath.Join(e.home, ".config", "sageox")
	require.NoError(t, os.MkdirAll(authDir, 0o700))
	stored := map[string]any{"tokens": map[string]any{endpoint.Default: map[string]any{
		"access_token": "opaque_fixture", "expires_at": time.Now().Add(time.Hour).UTC(),
		"user_info": map[string]any{"sub": "usr_release_fixture", "email": "private_email_value", "name": "private_display_value"},
	}}}
	raw, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(authDir, "auth.json"), raw, 0o600))
	output, code, _ := testguard.RunOx(t, e.bin, e.workDir, env, "version", "--json")
	require.Equal(t, 0, code, output)
	event := e.nextEvent(t)
	props := event["properties"].(map[string]any)
	assert.Equal(t, "usr_release_fixture", event["distinct_id"])
	assert.Equal(t, "usr_release_fixture", props["user_id"])
	install, err := os.ReadFile(filepath.Join(authDir, "client_id"))
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(string(install)), props["install_id"])
	assert.NotEmpty(t, event["uuid"])
	assert.Equal(t, "production", props["env"])
	assert.Equal(t, "person", props["actor"])
	assert.Equal(t, "human", props["cli_actor"])
	assert.NotEmpty(t, props["cli_version"])
	assert.Equal(t, "cli", props["surface"])
	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	var safe struct {
		UUID       string    `json:"uuid"`
		Event      string    `json:"event"`
		DistinctID string    `json:"distinct_id"`
		Timestamp  time.Time `json:"timestamp"`
		Properties struct {
			Command     string   `json:"command"`
			Flags       []string `json:"flags"`
			Success     bool     `json:"success"`
			ExitCode    int      `json:"exit_code"`
			Duration    int64    `json:"duration_ms"`
			Actor       string   `json:"actor"`
			CLIActor    string   `json:"cli_actor"`
			AgentType   string   `json:"agent_type"`
			Surface     string   `json:"surface"`
			Env         string   `json:"env"`
			Version     int      `json:"event_version"`
			Initialized bool     `json:"repo_initialized"`
			Ephemeral   string   `json:"ephemeral"`
			InstallID   string   `json:"install_id"`
			UserID      string   `json:"user_id"`
			CLIVersion  string   `json:"cli_version"`
			Lib         string   `json:"$lib"`
			LibVersion  string   `json:"$lib_version"`
			Profile     bool     `json:"$process_person_profile"`
			GeoIP       bool     `json:"$geoip_disable"`
			OS          string   `json:"os"`
			Arch        string   `json:"arch"`
		} `json:"properties"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	// strict-reader-ok: verifies the closed event payload ox itself writes.
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&safe))
	for _, private := range []string{"opaque_fixture", "private_email_value", "private_display_value"} {
		assert.NotContains(t, string(encoded), private)
	}
}

func newPostHogE2E(t *testing.T, api ...http.HandlerFunc) *postHogE2E {
	t.Helper()
	batches := make(chan map[string]any, 8)
	srv := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.URL.Path == "/batch/" && json.NewDecoder(r.Body).Decode(&body) == nil {
			batches <- body
		} else if len(api) > 0 {
			api[0](w, r)
			return
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

// Failure prevented: command events are captured before completion, shifting reconstructed start times.
func TestPostHog_ReleaseBuildCapturesCommandEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds and runs a real ox binary")
	}
	arrived := make(chan time.Time, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	e := newPostHogE2E(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/cli/repos" {
			arrived <- time.Now().UTC()
			<-release
			_, _ = w.Write([]byte(`{"repos":{},"teams":[]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	type result struct {
		output string
		code   int
	}
	done := make(chan result, 1)
	begin := time.Now().UTC()
	go func() {
		output, code, _ := testguard.RunOx(t, e.bin, e.workDir, append(e.env, "SAGEOX_TOKEN=opaque_fixture"), "teams", "--json")
		done <- result{output, code}
	}()
	var requested time.Time
	select {
	case requested = <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("command never reached the local server")
	}
	// Hold command work for a measured interval, independent of capture setup.
	blocked := time.NewTimer(50 * time.Millisecond)
	defer blocked.Stop()
	<-blocked.C
	released := time.Now().UTC()
	unblock()
	select {
	case got := <-done:
		require.Equal(t, 0, got.code, got.output)
	case <-time.After(15 * time.Second):
		t.Fatal("command never finished")
	}
	event := e.nextEvent(t)
	end, err := time.Parse(time.RFC3339Nano, event["timestamp"].(string))
	require.NoError(t, err)
	assert.False(t, end.Before(released), "capture must follow the command's blocked API operation")
	props := event["properties"].(map[string]any)
	duration := time.Duration(props["duration_ms"].(float64)) * time.Millisecond
	assert.GreaterOrEqual(t, duration.Milliseconds(), released.Sub(requested).Milliseconds(), "duration must include blocked command work")
	start := end.Add(-duration)
	assert.False(t, start.Before(begin.Add(-time.Millisecond)))
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
	assert.Equal(t, "person", props["actor"], "a person in a repo with .codex/ is not an AI coworker")
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
		assert.Equal(t, "person", props["actor"], "%v", m.args)
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
		{e.workDir, []string{"team", "invite", "a@example.com"}, "team invite", "not_logged_in", "unauthenticated"},
		{repo, []string{"session", "import"}, "session import", "not_initialized", "not_initialized"},
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
