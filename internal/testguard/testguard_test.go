package testguard

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- MinimalEnv ---

func TestMinimalEnv_AlwaysInjectsSentinels(t *testing.T) {
	env := MinimalEnv(nil)

	envMap := envToMap(env)
	assert.Equal(t, "1", envMap["OX_NO_DAEMON"], "must inject OX_NO_DAEMON=1")
	assert.Equal(t, "1", envMap["DO_NOT_TRACK"], "must inject DO_NOT_TRACK=1")
}

func TestMinimalEnv_InheritsAllowlistedVars(t *testing.T) {
	// set a known allowlisted var and confirm it's inherited
	t.Setenv("GOPATH", "/tmp/testgopath")
	t.Setenv("HOME", "/tmp/testhome")
	t.Setenv(TestGoCoverDirEnv, "/tmp/test-coverdir")

	env := MinimalEnv(nil)
	envMap := envToMap(env)

	assert.Equal(t, "/tmp/testgopath", envMap["GOPATH"])
	assert.Equal(t, "/tmp/testhome", envMap["HOME"])
	assert.Equal(t, "/tmp/test-coverdir", envMap["GOCOVERDIR"])
}

func TestMinimalEnv_DoesNotInheritGoTestInternalCoverDir(t *testing.T) {
	t.Setenv("GOCOVERDIR", "/tmp/go-test-workdir")
	t.Setenv(TestGoCoverDirEnv, "")

	env := envToMap(MinimalEnv(nil))
	assert.NotContains(t, env, "GOCOVERDIR")
}

func TestResolveTestOxBinary(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*testing.T) string
		wantErr string
	}{
		{
			name: "regular executable",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "ox")
				require.NoError(t, os.WriteFile(path, []byte("test binary"), 0o755))
				return path
			},
		},
		{
			name: "missing path",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing")
			},
			wantErr: "stat",
		},
		{
			name: "directory",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: "not a regular file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			resolved, err := resolveTestOxBinary(path)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, path, resolved)
		})
	}
}

func TestMinimalEnv_BlocksNonAllowlistedVars(t *testing.T) {
	blockedVars := []string{
		"SAGEOX_ENDPOINT",
		"AWS_SECRET_ACCESS_KEY",
		"ANTHROPIC_API_KEY",
		"DATABASE_URL",
		"RANDOM_VAR_12345",
	}
	for _, key := range blockedVars {
		t.Setenv(key, "some-value")
	}

	env := MinimalEnv(nil)
	envMap := envToMap(env)

	for _, key := range blockedVars {
		_, present := envMap[key]
		assert.False(t, present, "MinimalEnv must block non-allowlisted var %s", key)
	}
}

func TestMinimalEnv_TestVarsAppendedAfterBase(t *testing.T) {
	// test vars should appear after base env, so they can override
	// (e.g., a test providing its own PATH)
	overrides := []string{
		"SAGEOX_ENDPOINT=http://localhost:9999",
		"CUSTOM_FLAG=yes",
	}

	env := MinimalEnv(overrides)

	// find positions: OX_NO_DAEMON should come before test vars
	noDaemonIdx := -1
	endpointIdx := -1
	for i, kv := range env {
		if kv == "OX_NO_DAEMON=1" {
			noDaemonIdx = i
		}
		if strings.HasPrefix(kv, "SAGEOX_ENDPOINT=") {
			endpointIdx = i
		}
	}
	require.NotEqual(t, -1, noDaemonIdx, "OX_NO_DAEMON must be present")
	require.NotEqual(t, -1, endpointIdx, "test override must be present")
	assert.Less(t, noDaemonIdx, endpointIdx,
		"sentinel vars should appear before test overrides so overrides win")
}

func TestMinimalEnv_SkipsEmptyAllowlistedVars(t *testing.T) {
	// unset an allowlisted var; it should not appear as "KEY="
	t.Setenv("LC_ALL", "")

	env := MinimalEnv(nil)
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if parts[0] == "LC_ALL" {
			t.Fatal("MinimalEnv should skip allowlisted vars with empty values")
		}
	}
}

// --- validateEnv ---

