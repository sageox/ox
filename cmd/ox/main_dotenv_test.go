package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/telemetry"
	"github.com/sageox/ox/internal/testguard"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

const dotenvUntrustedEndpoint = "https://dotenv-controlled.invalid"

// Exercise main, not just Cobra: dotenv used to be applied before command setup,
// turning checkout values into trusted endpoint and user-config overrides.
func TestStartupIgnoresCheckoutDotenv(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real CLI subprocesses")
	}
	bin, err := os.Executable()
	require.NoError(t, err)
	for _, filename := range []string{".env", ".env.local"} {
		for _, tc := range []struct {
			name            string
			configContents  string
			token           string
			processOverride bool
		}{
			{name: "missing config", token: "opaque-test-token"},
			{name: "empty config", configContents: "\n", token: validPersonalToken},
			{name: "malformed config", configContents: "telemetry_enabled: [", token: validTeamToken},
			{name: "process overrides", token: "opaque-test-token", processOverride: true},
			{name: "no inherited token"},
		} {
			t.Run(filename+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				configPath := filepath.Join(dir, "checkout-config.yaml")
				if tc.configContents != "" {
					require.NoError(t, os.WriteFile(configPath, []byte(tc.configContents), 0o600))
				}
				dotenv := fmt.Sprintf("SAGEOX_ENDPOINT=%s\nSAGEOX_TOKEN=checkout-token\nOX_USER_CONFIG=%q\nDO_NOT_TRACK=0\nSAGEOX_TELEMETRY=true\n", dotenvUntrustedEndpoint, configPath)
				require.NoError(t, os.WriteFile(filepath.Join(dir, filename), []byte(dotenv), 0o600))
				env := []string{
					"OX_TEST_DOTENV_HELPER=1",
					// Test the persisted opt-out without testguard's process opt-out.
					"DO_NOT_TRACK=",
				}
				if tc.token != "" {
					env = append(env, "SAGEOX_TOKEN="+tc.token)
				}
				if tc.processOverride {
					env = append(env,
						"SAGEOX_ENDPOINT=https://operator.test.sageox.ai",
						"OX_USER_CONFIG="+filepath.Join(t.TempDir(), "operator-config.yaml"),
						"DO_NOT_TRACK=1", "SAGEOX_TELEMETRY=false")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := testguard.OxCmdContext(t, ctx, bin, dir, env, "-test.run=^TestStartupDotenvHelper$")
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "output=%s", out)
				require.Contains(t, string(out), "dotenv startup checks passed")
			})
		}
	}
}

type dotenvTestTransport func(*http.Request) (*http.Response, error)

func (f dotenvTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestStartupDotenvHelper(t *testing.T) {
	if os.Getenv("OX_TEST_DOTENV_HELPER") != "1" {
		return
	}
	// TestMain isolates HOME and moves away from cmd.Dir. Restore only the
	// checkout directory; save a real opt-out in the isolated user's config.
	t.Chdir(packageDir)
	userCfg := &config.UserConfig{}
	userCfg.SetTelemetryEnabled(false)
	require.NoError(t, config.SaveUserConfig(userCfg))
	wantEnv := make(map[string]string)
	for _, key := range []string{"SAGEOX_ENDPOINT", "SAGEOX_TOKEN", "OX_USER_CONFIG", "DO_NOT_TRACK", "SAGEOX_TELEMETRY"} {
		wantEnv[key] = os.Getenv(key)
	}
	wantEndpoint := auth.EnvTokenEndpoint()
	wantToken := os.Getenv(auth.EnvVarToken)

	// Capture the actual status introspection request without contacting any
	// network host, including production when the endpoint is unset.
	var bearerRequests atomic.Int32
	var unexpectedBearer atomic.Bool
	http.DefaultTransport = dotenvTestTransport(func(req *http.Request) (*http.Response, error) {
		if bearer := req.Header.Get("Authorization"); bearer != "" {
			bearerRequests.Add(1)
			if req.URL.String() != wantEndpoint+auth.IntrospectEndpoint || bearer != "Bearer "+wantToken {
				unexpectedBearer.Store(true)
			}
		}
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    req,
		}, nil
	})

	preRun := rootCmd.PersistentPreRunE
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		for key, value := range wantEnv {
			require.Equal(t, value, os.Getenv(key), "%s changed during startup", key)
		}
		require.Equal(t, wantEndpoint, endpoint.GetForProject(findGitRoot()))
		require.Equal(t, wantEndpoint, auth.EnvTokenEndpoint())
		_, state := auth.EnvTokenFor(dotenvUntrustedEndpoint)
		require.Equal(t, auth.EnvTokenAbsent, state)
		loaded, err := config.LoadUserConfig()
		require.NoError(t, err)
		require.False(t, loaded.IsTelemetryEnabled())
		// Check before initialization so a regression cannot start an exporter.
		require.False(t, telemetry.Enabled())
		return preRun(cmd, args)
	}
	statusCmd.PostRunE = func(cmd *cobra.Command, args []string) error {
		if wantToken == "" {
			require.Zero(t, bearerRequests.Load())
		} else {
			require.Positive(t, bearerRequests.Load(), "status must exercise introspection")
		}
		require.False(t, unexpectedBearer.Load(), "bearer sent outside its inherited endpoint binding")
		require.False(t, cliCtx.TelemetryClient.IsEnabled())
		fmt.Fprintln(os.Stderr, "dotenv startup checks passed")
		return nil
	}
	os.Args = []string{os.Args[0], "status", "--json"}
	main()
}
