package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckVersionResponse_UpgradeRequired(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusUpgradeRequired,
		Header:     http.Header{},
	}
	resp.Header.Set(HeaderMinVersion, "2.0.0")

	// 426 should signal abort
	abort := CheckVersionResponse(resp)
	assert.True(t, abort, "426 should return true (abort)")
}

func TestCheckVersionResponse_UpgradeRequired_NoMinVersion(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusUpgradeRequired,
		Header:     http.Header{},
	}

	abort := CheckVersionResponse(resp)
	assert.True(t, abort, "426 without min-version header should still abort")
}

func TestCheckVersionResponse_SuccessNoDeprecation(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
	}

	abort := CheckVersionResponse(resp)
	assert.False(t, abort, "200 without deprecation should not abort")
}

func TestCheckVersionResponse_NonUpgradeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
	}{
		{"400 bad request", http.StatusBadRequest},
		{"401 unauthorized", http.StatusUnauthorized},
		{"403 forbidden", http.StatusForbidden},
		{"404 not found", http.StatusNotFound},
		{"500 internal error", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := &http.Response{
				StatusCode: tt.statusCode,
				Header:     http.Header{},
			}
			abort := CheckVersionResponse(resp)
			assert.False(t, abort, "%d should not trigger upgrade abort", tt.statusCode)
		})
	}
}

func TestCheckVersionResponse_DeprecationHeader(t *testing.T) {
	// not parallel due to sync.Once state (deprecationShown)
	// create a fresh response with deprecation header
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
	}
	resp.Header.Set(HeaderDeprecated, "v1.0 will be removed on 2026-01-01")

	// should not abort (soft warning only)
	abort := CheckVersionResponse(resp)
	assert.False(t, abort, "deprecation warning should not abort")
}

func TestHeaderConstants(t *testing.T) {
	t.Parallel()

	// verify header constants match expected values
	assert.Equal(t, "X-SageOx-Min-Version", HeaderMinVersion)
	assert.Equal(t, "X-SageOx-Deprecated", HeaderDeprecated)
}

func TestCheckVersionResponse_IntegrationWithHTTPServer(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderMinVersion, "3.0.0")
		w.WriteHeader(http.StatusUpgradeRequired)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	assert.NoError(t, err)
	defer resp.Body.Close()

	abort := CheckVersionResponse(resp)
	assert.True(t, abort, "server returning 426 should trigger abort via full HTTP roundtrip")
}
