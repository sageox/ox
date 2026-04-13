package twinapi

import (
	"testing"
	"time"
)

// UserFixture holds all tokens and IDs for a fully authenticated user.
type UserFixture struct {
	UserID       string
	Email        string
	Name         string
	SessionToken string
	RefreshToken string
	JWT          string
}

// WithAuthenticatedUser creates a user, session, and JWT in one call.
// Returns a fixture with all credentials needed for authenticated API calls.
func (tw *Twin) WithAuthenticatedUser(email, name string) *UserFixture {
	userID := tw.store.generateID("usr_")
	user := tw.CreateUser(userID, email, name)

	sess := tw.CreateSession(user.ID)
	jwt := tw.store.mintJWT(user, defaultJWTExpiry)

	return &UserFixture{
		UserID:       user.ID,
		Email:        email,
		Name:         name,
		SessionToken: sess.Token,
		RefreshToken: sess.RefreshToken,
		JWT:          jwt,
	}
}

// CreateUser adds a user to the store.
func (tw *Twin) CreateUser(id, email, name string) *User {
	u := &User{
		ID:    id,
		Email: email,
		Name:  name,
		Tier:  "free",
	}

	tw.store.mu.Lock()
	tw.store.users[id] = u
	tw.store.mu.Unlock()

	return u
}

// CreateSession creates a session for the given user.
func (tw *Twin) CreateSession(userID string) *Session {
	tw.store.mu.Lock()
	defer tw.store.mu.Unlock()

	token := tw.store.generateToken()
	refreshToken := tw.store.generateToken()
	sess := &Session{
		Token:        token,
		UserID:       userID,
		RefreshToken: refreshToken,
		ExpiresAt:    tw.store.clock.Add(defaultSessionExpiry),
	}
	tw.store.sessions[token] = sess
	return sess
}

// CreateOrphanedSession creates a session pointing to a user ID that does not
// exist in the store. Useful for testing JWT exchange error paths.
func (tw *Twin) CreateOrphanedSession(fakeUserID string) *Session {
	tw.store.mu.Lock()
	defer tw.store.mu.Unlock()

	token := tw.store.generateToken()
	sess := &Session{
		Token:        token,
		UserID:       fakeUserID,
		RefreshToken: tw.store.generateToken(),
		ExpiresAt:    tw.store.clock.Add(defaultSessionExpiry),
	}
	tw.store.sessions[token] = sess
	return sess
}

// AdvanceTime moves the fake clock forward by d.
func (tw *Twin) AdvanceTime(d time.Duration) {
	tw.store.advanceClock(d)
}

// InjectFault configures a fault for the given path. All requests to that path
// will return the specified status code.
func (tw *Twin) InjectFault(path string, statusCode int) {
	tw.store.mu.Lock()
	tw.store.faults[path] = &EndpointFault{
		StatusCode: statusCode,
	}
	tw.store.mu.Unlock()
}

// InjectFaultAfter configures a fault that only triggers after n successful calls.
func (tw *Twin) InjectFaultAfter(path string, statusCode int, after int) {
	tw.store.mu.Lock()
	tw.store.faults[path] = &EndpointFault{
		StatusCode: statusCode,
		After:      after,
	}
	tw.store.mu.Unlock()
}

// ClearFaults removes all configured faults.
func (tw *Twin) ClearFaults() {
	tw.store.mu.Lock()
	tw.store.faults = make(map[string]*EndpointFault)
	tw.store.mu.Unlock()
}

// Calls returns a copy of all recorded API call records.
func (tw *Twin) Calls() []CallRecord {
	tw.store.mu.RLock()
	defer tw.store.mu.RUnlock()
	calls := make([]CallRecord, len(tw.store.calls))
	copy(calls, tw.store.calls)
	return calls
}

// CallCount returns how many times the given method+path was called.
func (tw *Twin) CallCount(method, path string) int {
	tw.store.mu.RLock()
	defer tw.store.mu.RUnlock()
	count := 0
	for _, c := range tw.store.calls {
		if c.Method == method && c.Path == path {
			count++
		}
	}
	return count
}

// AssertCalled fails the test if the given method+path was never called.
func (tw *Twin) AssertCalled(t testing.TB, method, path string) {
	t.Helper()
	if tw.CallCount(method, path) == 0 {
		t.Errorf("expected %s %s to be called, but it was not", method, path)
	}
}

// AssertNotCalled fails the test if the given method+path was called.
func (tw *Twin) AssertNotCalled(t testing.TB, method, path string) {
	t.Helper()
	if count := tw.CallCount(method, path); count > 0 {
		t.Errorf("expected %s %s not to be called, but it was called %d time(s)", method, path, count)
	}
}
