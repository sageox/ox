package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/secretguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const feedbackTestReport = "Sync stalls after reconnect\nThe command never finishes after the network reconnects."
const feedbackTestReceipt = "sup_0123456789abcdefab"

func executeFeedbackTest(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	cmd := newFeedbackCommand()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.Flags().Bool("json", false, "JSON output")
	cmd.SetIn(strings.NewReader(input))
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}

func feedbackTestDependencies(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	originalAuth, originalRules := feedbackTokenForEndpoint, feedbackLoadLatestRules
	t.Cleanup(func() {
		feedbackTokenForEndpoint, feedbackLoadLatestRules = originalAuth, originalRules
	})
	feedbackTokenForEndpoint = func(context.Context, string, int) (*auth.StoredToken, error) {
		return nil, nil
	}
	feedbackLoadLatestRules = func(context.Context) (*secretguard.RuleSet, error) {
		return secretguard.NewRules([]byte("[[rules]]\nid = 'test-current-secret'\nregex = 'CURRENT_SECRET_MARKER'\n"))
	}
}

// A preview must expose the same request body as submission without contacting auth or support.
func TestFeedbackPreviewMatchesSubmittedBytes(t *testing.T) {
	feedbackTestDependencies(t)
	feedbackTokenForEndpoint = func(context.Context, string, int) (*auth.StoredToken, error) {
		t.Fatal("explicit email or dry-run must not consult login")
		return nil, nil
	}
	var submitted []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/support/cases", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.Empty(t, r.Header.Get("Authorization"))
		var err error
		submitted, err = io.ReadAll(r.Body)
		assert.NoError(t, err)
		_, _ = io.WriteString(w, `{"case_id":"`+feedbackTestReceipt+`"}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("DO_NOT_TRACK", "1")
	preview, err := executeFeedbackTest(t, feedbackTestReport, "--file", "-", "--type", "bug", "--email", "person@example.com", "--dry-run")
	require.NoError(t, err)
	require.Nil(t, submitted)
	output, err := executeFeedbackTest(t, feedbackTestReport, "--file", "-", "--type", "bug", "--email", "person@example.com", "--json")
	require.NoError(t, err)
	assert.Equal(t, preview, string(submitted))
	var receipt map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &receipt))
	assert.Equal(t, feedbackTestReceipt, receipt["id"])
	assert.Equal(t, "sent", receipt["status"])
	var payload api.SupportCaseRequest
	require.NoError(t, json.Unmarshal(submitted, &payload))
	assert.Equal(t, feedbackTestReport, payload.Description)
	assert.Equal(t, "ox-cli", payload.Context.Surface)
	assert.Equal(t, "feedback", payload.Context.Intent)
	assert.Equal(t, "bug", payload.Context.Category)
}

// An offline preview must work even when the catalog and login services are unavailable.
func TestFeedbackDryRunIsOffline(t *testing.T) {
	feedbackTestDependencies(t)
	feedbackTokenForEndpoint = func(context.Context, string, int) (*auth.StoredToken, error) {
		t.Fatal("dry-run tried authentication")
		return nil, nil
	}
	feedbackLoadLatestRules = func(context.Context) (*secretguard.RuleSet, error) {
		t.Fatal("dry-run tried a rules download")
		return nil, nil
	}
	output, err := executeFeedbackTest(t, feedbackTestReport, "--file", "-", "--dry-run")
	require.NoError(t, err)
	assert.True(t, json.Valid([]byte(output)))
	assert.NotContains(t, output, "email")
}

// Feedback must never link its request trace to authenticated command telemetry.
func TestFeedbackSkipsRootLifecycle(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "1")
	previousContext, previousConfig := cliCtx, cfg
	previousPreRun := preRunReached
	cliCtx = nil
	t.Cleanup(func() {
		cliCtx, cfg = previousContext, previousConfig
		preRunReached = previousPreRun
	})
	cmd, _, err := rootCmd.Find([]string{"feedback"})
	require.NoError(t, err)
	cmd.InheritedFlags()
	flag := cmd.Flags().Lookup("dry-run")
	previousValue, previousChanged := flag.Value.String(), flag.Changed
	t.Cleanup(func() { _ = flag.Value.Set(previousValue); flag.Changed = previousChanged })
	configFlag := cmd.Flags().Lookup("config")
	previousConfigValue, previousConfigChanged := configFlag.Value.String(), configFlag.Changed
	t.Cleanup(func() { _ = configFlag.Value.Set(previousConfigValue); configFlag.Changed = previousConfigChanged })
	configFile := filepath.Join(t.TempDir(), "selected.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte("display_name: feedback\n"), 0o600))
	t.Setenv(config.EnvUserConfig, "")
	require.NoError(t, cmd.Flags().Set("config", configFile))
	for _, dryRun := range []string{"false", "true"} {
		require.NoError(t, cmd.Flags().Set("dry-run", dryRun))
		require.NoError(t, rootCmd.PersistentPreRunE(cmd, nil))
		assert.True(t, preRunReached)
		assert.Nil(t, cliCtx, "feedback must never initialize the network/telemetry context")
		require.NoError(t, rootCmd.PersistentPostRunE(cmd, nil))
		assert.Nil(t, cliCtx)
		assert.Equal(t, configFile, os.Getenv(config.EnvUserConfig))
	}
	assert.False(t, isFeedbackCommand(rootCmd))
}

func TestFeedbackJSONEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, environment string
		args              []string
		wantJSON          bool
	}{
		{name: "environment", environment: "1", wantJSON: true},
		{name: "explicit false", environment: "1", args: []string{"--json=false"}},
		{name: "explicit true", args: []string{"--json"}, wantJSON: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			feedbackTestDependencies(t)
			t.Setenv("OX_JSON", tc.environment)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"case_id":"`+feedbackTestReceipt+`"}`)
			}))
			defer server.Close()
			t.Setenv("SAGEOX_ENDPOINT", server.URL)
			output, err := executeFeedbackTest(t, feedbackTestReport, append([]string{"--file", "-", "--anonymous"}, tc.args...)...)
			require.NoError(t, err)
			assert.Equal(t, tc.wantJSON, json.Valid([]byte(output)))
			assert.Contains(t, output, feedbackTestReceipt)
		})
	}
}