func TestValidateEnv_SafeValues(t *testing.T) {
	// these should all pass without fataling
	tests := []struct {
		name string
		env  []string
	}{
		{
			name: "localhost is always safe",
			env:  []string{"ENDPOINT=http://localhost:8080"},
		},
		{
			name: "test.sageox.ai is exempted",
			env:  []string{"SAGEOX_ENDPOINT=https://test.sageox.ai"},
		},
		{
			name: "case insensitive test prefix",
			env:  []string{"ENDPOINT=https://TEST.SAGEOX.AI"},
		},
		{
			name: "malformed entry without equals is skipped",
			env:  []string{"MALFORMED_NO_EQUALS"},
		},
		{
			name: "empty value is safe",
			env:  []string{"SAGEOX_ENDPOINT="},
		},
		{
			name: "no sageox at all",
			env:  []string{"FOO=bar", "DB=postgres://localhost/test"},
		},
		{
			name: "localhost exempts even if sageox.ai substring present",
			env:  []string{"REDIRECT=http://localhost:3000?next=sageox.ai"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// if validateEnv fatals here, the test fails — which is correct
			validateEnv(t, tt.env)
		})
	}
}

// containsProductionHost mirrors validateEnv's detection logic to test
// the rejection cases. We can't call validateEnv with a fake *testing.T
// because it takes a concrete *testing.T and calls Fatalf (which does
// runtime.Goexit). Instead we verify the detection logic directly.
func containsProductionHost(env []string) bool {
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		lower := strings.ToLower(parts[1])
		for _, pattern := range productionHostPatterns {
			if !strings.Contains(lower, pattern) {
				continue
			}
			exempted := false
			for _, allowed := range allowedTestHostPatterns {
				if strings.Contains(lower, allowed) {
					exempted = true
					break
				}
			}
			if !exempted {
				return true
			}
		}
	}
	return false
}

func TestValidateEnv_RejectsProductionHosts(t *testing.T) {
	tests := []struct {
		name string
		env  []string
	}{
		{
			name: "bare sageox.ai",
			env:  []string{"SAGEOX_ENDPOINT=https://sageox.ai"},
		},
		{
			name: "api.sageox.ai",
			env:  []string{"SAGEOX_ENDPOINT=https://api.sageox.ai"},
		},
		{
			name: "www.sageox.ai",
			env:  []string{"URL=https://www.sageox.ai/path"},
		},
		{
			name: "uppercase SAGEOX.AI",
			env:  []string{"ENDPOINT=https://SAGEOX.AI/api"},
		},
		{
			name: "production host in path segment",
			env:  []string{"WEBHOOK=https://hooks.example.com/sageox.ai/callback"},
		},
		{
			name: "multiple vars one bad",
			env:  []string{"GOOD=http://localhost:1234", "BAD=https://sageox.ai"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.True(t, containsProductionHost(tt.env),
				"expected production host detection for env: %v", tt.env)
		})
	}
}

// --- responseValidator (mock server body scanning) ---

func TestResponseValidator(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantError  bool
		wantSubstr string // substring expected in error message
	}{
		{
			name:      "clean localhost response",
			body:      `{"clone_url": "http://localhost:4567/repo.git"}`,
			wantError: false,
		},
		{
			name:      "no URLs at all",
			body:      `{"status": "ok", "count": 42}`,
			wantError: false,
		},
		{
			name:       "bare sageox.ai in response body",
			body:       `{"url": "https://sageox.ai/repo"}`,
			wantError:  true,
			wantSubstr: "sageox.ai",
		},
		{
			name:       "test.sageox.ai is ALSO rejected in mock responses",
			body:       `{"url": "https://test.sageox.ai/repo.git"}`,
			wantError:  true,
			wantSubstr: "sageox.ai",
		},
		{
			name:       "case insensitive body scanning",
			body:       `{"url": "https://SAGEOX.AI/api"}`,
			wantError:  true,
			wantSubstr: "sageox.ai",
		},
		{
			name:      "empty body",
			body:      "",
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tt.body))
			})

			ct := &captureT{}
			wrapped := &responseValidator{t: ct, handler: handler}
			server := httptest.NewServer(wrapped)
			defer server.Close()

			resp, err := http.Get(server.URL + "/test")
			require.NoError(t, err)
			resp.Body.Close()

			if tt.wantError {
				assert.True(t, ct.errored, "expected error for body: %s", tt.body)
				assert.Contains(t, ct.msg, tt.wantSubstr)
			} else {
				assert.False(t, ct.errored, "unexpected error for body: %s", tt.body)
			}
		})
	}
}

