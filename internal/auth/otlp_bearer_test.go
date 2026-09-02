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

	assert.Equal(t, "tok-fresh", exportBearer(ep, fresh, nil))
	assert.Equal(t, "", exportBearer(ep, stale, nil), "expired stored token must yield, not be sent")
	assert.Equal(t, "", exportBearer(ep, nil, nil), "logged out")
	assert.Equal(t, "", exportBearer(ep, fresh, errors.New("auth.json unreadable")))
	assert.Equal(t, "", exportBearer(ep, &StoredToken{ExpiresAt: time.Now().Add(time.Hour)}, nil), "empty access token")
}