func TestFeedbackRejectedInvocationSkipsTelemetry(t *testing.T) {
	restoreRootCmdState(t)
	t.Setenv("DO_NOT_TRACK", "1")
	cmd, _, err := rootCmd.Find([]string{"feedback"})
	require.NoError(t, err)
	flag := cmd.Flags().Lookup("anonymous")
	previousValue, previousChanged := flag.Value.String(), flag.Changed
	t.Cleanup(func() { _ = flag.Value.Set(previousValue); flag.Changed = previousChanged })
	cliCtx = nil
	code := executeWithFrictionRecovery(context.Background(), []string{"feedback", "--anonymous", "--unknown-feedback-flag"}, 0)
	assert.NotZero(t, code)
	assert.Nil(t, cliCtx, "rejected feedback must not create a usage telemetry context")
}

// Invalid input and credentials in any field must fail before auth or any HTTP request.
func TestFeedbackUnsafeInputNeverLeavesMachine(t *testing.T) {
	feedbackTestDependencies(t)
	feedbackTokenForEndpoint = func(context.Context, string, int) (*auth.StoredToken, error) {
		t.Fatal("invalid input reached authentication")
		return nil, nil
	}
	feedbackLoadLatestRules = func(context.Context) (*secretguard.RuleSet, error) {
		t.Fatal("invalid input reached catalog download")
		return nil, nil
	}
	for _, tc := range []struct {
		name, input string
		args        []string
	}{
		{"token", "Login fails with a private token\noxp_" + strings.Repeat("a", 40), nil},
		{"private key", "Login fails with private key\n-----BEGIN RSA PRIVATE KEY-----\n" + strings.Repeat("synthetic-key-material", 5) + "\n-----END RSA PRIVATE KEY-----", nil},
		{"control", "Terminal escape example\n\x1b[31m", nil},
		{"short title", "short", nil},
		{"oversize", "Oversize report example\n" + strings.Repeat("a", 4096), nil},
		{"invalid type", feedbackTestReport, []string{"--type", "internal"}},
		{"invalid email", feedbackTestReport, []string{"--email", "A Person <person@example.com>"}},
		{"empty email", feedbackTestReport, []string{"--email="}},
		{"anonymous token", "Login fails with a private token\noxp_" + strings.Repeat("a", 40), []string{"--anonymous"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--file", "-"}, tc.args...)
			output, err := executeFeedbackTest(t, tc.input, args...)
			require.Error(t, err)
			assert.Empty(t, output)
			assert.NotContains(t, err.Error(), tc.input)
		})
	}
}

