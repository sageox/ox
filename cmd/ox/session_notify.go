package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/session"
)

// sessionSignalWait bounds how long the CLI waits for a fire-and-forget
// session lifecycle signal before moving on. The goroutine would not
// survive process exit, so a short bounded wait is what makes "fire and
// forget" actually deliver in a short-lived CLI — while capping the
// latency added to prime/start/abort. The uploaded notification at stop
// remains the authoritative signal; losing a started/aborted signal only
// degrades the /c/ pending-page UX.
const sessionSignalWait = 750 * time.Millisecond

// notifySessionStartedAsync registers a just-started recording with the
// server so its /c/<session_id> conversation link resolves from t=0. The
// recording remains local-first, but the persisted outcome prevents later
// output from presenting a remote URL after authentication/network failure.
func notifySessionStartedAsync(projectRoot string, state *session.RecordingState) {
	if state == nil || state.SessionID == "" {
		return
	}
	attr := loadResolvedAttribution()
	if attr.Session == "" {
		return
	}

	// Don't register a session the coworker never actually used. At recording
	// start (and on subagent primes) there is no user turn yet — registering
	// then is what tells the server "a session started" for the ~majority of
	// recordings that stay header-only, inflating the team's session count with
	// sessions that never happened. Defer: mark the state so the prime retry and
	// the per-turn draft path re-fire this exactly once a real turn exists.
	if !session.HasUserTurn(filepath.Join(state.SessionPath, "raw.jsonl")) {
		state.LifecycleRegistrationState = "deferred"
		if saveErr := session.SaveRecordingState(projectRoot, state); saveErr != nil {
			slog.Debug("session registration deferred save failed", "session_id", state.SessionID, "error", saveErr)
		}
		return
	}
	err := runSessionSignal("started", func(client *api.RepoClient, repoID string) error {
		return client.NotifySessionStarted(api.SessionStartedNotification{
			SessionID:   state.SessionID,
			RepoID:      repoID,
			SessionName: session.GetSessionName(state.SessionPath),
			AgentID:     state.AgentID,
			AgentType:   state.AgentType,
			Branch:      state.Branch,
			StartedAt:   state.StartedAt.Format(time.RFC3339),
		})
	}, projectRoot)
	if err != nil {
		state.LifecycleRegistrationState = "pending"
		state.LifecycleRegistrationError = err.Error()
		slog.Debug("session registration pending", "session_id", state.SessionID, "error", err)
	} else {
		state.LifecycleRegistrationState = "confirmed"
		state.LifecycleRegistrationError = ""
	}
	if saveErr := session.SaveRecordingState(projectRoot, state); saveErr != nil {
		slog.Debug("session registration status save failed", "session_id", state.SessionID, "error", saveErr)
	}
}

// notifySessionAbortedAsync flips a registered recording to "discarded" so
// its /c/ page stops claiming "in progress" and the server drops pending
// PR-link repair tasks. Same fire-and-forget contract as started.
func notifySessionAbortedAsync(projectRoot, sessionID string) {
	if sessionID == "" {
		return
	}
	runSessionSignal("aborted", func(client *api.RepoClient, repoID string) error {
		return client.NotifySessionAborted(api.SessionAbortedNotification{
			SessionID: sessionID,
			RepoID:    repoID,
		})
	}, projectRoot)
}

// runSessionSignal resolves config/auth and runs send in a goroutine, waiting
// at most sessionSignalWait. Its error is deliberately advisory to recording,
// but callers persist it so a dead link never looks server-confirmed.
func runSessionSignal(label string, send func(client *api.RepoClient, repoID string) error, projectRoot string) error {
	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil || cfg == nil || cfg.RepoID == "" {
		return fmt.Errorf("project configuration unavailable")
	}
	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return fmt.Errorf("authentication required")
	}
	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)

	done := make(chan error, 1)
	go func() {
		done <- send(client, cfg.RepoID)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(sessionSignalWait):
		slog.Debug("session signal timed out", "signal", label, "wait_ms", sessionSignalWait.Milliseconds())
		return fmt.Errorf("server confirmation timed out")
	}
}
