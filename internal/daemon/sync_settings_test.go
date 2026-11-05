package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/flags"
	"github.com/stretchr/testify/require"
)

// A heartbeat from a CLI using production must not authenticate a daemon's
// repository-selected HTTPS host, including when production auth is on disk.
func TestSettingsFetcher_HeartbeatOriginBinding(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv(endpoint.EnvVar, "")
	require.NoError(t, auth.SaveTokenForEndpoint(endpoint.Production, &auth.StoredToken{
		AccessToken: "production-token",
		ExpiresAt:   time.Now().Add(time.Hour),
	}))

	var calls atomic.Int32
	var authorization atomic.Value
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		authorization.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	// Trust the test certificate so an accidental request cannot be hidden by
	// a TLS failure, and the matching-origin case exercises a real HTTPS request.
	originalTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	projectRoot := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(projectRoot, &config.ProjectConfig{Endpoint: srv.URL}))
	daemonEndpoint := endpoint.GetForProject(projectRoot)
	require.Equal(t, srv.URL, daemonEndpoint)

	// The CLI's explicit production override can differ from an already-running
	// daemon's destination. Resolution consistency alone cannot secure that case.
	t.Setenv(endpoint.EnvVar, endpoint.Production)
	cliEndpoint := endpoint.GetForProject(projectRoot)
	require.Equal(t, endpoint.Production, cliEndpoint)
	registry := NewWorkspaceRegistry(projectRoot, "test-repo")
	require.NoError(t, registry.LoadFromConfig())
	require.Equal(t, cliEndpoint, registry.GetEndpoint())

	for _, tt := range []struct {
		name   string
		source string
		want   int32
	}{
		{"production token for repository host", cliEndpoint, 0},
		{"legacy heartbeat without provenance", "", 0},
		{"matching trusted origin", daemonEndpoint, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls.Store(0)
			authorization.Store("")
			handler := NewHeartbeatHandler(testLogger())
			handler.Handle("", mustMarshal(HeartbeatPayload{
				Credentials: &HeartbeatCreds{
					Token:        "git-pat",
					ExpiresAt:    time.Now().Add(time.Hour),
					AuthToken:    "heartbeat-token",
					AuthEndpoint: tt.source,
				},
			}))
			f := NewSettingsFetcher(testLogger(), daemonEndpoint)
			f.SetAuthTokenGetter(handler.GetAuthToken)
			f.Fetch(context.Background())

			require.NoError(t, f.lastErr)
			require.Equal(t, tt.want, calls.Load())
			if tt.want == 0 {
				require.Equal(t, "", authorization.Load())
				require.Nil(t, f.CachedSettings())
			} else {
				require.Equal(t, "Bearer heartbeat-token", authorization.Load())
				require.NotNil(t, f.CachedSettings())
			}
		})
	}
}

func TestSettingsFetcher_Fetch_CachesResult(t *testing.T) {
	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.URL.Path != "/api/v1/cli/settings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := flags.CLISettingsResponse{
			Features: flags.CLIFeatures{
				CodeDB: boolPtr(true),
				Attest: boolPtr(true),
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	f := NewSettingsFetcher(logger, srv.URL)
	f.SetAuthTokenGetter(func(destination string) string {
		require.Equal(t, srv.URL, destination)
		return "test-token"
	})

	f.Fetch(context.Background())

	if called != 1 {
		t.Fatalf("expected 1 API call, got %d", called)
	}

	cached := f.CachedSettings()
	if cached == nil {
		t.Fatal("expected cached settings, got nil")
	}
	if cached.Features.CodeDB == nil || !*cached.Features.CodeDB {
		t.Error("expected CodeDB=true in cached settings")
	}
	if cached.Features.Attest == nil || !*cached.Features.Attest {
		t.Error("expected Attest=true in cached settings")
	}
}

func TestSettingsFetcher_Fetch_DeduplicatesWithinMaxAge(t *testing.T) {
	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		json.NewEncoder(w).Encode(flags.CLISettingsResponse{})
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	f := NewSettingsFetcher(logger, srv.URL)
	f.SetAuthTokenGetter(func(destination string) string {
		require.Equal(t, srv.URL, destination)
		return "test-token"
	})

	// first fetch succeeds
	f.Fetch(context.Background())
	// second fetch within CLISettingsMaxAge is deduped
	f.Fetch(context.Background())

	if called != 1 {
		t.Fatalf("expected 1 API call (dedup), got %d", called)
	}
}

func TestSettingsFetcher_Fetch_SkipsWithoutAuthToken(t *testing.T) {
	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		json.NewEncoder(w).Encode(flags.CLISettingsResponse{})
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	f := NewSettingsFetcher(logger, srv.URL)
	// no auth token getter set

	f.Fetch(context.Background())

	if called != 0 {
		t.Fatalf("expected 0 API calls (no auth), got %d", called)
	}
	if f.CachedSettings() != nil {
		t.Error("expected nil cached settings without auth")
	}
}

func TestSettingsFetcher_Fetch_WritesToDiskCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := flags.CLISettingsResponse{
			Features: flags.CLIFeatures{
				Whisper: boolPtr(false),
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	f := NewSettingsFetcher(logger, srv.URL)
	f.SetAuthTokenGetter(func(destination string) string {
		require.Equal(t, srv.URL, destination)
		return "test-token"
	})

	f.Fetch(context.Background())

	// verify disk cache was written (LoadCachedSettings should find it)
	cached, err := flags.LoadCachedSettings(srv.URL)
	if err != nil {
		t.Fatalf("LoadCachedSettings: %v", err)
	}
	if cached == nil {
		t.Fatal("expected disk-cached settings, got nil")
	}
	if cached.Features.Whisper == nil || *cached.Features.Whisper {
		t.Error("expected Whisper=false in disk cache")
	}
}

func TestSettingsFetcher_CachedSettings_NilBeforeFetch(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	f := NewSettingsFetcher(logger, "https://example.com")

	if f.CachedSettings() != nil {
		t.Error("expected nil cached settings before any fetch")
	}
}

func TestSettingsFetcher_Fetch_HandlesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	f := NewSettingsFetcher(logger, srv.URL)
	f.SetAuthTokenGetter(func(destination string) string {
		require.Equal(t, srv.URL, destination)
		return "test-token"
	})

	f.Fetch(context.Background())

	if f.CachedSettings() != nil {
		t.Error("expected nil cached settings after server error")
	}
}

func TestSettingsFetcher_Fetch_404UsesDefaultsWithoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	f := NewSettingsFetcher(logger, srv.URL)
	f.SetAuthTokenGetter(func(destination string) string {
		require.Equal(t, srv.URL, destination)
		return "test-token"
	})
	f.Fetch(context.Background())

	require.Nil(t, f.CachedSettings())
	require.NoError(t, f.lastErr)
}

func TestSettingsFetcher_Fetch_FetchedAtIsSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(flags.CLISettingsResponse{})
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	f := NewSettingsFetcher(logger, srv.URL)
	f.SetAuthTokenGetter(func(destination string) string {
		require.Equal(t, srv.URL, destination)
		return "test-token"
	})

	before := time.Now()
	f.Fetch(context.Background())
	after := time.Now()

	cached := f.CachedSettings()
	if cached == nil {
		t.Fatal("expected cached settings")
	}
	if cached.FetchedAt.Before(before) || cached.FetchedAt.After(after) {
		t.Errorf("FetchedAt %v not between %v and %v", cached.FetchedAt, before, after)
	}
}

func boolPtr(b bool) *bool { return &b }
