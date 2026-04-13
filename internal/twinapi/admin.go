package twinapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// handleAdminReset handles POST /reset.
// Wipes all state including users, sessions, faults, and call records.
func (tw *Twin) handleAdminReset(w http.ResponseWriter, _ *http.Request) {
	tw.store.reset()
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminCreateUser handles POST /users.
// Creates a user from JSON body {id, email, name, tier}.
func (tw *Twin) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var u User
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if u.ID == "" {
		u.ID = tw.store.generateID("usr_")
	}
	if u.Tier == "" {
		u.Tier = "free"
	}

	tw.store.mu.Lock()
	tw.store.users[u.ID] = &u
	tw.store.mu.Unlock()

	writeJSON(w, http.StatusCreated, &u)
}

// handleAdminCreateSession handles POST /users/{id}/sessions.
// Creates a session for an existing user.
func (tw *Twin) handleAdminCreateSession(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")

	tw.store.mu.Lock()
	if _, ok := tw.store.users[userID]; !ok {
		tw.store.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}

	token := tw.store.generateToken()
	refreshToken := tw.store.generateToken()
	sess := &Session{
		Token:        token,
		UserID:       userID,
		RefreshToken: refreshToken,
		ExpiresAt:    tw.store.clock.Add(defaultSessionExpiry),
	}
	tw.store.sessions[token] = sess
	tw.store.mu.Unlock()

	writeJSON(w, http.StatusCreated, map[string]any{
		"token":         token,
		"refresh_token": refreshToken,
		"expires_at":    sess.ExpiresAt.Format(time.RFC3339),
	})
}

// handleAdminCreateOrphanedSession handles POST /sessions/orphaned.
// Creates a session pointing to a non-existent user (for error-path testing).
func (tw *Twin) handleAdminCreateOrphanedSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if body.UserID == "" {
		body.UserID = "usr_nonexistent"
	}

	tw.store.mu.Lock()
	token := tw.store.generateToken()
	sess := &Session{
		Token:        token,
		UserID:       body.UserID,
		RefreshToken: tw.store.generateToken(),
		ExpiresAt:    tw.store.clock.Add(defaultSessionExpiry),
	}
	tw.store.sessions[token] = sess
	tw.store.mu.Unlock()

	writeJSON(w, http.StatusCreated, map[string]any{
		"token": token,
	})
}

// handleAdminInjectFault handles POST /fault.
// Configures a fault for a given path pattern.
func (tw *Twin) handleAdminInjectFault(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path       string `json:"path"`
		StatusCode int    `json:"status_code"`
		Body       string `json:"body"`
		LatencyMs  int    `json:"latency_ms"`
		After      int    `json:"after"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if body.Path == "" || body.StatusCode == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path and status_code required"})
		return
	}

	tw.store.mu.Lock()
	tw.store.faults[body.Path] = &EndpointFault{
		StatusCode: body.StatusCode,
		Body:       body.Body,
		Latency:    time.Duration(body.LatencyMs) * time.Millisecond,
		After:      body.After,
		count:      0,
	}
	tw.store.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
}

// handleAdminClearFaults handles DELETE /fault.
// Removes all configured faults.
func (tw *Twin) handleAdminClearFaults(w http.ResponseWriter, _ *http.Request) {
	tw.store.mu.Lock()
	tw.store.faults = make(map[string]*EndpointFault)
	tw.store.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

// handleAdminGetCalls handles GET /calls.
// Returns all recorded API call records.
func (tw *Twin) handleAdminGetCalls(w http.ResponseWriter, _ *http.Request) {
	tw.store.mu.RLock()
	calls := make([]CallRecord, len(tw.store.calls))
	copy(calls, tw.store.calls)
	tw.store.mu.RUnlock()

	writeJSON(w, http.StatusOK, calls)
}

// handleAdminAdvanceClock handles POST /clock/advance.
// Advances the fake clock by the given number of seconds.
func (tw *Twin) handleAdminAdvanceClock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Seconds int `json:"seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	newTime := tw.store.advanceClock(time.Duration(body.Seconds) * time.Second)

	writeJSON(w, http.StatusOK, map[string]string{
		"now": newTime.Format(time.RFC3339),
	})
}
