package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckSlugGitPATLiveness_Registered(t *testing.T) {
	check := GetDoctorCheck(CheckSlugGitPATLiveness)
	require.NotNil(t, check, "git-pat-liveness check should be registered")
	assert.Equal(t, "Authentication", check.Category)
	assert.Equal(t, FixLevelAuto, check.FixLevel)
	assert.Equal(t, FixLevelAuto, check.FixLevel, "PAT liveness auto-repairs via credential sync")
}

func TestValidatePATLiveness_ValidToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") == "valid-pat" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result := gitserver.ValidatePATLiveness(ctx, srv.URL, "valid-pat")
	assert.True(t, result.Valid)
	assert.False(t, result.Skipped)
	assert.Empty(t, result.Reason)
}

func TestValidatePATLiveness_RevokedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result := gitserver.ValidatePATLiveness(ctx, srv.URL, "revoked-pat")
	assert.False(t, result.Valid)
	assert.False(t, result.Skipped)
	assert.Contains(t, result.Reason, "rejected")
}

func TestValidatePATLiveness_NoCredentials(t *testing.T) {
	ctx := context.Background()

	result := gitserver.ValidatePATLiveness(ctx, "", "")
	assert.True(t, result.Skipped)
	assert.Contains(t, result.Reason, "no credentials")
}

func TestValidatePATLiveness_NetworkError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// unreachable server
	result := gitserver.ValidatePATLiveness(ctx, "http://192.0.2.1:1", "some-token")
	assert.True(t, result.Skipped)
	assert.Contains(t, result.Reason, "network unreachable")
}
