package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHeartbeatHandler_Handle(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// track callbacks
	var activityCalled bool
	var teamsNeeded []string

	handler.SetActivityCallback(func() {
		activityCalled = true
	})
	handler.SetTeamNeededCallback(func(teamID string) {
		teamsNeeded = append(teamsNeeded, teamID)
	})

	// send heartbeat
	payload := HeartbeatPayload{
		RepoPath:    "/path/to/repo",
		WorkspaceID: "workspace-123",
		TeamIDs:     []string{"team-a", "team-b"},
		Credentials: &HeartbeatCreds{
			Token:     "test-token",
			ServerURL: "https://git.example.com",
			ExpiresAt: time.Now().Add(1 * time.Hour),
		},
		Timestamp: time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle(data)

	// verify activity callback was called
	if !activityCalled {
		t.Error("expected activity callback to be called")
	}

	// verify teams needed callback was called
	if len(teamsNeeded) != 2 {
		t.Errorf("expected 2 teams needed, got %d", len(teamsNeeded))
	}

	// verify activity was recorded
	if handler.GetRepoActivity().Count("/path/to/repo") != 1 {
		t.Error("expected repo activity to be recorded")
	}
	if handler.GetWorkspaceActivity().Count("workspace-123") != 1 {
		t.Error("expected workspace activity to be recorded")
	}
	if handler.GetTeamActivity().Count("team-a") != 1 {
		t.Error("expected team-a activity to be recorded")
	}
	if handler.GetTeamActivity().Count("team-b") != 1 {
		t.Error("expected team-b activity to be recorded")
	}

	// verify credentials were stored
	if !handler.HasValidCredentials() {
		t.Error("expected valid credentials")
	}
	creds, _ := handler.GetCredentials()
	if creds.Token != "test-token" {
		t.Errorf("expected token 'test-token', got %s", creds.Token)
	}
}

func TestHeartbeatHandler_PartialPayload(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// heartbeat with only repo
	payload := HeartbeatPayload{
		RepoPath:  "/path/to/repo",
		Timestamp: time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle(data)

	if handler.GetRepoActivity().Count("/path/to/repo") != 1 {
		t.Error("expected repo activity to be recorded")
	}
	if handler.HasValidCredentials() {
		t.Error("expected no credentials")
	}
}

func TestHeartbeatHandler_ExpiredCredentials(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// send heartbeat with expired credentials
	payload := HeartbeatPayload{
		Credentials: &HeartbeatCreds{
			Token:     "expired-token",
			ServerURL: "https://git.example.com",
			ExpiresAt: time.Now().Add(-1 * time.Hour), // expired
		},
		Timestamp: time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle(data)

	if handler.HasValidCredentials() {
		t.Error("expected credentials to be invalid (expired)")
	}
}

func TestHeartbeatHandler_NoExpirationCredentials(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// send heartbeat with no expiration
	payload := HeartbeatPayload{
		Credentials: &HeartbeatCreds{
			Token:     "no-expire-token",
			ServerURL: "https://git.example.com",
			// ExpiresAt is zero
		},
		Timestamp: time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle(data)

	if !handler.HasValidCredentials() {
		t.Error("expected credentials with no expiration to be valid")
	}
}

func TestHeartbeatHandler_InvalidPayload(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// invalid JSON should not panic
	handler.Handle([]byte("not json"))

	// should still work after invalid payload
	payload := HeartbeatPayload{RepoPath: "/valid"}
	data, _ := json.Marshal(payload)
	handler.Handle(data)

	if handler.GetRepoActivity().Count("/valid") != 1 {
		t.Error("handler should work after invalid payload")
	}
}

func TestHeartbeatHandler_ActivitySummary(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// record some activity
	for i := 0; i < 3; i++ {
		handler.Handle(mustMarshal(HeartbeatPayload{
			RepoPath:    "/repo-a",
			WorkspaceID: "ws-1",
			TeamIDs:     []string{"team-x"},
		}))
	}
	handler.Handle(mustMarshal(HeartbeatPayload{
		RepoPath: "/repo-b",
	}))

	summary := handler.GetActivitySummary()

	if len(summary.Repos) != 2 {
		t.Errorf("expected 2 repos, got %d", len(summary.Repos))
	}
	if len(summary.Workspaces) != 1 {
		t.Errorf("expected 1 workspace, got %d", len(summary.Workspaces))
	}
	if len(summary.Teams) != 1 {
		t.Errorf("expected 1 team, got %d", len(summary.Teams))
	}
}

func mustMarshal(v interface{}) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

func TestHeartbeatHandler_ConcurrentCallbackAccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	var activityCount int
	var mu sync.Mutex

	// concurrent callback setters and handlers to test race conditions
	var wg sync.WaitGroup

	// goroutines setting callbacks
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				handler.SetActivityCallback(func() {
					mu.Lock()
					activityCount++
					mu.Unlock()
				})
				handler.SetTeamNeededCallback(func(teamID string) {
					// no-op
				})
			}
		}(i)
	}

	// goroutines sending heartbeats
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				handler.Handle(mustMarshal(HeartbeatPayload{
					RepoPath:    "/repo",
					WorkspaceID: "ws",
					TeamIDs:     []string{"team"},
				}))
			}
		}(i)
	}

	// should not panic or deadlock
	wg.Wait()

	// activity callback should have been called at least once
	mu.Lock()
	finalCount := activityCount
	mu.Unlock()
	if finalCount == 0 {
		t.Error("expected activity callback to be called at least once")
	}
}

