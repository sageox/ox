package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsPreTrustedEndpoint verifies the sageox.ai-family allow rules.
// These hosts must NEVER trigger the first-use prompt, otherwise normal
// `ox login` becomes unusably noisy.
func TestIsPreTrustedEndpoint(t *testing.T) {
	cases := map[string]bool{
		"https://sageox.ai":         true,
		"https://test.sageox.ai":    true,
		"https://api.test.sageox.ai": true,
		"https://attacker.example":  false,
		"http://localhost:8080":     false, // not pre-trusted; user must approve
		"":                          false,
	}
	for ep, want := range cases {
		t.Run(ep, func(t *testing.T) {
			assert.Equal(t, want, isPreTrustedEndpoint(ep))
		})
	}
}

// TestConfirmNewEndpointTrust_AcceptsSageoxSilent verifies sageox.ai is
// pre-trusted and produces no prompt output.
func TestConfirmNewEndpointTrust_AcceptsSageoxSilent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetIn(strings.NewReader(""))

	err := confirmNewEndpointTrust(cmd, "https://sageox.ai")
	require.NoError(t, err)
	assert.Empty(t, out.String(), "pre-trusted endpoint should produce no output")
}

// TestConfirmNewEndpointTrust_PromptsForNewHost verifies the prompt
// fires for an unknown host and aborts on "no".
func TestConfirmNewEndpointTrust_PromptsForNewHost(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetIn(strings.NewReader("no\n"))

	err := confirmNewEndpointTrust(cmd, "https://attacker.example")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declined")
	assert.Contains(t, out.String(), "First-use confirmation")
}

// TestConfirmNewEndpointTrust_PersistsOnYes verifies that answering yes
// records the endpoint so future logins are silent.
func TestConfirmNewEndpointTrust_PersistsOnYes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetIn(strings.NewReader("yes\n"))

	err := confirmNewEndpointTrust(cmd, "https://selfhosted.example.com")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "trusted for future logins")

	// second call must be silent
	out2 := &bytes.Buffer{}
	cmd2 := &cobra.Command{}
	cmd2.SetOut(out2)
	cmd2.SetIn(strings.NewReader(""))
	err = confirmNewEndpointTrust(cmd2, "https://selfhosted.example.com")
	require.NoError(t, err)
	assert.Empty(t, out2.String(), "second login to trusted endpoint must be silent")
}

// TestConfirmNewEndpointTrust_OXTrustEndpointBypass verifies the
// automation escape hatch logs a warning AND persists.
func TestConfirmNewEndpointTrust_OXTrustEndpointBypass(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("OX_TRUST_ENDPOINT", "1")

	stderr := &bytes.Buffer{}
	stdout := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))

	err := confirmNewEndpointTrust(cmd, "https://ci-trust.example")
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "WARNING")
	// subsequent calls (post-persist) should not even need the bypass
	t.Setenv("OX_TRUST_ENDPOINT", "")
	err = confirmNewEndpointTrust(cmd, "https://ci-trust.example")
	require.NoError(t, err)
}

// TestValidateEndpoint_RejectsPlaintextNonLoopback (ox-v5de) verifies
// the scheme check fires.
func TestValidateEndpoint_RejectsPlaintextNonLoopback(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "")
	err := endpoint.ValidateEndpoint("http://attacker.example")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plaintext")
}

func TestValidateEndpoint_AllowsLoopback(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "")
	for _, ep := range []string{
		"http://localhost:8080",
		"http://127.0.0.1:8080",
	} {
		assert.NoError(t, endpoint.ValidateEndpoint(ep))
	}
}

func TestValidateEndpoint_AllowsHTTPS(t *testing.T) {
	assert.NoError(t, endpoint.ValidateEndpoint("https://sageox.ai"))
	assert.NoError(t, endpoint.ValidateEndpoint("https://selfhosted.example.com"))
}

func TestValidateEndpoint_AllowsPlaintextWithFlag(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")
	assert.NoError(t, endpoint.ValidateEndpoint("http://staging.example.com"))
}

// TestStripSubdomainPrefix_RestrictedToSageoxFamily verifies that the
// prefix-stripping logic doesn't silently rewrite third-party hosts
// like git.example.com → example.com.
func TestStripSubdomainPrefix_RestrictedToSageoxFamily(t *testing.T) {
	cases := map[string]string{
		"git.sageox.ai":      "sageox.ai",
		"git.test.sageox.ai": "test.sageox.ai",
		"api.sageox.ai":      "sageox.ai",
		"www.sageox.ai":      "sageox.ai",
		"app.sageox.ai":      "sageox.ai",
		"git.example.com":    "git.example.com", // NOT rewritten — third-party host
		"api.example.com":    "api.example.com",
		"www.example.com":    "www.example.com",
		"git.localhost":      "localhost",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			ep := "https://" + in
			got := endpoint.NormalizeEndpoint(ep)
			expected := "https://" + want
			assert.Equal(t, expected, got)
		})
	}
}
