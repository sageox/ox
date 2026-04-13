package twinapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const (
	defaultJWTExpiry     = 24 * time.Hour
	defaultSessionExpiry = 90 * 24 * time.Hour
	deviceCodeExpiry     = 5 * time.Minute
)

// handleDeviceCode handles POST /api/auth/device/code.
// Initiates the device authorization flow by returning a device code and user code.
func (tw *Twin) handleDeviceCode(w http.ResponseWriter, r *http.Request) {
	dc := &DeviceCode{
		DeviceCode: tw.store.generateToken(),
		UserCode:   strings.ToUpper(tw.store.generateToken()[:8]),
		Approved:   false,
		ExpiresAt:  tw.store.now().Add(deviceCodeExpiry),
	}

	tw.store.mu.Lock()
	tw.store.deviceCodes[dc.DeviceCode] = dc
	tw.store.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":      dc.DeviceCode,
		"user_code":        dc.UserCode,
		"verification_uri": tw.APIURL + "/device",
		"expires_in":       300,
		"interval":         1,
	})
}

// handleDeviceToken handles POST /api/v1/device/token.
// Polls for device authorization completion. Auto-approves on first poll
// when at least one user exists in the store.
func (tw *Twin) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceCode string `json:"device_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	tw.store.mu.Lock()
	dc, ok := tw.store.deviceCodes[body.DeviceCode]
	if !ok {
		tw.store.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}

	if tw.store.clock.After(dc.ExpiresAt) {
		tw.store.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expired_token"})
		return
	}

	if !dc.Approved {
		// auto-approve if at least one user exists
		if len(tw.store.users) > 0 {
			// pick first user
			for _, u := range tw.store.users {
				dc.Approved = true
				dc.UserID = u.ID
				break
			}
		}
	}

	if !dc.Approved {
		tw.store.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"error": "authorization_pending"})
		return
	}

	// create session for the approved user
	token := tw.store.generateToken()
	refreshToken := tw.store.generateToken()
	sess := &Session{
		Token:        token,
		UserID:       dc.UserID,
		RefreshToken: refreshToken,
		ExpiresAt:    tw.store.clock.Add(defaultSessionExpiry),
	}
	tw.store.sessions[token] = sess

	// clean up the device code
	delete(tw.store.deviceCodes, body.DeviceCode)
	tw.store.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  token,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"expires_in":    int(defaultSessionExpiry.Seconds()),
	})
}

// handleJWTExchange handles GET /api/v1/cli/auth/token.
// Exchanges an opaque session token (Bearer) for a signed JWT.
func (tw *Twin) handleJWTExchange(w http.ResponseWriter, r *http.Request) {
	token := extractBearer(r)
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false,
			"error":   "missing authorization header",
		})
		return
	}

	tw.store.mu.RLock()
	sess, ok := tw.store.sessions[token]
	if !ok {
		tw.store.mu.RUnlock()
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false,
			"error":   "invalid session token",
		})
		return
	}

	if !tw.store.clock.Before(sess.ExpiresAt) {
		tw.store.mu.RUnlock()
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false,
			"error":   "session expired",
		})
		return
	}

	user, ok := tw.store.users[sess.UserID]
	tw.store.mu.RUnlock()

	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false,
			"error":   "user account not found",
		})
		return
	}

	jwt := tw.store.mintJWT(user, defaultJWTExpiry)

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": jwt,
		"token_type":   "Bearer",
		"expires_in":   int(defaultJWTExpiry.Seconds()),
	})
}

// handleUserInfo handles GET /oauth2/userinfo.
// Validates a Bearer JWT and returns user claims.
func (tw *Twin) handleUserInfo(w http.ResponseWriter, r *http.Request) {
	token := extractBearer(r)
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":             "invalid_token",
			"error_description": "missing authorization header",
		})
		return
	}

	claims, err := tw.store.validateJWT(token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":             "invalid_token",
			"error_description": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"sub":   claims.Sub,
		"email": claims.Email,
		"name":  claims.Name,
		"tier":  claims.Tier,
	})
}

// handleTokenRefresh handles POST /oauth2/token.
// Accepts grant_type=refresh_token and returns a new JWT + refresh token.
func (tw *Twin) handleTokenRefresh(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	grantType := r.FormValue("grant_type")
	if grantType != "refresh_token" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}

	refreshToken := r.FormValue("refresh_token")
	if refreshToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	// find session by refresh token
	tw.store.mu.Lock()
	var sess *Session
	for _, s := range tw.store.sessions {
		if s.RefreshToken == refreshToken {
			sess = s
			break
		}
	}

	if sess == nil || !tw.store.clock.Before(sess.ExpiresAt) {
		tw.store.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}

	user, ok := tw.store.users[sess.UserID]
	if !ok {
		tw.store.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}

	// rotate refresh token
	newRefresh := tw.store.generateToken()
	sess.RefreshToken = newRefresh
	tw.store.mu.Unlock()

	jwt := tw.store.mintJWT(user, defaultJWTExpiry)

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  jwt,
		"refresh_token": newRefresh,
		"expires_in":    int(defaultJWTExpiry.Seconds()),
		"token_type":    "Bearer",
	})
}

// handleRevoke handles POST /oauth2/revoke.
// Per RFC 7009, always returns 200 OK. Best-effort removal from store.
func (tw *Twin) handleRevoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	revokeToken := r.FormValue("token")
	if revokeToken != "" {
		tw.store.mu.Lock()
		// try as session token
		delete(tw.store.sessions, revokeToken)
		// try as refresh token
		for key, s := range tw.store.sessions {
			if s.RefreshToken == revokeToken {
				delete(tw.store.sessions, key)
				break
			}
		}
		tw.store.mu.Unlock()
	}

	w.WriteHeader(http.StatusOK)
}

// extractBearer pulls the token from "Authorization: Bearer <token>".
func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

// writeJSON marshals v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