func TestHeartbeatHandler_CallbackDeadlockPrevention(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// set callbacks that try to acquire handler state - should not deadlock
	// because callbacks are called outside the lock
	handler.SetActivityCallback(func() {
		// this would deadlock if called inside the lock
		_ = handler.HasValidCredentials()
		_, _ = handler.GetCredentials()
	})

	handler.SetTeamNeededCallback(func(teamID string) {
		// access handler state from callback
		_ = handler.GetTeamActivity().Count(teamID)
	})

	done := make(chan struct{})
	go func() {
		handler.Handle(mustMarshal(HeartbeatPayload{
			RepoPath: "/repo",
			TeamIDs:  []string{"team-a"},
			Credentials: &HeartbeatCreds{
				Token:     "token",
				ServerURL: "https://example.com",
				ExpiresAt: time.Now().Add(1 * time.Hour),
			},
		}))
		close(done)
	}()

	select {
	case <-done:
		// success - no deadlock
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock detected: Handle() did not complete within 2 seconds")
	}
}

func TestHeartbeatHandler_SetInitialCredentials_Race(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	var wg sync.WaitGroup

	// concurrent initial credential setting
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				handler.SetInitialCredentials(&HeartbeatCreds{
					Token:     "token",
					ServerURL: "https://example.com",
					ExpiresAt: time.Now().Add(1 * time.Hour),
				})
			}
		}(i)
	}

	// concurrent credential reads
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = handler.HasValidCredentials()
				_, _ = handler.GetCredentials()
			}
		}()
	}

	// should not panic
	wg.Wait()
}

// ======
// Version Mismatch Detection Tests
// ======

func TestHeartbeatHandler_VersionMismatch_TriggersCallback(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	var callbackCalled bool
	var receivedCLIVersion string
	var receivedDaemonVersion string

	handler.SetVersionMismatchCallback(func(cliVersion, daemonVersion string) {
		callbackCalled = true
		receivedCLIVersion = cliVersion
		receivedDaemonVersion = daemonVersion
	})

	// send heartbeat with different version
	payload := HeartbeatPayload{
		CLIVersion: "9.9.9", // different from daemon's Version constant
		Timestamp:  time.Now(),
	}
	handler.Handle(mustMarshal(payload))

	if !callbackCalled {
		t.Error("expected version mismatch callback to be called")
	}
	if receivedCLIVersion != "9.9.9" {
		t.Errorf("expected CLI version '9.9.9', got %s", receivedCLIVersion)
	}
	if receivedDaemonVersion != Version() {
		t.Errorf("expected daemon version %s, got %s", Version(), receivedDaemonVersion)
	}
}

