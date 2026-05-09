package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
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
	handler.Handle("", data)

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
	handler.Handle("", data)

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
	handler.Handle("", data)

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
	handler.Handle("", data)

	if !handler.HasValidCredentials() {
		t.Error("expected credentials with no expiration to be valid")
	}
}

func TestHeartbeatHandler_InvalidPayload(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// invalid JSON should not panic
	handler.Handle("", []byte("not json"))

	// should still work after invalid payload
	payload := HeartbeatPayload{RepoPath: "/valid"}
	data, _ := json.Marshal(payload)
	handler.Handle("", data)

	if handler.GetRepoActivity().Count("/valid") != 1 {
		t.Error("handler should work after invalid payload")
	}
}

func TestHeartbeatHandler_ActivitySummary(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// record some activity
	for i := 0; i < 3; i++ {
		handler.Handle("", mustMarshal(HeartbeatPayload{
			RepoPath:    "/repo-a",
			WorkspaceID: "ws-1",
			TeamIDs:     []string{"team-x"},
		}))
	}
	handler.Handle("", mustMarshal(HeartbeatPayload{
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
				handler.Handle("", mustMarshal(HeartbeatPayload{
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
		handler.Handle("", mustMarshal(HeartbeatPayload{
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
	handler.Handle("", mustMarshal(payload))

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
	handler.Handle("", mustMarshal(payload))

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
	handler.Handle("", mustMarshal(payload))

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
	handler.Handle("", mustMarshal(payload))

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
	handler.Handle("", mustMarshal(payload))

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
	handler.Handle("", mustMarshal(payload1))

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
	handler.Handle("", mustMarshal(payload2))

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
	handler.Handle("", mustMarshal(payload))

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
	handler.Handle("", mustMarshal(payload))

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
	handler.Handle("", mustMarshal(payload1))

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
	handler.Handle("", mustMarshal(payload2))

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
	handler.Handle("", mustMarshal(payload))

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
	handler.Handle("", mustMarshal(payload))

	// should still have the old valid credentials
	creds, _ := handler.GetCredentials()
	if creds.Token != "valid-token" {
		t.Error("expired heartbeat credentials should be rejected, keeping old valid ones")
	}
}

func TestHeartbeatHandler_ContextTokensTracking(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// send heartbeat with context tokens
	handler.Handle("", mustMarshal(HeartbeatPayload{
		AgentID:       "Oxa7b3",
		ContextTokens: 1250,
		Timestamp:     time.Now(),
	}))

	stats := handler.GetAgentContextStats("Oxa7b3")
	if stats.ContextTokens != 1250 {
		t.Errorf("expected 1250 context tokens, got %d", stats.ContextTokens)
	}
	if stats.CommandCount != 1 {
		t.Errorf("expected 1 command, got %d", stats.CommandCount)
	}

	// send another heartbeat — should accumulate
	handler.Handle("", mustMarshal(HeartbeatPayload{
		AgentID:       "Oxa7b3",
		ContextTokens: 750,
		Timestamp:     time.Now(),
	}))

	stats = handler.GetAgentContextStats("Oxa7b3")
	if stats.ContextTokens != 2000 {
		t.Errorf("expected 2000 cumulative context tokens, got %d", stats.ContextTokens)
	}
	if stats.CommandCount != 2 {
		t.Errorf("expected 2 commands, got %d", stats.CommandCount)
	}

	// different agent should have zero
	stats = handler.GetAgentContextStats("OxZ9k2")
	if stats.ContextTokens != 0 {
		t.Errorf("expected 0 for unknown agent, got %d", stats.ContextTokens)
	}
}

// TestHeartbeatHandler_ContextTokensBySource verifies the daemon
// accumulates per-source cumulative tokens via the open ContextTokensBySource
// map (extensible for future knowledge bubbles), and falls back to crediting
// the rolled-up total to "sageox" when older clients send only ContextTokens.
//
// Failure prevented: the daemon silently drops the per-source data, making
// it impossible to judge SageOx independently from authored content; OR a
// future knowledge bubble's tokens get dropped because the daemon hard-codes
// the known source list.
func TestHeartbeatHandler_ContextTokensBySource(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// New-style heartbeat with explicit per-source split, including a
	// hypothetical future bubble ("user") to prove the open schema works.
	handler.Handle("", mustMarshal(HeartbeatPayload{
		AgentID:       "Oxa7b3",
		ContextTokens: 1000,
		ContextTokensBySource: map[string]int64{
			"sageox":  300,
			"team":    500,
			"project": 200,
		},
		Timestamp: time.Now(),
	}))

	stats := handler.GetAgentContextStats("Oxa7b3")
	if stats.ContextTokens != 1000 {
		t.Errorf("expected 1000 total, got %d", stats.ContextTokens)
	}
	if stats.ContextTokensBySource["sageox"] != 300 {
		t.Errorf("expected 300 sageox, got %d", stats.ContextTokensBySource["sageox"])
	}
	if stats.ContextTokensBySource["team"] != 500 {
		t.Errorf("expected 500 team, got %d", stats.ContextTokensBySource["team"])
	}
	if stats.ContextTokensBySource["project"] != 200 {
		t.Errorf("expected 200 project, got %d", stats.ContextTokensBySource["project"])
	}

	// Second heartbeat introduces a NEW source ("user") on the same agent —
	// future knowledge bubble. Daemon must accept it without code changes.
	handler.Handle("", mustMarshal(HeartbeatPayload{
		AgentID:       "Oxa7b3",
		ContextTokens: 500,
		ContextTokensBySource: map[string]int64{
			"sageox": 100,
			"team":   300,
			"user":   100, // unknown to today's display layer; daemon must still aggregate
		},
		Timestamp: time.Now(),
	}))
	stats = handler.GetAgentContextStats("Oxa7b3")
	if stats.ContextTokensBySource["sageox"] != 400 {
		t.Errorf("expected 400 sageox cumulative, got %d", stats.ContextTokensBySource["sageox"])
	}
	if stats.ContextTokensBySource["team"] != 800 {
		t.Errorf("expected 800 team cumulative, got %d", stats.ContextTokensBySource["team"])
	}
	if stats.ContextTokensBySource["user"] != 100 {
		t.Errorf("expected 100 user (new bubble), got %d — daemon must accept unknown sources",
			stats.ContextTokensBySource["user"])
	}

	// Legacy heartbeat from older client: only ContextTokens, no map.
	// Should be credited to sageox so we don't silently drop the signal.
	handler.Handle("", mustMarshal(HeartbeatPayload{
		AgentID:       "OxLegacy",
		ContextTokens: 750,
		Timestamp:     time.Now(),
	}))
	stats = handler.GetAgentContextStats("OxLegacy")
	if stats.ContextTokens != 750 {
		t.Errorf("expected legacy total 750, got %d", stats.ContextTokens)
	}
	if stats.ContextTokensBySource["sageox"] != 750 {
		t.Errorf("expected legacy fallback to credit 750 to sageox, got %d", stats.ContextTokensBySource["sageox"])
	}
	if stats.ContextTokensBySource["team"] != 0 || stats.ContextTokensBySource["project"] != 0 {
		t.Errorf("legacy heartbeat should not populate team/project, got team=%d project=%d",
			stats.ContextTokensBySource["team"], stats.ContextTokensBySource["project"])
	}
}

// TestHeartbeatHandler_ContextTokensByKBType verifies that token counts
// surface a per-kb_type split alongside the per-source split, so dashboards
// can answer "personal vs team load" without dereferencing each source.
//
// Failure prevented: the daemon drops the kb_type signal (or panics on
// unrecognized values), making it impossible to chart load by knowledge-bubble
// kind once the kb migration completes.
func TestHeartbeatHandler_ContextTokensByKBType_Explicit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// New-style heartbeat with explicit per-kb_type split, including a
	// "personal" bubble — the field the new kb migration unlocks.
	handler.Handle("", mustMarshal(HeartbeatPayload{
		AgentID:       "OxKB1",
		ContextTokens: 1000,
		ContextTokensBySource: map[string]int64{
			"team":   600,
			"sageox": 100,
			"bub-1":  300,
		},
		ContextTokensByKBType: map[string]int64{
			"personal": 300,
			"team":     600,
			"unknown":  100,
		},
		Timestamp: time.Now(),
	}))

	stats := handler.GetAgentContextStats("OxKB1")
	if stats.ContextTokensByKBType["personal"] != 300 {
		t.Errorf("expected 300 personal, got %d", stats.ContextTokensByKBType["personal"])
	}
	if stats.ContextTokensByKBType["team"] != 600 {
		t.Errorf("expected 600 team kb_type, got %d", stats.ContextTokensByKBType["team"])
	}
	if stats.ContextTokensByKBType["unknown"] != 100 {
		t.Errorf("expected 100 unknown, got %d", stats.ContextTokensByKBType["unknown"])
	}
}

// TestHeartbeatHandler_ContextTokensByKBType_LegacySourceFallback verifies
// that when older clients send only a per-source split, the daemon
// synthesizes a kb_type bucket from each source: "team" → "team",
// "project" (legacy ledger) → "repo", and unmapped sources → "unknown".
//
// Failure prevented: dashboards built on kb_type are blank for every
// existing client that hasn't been updated to declare kb_type.
func TestHeartbeatHandler_ContextTokensByKBType_LegacySourceFallback(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	handler.Handle("", mustMarshal(HeartbeatPayload{
		AgentID:       "OxLegacy2",
		ContextTokens: 900,
		ContextTokensBySource: map[string]int64{
			"sageox":  200, // tool overhead → unknown
			"team":    400, // legacy team-context → team
			"project": 300, // legacy ledger → repo
		},
		Timestamp: time.Now(),
	}))

	stats := handler.GetAgentContextStats("OxLegacy2")
	if stats.ContextTokensByKBType["team"] != 400 {
		t.Errorf("expected 400 team (from legacy 'team' source), got %d", stats.ContextTokensByKBType["team"])
	}
	if stats.ContextTokensByKBType["repo"] != 300 {
		t.Errorf("expected 300 repo (from legacy 'project' source), got %d", stats.ContextTokensByKBType["repo"])
	}
	if stats.ContextTokensByKBType["unknown"] != 200 {
		t.Errorf("expected 200 unknown (from 'sageox' tool overhead), got %d", stats.ContextTokensByKBType["unknown"])
	}
	// no personal/profile/custom expected without explicit declaration
	if stats.ContextTokensByKBType["personal"] != 0 || stats.ContextTokensByKBType["profile"] != 0 {
		t.Errorf("legacy source fallback should not synthesize personal/profile, got personal=%d profile=%d",
			stats.ContextTokensByKBType["personal"], stats.ContextTokensByKBType["profile"])
	}
}

// TestHeartbeatHandler_ContextTokensByKBType_UnknownNormalizes verifies
// that an unrecognized kb_type slug aggregates under "unknown" rather
// than panicking or being passed through verbatim.
//
// Failure prevented: a future server-only kb_type leaks into client
// dashboards as a phantom bucket, or worse, the daemon panics on an
// unexpected string.
func TestHeartbeatHandler_ContextTokensByKBType_UnknownNormalizes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	handler.Handle("", mustMarshal(HeartbeatPayload{
		AgentID:       "OxKBFuture",
		ContextTokens: 500,
		ContextTokensByKBType: map[string]int64{
			"team":                    200,
			"future-kind-from-server": 200, // not in api.KBType today
			"":                        100, // empty-string kb_type also normalizes
		},
		Timestamp: time.Now(),
	}))

	stats := handler.GetAgentContextStats("OxKBFuture")
	if stats.ContextTokensByKBType["team"] != 200 {
		t.Errorf("expected 200 team, got %d", stats.ContextTokensByKBType["team"])
	}
	if stats.ContextTokensByKBType["unknown"] != 300 {
		t.Errorf("expected 300 unknown (200 future + 100 empty), got %d", stats.ContextTokensByKBType["unknown"])
	}
	if _, ok := stats.ContextTokensByKBType["future-kind-from-server"]; ok {
		t.Error("unrecognized kb_type leaked into stats verbatim — must collapse to 'unknown'")
	}
}

// TestHeartbeatHandler_ContextTokensByKBType_NoSplitFallback verifies the
// oldest-CLI path: a heartbeat with neither per-source nor per-kb_type
// split still produces a kb_type bucket so the signal survives.
//
// Failure prevented: legacy heartbeats silently drop out of any
// kb_type-keyed dashboard, hiding genuine load.
func TestHeartbeatHandler_ContextTokensByKBType_NoSplitFallback(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	handler.Handle("", mustMarshal(HeartbeatPayload{
		AgentID:       "OxOldest",
		ContextTokens: 750,
		Timestamp:     time.Now(),
	}))

	stats := handler.GetAgentContextStats("OxOldest")
	if stats.ContextTokens != 750 {
		t.Errorf("expected 750 total, got %d", stats.ContextTokens)
	}
	if stats.ContextTokensByKBType["unknown"] != 750 {
		t.Errorf("expected 750 credited to unknown for oldest CLI, got %d", stats.ContextTokensByKBType["unknown"])
	}
}

// TestHeartbeatHandler_ContextTokensByKBType_JSONShape pins the JSON
// serialization of the heartbeat payload and AgentContextStats so
// downstream consumers (ipc.go InstanceInfo, status display, telemetry)
// don't silently break when the schema evolves. Adding a kb_type field
// must be additive — old keys keep their names, casing, and omitempty
// semantics.
//
// Failure prevented: a refactor renames kb_type to kbType (or drops
// omitempty), and every consumer parsing this JSON breaks at once
// without a test failure to catch it.
func TestHeartbeatHandler_ContextTokensByKBType_JSONShape(t *testing.T) {
	payload := HeartbeatPayload{
		AgentID:               "OxShape",
		ContextTokens:         100,
		ContextTokensBySource: map[string]int64{"team": 100},
		ContextTokensByKBType: map[string]int64{"team": 100},
		Timestamp:             time.Time{}, // zero — avoid timezone flakiness
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"agent_id":"OxShape"`,
		`"context_tokens":100`,
		`"context_tokens_by_source":{"team":100}`,
		`"context_tokens_by_kb_type":{"team":100}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("payload JSON missing %q\n got=%s", want, got)
		}
	}

	// Round-trip through AgentContextStats too — that's the shape
	// surfaced over IPC to InstanceInfo.
	stats := AgentContextStats{
		ContextTokens:         100,
		ContextTokensBySource: map[string]int64{"team": 100},
		ContextTokensByKBType: map[string]int64{"team": 100},
		CommandCount:          1,
	}
	statsData, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("marshal stats: %v", err)
	}
	statsGot := string(statsData)
	for _, want := range []string{
		`"context_tokens":100`,
		`"context_tokens_by_source":{"team":100}`,
		`"context_tokens_by_kb_type":{"team":100}`,
		`"command_count":1`,
	} {
		if !strings.Contains(statsGot, want) {
			t.Errorf("stats JSON missing %q\n got=%s", want, statsGot)
		}
	}

	// Empty maps must omitempty — older clients should not see a
	// dangling `"context_tokens_by_kb_type":{}` field.
	emptyStats := AgentContextStats{ContextTokens: 5, CommandCount: 1}
	emptyData, _ := json.Marshal(emptyStats)
	if strings.Contains(string(emptyData), "context_tokens_by_kb_type") {
		t.Errorf("empty stats should omit context_tokens_by_kb_type, got %s", string(emptyData))
	}
}

func TestHeartbeatHandler_ContextTokensZeroIgnored(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// heartbeat without context tokens should not create entry
	handler.Handle("", mustMarshal(HeartbeatPayload{
		AgentID:   "Oxa7b3",
		Timestamp: time.Now(),
	}))

	stats := handler.GetAgentContextStats("Oxa7b3")
	if stats.ContextTokens != 0 {
		t.Errorf("expected 0 context tokens for heartbeat without context, got %d", stats.ContextTokens)
	}
	if stats.CommandCount != 0 {
		t.Errorf("expected 0 commands for heartbeat without context, got %d", stats.CommandCount)
	}
}

func TestHeartbeatHandler_CallerTracking(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// initially empty
	if got := handler.LastCallerPath(); got != "" {
		t.Errorf("expected empty caller path initially, got %q", got)
	}
	if got := handler.GetCallers(); len(got) != 0 {
		t.Errorf("expected no callers initially, got %d", len(got))
	}

	// heartbeat without callerID doesn't register a caller
	handler.Handle("", mustMarshal(HeartbeatPayload{
		RepoPath:  "/some/repo",
		Timestamp: time.Now(),
	}))
	if got := handler.GetCallers(); len(got) != 0 {
		t.Errorf("expected no callers after empty callerID, got %d", len(got))
	}

	// heartbeat from stuttgart
	handler.Handle("ab12cd34", mustMarshal(HeartbeatPayload{
		CallerPath: "/Users/dev/conductor/workspaces/ox/stuttgart-v1",
		AgentID:    "OxA1b2",
		Timestamp:  time.Now(),
	}))
	callers := handler.GetCallers()
	if len(callers) != 1 {
		t.Fatalf("expected 1 caller, got %d", len(callers))
	}
	if callers[0].Path != "/Users/dev/conductor/workspaces/ox/stuttgart-v1" {
		t.Errorf("expected stuttgart path, got %q", callers[0].Path)
	}
	if callers[0].ID != "ab12cd34" {
		t.Errorf("expected caller ID ab12cd34, got %q", callers[0].ID)
	}
	if callers[0].AgentID != "OxA1b2" {
		t.Errorf("expected agent ID OxA1b2, got %q", callers[0].AgentID)
	}

	// heartbeat from zagreb (different clone, different callerID)
	handler.Handle("ef56gh78", mustMarshal(HeartbeatPayload{
		CallerPath: "/Users/dev/conductor/workspaces/ox/zagreb-v2",
		Timestamp:  time.Now(),
	}))
	callers = handler.GetCallers()
	if len(callers) != 2 {
		t.Fatalf("expected 2 callers, got %d", len(callers))
	}

	// LastCallerPath returns the most recent
	if got := handler.LastCallerPath(); got != "/Users/dev/conductor/workspaces/ox/zagreb-v2" {
		t.Errorf("expected zagreb as most recent, got %q", got)
	}

	// stuttgart sends another heartbeat — it should update, not duplicate
	handler.Handle("ab12cd34", mustMarshal(HeartbeatPayload{
		CallerPath: "/Users/dev/conductor/workspaces/ox/stuttgart-v1",
		Timestamp:  time.Now(),
	}))
	callers = handler.GetCallers()
	if len(callers) != 2 {
		t.Fatalf("expected still 2 callers after update, got %d", len(callers))
	}

	// now stuttgart is most recent
	if got := handler.LastCallerPath(); got != "/Users/dev/conductor/workspaces/ox/stuttgart-v1" {
		t.Errorf("expected stuttgart as most recent after update, got %q", got)
	}
}

// ======
// CleanupStaleAgents Tests
// ======

func TestHeartbeatHandler_CleanupStaleAgents(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// register three agents with context tokens and metadata
	for _, id := range []string{"Ox-a", "Ox-b", "Ox-c"} {
		handler.Handle("", mustMarshal(HeartbeatPayload{
			AgentID:       id,
			ContextTokens: 500,
			AgentType:     "claude-code",
			ParentPID:     1234,
			Timestamp:     time.Now(),
		}))
	}

	// verify all three have context stats
	for _, id := range []string{"Ox-a", "Ox-b", "Ox-c"} {
		stats := handler.GetAgentContextStats(id)
		if stats.ContextTokens != 500 {
			t.Errorf("expected 500 tokens for %s, got %d", id, stats.ContextTokens)
		}
	}

	// cleanup, keeping only Ox-b active
	handler.CleanupStaleAgents([]string{"Ox-b"})

	// Ox-b should still have stats
	stats := handler.GetAgentContextStats("Ox-b")
	if stats.ContextTokens != 500 {
		t.Errorf("expected 500 tokens for active agent Ox-b, got %d", stats.ContextTokens)
	}

	// Ox-a and Ox-c should be cleaned up
	for _, id := range []string{"Ox-a", "Ox-c"} {
		stats := handler.GetAgentContextStats(id)
		if stats.ContextTokens != 0 {
			t.Errorf("expected 0 tokens for stale agent %s, got %d", id, stats.ContextTokens)
		}
		if stats.CommandCount != 0 {
			t.Errorf("expected 0 commands for stale agent %s, got %d", id, stats.CommandCount)
		}
	}

	// metadata maps should also be cleaned
	for _, id := range []string{"Ox-a", "Ox-c"} {
		if got := handler.GetAgentType(id); got != "" {
			t.Errorf("expected empty agent type for stale %s, got %q", id, got)
		}
		if got := handler.GetAgentPID(id); got != 0 {
			t.Errorf("expected 0 PID for stale %s, got %d", id, got)
		}
	}

	// Ox-b metadata should survive
	if got := handler.GetAgentType("Ox-b"); got != "claude-code" {
		t.Errorf("expected agent type claude-code for active Ox-b, got %q", got)
	}
}

func TestHeartbeatHandler_CleanupStaleAgents_EmptyActiveList(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	handler.Handle("", mustMarshal(HeartbeatPayload{
		AgentID:       "Ox-x",
		ContextTokens: 100,
		AgentType:     "test",
		Timestamp:     time.Now(),
	}))

	// cleanup with empty list should remove all
	handler.CleanupStaleAgents(nil)

	stats := handler.GetAgentContextStats("Ox-x")
	if stats.ContextTokens != 0 {
		t.Errorf("expected 0 tokens after cleanup with empty list, got %d", stats.ContextTokens)
	}
}

func TestHeartbeatHandler_CleanupStaleAgents_ConcurrentSafe(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	var wg sync.WaitGroup

	// concurrent heartbeats
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				handler.Handle("", mustMarshal(HeartbeatPayload{
					AgentID:       "Ox-concurrent",
					ContextTokens: 10,
					Timestamp:     time.Now(),
				}))
			}
		}(i)
	}

	// concurrent cleanups
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				handler.CleanupStaleAgents([]string{"Ox-concurrent"})
			}
		}()
	}

	wg.Wait() // should not panic or deadlock
}