func TestResponseValidator_IncludesMethodAndPath(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"git": "https://sageox.ai/repo.git"}`))
	})

	ct := &captureT{}
	wrapped := &responseValidator{t: ct, handler: handler}
	server := httptest.NewServer(wrapped)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/v1/repos")
	require.NoError(t, err)
	resp.Body.Close()

	require.True(t, ct.errored)
	assert.Contains(t, ct.msg, "GET")
	assert.Contains(t, ct.msg, "/api/v1/repos")
}

func TestResponseValidator_PassesThroughResponseToClient(t *testing.T) {
	// verify the tee doesn't swallow the response body
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id": 1, "status": "created"}`))
	})

	server := SafeMockServer(t, handler)

	resp, err := http.Get(server.URL + "/create")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	buf := make([]byte, 1024)
	n, _ := resp.Body.Read(buf)
	assert.Contains(t, string(buf[:n]), `"status": "created"`)
}

// --- truncate ---

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		max    int
		expect string
	}{
		{
			name:   "shorter than max",
			input:  "hello",
			max:    10,
			expect: "hello",
		},
		{
			name:   "exactly max",
			input:  "hello",
			max:    5,
			expect: "hello",
		},
		{
			name:   "longer than max",
			input:  "hello world",
			max:    5,
			expect: "hello...",
		},
		{
			name:   "zero max",
			input:  "any",
			max:    0,
			expect: "...",
		},
		{
			name:   "empty string",
			input:  "",
			max:    5,
			expect: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, truncate(tt.input, tt.max))
		})
	}
}

// --- SafeMockServer lifecycle ---

func TestSafeMockServer_CleanupClosesServer(t *testing.T) {
	var serverURL string

	// run in a sub-test so cleanup fires when sub-test ends
	t.Run("inner", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		})
		server := SafeMockServer(t, handler)
		serverURL = server.URL
	})

	// after sub-test cleanup, server should be closed
	_, err := http.Get(serverURL + "/probe")
	assert.Error(t, err, "server should be closed after test cleanup")
}

// --- OxCmdContext env isolation ---

func TestOxCmdContext_SetsWorkingDir(t *testing.T) {
	dir := t.TempDir()
	cmd := OxCmd(t, "/bin/echo", dir, []string{}, "hello")
	assert.Equal(t, dir, cmd.Dir)
}

func TestOxCmdContext_EnvIsolation(t *testing.T) {
	// set vars that should NOT leak into the command
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")
	t.Setenv("SAGEOX_ENDPOINT", "http://localhost:9999")

	cmd := OxCmd(t, "/bin/echo", t.TempDir(), []string{
		"SAGEOX_ENDPOINT=http://localhost:9999",
	}, "test")

	envMap := envToMap(cmd.Env)

	// should have our sentinel
	assert.Equal(t, "1", envMap["OX_NO_DAEMON"])

	// should NOT have leaked secret
	_, hasSecret := envMap["ANTHROPIC_API_KEY"]
	assert.False(t, hasSecret, "secrets must not leak into subprocess env")

	// should have explicitly passed test var
	assert.Equal(t, "http://localhost:9999", envMap["SAGEOX_ENDPOINT"])
}

// --- helpers ---

// captureT captures Errorf calls without stopping execution.
type captureT struct {
	testing.T
	errored bool
	msg     string
}

func (c *captureT) Errorf(format string, args ...any) {
	c.errored = true
	c.msg = fmt.Sprintf(format, args...)
}

func (c *captureT) Helper() {}

// envToMap converts a []string{"K=V"} slice to map[string]string.
// If a key appears multiple times, last value wins (matching real env behavior).
func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	return m
}