func TestHeartbeatHandler_VersionMatch_NoCallback(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	var callbackCalled bool

	handler.SetVersionMismatchCallback(func(cliVersion, daemonVersion string) {
		callbackCalled = true
	})

	// send heartbeat with matching version
	payload := HeartbeatPayload{
		CLIVersion: Version(), // same as daemon's version
		Timestamp:  time.Now(),
	}
	handler.Handle(mustMarshal(payload))

	if callbackCalled {
		t.Error("callback should NOT be called when versions match")
	}
}

func TestHeartbeatHandler_EmptyVersion_NoCallback(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	var callbackCalled bool

	handler.SetVersionMismatchCallback(func(cliVersion, daemonVersion string) {
		callbackCalled = true
	})

	// send heartbeat with no version (backward compat with old CLI)
	payload := HeartbeatPayload{
		RepoPath:  "/some/repo",
		Timestamp: time.Now(),
		// CLIVersion is empty
	}
	handler.Handle(mustMarshal(payload))

	if callbackCalled {
		t.Error("callback should NOT be called when CLI version is empty (backward compat)")
	}
}

func TestHeartbeatHandler_SameSemverDifferentBuild_NoCallback(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	var callbackCalled bool

	handler.SetVersionMismatchCallback(func(cliVersion, daemonVersion string) {
		callbackCalled = true
	})

	// send heartbeat with same semver but different build timestamp
	// this simulates `go install` rebuilding with a new timestamp
	daemonSemver := semverOnly(Version())
	cliVersion := daemonSemver + "+2099-01-01T00:00:00Z"

	payload := HeartbeatPayload{
		CLIVersion: cliVersion,
		Timestamp:  time.Now(),
	}
	handler.Handle(mustMarshal(payload))

	if callbackCalled {
		t.Error("callback should NOT be called when semver matches (only build metadata differs)")
	}
}

// ======
// Auth Token Propagation Tests
// ======

func TestHeartbeatHandler_AuthTokenPropagation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// send heartbeat with auth token
	payload := HeartbeatPayload{
		Credentials: &HeartbeatCreds{
			Token:     "git-pat",
			ServerURL: "https://git.example.com",
			ExpiresAt: time.Now().Add(1 * time.Hour),
			AuthToken: "oauth-access-token",
		},
		Timestamp: time.Now(),
	}
	handler.Handle(mustMarshal(payload))

	// verify auth token is accessible
	authToken := handler.GetAuthToken()
	if authToken != "oauth-access-token" {
		t.Errorf("expected auth token 'oauth-access-token', got %s", authToken)
	}
}

func TestHeartbeatHandler_AuthToken_Empty(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// no credentials set
	authToken := handler.GetAuthToken()
	if authToken != "" {
		t.Errorf("expected empty auth token, got %s", authToken)
	}
}

func TestHeartbeatHandler_AuthTokenUpdate(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// first heartbeat
	payload1 := HeartbeatPayload{
		Credentials: &HeartbeatCreds{
			Token:     "git-pat",
			AuthToken: "token-v1",
			ExpiresAt: time.Now().Add(1 * time.Hour),
		},
	}
	handler.Handle(mustMarshal(payload1))

	if handler.GetAuthToken() != "token-v1" {
		t.Error("expected token-v1")
	}

	// second heartbeat with new token (refresh)
	payload2 := HeartbeatPayload{
		Credentials: &HeartbeatCreds{
			Token:     "git-pat",
			AuthToken: "token-v2",
			ExpiresAt: time.Now().Add(1 * time.Hour),
		},
	}
	handler.Handle(mustMarshal(payload2))

	if handler.GetAuthToken() != "token-v2" {
		t.Error("expected token-v2 after update")
	}
}

// ======
// User Identity Tracking Tests
// ======

func TestHeartbeatHandler_UserInfoPropagation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// send heartbeat with user info
	payload := HeartbeatPayload{
		Credentials: &HeartbeatCreds{
			Token:     "git-pat",
			ExpiresAt: time.Now().Add(1 * time.Hour),
			UserEmail: "user@example.com",
			UserID:    "user_abc123",
		},
		Timestamp: time.Now(),
	}
	handler.Handle(mustMarshal(payload))

	// verify user info is accessible
	user := handler.GetAuthenticatedUser()
	if user == nil {
		t.Fatal("expected authenticated user, got nil")
	}
	if user.Email != "user@example.com" {
		t.Errorf("expected email 'user@example.com', got %s", user.Email)
	}
	if user.ID != "user_abc123" {
		t.Errorf("expected ID 'user_abc123', got %s", user.ID)
	}
}