// ======
// Caller Eviction Tests
// ======

func TestHeartbeatHandler_CallerEviction(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)

	// fill callers up to maxCallers+1 to trigger eviction
	// first caller is the oldest
	handler.Handle("oldest-caller", mustMarshal(HeartbeatPayload{
		CallerPath: "/workspace/oldest",
		Timestamp:  time.Now(),
	}))
	time.Sleep(time.Millisecond) // ensure distinct timestamps

	// fill remaining slots
	for i := 1; i <= maxCallers; i++ {
		callerID := "caller-" + strconv.Itoa(i)
		handler.Handle(callerID, mustMarshal(HeartbeatPayload{
			CallerPath: "/workspace/" + callerID,
			Timestamp:  time.Now(),
		}))
	}

	callers := handler.GetCallers()

	// should have exactly maxCallers (oldest evicted)
	if len(callers) != maxCallers {
		t.Fatalf("expected %d callers after eviction, got %d", maxCallers, len(callers))
	}

	// oldest-caller should have been evicted
	for _, c := range callers {
		if c.ID == "oldest-caller" {
			t.Fatal("expected oldest-caller to be evicted")
		}
	}
}

func TestHeartbeatHandler_CredentialPersistenceRoundTrip(t *testing.T) {
	// round-trip: Save → Load → SetInitialCredentials → HasValidCredentials
	// mirrors the daemon.go:787-796 cold-start pattern
	tmpDir := t.TempDir()
	prev := gitserver.TestSetConfigDirOverride(tmpDir)
	prevForce := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prev)
		gitserver.TestSetForceFileStorage(prevForce)
	})

	testEndpoint := "https://test.sageox.ai"
	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second) // truncate for JSON

	// step 1: save credentials to disk (CLI does this)
	savedCreds := gitserver.GitCredentials{
		Token:     "glpat-test-token-abc123",
		ServerURL: "https://git.test.sageox.ai",
		ExpiresAt: expiresAt,
	}
	err := gitserver.SaveCredentialsForEndpoint(testEndpoint, savedCreds)
	if err != nil {
		t.Fatalf("SaveCredentialsForEndpoint: %v", err)
	}

	// step 2: load credentials from disk (daemon cold-start does this)
	loaded, err := gitserver.LoadCredentialsForEndpoint(testEndpoint)
	if err != nil {
		t.Fatalf("LoadCredentialsForEndpoint: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded credentials should not be nil")
	}

	// step 3: convert to HeartbeatCreds (same as daemon.go:788-792)
	hbCreds := &HeartbeatCreds{
		Token:     loaded.Token,
		ServerURL: loaded.ServerURL,
		ExpiresAt: loaded.ExpiresAt,
	}

	// step 4: set on a fresh handler
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHeartbeatHandler(logger)
	handler.SetInitialCredentials(hbCreds)

	// step 5: verify
	if !handler.HasValidCredentials() {
		t.Error("handler should have valid credentials after round-trip")
	}
	retrieved, _ := handler.GetCredentials()
	if retrieved == nil {
		t.Fatal("GetCredentials should return non-nil after SetInitialCredentials")
	}
	if retrieved.Token != savedCreds.Token {
		t.Errorf("token mismatch: got %q, want %q", retrieved.Token, savedCreds.Token)
	}
	if retrieved.ServerURL != savedCreds.ServerURL {
		t.Errorf("serverURL mismatch: got %q, want %q", retrieved.ServerURL, savedCreds.ServerURL)
	}
	if !retrieved.ExpiresAt.Equal(expiresAt) {
		t.Errorf("expiresAt mismatch: got %v, want %v", retrieved.ExpiresAt, expiresAt)
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
