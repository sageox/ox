package secretguardtest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	InstallOfflineCatalog()
	os.Exit(m.Run())
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// noNetwork fails the test if the offline transport hands a request to its base.
func noNetwork(t *testing.T) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("request reached the base transport: %s", r.URL)
		return nil, errors.New("network refused")
	})
}

func withDefaultTransport(t *testing.T, rt http.RoundTripper) {
	previous := http.DefaultTransport
	http.DefaultTransport = rt
	t.Cleanup(func() { http.DefaultTransport = previous })
}

// Failure prevented: a publication test waits on, or fails with, the live
// upstream catalog. The catalog request never reaches the base transport.
func TestTransport_ServesCheckedInCatalogOffline(t *testing.T) {
	want, err := os.ReadFile(catalogFile())
	require.NoError(t, err)
	tr := transport{base: noNetwork(t), catalogFile: catalogFile()}
	for _, url := range []string{
		"https://" + catalogHost + catalogPath,
		"https://" + catalogHost + catalogPath + "?cache=bust",
	} {
		t.Run(url, func(t *testing.T) {
			before := served.Load()
			req, err := http.NewRequest(http.MethodGet, url, nil)
			require.NoError(t, err)
			resp, err := tr.RoundTrip(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			got, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, int64(len(want)), resp.ContentLength)
			assert.Equal(t, want, got)
			assert.Equal(t, before+1, served.Load())
		})
	}
}

// Failure prevented: a missing catalog file silently falls back to the network.
func TestTransport_UnreadableCatalogNeverReachesNetwork(t *testing.T) {
	tr := transport{base: noNetwork(t), catalogFile: filepath.Join(t.TempDir(), "missing.toml")}
	req, err := http.NewRequest(http.MethodGet, "https://"+catalogHost+catalogPath, nil)
	require.NoError(t, err)
	resp, err := tr.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.ErrorContains(t, err, "cannot read checked-in catalog")
}

// Failure prevented: the wrapper swallows a package's own test servers.
func TestTransport_PassesOtherRequestsThrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "from "+r.URL.Path)
	}))
	t.Cleanup(server.Close)
	var forwarded []string
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		forwarded = append(forwarded, r.URL.String())
		if r.URL.Host == catalogHost {
			return nil, errors.New("network refused")
		}
		return server.Client().Transport.RoundTrip(r)
	})
	tr := transport{base: base, catalogFile: catalogFile()}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/local", nil)
	require.NoError(t, err)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, "from /local", string(body))

	other := "https://" + catalogHost + "/gitleaks/gitleaks/master/README.md"
	req, err = http.NewRequest(http.MethodGet, other, nil)
	require.NoError(t, err)
	resp, err = tr.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	assert.Equal(t, []string{server.URL + "/local", other}, forwarded)
}

func TestRequireOfflineCatalog_PassesWhenInstalled(t *testing.T) {
	RequireOfflineCatalog(t)
}

// fatalRecorder captures the first Fatal and stops the calling goroutine,
// as testing.T does, so the assertion's failure paths can be observed.
type fatalRecorder struct {
	testing.TB
	msg string
}

func (r *fatalRecorder) Helper() {}
func (r *fatalRecorder) Fatal(args ...any) {
	r.msg = fmt.Sprint(args...)
	runtime.Goexit()
}
func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func requireOfflineCatalogFailure(t *testing.T) string {
	t.Helper()
	rec := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		RequireOfflineCatalog(rec)
	}()
	<-done
	return rec.msg
}

// Failure prevented: the per-package assertion passes in a package whose
// TestMain does not install the offline catalog.
func TestRequireOfflineCatalog_FailsWithoutOfflineTransport(t *testing.T) {
	bundled, err := os.ReadFile(catalogFile())
	require.NoError(t, err)
	other := filepath.Join(t.TempDir(), "other.toml")
	require.NoError(t, os.WriteFile(other, []byte("[[rules]]\nid='synthetic'\nregex='synthetic_[a-z]+'\n"), 0o600))

	tests := []struct {
		name      string
		transport http.RoundTripper
		want      string
	}{
		{
			name: "network unavailable",
			transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("network refused")
			}),
			want: "publication catalog fetch failed",
		},
		{
			name: "network answered",
			transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(bundled)), Request: r}, nil
			}),
			want: "publication catalog came from the network",
		},
		{
			name:      "different catalog",
			transport: transport{base: noNetwork(t), catalogFile: other},
			want:      "differs from internal/secretguard/gitleaks.toml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withDefaultTransport(t, tt.transport)
			assert.Contains(t, requireOfflineCatalogFailure(t), tt.want)
		})
	}
}
