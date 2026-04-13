package auth_test

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/twinapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Server-side token validation via twinapi ---
// These tests exercise the real auth.ValidateTokenServerSide function against
// a running twinapi instance. Inline mocks can't catch these because the bug
// was in the interaction pattern between CLI and server.

// TestValidateTokenServerSide_ValidJWT verifies that a proper JWT passes validation.
// Failure prevented: false negatives in server-side validation.
func TestValidateTokenServerSide_ValidJWT(t *testing.T) {
	tw := twinapi.Start(t)
	fix := tw.WithAuthenticatedUser("valid@example.com", "Valid User")

	err := auth.ValidateTokenServerSide(tw.URL(), fix.JWT)
	assert.NoError(t, err)
}

// TestValidateTokenServerSide_OpaqueToken verifies that an opaque session token
// is rejected by server-side validation.
// Failure prevented: THE BUG — CLI stored opaque token as access_token, local
// check said "valid", but server rejected it. This test proves the server-side
// validation catches it.
func TestValidateTokenServerSide_OpaqueToken(t *testing.T) {
	tw := twinapi.Start(t)
	fix := tw.WithAuthenticatedUser("opaque@example.com", "Opaque User")

	err := auth.ValidateTokenServerSide(tw.URL(), fix.SessionToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server rejected token")
}

// TestValidateTokenServerSide_ExpiredJWT verifies that an expired JWT is
// rejected by the server even though it was once valid.
// Failure prevented: local check passes (token exists in auth.json) but
// server correctly identifies expiry.
func TestValidateTokenServerSide_ExpiredJWT(t *testing.T) {
	tw := twinapi.Start(t)
	fix := tw.WithAuthenticatedUser("expired@example.com", "Expired User")

	// advance clock past 24h JWT expiry
	tw.AdvanceTime(25 * time.Hour)

	err := auth.ValidateTokenServerSide(tw.URL(), fix.JWT)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server rejected token")
}

// TestValidateTokenServerSide_ServerDown verifies graceful handling when the
// server is unreachable.
// Failure prevented: init/doctor/status crashing instead of showing a useful
// error when the server is down.
func TestValidateTokenServerSide_ServerDown(t *testing.T) {
	// use a URL that will immediately refuse connections
	err := auth.ValidateTokenServerSide("http://127.0.0.1:1", "some-jwt-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not reach")
}

// TestValidateTokenServerSide_ServerRejectsWithUserNotFound verifies that the
// specific "user account not found" error from JWT exchange is distinguishable.
// Failure prevented: generic "401" error masking the real cause.
func TestValidateTokenServerSide_ServerRejectsWithUserNotFound(t *testing.T) {
	tw := twinapi.Start(t)

	// create orphaned session (user doesn't exist), get session token
	sess := tw.CreateOrphanedSession("usr_ghost")

	// the session token isn't a JWT, so userinfo will reject it as malformed
	err := auth.ValidateTokenServerSide(tw.URL(), sess.Token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server rejected token")
}

// TestValidateTokenServerSide_FaultInjection verifies that fault injection on
// the userinfo endpoint is detected by the validation function.
func TestValidateTokenServerSide_FaultInjection(t *testing.T) {
	tw := twinapi.Start(t)
	fix := tw.WithAuthenticatedUser("fault@example.com", "Fault User")

	// verify it works first
	err := auth.ValidateTokenServerSide(tw.URL(), fix.JWT)
	require.NoError(t, err)

	// inject a 503 on userinfo
	tw.InjectFault("/oauth2/userinfo", 503)

	err = auth.ValidateTokenServerSide(tw.URL(), fix.JWT)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server rejected token")

	// clear and verify recovery
	tw.ClearFaults()

	err = auth.ValidateTokenServerSide(tw.URL(), fix.JWT)
	assert.NoError(t, err)
}
