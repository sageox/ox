package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
)

// Failure prevented: `ox init --no-input` demanded --endpoint from a coworker
// who is logged in to exactly one endpoint, so scripted/AI-coworker setups
// failed on a choice that had only one answer.
func TestSelectInitEndpoint_NoInputDefaultsToSoleLoggedInEndpoint(t *testing.T) {
	tests := []struct {
		name          string
		stored        []string
		expired       bool
		staleEndpoint string // extra stored login that is expired
		wantEndpoint  string
		wantNeedLogin bool
		wantErr       bool
	}{
		{name: "one non-default endpoint, valid", stored: []string{"http://127.0.0.1:9"}, wantEndpoint: "http://127.0.0.1:9"},
		// an expired access token is the normal state of a login not used recently:
		// the auth gate refreshes it, so init must not tell the coworker to log in
		{name: "one non-default endpoint, expired access token", stored: []string{"http://127.0.0.1:9"}, expired: true, wantEndpoint: "http://127.0.0.1:9"},
		// the other login may refresh fine, so a valid one beside an expired one is
		// still two logins: silently preferring the valid one picks the wrong team host
		{name: "one valid plus one expired login stays ambiguous", stored: []string{"http://127.0.0.1:9"}, staleEndpoint: "http://127.0.0.1:19", wantErr: true},
		{name: "two endpoints stay ambiguous", stored: []string{"http://127.0.0.1:9", "http://127.0.0.1:19"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			noInputCLIEnv(t)
			previous := cli.NoInput()
			cli.SetNoInput(true)
			t.Cleanup(func() { cli.SetNoInput(previous) })

			expiry := time.Now().Add(time.Hour)
			if tt.expired {
				expiry = time.Now().Add(-time.Hour)
			}
			for _, ep := range tt.stored {
				require.NoError(t, auth.SaveTokenForEndpoint(ep, &auth.StoredToken{
					AccessToken: "test-access", TokenType: "Bearer", ExpiresAt: expiry,
				}))
			}

			if tt.staleEndpoint != "" {
				require.NoError(t, auth.SaveTokenForEndpoint(tt.staleEndpoint, &auth.StoredToken{
					AccessToken: "test-stale", TokenType: "Bearer", ExpiresAt: time.Now().Add(-time.Hour),
				}))
			}

			got, needsLogin, err := selectInitEndpoint()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "--no-input requires --endpoint")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEndpoint, got)
			assert.Equal(t, tt.wantNeedLogin, needsLogin)
		})
	}
}
