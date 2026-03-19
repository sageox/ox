package doctorapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetContext_Success(t *testing.T) {
	t.Parallel()

	expected := DoctorContextResponse{
		User: &UserInfo{
			ID:    "user-1",
			Email: "test@example.com",
			Name:  "Test User",
			Tier:  "pro",
		},
		Server: ServerInfo{Version: "1.0.0"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/cli/doctor/context", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
		AuthFunc: func() string { return "test-token" },
	})

	result, err := client.GetContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "user-1", result.User.ID)
	assert.Equal(t, "test@example.com", result.User.Email)
	assert.Equal(t, "1.0.0", result.Server.Version)
}

func TestGetContext_Unauthorized(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
	})

	_, err := client.GetContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unauthorized")
}

func TestGetContext_ServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
	})

	_, err := client.GetContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestGetContext_MalformedJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"invalid json`))
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
	})

	_, err := client.GetContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestGetContext_Timeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
		Timeout:  50 * time.Millisecond,
	})

	_, err := client.GetContext(context.Background())
	require.Error(t, err)
	assert.True(t, os.IsTimeout(err) || strings.Contains(err.Error(), "deadline exceeded") || strings.Contains(err.Error(), "Client.Timeout"),
		"expected timeout error, got: %v", err)
}

func TestGetContext_NoAuthFunc(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// should have no Authorization header
		assert.Empty(t, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(DoctorContextResponse{})
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
		// no AuthFunc
	})

	_, err := client.GetContext(context.Background())
	require.NoError(t, err)
}

func TestNewClient_Defaults(t *testing.T) {
	t.Parallel()

	client := NewClient(ClientConfig{Endpoint: "https://example.com"})
	assert.Equal(t, 10*time.Second, client.config.Timeout)
	assert.NotNil(t, client.config.HTTPClient)
}
