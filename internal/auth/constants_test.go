package auth

import (
	"os"
	"testing"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/stretchr/testify/assert"
)

func TestGetAPIURL_Default(t *testing.T) {
	// ensure SAGEOX_ENDPOINT is not set
	t.Setenv("SAGEOX_ENDPOINT", "")
	os.Unsetenv("SAGEOX_ENDPOINT")
	os.Unsetenv("SAGEOX_API")

	// also clear LoggedInEndpointsGetter to ensure we get the true default
	oldGetter := endpoint.LoggedInEndpointsGetter
	endpoint.LoggedInEndpointsGetter = nil
	defer func() { endpoint.LoggedInEndpointsGetter = oldGetter }()

	url := endpoint.Get()
	assert.Equal(t, endpoint.Default, url)
}

func TestGetAPIURL_Override(t *testing.T) {
	// set SAGEOX_ENDPOINT env var
	os.Setenv("SAGEOX_ENDPOINT", "https://custom.api.example.com")
	defer os.Unsetenv("SAGEOX_ENDPOINT")

	url := endpoint.Get()
	assert.Equal(t, "https://custom.api.example.com", url)
}

func TestClientID(t *testing.T) {
	assert.Equal(t, "ox", ClientID)
}

func TestDefaultScopes(t *testing.T) {
	assert.Len(t, DefaultScopes, 2)
	// verify expected scopes
	expectedScopes := map[string]bool{
		"user:profile": true,
		"sageox:write": true,
	}
	for _, scope := range DefaultScopes {
		assert.True(t, expectedScopes[scope], "unexpected scope: %s", scope)
	}
}

func TestDefaultAPIURL(t *testing.T) {
	assert.Equal(t, "https://sageox.ai", endpoint.Default)
}

func TestDeviceFlowEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		expected string
	}{
		{"DeviceCodeEndpoint", DeviceCodeEndpoint, "/api/auth/device/code"},
		{"DeviceTokenEndpoint", DeviceTokenEndpoint, "/api/v1/device/token"},
		{"UserInfoEndpoint", UserInfoEndpoint, "/oauth2/userinfo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotEmpty(t, tt.endpoint, "%s should not be empty", tt.name)
			assert.Equal(t, tt.expected, tt.endpoint)
		})
	}
}

func TestOAuth2Endpoints(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		expected string
	}{
		{"TokenEndpoint", TokenEndpoint, "/oauth2/token"},
		{"RevokeEndpoint", RevokeEndpoint, "/oauth2/revoke"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotEmpty(t, tt.endpoint, "%s should not be empty", tt.name)
			assert.Equal(t, tt.expected, tt.endpoint)
		})
	}
}
