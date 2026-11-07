package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Tests for devin-plugin/bin/ox-devin-hook, driven as Devin drives it: event
// JSON on stdin, a fake ox on PATH, and a gated personal SAGEOX_TOKEN that only
// reaches commands Devin runs (save-token), never the hook itself.

const hookTestToken = "hooktest-token-7f3a9c2e81d4"

type hookEnv struct {
	t     *testing.T
	home  string
	state string
	repo  string
	sf    string
}

func newHookEnv(t *testing.T) *hookEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("short: runs the bash hook with git and python3 subprocesses")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the Devin hook targets Linux VMs")
	}
	for _, bin := range []string{"bash", "python3", "git", "flock"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	t.Parallel()
	root := t.TempDir()
	e := &hookEnv{t: t, home: filepath.Join(root, "home"), state: filepath.Join(root, "state")}
	e.repo = filepath.Join(e.home, "repos", "proj")
	e.sf = filepath.Join(root, "session.jsonl")
	if err := os.MkdirAll(filepath.Join(e.repo, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.repo, ".sageox", "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", e.repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	// Fake ox: the policy follows OX_SESSION_RECORDING, as ox's effective config does.
	// prime leaves a background process behind, as the real ox daemon does.
	fakeOx := `#!/bin/sh
case "$1 $2" in
  "config get") printf '{"value":"%s"}\n' "${OX_SESSION_RECORDING:-auto}" ;;
  "agent prime") sleep 120 </dev/null >/dev/null 2>&1 & echo 'agent_id="OxTest1"' ;;
  "status --json") echo '{}' ;;
  "agent OxTest1") case "$3 $4" in
    "session start") printf '{"session_file":"%s"}\n' "$FAKE_SESSION_FILE" ;;
    "session stop") echo '"entry_count": 2,' ;;
  esac ;;
esac
`
	bin := filepath.Join(e.home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ox"), []byte(fakeOx), 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *hookEnv) run(dir string, extraEnv []string, stdin string, args ...string) (string, int) {
	e.t.Helper()
	// Other tests in this package chdir, so locate the hook from this file.
	_, self, _, _ := runtime.Caller(0)
	hook := filepath.Join(filepath.Dir(self), "..", "..", "devin-plugin", "bin", "ox-devin-hook")
	cmd := exec.Command("bash", append([]string{hook}, args...)...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	env := []string{}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "HOME", "SAGEOX_TOKEN", "OX_SESSION_RECORDING", "OX_PROJECT_ROOT", "DEVIN_PROJECT_DIR", "CLAUDE_PROJECT_DIR", "OX_DEVIN_STATE":
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, "HOME="+e.home, "OX_DEVIN_STATE="+e.state, "FAKE_SESSION_FILE="+e.sf)
	cmd.Env = append(cmd.Env, extraEnv...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	// Don't wait on output pipes a leftover background process may hold open.
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	code := 0
	if errors.Is(err, exec.ErrWaitDelay) {
		e.t.Fatalf("hook left a background process holding its output: %v", err)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	return out.String(), code
}

func postToolUse(t *testing.T, output string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"session_id":    "devin-hooktest",
		"tool_name":     "exec",
		"tool_input":    map[string]any{"command": "env", "env": map[string]string{"SAGEOX_TOKEN": "secret:personal:SAGEOX_TOKEN"}},
		"tool_response": map[string]any{"output": output},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestDevinHookSaveTokenRejectsUnresolvedSecret verifies save-token refuses an
// unset token or a bare secret reference.
// Failure prevented: hooks authenticate with "secret:personal:..." and every upload fails.
func TestDevinHookSaveTokenRejectsUnresolvedSecret(t *testing.T) {
	e := newHookEnv(t)
	for _, env := range [][]string{nil, {"SAGEOX_TOKEN=secret:personal:SAGEOX_TOKEN"}} {
		if out, code := e.run(e.repo, env, "", "save-token", "devin-hooktest"); code != 1 {
			t.Fatalf("env %v: exit %d, want 1: %s", env, code, out)
		}
	}
	if _, err := os.Stat(filepath.Join(e.state, "devin-hooktest", "token")); !os.IsNotExist(err) {
		t.Fatalf("token cached from an unresolved secret: %v", err)
	}
}

// TestDevinHookSaveTokenCachesTokenAndRepo verifies save-token caches the token
// owner-only, never prints it, and keeps the session inside the state dir.
// Failure prevented: the token leaks to other users, to output, or outside STATE_ROOT.
func TestDevinHookSaveTokenCachesTokenAndRepo(t *testing.T) {
	e := newHookEnv(t)
	out, code := e.run(e.repo, []string{"SAGEOX_TOKEN=" + hookTestToken}, "", "save-token", "devin-hooktest/../x")
	if code != 0 || strings.Contains(out, hookTestToken) {
		t.Fatalf("exit %d, output %q", code, out)
	}
	dir := filepath.Join(e.state, "devin-hooktestx")
	fi, err := os.Stat(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("token mode %v, want 0600", fi.Mode().Perm())
	}
	repo, _ := os.ReadFile(filepath.Join(dir, "repo"))
	want, _ := filepath.EvalSymlinks(e.repo)
	if got, _ := filepath.EvalSymlinks(strings.TrimSpace(string(repo))); got != want {
		t.Fatalf("repo = %q, want %q", repo, e.repo)
	}
}

// TestDevinHookRequestsTokenOnce verifies a tokenless session is asked once to
// run save-token, even when Devin's project dir is "/" and no workdir is known,
// and that a recording opt-out suppresses the request.
// Failure prevented: gated-secret users are never recorded, are nagged every
// call, or opted-out users are asked to hand over their token.
func TestDevinHookRequestsTokenOnce(t *testing.T) {
	e := newHookEnv(t)
	env := []string{"DEVIN_PROJECT_DIR=/"}
	out, _ := e.run("/", append(env, "OX_SESSION_RECORDING=disabled"), postToolUse(t, "x"), "PostToolUse")
	if out != "" {
		t.Fatalf("opted-out session was asked for its token: %s", out)
	}
	out, _ = e.run("/", env, postToolUse(t, "x"), "PostToolUse")
	var reply struct {
		HookSpecificOutput struct{ AdditionalContext string } `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil || !strings.Contains(reply.HookSpecificOutput.AdditionalContext, "save-token devin-hooktest") {
		t.Fatalf("no save-token request: %v %q", err, out)
	}
	if out, _ = e.run("/", env, postToolUse(t, "x"), "PostToolUse"); out != "" {
		t.Fatalf("token requested twice: %s", out)
	}
}

// TestDevinHookRedactsTokenInBufferedEvents verifies a token echoed before
// save-token is scrubbed when the buffered event joins the recording.
// Failure prevented: the user's token is uploaded to the team Ledger.
func TestDevinHookRedactsTokenInBufferedEvents(t *testing.T) {
	e := newHookEnv(t)
	env := []string{"DEVIN_PROJECT_DIR=/"}
	e.run("/", env, postToolUse(t, "before SAGEOX_TOKEN="+hookTestToken), "PostToolUse")
	if out, code := e.run(e.repo, []string{"SAGEOX_TOKEN=" + hookTestToken}, "", "save-token", "devin-hooktest"); code != 0 {
		t.Fatalf("save-token: %d %s", code, out)
	}
	e.run("/", env, postToolUse(t, "after SAGEOX_TOKEN="+hookTestToken), "PostToolUse")
	rec, err := os.ReadFile(e.sf)
	if err != nil {
		t.Fatalf("no recording: %v", err)
	}
	if strings.Contains(string(rec), hookTestToken) {
		t.Fatalf("token in recording:\n%s", strings.ReplaceAll(string(rec), hookTestToken, "<TOKEN>"))
	}
	if n := strings.Count(string(rec), "[REDACTED]"); n != 2 {
		t.Fatalf("want 2 redacted entries (buffered + live), got %d:\n%s", n, rec)
	}
}

// TestDevinHookStopUploadsAfterOxSpawnsDaemon verifies the turn-end upload
// runs even when starting the recording left a long-lived ox process behind.
// Failure prevented: the daemon inherits the session lock, every Stop times
// out on it silently, and no session ever uploads.
func TestDevinHookStopUploadsAfterOxSpawnsDaemon(t *testing.T) {
	e := newHookEnv(t)
	if out, code := e.run(e.repo, []string{"SAGEOX_TOKEN=" + hookTestToken}, "", "save-token", "devin-hooktest"); code != 0 {
		t.Fatalf("save-token: %d %s", code, out)
	}
	e.run("/", nil, postToolUse(t, "hello"), "PostToolUse")
	start := time.Now()
	e.run("/", nil, `{"session_id":"devin-hooktest"}`, "Stop")
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("Stop took %s; it waited on a lock held by ox's background process", d)
	}
	log, err := os.ReadFile(filepath.Join(e.state, "hook.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "stop agent=OxTest1 rc=0") {
		t.Fatalf("Stop did not upload; hook.log:\n%s", log)
	}
}
