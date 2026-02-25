package testguard

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMinimalEnv_InjectsNoDaemon(t *testing.T) {
	env := MinimalEnv(nil)

	found := false
	for _, kv := range env {
		if kv == "OX_NO_DAEMON=1" {
			found = true
			break
		}
	}
	assert.True(t, found, "MinimalEnv should always inject OX_NO_DAEMON=1")
}

func TestMinimalEnv_ExcludesSageoxEndpoint(t *testing.T) {
	// even if SAGEOX_ENDPOINT is in the real env, MinimalEnv should NOT inherit it
	t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")

	env := MinimalEnv(nil)

	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		assert.NotEqual(t, "SAGEOX_ENDPOINT", parts[0],
			"MinimalEnv should not inherit SAGEOX_ENDPOINT from real environment")
	}
}

func TestMinimalEnv_AllowsTestOverrides(t *testing.T) {
	env := MinimalEnv([]string{
		"SAGEOX_ENDPOINT=http://localhost:12345",
		"OX_XDG_ENABLE=1",
	})

	found := false
	for _, kv := range env {
		if kv == "SAGEOX_ENDPOINT=http://localhost:12345" {
			found = true
		}
	}
	assert.True(t, found, "test overrides should be included")
}

func TestValidateEnv_AllowsTestInfra(t *testing.T) {
	// test.sageox.ai should be allowed (it's test infrastructure)
	validateEnv(t, []string{
		"SAGEOX_ENDPOINT=https://test.sageox.ai",
	})
	// if we get here without Fatalf, the test passes
}

func TestValidateEnv_RejectsProductionHost(t *testing.T) {
	// production sageox.ai (without test. prefix) should be detected
	env := []string{
		"SAGEOX_ENDPOINT=https://sageox.ai",
	}

	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		val := parts[1]
		lower := strings.ToLower(val)
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
				return // test passes: production URL detected and not exempted
			}
		}
	}
	t.Error("should have detected production hostname in env value")
}

func TestSafeMockServer_PassesCleanResponses(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"url": "https://localhost:1/repo.git"}`))
	})

	server := SafeMockServer(t, handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/test")
	assert.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()
}

func TestResponseValidator_DetectsProductionURL(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"url": "https://git.test.sageox.ai/repo.git"}`))
	})

	// use httptest directly to avoid SafeMockServer's cleanup interfering
	fakeT := &captureT{}
	wrapped := &responseValidator{t: fakeT, handler: handler}
	server := httptest.NewServer(wrapped)
	defer server.Close()

	resp, err := http.Get(server.URL + "/test")
	assert.NoError(t, err)
	resp.Body.Close()

	assert.True(t, fakeT.errored, "should have reported error for production URL in response")
	assert.Contains(t, fakeT.msg, "sageox.ai")
}

// captureT captures Errorf calls without stopping execution
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
