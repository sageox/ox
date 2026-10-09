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
	notifySessionStarted(projectRoot, state, func(notification api.SessionStartedNotification) error {
		return runSessionSignal("started", func(client *api.RepoClient, repoID string) error {
			notification.RepoID = repoID
			return client.NotifySessionStarted(notification)
		}, projectRoot)
	})
}

// notifySessionStarted contains the state transition around the network request.
// The injected sender keeps the persistence behavior testable without auth or a
// live server.
func notifySessionStarted(projectRoot string, state *session.RecordingState, send func(api.SessionStartedNotification) error) {
	if state == nil || state.SessionID == "" {
		return
	}

	// Don't register a session the coworker never actually used. At recording
	// start (and on subagent primes) there is no user turn yet — registering
	// then is what tells the server "a session started" for the ~majority of
	// recordings that stay header-only, inflating the team's session count with
	// sessions that never happened. Defer: mark the state so the prime retry and
	// the per-turn draft path re-fire this exactly once a real turn exists.
	if !session.HasUserTurn(filepath.Join(state.SessionPath, "raw.jsonl")) {
		persistSessionRegistrationState(state, "deferred", "")
		return
	}

	// session_publishing: manual promises that nothing about this session
	// reaches the cloud until the user explicitly uploads it. This POST would
	// send session_id, repo_id, session_name, agent id/type, and the BRANCH
	// NAME — exactly the kind of implicit publish manual mode exists to
	// prevent. Deliberately checked AFTER the no-user-turn branch above and
	// left at "deferred" (not flipped to "pending"/"confirmed"): every retry
	// site (prime, doctor, the per-turn draft path) re-runs this same check,
	// so switching back to auto mid-session, or an explicit later upload,
	// registers normally on the next attempt rather than being wedged.
	if config.GetSessionPublishing(projectRoot) == config.SessionPublishingManual {
		slog.Debug("session registration suppressed: session_publishing is manual", "session_id", state.SessionID)
		return
	}
	err := send(api.SessionStartedNotification{
		SessionID:   state.SessionID,
		SessionName: session.GetSessionName(state.SessionPath),
		AgentID:     state.AgentID,
		AgentType:   state.AgentType,
		Branch:      state.Branch,
		StartedAt:   state.StartedAt.Format(time.RFC3339),
	})
	if err != nil {
		slog.Debug("session registration pending", "session_id", state.SessionID, "error", err)
		persistSessionRegistrationState(state, "pending", err.Error())
	} else {
		persistSessionRegistrationState(state, "confirmed", "")
	}
}

// persistSessionRegistrationState merges the notification outcome into the
// latest durable state. A network response may arrive after the recording was
// paused, checkpointed, stopped, removed, or replaced by a new session using
// the same agent ID, so the caller's pre-request snapshot must never be saved
// wholesale.
func persistSessionRegistrationState(state *session.RecordingState, status, statusErr string) {
	// Current callers inspect this snapshot after registration, so retain the
	// existing in-memory outcome while treating durable state as authoritative
	// for every other field.
	state.LifecycleRegistrationState = status
	state.LifecycleRegistrationError = statusErr
	persistLifecycleRegistration(state)
}

// persistLifecycleRegistration writes only the registration outcome under the
// state lock. The state identity prevents a late network response from
// recreating a stopped recording or updating its replacement.
func persistLifecycleRegistration(state *session.RecordingState) {
	err := session.UpdateRecordingStateAt(state.SessionPath, state.SessionID, func(current *session.RecordingState) {
		current.LifecycleRegistrationState = state.LifecycleRegistrationState
		current.LifecycleRegistrationError = state.LifecycleRegistrationError
	})
	if err != nil {
		slog.Debug("session registration status save failed", "session_id", state.SessionID, "error", err)
	}
}

// notifySessionAbortedAsync flips a registered recording to "discarded" so
// its /c/ page stops claiming "in progress" and the server drops pending
// PR-link repair tasks. Same fire-and-forget contract as started.
// everRegistered reports whether the server was ever told this session exists.
// Manual publishing suppresses start-registration, so a purely manual session is
// unknown to the server and an abort signal would be the FIRST thing it hears
// about it — a leak of exactly the kind manual mode promises to prevent.
//
// A session that DID register (started under auto, flipped to manual later) must
// still be tombstoned: leaving a stale "in progress" /c/ page up, for a session
// the user explicitly discarded, is a worse privacy outcome than the two opaque
// ids the abort call carries.
func notifySessionAbortedAsync(projectRoot, sessionID string, everRegistered bool) {
	if sessionID == "" {
		return
	}
	if !everRegistered {
		return
	}
	_ = runSessionSignal("aborted", func(client *api.RepoClient, repoID string) error { // fire-and-forget
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
