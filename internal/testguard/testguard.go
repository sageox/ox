// Package testguard provides test isolation primitives that prevent
// ox subprocesses from hitting production endpoints during tests.
//
// All test code that runs ox as a subprocess MUST use RunOx or OxCmd
// instead of exec.Command + os.Environ(). This ensures:
//   - minimal environment (no leaked auth, daemon sockets, or SAGEOX_ENDPOINT)
//   - OX_NO_DAEMON=1 always injected
//   - production hostnames in env values cause immediate test failure
//   - mock server responses are validated for production URL leaks
package testguard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// allowedBaseEnv is the allowlist of environment variables inherited from
// the developer's shell. Everything else is blocked.
var allowedBaseEnv = []string{
	"PATH", "HOME", "TMPDIR", "USER", "LANG", "LC_ALL",
	"GOPATH", "GOROOT",
	// git needs these for commits
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
	"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
}

// productionHostPatterns are substrings that must NEVER appear in test
// env values or mock server responses unless exempted by allowedTestHostPatterns.
var productionHostPatterns = []string{
	"sageox.ai",
}

// allowedTestHostPatterns exempt specific hosts from the production block.
// Values containing these patterns are considered safe test infrastructure.
var allowedTestHostPatterns = []string{
	"test.sageox.ai",
	"localhost",
}

// RunOx executes an ox subprocess with isolated environment.
// Returns combined output, exit code, and duration.
// Uses a 60-second timeout to prevent hangs.
func RunOx(t *testing.T, oxBin, dir string, envVars []string, args ...string) (string, int, time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := OxCmdContext(t, ctx, oxBin, dir, envVars, args...)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	dur := time.Since(start)

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Logf("warning: command error (not exit code): %v", err)
			exitCode = -1
		}
	}

	return string(out), exitCode, dur
}

// OxCmd builds an isolated exec.Cmd for running the ox binary in tests.
func OxCmd(t *testing.T, oxBin, dir string, envVars []string, args ...string) *exec.Cmd {
	t.Helper()
	return OxCmdContext(t, context.Background(), oxBin, dir, envVars, args...)
}

// OxCmdContext builds an isolated exec.Cmd with an explicit context.
// Validates that no env value contains production hostnames.
func OxCmdContext(t *testing.T, ctx context.Context, oxBin, dir string, envVars []string, args ...string) *exec.Cmd {
	t.Helper()

	env := MinimalEnv(envVars)
	validateEnv(t, env)

	cmd := exec.CommandContext(ctx, oxBin, args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd
}

// MinimalEnv builds a clean environment from the allowlist + caller overrides.
// Always injects OX_NO_DAEMON=1.
func MinimalEnv(testVars []string) []string {
	var env []string
	for _, key := range allowedBaseEnv {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	env = append(env, "OX_NO_DAEMON=1")
	env = append(env, "DO_NOT_TRACK=1") // prevent friction telemetry hitting production
	env = append(env, testVars...)
	return env
}

// validateEnv checks that no env value contains production hostnames.
// Values matching allowedTestHostPatterns are exempted.
func validateEnv(t *testing.T, env []string) {
	t.Helper()
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := parts[0], parts[1]
		lower := strings.ToLower(val)
		for _, pattern := range productionHostPatterns {
			if !strings.Contains(lower, pattern) {
				continue
			}
			// check if exempted by a test host pattern
			exempted := false
			for _, allowed := range allowedTestHostPatterns {
				if strings.Contains(lower, allowed) {
					exempted = true
					break
				}
			}
			if !exempted {
				t.Fatalf("testguard: env var %s contains production host %q (value: %s); "+
					"use localhost or test.sageox.ai URLs in test fixtures", key, pattern, val)
			}
		}
	}
}

// MockServer is an alias for httptest.Server, returned by SafeMockServer.
type MockServer = httptest.Server

// SafeMockServer wraps an http.Handler to validate that response bodies
// never contain production URLs. If detected, the test fails immediately.
func SafeMockServer(t *testing.T, handler http.Handler) *MockServer {
	t.Helper()
	wrapped := &responseValidator{t: t, handler: handler}
	server := httptest.NewServer(wrapped)
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	return server
}

// testReporter is the subset of testing.T needed by responseValidator.
type testReporter interface {
	Errorf(format string, args ...any)
	Helper()
}

type responseValidator struct {
	t       testReporter
	handler http.Handler
}

func (rv *responseValidator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := &responseTee{ResponseWriter: w, body: &strings.Builder{}}
	rv.handler.ServeHTTP(rec, r)

	body := rec.body.String()
	lower := strings.ToLower(body)
	for _, pattern := range productionHostPatterns {
		if !strings.Contains(lower, pattern) {
			continue
		}
		// mock responses should use localhost, not even test.sageox.ai
		// (test infra URLs belong in env vars, not mock responses)
		rv.t.Errorf("testguard: mock response for %s %s contains production host %q; "+
			"use localhost URLs instead. Body: %s",
			r.Method, r.URL.Path, pattern, truncate(body, 500))
	}
}

type responseTee struct {
	http.ResponseWriter
	body *strings.Builder
}

func (rt *responseTee) Write(b []byte) (int, error) {
	rt.body.Write(b)
	return rt.ResponseWriter.Write(b)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// StopDaemonCleanup registers a cleanup that stops any daemon in the test env.
func StopDaemonCleanup(t *testing.T, oxBin, repoDir string, envVars []string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := OxCmdContext(t, ctx, oxBin, repoDir, envVars, "daemon", "stop")
		_ = cmd.Run()
	})
}

// BuildOxBinary compiles the ox binary and returns its path.
// This is the one place where os.Environ() is acceptable (go build only).
func BuildOxBinary(t *testing.T, projectRoot string) string {
	t.Helper()

	binDir := t.TempDir()
	binPath := fmt.Sprintf("%s/ox", binDir)

	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/ox")
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0") // safe: go-build only, no credentials
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build ox binary: %s", string(out))
	}

	return binPath
}
