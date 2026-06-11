package twinapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Twin lifecycle ---

// TestStart_ParallelIsolation verifies two twins have independent state.
// Failure prevented: shared package-level state leaking between tests.
func TestStart_ParallelIsolation(t *testing.T) {
	t.Parallel()

	tw1 := Start(t)
	tw2 := Start(t)

	// different ports
	assert.NotEqual(t, tw1.APIURL, tw2.APIURL)
	assert.NotEqual(t, tw1.AdminURL, tw2.AdminURL)

	// state is isolated: user in tw1 does not exist in tw2
	tw1.CreateUser("usr_1", "one@example.com", "One")
	fix := tw2.WithAuthenticatedUser("two@example.com", "Two")

	assert.Equal(t, 1, len(tw1.store.users))
	assert.Equal(t, 1, len(tw2.store.users))
	assert.NotEmpty(t, fix.JWT)
}

// TestStart_JWTIsolation verifies JWT from one twin is rejected by another.
// Failure prevented: per-instance secret not generated correctly.
func TestStart_JWTIsolation(t *testing.T) {
	t.Parallel()

	tw1 := Start(t)
	tw2 := Start(t)

	fix := tw1.WithAuthenticatedUser("user@example.com", "User")

	// JWT from tw1 should be rejected by tw2's userinfo endpoint
	req, _ := http.NewRequest("GET", tw2.APIURL+"/oauth2/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+fix.JWT)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// --- B. Device flow ---

// TestDeviceFlow_HappyPath verifies the full device code -> token -> JWT exchange.
// Failure prevented: device flow not returning valid session tokens.
func TestDeviceFlow_HappyPath(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	tw.CreateUser("usr_test", "dev@example.com", "Dev")

	client := &http.Client{Timeout: 5 * time.Second}

	// step 1: request device code
	resp, err := client.Post(tw.APIURL+"/api/auth/device/code", "application/json", nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var codeResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&codeResp))
	deviceCode := codeResp["device_code"].(string)
	assert.NotEmpty(t, deviceCode)
	assert.NotEmpty(t, codeResp["user_code"])

	// step 2: poll for token (auto-approves because user exists)
	body := fmt.Sprintf(`{"device_code":"%s","grant_type":"urn:ietf:params:oauth:grant-type:device_code","client_id":"ox"}`, deviceCode)
	resp2, err := client.Post(tw.APIURL+"/api/v1/device/token", "application/json",
		jsonReader(body))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	var tokenResp map[string]any
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&tokenResp))
	sessionToken := tokenResp["access_token"].(string)
	assert.Len(t, sessionToken, 32)
	assert.NotEmpty(t, tokenResp["refresh_token"])

	// step 3: exchange session token for JWT
	req, _ := http.NewRequest("GET", tw.APIURL+"/api/v1/cli/auth/token", nil)
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp3, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp3.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp3.StatusCode)

	var jwtResp map[string]any
	require.NoError(t, json.NewDecoder(resp3.Body).Decode(&jwtResp))
	jwt := jwtResp["access_token"].(string)
	assert.Contains(t, jwt, ".") // JWT has dots

	// step 4: validate JWT via userinfo
	req2, _ := http.NewRequest("GET", tw.APIURL+"/oauth2/userinfo", nil)
	req2.Header.Set("Authorization", "Bearer "+jwt)
	resp4, err := client.Do(req2)
	require.NoError(t, err)
	defer func() { _ = resp4.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp4.StatusCode)

	var userInfo map[string]string
	require.NoError(t, json.NewDecoder(resp4.Body).Decode(&userInfo))
	assert.Equal(t, "dev@example.com", userInfo["email"])
	assert.Equal(t, "Dev", userInfo["name"])
}

// TestDeviceFlow_NoUsers_Pending verifies authorization_pending when no users exist.
// Failure prevented: auto-approve firing when it shouldn't.
func TestDeviceFlow_NoUsers_Pending(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	// no users created

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Post(tw.APIURL+"/api/auth/device/code", "application/json", nil)
	require.NoError(t, err)
	var codeResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&codeResp))
	_ = resp.Body.Close()

	body := fmt.Sprintf(`{"device_code":"%s"}`, codeResp["device_code"])
	resp2, err := client.Post(tw.APIURL+"/api/v1/device/token", "application/json", jsonReader(body))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()

	var tokenResp map[string]string
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&tokenResp))
	assert.Equal(t, "authorization_pending", tokenResp["error"])
}

// --- C. JWT exchange — the exact bug we hit ---

// TestJWTExchange_OrphanedSession verifies "user account not found" when session
// points to a deleted/non-existent user.
// Failure prevented: the exact test.sageox.ai bug — stale session token from an
// orphaned user record silently stored as access_token.
func TestJWTExchange_OrphanedSession(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	sess := tw.CreateOrphanedSession("usr_deleted_user")

	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", tw.APIURL+"/api/v1/cli/auth/token", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var errResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	assert.Equal(t, "user account not found", errResp["error"])
	assert.Equal(t, false, errResp["success"])
}