// A fresh catalog can find a secret the bundled catalog does not yet know.
func TestFeedbackFreshRulesFailurePreventsSubmission(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		catalogErr  bool
	}{
		{"new secret", "Command failed unexpectedly\nCURRENT_SECRET_MARKER", false},
		{"unavailable catalog", feedbackTestReport, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			feedbackTestDependencies(t)
			if tc.catalogErr {
				feedbackLoadLatestRules = func(context.Context) (*secretguard.RuleSet, error) {
					return nil, errors.New("private upstream response")
				}
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
			defer server.Close()
			t.Setenv("SAGEOX_ENDPOINT", server.URL)
			output, err := executeFeedbackTest(t, tc.input, "--file", "-", "--email", "person@example.com")
			require.Error(t, err)
			assert.Empty(t, output)
			assert.Zero(t, calls)
			assert.NotContains(t, err.Error(), "private upstream response")
			assert.NotContains(t, err.Error(), "CURRENT_SECRET_MARKER")
		})
	}
}

// JSON escaping must not hide newly recognized multiline credentials.
func TestFeedbackFreshRulesScanEscapedReport(t *testing.T) {
	feedbackTestDependencies(t)
	feedbackLoadLatestRules = func(context.Context) (*secretguard.RuleSet, error) {
		return secretguard.NewRules([]byte("[[rules]]\nid = 'test-multiline-secret'\nregex = '''FUTURE_CREDENTIAL_START\nsynthetic-private-value\nFUTURE_CREDENTIAL_END'''\n"))
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"case_id":"`+feedbackTestReceipt+`"}`)
	}))
	defer server.Close()
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	input := "Login fails unexpectedly\nFUTURE_CREDENTIAL_START\nsynthetic-private-value\nFUTURE_CREDENTIAL_END"
	output, err := executeFeedbackTest(t, input, "--file", "-", "--email", "person@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "possible secret")
	assert.Empty(t, output)
	assert.Zero(t, calls)
	assert.NotContains(t, err.Error(), "synthetic-private-value")
}

// A broken stored identity must not silently turn into an anonymous submission.
func TestFeedbackBrokenLoginRequiresExplicitChoice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token *auth.StoredToken
		err   error
	}{
		{name: "empty token", token: &auth.StoredToken{}},
		{name: "broken login", err: errors.New("private refresh error")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			feedbackTestDependencies(t)
			feedbackTokenForEndpoint = func(context.Context, string, int) (*auth.StoredToken, error) { return tc.token, tc.err }
			feedbackLoadLatestRules = func(context.Context) (*secretguard.RuleSet, error) {
				t.Fatal("missing identity reached network checks")
				return nil, nil
			}
			output, err := executeFeedbackTest(t, feedbackTestReport, "--file", "-", "--json")
			require.Error(t, err)
			assert.Empty(t, output)
			assert.Contains(t, err.Error(), "email")
			assert.NotContains(t, err.Error(), "private refresh error")
		})
	}
}

func TestFeedbackAnonymousSubmission(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "no login", true: "explicit anonymous"}[explicit], func(t *testing.T) {
			feedbackTestDependencies(t)
			authCalls := 0
			feedbackTokenForEndpoint = func(context.Context, string, int) (*auth.StoredToken, error) {
				authCalls++
				if explicit {
					t.Fatal("--anonymous consulted stored credentials")
				}
				return nil, nil
			}
			var submitted []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Empty(t, r.Header.Get("Authorization"))
				var err error
				submitted, err = io.ReadAll(r.Body)
				assert.NoError(t, err)
				_, _ = io.WriteString(w, `{"case_id":"`+feedbackTestReceipt+`"}`)
			}))
			defer server.Close()
			t.Setenv("SAGEOX_ENDPOINT", server.URL)
			args := []string{"--file", "-"}
			if explicit {
				args = append(args, "--anonymous")
			}
			preview, err := executeFeedbackTest(t, feedbackTestReport, append(args, "--dry-run")...)
			require.NoError(t, err)
			assert.Zero(t, authCalls)
			require.Nil(t, submitted)
			output, err := executeFeedbackTest(t, feedbackTestReport, append(args, "--json")...)
			require.NoError(t, err)
			assert.Equal(t, preview, string(submitted))
			var payload map[string]any
			require.NoError(t, json.Unmarshal(submitted, &payload))
			assert.NotContains(t, payload, "email")
			assert.NotContains(t, payload, "anonymous")
			var receipt map[string]any
			require.NoError(t, json.Unmarshal([]byte(output), &receipt))
			assert.Equal(t, feedbackTestReceipt, receipt["id"])
			assert.Equal(t, "sent", receipt["status"])
			assert.Contains(t, receipt["guidance"], "support cannot reply")
			if explicit {
				assert.Zero(t, authCalls)
			} else {
				assert.Equal(t, 1, authCalls)
			}
		})
	}
}

func TestFeedbackAnonymousConflictsWithEmail(t *testing.T) {
	for _, email := range []string{"person@example.com", ""} {
		t.Run(email, func(t *testing.T) {
			feedbackTestDependencies(t)
			feedbackTokenForEndpoint = func(context.Context, string, int) (*auth.StoredToken, error) {
				t.Fatal("conflicting flags reached authentication")
				return nil, nil
			}
			feedbackLoadLatestRules = func(context.Context) (*secretguard.RuleSet, error) {
				t.Fatal("conflicting flags reached network checks")
				return nil, nil
			}
			output, err := executeFeedbackTest(t, feedbackTestReport, "--file", "-", "--anonymous", "--email="+email)
			require.Error(t, err)
			assert.Empty(t, output)
		})
	}
}

// Normal login must use the endpoint-bound access credential, never a refresh token.
func TestFeedbackUsesPersonalEndpointToken(t *testing.T) {
	feedbackTestDependencies(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer personal-access", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"case_id":"`+feedbackTestReceipt+`"}`)
	}))
	defer server.Close()
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	feedbackTokenForEndpoint = func(_ context.Context, ep string, buffer int) (*auth.StoredToken, error) {
		assert.Equal(t, server.URL, ep)
		assert.Equal(t, 300, buffer)
		return &auth.StoredToken{AccessToken: "personal-access", RefreshToken: "never-send-refresh"}, nil
	}
	output, err := executeFeedbackTest(t, feedbackTestReport, "--file", "-")
	require.NoError(t, err)
	assert.Contains(t, output, feedbackTestReceipt)
}

// Failed or unconfirmed delivery must leave the source intact without printing a success receipt.
func TestFeedbackFailurePreservesReport(t *testing.T) {
	feedbackTestDependencies(t)
	file := filepath.Join(t.TempDir(), "report.txt")
	require.NoError(t, os.WriteFile(file, []byte(feedbackTestReport), 0o600))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "private response must stay private")
	}))
	defer server.Close()
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	output, err := executeFeedbackTest(t, "", "--file", file, "--email", "person@example.com", "--json")
	require.Error(t, err)
	assert.Empty(t, output)
	assert.Contains(t, err.Error(), "not confirmed")
	assert.NotContains(t, err.Error(), "private response")
	content, readErr := os.ReadFile(file)
	require.NoError(t, readErr)
	assert.Equal(t, feedbackTestReport, string(content))
}
