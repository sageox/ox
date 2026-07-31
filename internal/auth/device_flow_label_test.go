package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- GH #625: ox login sends a device label ---
//
// These assert the WIRE, not just the helper: the whole point is what
// lands in the token-exchange request body.

// captureTokenRequestBody runs pollToken against a stub server and
// returns the decoded request body.
func captureTokenRequestBody(t *testing.T) map[string]any {
	t.Helper()

	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &body))

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(TokenResponse{
			AccessToken: "test-access-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		}))
	}))
	defer server.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	_, err := pollToken(client, server.URL, "test-device-code")
	require.NoError(t, err)
	return body
}

func TestPollToken_SendsDeviceLabel(t *testing.T) {
	if testing.Short() {
		t.Skip("short: device flow polling")
	}
	t.Setenv(EnvVarNoDeviceLabel, "")
	t.Setenv("USER", "ryan")

	body := captureTokenRequestBody(t)

	// the pre-existing fields must be untouched
	assert.Equal(t, "urn:ietf:params:oauth:grant-type:device_code", body["grant_type"])
	assert.Equal(t, "test-device-code", body["device_code"])
	assert.Equal(t, ClientID, body["client_id"])

	label, ok := body["device_label"].(string)
	require.True(t, ok, "device_label must be present and a string; got body %v", body)
	assert.NotEmpty(t, label)
	assert.Regexp(t, deviceLabelShape, label,
		"the label is rendered in a web UI and must carry no injectable characters")
	assert.LessOrEqual(t, utf8.RuneCountInString(label), maxDeviceLabelUser+1+maxDeviceLabelHost)
}

// TestPollToken_OmitsDeviceLabelWhenOptedOut is what proves `omitempty`
// is doing its job: the KEY must be absent, not present-and-empty. An
// empty string would still be a behavior change on the wire, which is
// exactly what an opt-out must not be.
func TestPollToken_OmitsDeviceLabelWhenOptedOut(t *testing.T) {
	if testing.Short() {
		t.Skip("short: device flow polling")
	}
	t.Setenv(EnvVarNoDeviceLabel, "1")
	t.Setenv("USER", "ryan")

	body := captureTokenRequestBody(t)

	_, present := body["device_label"]
	assert.False(t, present,
		"opting out must remove the key entirely, leaving a request byte-identical "+
			"to what ox sent before device labels existed")

	// and the exchange must still work
	assert.Equal(t, "test-device-code", body["device_code"])
}