// TestJWTExchange_InvalidSessionToken verifies rejection of unknown tokens.
// Failure prevented: JWT exchange accepting arbitrary bearer tokens.
func TestJWTExchange_InvalidSessionToken(t *testing.T) {
	t.Parallel()

	tw := Start(t)

	req, _ := http.NewRequest("GET", tw.APIURL+"/api/v1/cli/auth/token", nil)
	req.Header.Set("Authorization", "Bearer totally_bogus_token")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestJWTExchange_MissingAuthHeader verifies proper error without auth header.
func TestJWTExchange_MissingAuthHeader(t *testing.T) {
	t.Parallel()

	tw := Start(t)

	req, _ := http.NewRequest("GET", tw.APIURL+"/api/v1/cli/auth/token", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// --- D. Userinfo (server-side token validation) ---

// TestUserInfo_ValidJWT verifies userinfo returns correct claims for valid JWT.
func TestUserInfo_ValidJWT(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	fix := tw.WithAuthenticatedUser("alice@example.com", "Alice")

	req, _ := http.NewRequest("GET", tw.APIURL+"/oauth2/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+fix.JWT)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var info map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
	assert.Equal(t, fix.UserID, info["sub"])
	assert.Equal(t, "alice@example.com", info["email"])
}

// TestUserInfo_ExpiredJWT verifies rejection after clock advances past JWT expiry.
// Failure prevented: IsAuthenticatedForEndpoint not detecting expired tokens
// server-side. This is the scenario where local check passes but server rejects.
func TestUserInfo_ExpiredJWT(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	fix := tw.WithAuthenticatedUser("bob@example.com", "Bob")

	// advance clock past JWT expiry (24h default)
	tw.AdvanceTime(25 * time.Hour)

	req, _ := http.NewRequest("GET", tw.APIURL+"/oauth2/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+fix.JWT)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var errResp map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	assert.Equal(t, "invalid_token", errResp["error"])
	assert.Contains(t, errResp["error_description"], "expired")
}

// TestUserInfo_OpaqueTokenRejected verifies that sending a raw session token
// (not a JWT) to userinfo is rejected.
// Failure prevented: the exact bug — opaque session token stored as access_token
// and sent to API endpoints that expect JWTs.
func TestUserInfo_OpaqueTokenRejected(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	fix := tw.WithAuthenticatedUser("charlie@example.com", "Charlie")

	// send the session token (opaque, 32-char hex) instead of the JWT
	req, _ := http.NewRequest("GET", tw.APIURL+"/oauth2/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+fix.SessionToken)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// --- E. Token refresh ---

// TestTokenRefresh_HappyPath verifies refresh returns a new JWT and rotates
// the refresh token.
func TestTokenRefresh_HappyPath(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	fix := tw.WithAuthenticatedUser("refresh@example.com", "Refresher")

	resp, err := http.DefaultClient.PostForm(tw.APIURL+"/oauth2/token", map[string][]string{
		"grant_type":    {"refresh_token"},
		"refresh_token": {fix.RefreshToken},
		"client_id":     {"ox"},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var tokenResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tokenResp))

	newJWT := tokenResp["access_token"].(string)
	newRefresh := tokenResp["refresh_token"].(string)

	assert.Contains(t, newJWT, ".")                  // is a JWT
	assert.NotEqual(t, fix.RefreshToken, newRefresh) // rotated
	assert.Len(t, newRefresh, 32)
}

// TestTokenRefresh_InvalidRefreshToken verifies invalid_grant for bad refresh tokens.
func TestTokenRefresh_InvalidRefreshToken(t *testing.T) {
	t.Parallel()

	tw := Start(t)

	resp, err := http.DefaultClient.PostForm(tw.APIURL+"/oauth2/token", map[string][]string{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"completely_bogus_refresh_token"},
		"client_id":     {"ox"},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var errResp map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	assert.Equal(t, "invalid_grant", errResp["error"])
}

// TestTokenRefresh_OldRefreshTokenRejected verifies that after rotation, the old
// refresh token no longer works.
// Failure prevented: refresh token reuse after rotation (security issue).
func TestTokenRefresh_OldRefreshTokenRejected(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	fix := tw.WithAuthenticatedUser("rotate@example.com", "Rotator")
	oldRefresh := fix.RefreshToken

	// first refresh succeeds
	resp, err := http.DefaultClient.PostForm(tw.APIURL+"/oauth2/token", map[string][]string{
		"grant_type":    {"refresh_token"},
		"refresh_token": {oldRefresh},
		"client_id":     {"ox"},
	})
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// second refresh with OLD token fails (it was rotated)
	resp2, err := http.DefaultClient.PostForm(tw.APIURL+"/oauth2/token", map[string][]string{
		"grant_type":    {"refresh_token"},
		"refresh_token": {oldRefresh},
		"client_id":     {"ox"},
	})
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

// --- F. Token revocation ---

// TestRevoke_AlwaysReturns200 verifies RFC 7009 compliance — always 200 OK.
func TestRevoke_AlwaysReturns200(t *testing.T) {
	t.Parallel()

	tw := Start(t)

	// revoke a non-existent token — should still return 200
	resp, err := http.DefaultClient.PostForm(tw.APIURL+"/oauth2/revoke", map[string][]string{
		"token": {"nonexistent_token"},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestRevoke_SessionInvalidated verifies that after revoking a session token,
// the JWT exchange no longer works.
func TestRevoke_SessionInvalidated(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	fix := tw.WithAuthenticatedUser("revoke@example.com", "Revoker")

	// revoke the session token
	resp, err := http.DefaultClient.PostForm(tw.APIURL+"/oauth2/revoke", map[string][]string{
		"token": {fix.SessionToken},
	})
	require.NoError(t, err)
	_ = resp.Body.Close()

	// JWT exchange should now fail
	req, _ := http.NewRequest("GET", tw.APIURL+"/api/v1/cli/auth/token", nil)
	req.Header.Set("Authorization", "Bearer "+fix.SessionToken)
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

// --- G. Fault injection ---

// TestFaultInjection_Userinfo401 verifies that injecting a 401 on userinfo
// causes server-side validation to fail.
// Failure prevented: IsAuthenticatedForEndpoint not propagating server errors.
func TestFaultInjection_Userinfo401(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	fix := tw.WithAuthenticatedUser("fault@example.com", "Faulter")

	// inject 401 on userinfo
	tw.InjectFault("/oauth2/userinfo", http.StatusUnauthorized)

	req, _ := http.NewRequest("GET", tw.APIURL+"/oauth2/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+fix.JWT)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// clear fault — should work again
	tw.ClearFaults()

	req2, _ := http.NewRequest("GET", tw.APIURL+"/oauth2/userinfo", nil)
	req2.Header.Set("Authorization", "Bearer "+fix.JWT)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp2.StatusCode)
}

// TestFaultInjection_AfterN verifies faults that trigger only after N calls.
// Failure prevented: retry logic not exercised because faults are all-or-nothing.
func TestFaultInjection_AfterN(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	fix := tw.WithAuthenticatedUser("retry@example.com", "Retrier")

	// fail after 2 successful calls
	tw.InjectFaultAfter("/oauth2/userinfo", http.StatusServiceUnavailable, 2)

	doRequest := func() int {
		req, err := http.NewRequest("GET", tw.APIURL+"/oauth2/userinfo", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+fix.JWT)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	assert.Equal(t, http.StatusOK, doRequest())                 // call 1: ok
	assert.Equal(t, http.StatusOK, doRequest())                 // call 2: ok
	assert.Equal(t, http.StatusServiceUnavailable, doRequest()) // call 3: fault
	assert.Equal(t, http.StatusServiceUnavailable, doRequest()) // call 4: still faulted
}

// --- H. Call recording ---

// TestCallRecording verifies API calls are recorded for assertions.
func TestCallRecording(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	fix := tw.WithAuthenticatedUser("record@example.com", "Recorder")

	// make a userinfo request
	req, err := http.NewRequest("GET", tw.APIURL+"/oauth2/userinfo", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+fix.JWT)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	tw.AssertCalled(t, "GET", "/oauth2/userinfo")
	tw.AssertNotCalled(t, "POST", "/oauth2/token")
	assert.Equal(t, 1, tw.CallCount("GET", "/oauth2/userinfo"))
}

// --- I. Clock manipulation ---

// TestClockAdvance_JWTExpiry verifies that advancing the clock past JWT expiry
// causes the twin to reject previously valid tokens.
func TestClockAdvance_JWTExpiry(t *testing.T) {
	t.Parallel()

	tw := Start(t)
	fix := tw.WithAuthenticatedUser("clock@example.com", "Clock")

	// verify JWT is valid now
	req, err := http.NewRequest("GET", tw.APIURL+"/oauth2/userinfo", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+fix.JWT)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// advance past expiry
	tw.AdvanceTime(25 * time.Hour)

	// same JWT should now fail
	req2, err := http.NewRequest("GET", tw.APIURL+"/oauth2/userinfo", nil)
	require.NoError(t, err)
	req2.Header.Set("Authorization", "Bearer "+fix.JWT)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	_ = resp2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)

	// but a fresh JWT should work
	tw.store.mu.RLock()
	user := tw.store.users[fix.UserID]
	tw.store.mu.RUnlock()
	freshJWT := tw.store.mintJWT(user, defaultJWTExpiry)

	req3, err := http.NewRequest("GET", tw.APIURL+"/oauth2/userinfo", nil)
	require.NoError(t, err)
	req3.Header.Set("Authorization", "Bearer "+freshJWT)
	resp3, err := http.DefaultClient.Do(req3)
	require.NoError(t, err)
	_ = resp3.Body.Close()
	assert.Equal(t, http.StatusOK, resp3.StatusCode)
}

// --- helpers ---

func jsonReader(s string) *strings.Reader {
	return strings.NewReader(s)
}
