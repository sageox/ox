package gitserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePATLiveness(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		serverURL  string
		token      string
		wantValid  bool
		wantSkip   bool
		wantReason string
	}{
		{
			name: "valid PAT returns 200",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, r.Header.Get("PRIVATE-TOKEN"), "good-token")
				w.WriteHeader(http.StatusOK)
			},
			token:     "good-token",
			wantValid: true,
		},
		{
			name: "revoked PAT returns 401",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			token:      "bad-token",
			wantValid:  false,
			wantReason: "PAT rejected by server (revoked or invalid)",
		},
		{
			name: "forbidden PAT returns 403",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
			token:      "forbidden-token",
			wantValid:  false,
			wantReason: "PAT rejected by server (revoked or invalid)",
		},
		{
			name:      "empty token skips",
			handler:   nil,
			token:     "",
			wantSkip:  true,
			wantReason: "no credentials",
		},
		{
			name:       "empty server URL skips",
			handler:    nil,
			serverURL:  "",
			token:      "some-token",
			wantSkip:   true,
			wantReason: "no credentials",
		},
		{
			name: "unexpected status skips",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			token:    "some-token",
			wantSkip: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var serverURL string
			if tt.handler != nil {
				srv := httptest.NewServer(tt.handler)
				defer srv.Close()
				serverURL = srv.URL
			} else if tt.serverURL != "" {
				serverURL = tt.serverURL
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			result := ValidatePATLiveness(ctx, serverURL, tt.token)

			assert.Equal(t, tt.wantValid, result.Valid, "Valid mismatch")
			assert.Equal(t, tt.wantSkip, result.Skipped, "Skipped mismatch")
			if tt.wantReason != "" {
				require.Contains(t, result.Reason, tt.wantReason)
			}
		})
	}
}

func TestValidatePATLiveness_Timeout(t *testing.T) {
	// server that never responds
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := ValidatePATLiveness(ctx, srv.URL, "some-token")
	assert.True(t, result.Skipped, "should skip on timeout")
	assert.Contains(t, result.Reason, "network unreachable")
}

func TestBuildProbeURL(t *testing.T) {
	tests := []struct {
		name      string
		serverURL string
		want      string
		wantErr   bool
	}{
		{
			name:      "https URL",
			serverURL: "https://git.sageox.ai",
			want:      "https://git.sageox.ai/api/v4/user",
		},
		{
			name:      "http URL with port",
			serverURL: "http://localhost:3000",
			want:      "http://localhost:3000/api/v4/user",
		},
		{
			name:      "URL with existing path stripped",
			serverURL: "https://git.sageox.ai/some/path",
			want:      "https://git.sageox.ai/api/v4/user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildProbeURL(tt.serverURL)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
