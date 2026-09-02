package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The exporter must never send a bearer the server is guaranteed to reject:
// an expired stored token is the case that flooded prod with 401s
// (sageox-9naj9). Failure prevented: every batch from a stale daemon 401ing.
func TestExportBearer(t *testing.T) {
	const ep = "https://api.test.sageox.ai"
	fresh := &StoredToken{AccessToken: "tok-fresh", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)}
	stale := &StoredToken{AccessToken: "tok-stale", RefreshToken: "r", ExpiresAt: time.Now().Add(-time.Minute)}

	cases := []struct {
		name    string
		tok     *StoredToken
		err     error
		want    string
		expired bool
	}{
		{"fresh", fresh, nil, "tok-fresh", false},
		{"expired stored token must yield, not be sent", stale, nil, "", true},
		{"logged out is not an expiry", nil, nil, "", false},
		{"unreadable auth.json", fresh, errors.New("auth.json unreadable"), "", false},
		{"empty access token", &StoredToken{ExpiresAt: time.Now().Add(time.Hour)}, nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, expired := exportBearer(ep, tc.tok, tc.err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.expired, expired)
		})
	}
}

// The expiry yield is silent on the wire, so the transition must be logged
// exactly once each way — not once per batch (a daemon exports every few
// seconds) and not never. Failure prevented: a daemon whose traces stopped
// with nothing in its log explaining why.
func TestNoteExportYield_LogsOncePerTransition(t *testing.T) {
	const ep = "https://api.yield-test.sageox.ai"
	assert.False(t, noteExportYield(ep, false), "initial fresh state is not a transition")
	assert.True(t, noteExportYield(ep, true), "fresh -> expired")
	assert.False(t, noteExportYield(ep, true), "still expired: no repeat")
	assert.False(t, noteExportYield(ep, true))
	assert.True(t, noteExportYield(ep, false), "expired -> fresh")
	assert.False(t, noteExportYield(ep, false))
}

// Only a usable bearer ends an expired-token pause. A logout (nil token) or
// an unreadable auth.json after the pause must leave it paused, otherwise the
// log claims "resumed: fresh token on disk" when there is no token at all.
func TestExportBearerNoting_LogoutDoesNotResume(t *testing.T) {
	const ep = "https://api.noting-test.sageox.ai"
	stale := &StoredToken{AccessToken: "tok-stale", RefreshToken: "r", ExpiresAt: time.Now().Add(-time.Minute)}
	fresh := &StoredToken{AccessToken: "tok-fresh", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)}
	paused := func() bool {
		exportYield.mu.Lock()
		defer exportYield.mu.Unlock()
		return exportYield.expired[ep]
	}

	assert.Equal(t, "", exportBearerNoting(ep, stale, nil))
	assert.True(t, paused(), "expired token pauses")
	assert.Equal(t, "", exportBearerNoting(ep, nil, nil))
	assert.True(t, paused(), "logout must not read as a resume")
	assert.Equal(t, "", exportBearerNoting(ep, fresh, errors.New("auth.json unreadable")))
	assert.True(t, paused(), "unreadable store must not read as a resume")
	assert.Equal(t, "tok-fresh", exportBearerNoting(ep, fresh, nil))
	assert.False(t, paused(), "a usable bearer resumes")
}