func TestHeartbeatHandler_NoUser(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// no credentials set
	user := handler.GetAuthenticatedUser()
	if user != nil {
		t.Error("expected nil user when no credentials")
	}
}

func TestHeartbeatHandler_UserWithEmptyEmail(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// credentials without email
	payload := HeartbeatPayload{
		Credentials: &HeartbeatCreds{
			Token:     "git-pat",
			ExpiresAt: time.Now().Add(1 * time.Hour),
			// UserEmail is empty
		},
	}
	handler.Handle(mustMarshal(payload))

	user := handler.GetAuthenticatedUser()
	if user != nil {
		t.Error("expected nil user when email is empty")
	}
}

func TestHeartbeatHandler_UserChange(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// first user
	payload1 := HeartbeatPayload{
		Credentials: &HeartbeatCreds{
			Token:     "git-pat",
			ExpiresAt: time.Now().Add(1 * time.Hour),
			UserEmail: "alice@example.com",
			UserID:    "user_alice",
		},
	}
	handler.Handle(mustMarshal(payload1))

	user1 := handler.GetAuthenticatedUser()
	if user1.Email != "alice@example.com" {
		t.Error("expected alice")
	}

	// second user (re-login as different user)
	payload2 := HeartbeatPayload{
		Credentials: &HeartbeatCreds{
			Token:     "git-pat",
			ExpiresAt: time.Now().Add(1 * time.Hour),
			UserEmail: "bob@example.com",
			UserID:    "user_bob",
		},
	}
	handler.Handle(mustMarshal(payload2))

	user2 := handler.GetAuthenticatedUser()
	if user2.Email != "bob@example.com" {
		t.Error("expected bob after user change")
	}
}

// ======
// Credential Freshness Tests
// ======

func TestHeartbeatHandler_CredentialsFreshness(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	before := time.Now()

	payload := HeartbeatPayload{
		Credentials: &HeartbeatCreds{
			Token:     "git-pat",
			ExpiresAt: time.Now().Add(1 * time.Hour),
		},
	}
	handler.Handle(mustMarshal(payload))

	_, updatedAt := handler.GetCredentials()

	if updatedAt.Before(before) {
		t.Error("credentials time should be after test start")
	}
	if updatedAt.After(time.Now()) {
		t.Error("credentials time should not be in future")
	}
}

func TestHeartbeatHandler_RejectsExpiredInitialCredentials(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// try to set expired initial credentials
	handler.SetInitialCredentials(&HeartbeatCreds{
		Token:     "expired-token",
		ExpiresAt: time.Now().Add(-1 * time.Hour), // already expired
	})

	if handler.HasValidCredentials() {
		t.Error("should reject expired initial credentials")
	}
}

func TestHeartbeatHandler_RejectsExpiredHeartbeatCredentials(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// first set valid credentials
	handler.SetInitialCredentials(&HeartbeatCreds{
		Token:     "valid-token",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	})

	if !handler.HasValidCredentials() {
		t.Fatal("should have valid credentials")
	}

	// heartbeat with expired credentials should be rejected
	payload := HeartbeatPayload{
		Credentials: &HeartbeatCreds{
			Token:     "expired-token",
			ExpiresAt: time.Now().Add(-1 * time.Hour),
		},
	}
	handler.Handle(mustMarshal(payload))

	// should still have the old valid credentials
	creds, _ := handler.GetCredentials()
	if creds.Token != "valid-token" {
		t.Error("expired heartbeat credentials should be rejected, keeping old valid ones")
	}
}

func TestReadHeartbeatsFromPath_DirectoryPath(t *testing.T) {
	dirPath := t.TempDir()
	_, err := readHeartbeatsFromPath(dirPath)
	if err == nil {
		t.Fatal("expected error for directory path, got nil")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("expected 'not a regular file' error, got: %v", err)
	}
}
