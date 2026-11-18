package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/flags"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
)

// Real-time session streaming (ADR-247, shadow phase) as seen from the CLI.
//
// Hooks never wait on the network: start only writes a local pending state and
// the header line, capture appends committed raw.jsonl bytes to a local spool,
// and the daemon opens, drains and closes the stream in the background. Only
// stop (bounded by livesession.CloseBudget) and abort touch the network
// themselves. Every failure here degrades to "not streaming" and is logged on one
// line; none may fail a hook, a start, a stop or an abort, and none may touch the
// v1 Ledger push. The one exception is deliberate: a session deleted on SageOx
// while it streamed is held on this machine and never published
// (liveDeletedOnServer). Hooks stay silent about it; stop says so once.

const (
	// liveSettingsFetchBound caps the one-off CLI settings fetch when no cache
	// exists yet. Short on purpose: a session start must not wait on the server,
	// and a miss only means this session does not stream.
	liveSettingsFetchBound = 1500 * time.Millisecond
	// liveLocalBound caps local-only work (state + spool writes) in a hook.
	liveLocalBound = 1 * time.Second
	// liveEndCloseBudget is how long the SessionEnd hook gives the close; the
	// daemon finishes a stream left closing.
	liveEndCloseBudget = 1500 * time.Millisecond
)

func liveConfig(projectRoot string) livesession.Config {
	return livesession.Config{Endpoint: endpoint.GetForProject(projectRoot)}
}

// liveStreamingEnabled is C1: the server rollout flag is on and the project is
// not in manual publishing mode.
func liveStreamingEnabled(projectRoot string) bool {
	if config.GetSessionPublishing(projectRoot) == config.SessionPublishingManual {
		return false
	}
	ep := endpoint.GetForProject(projectRoot)
	if ep == "" {
		return false
	}
	cached, _ := flags.LoadCachedSettings(ep)
	if cached == nil || time.Since(cached.FetchedAt) > 2*flags.CLISettingsMaxAge {
		// a missing cache must not mean "off forever"; a stale one is not trusted
		// to say "on". Both get one bounded fetch.
		if fetched := fetchLiveSettings(ep); fetched != nil {
			cached = fetched
		}
	}
	return flags.Resolve(context.Background(), flags.DaemonProvider{CachedSettings: cached}).SessionStreamingEnabled
}

// fetchLiveSettings fetches and caches CLI settings within liveSettingsFetchBound.
// The goroutine is abandoned on timeout; the process is short-lived.
func fetchLiveSettings(ep string) *flags.CLISettingsResponse {
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return nil
	}
	type result struct {
		resp *flags.CLISettingsResponse
	}
	done := make(chan result, 1)
	go func() {
		raw, err := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken).GetCLISettings()
		if err != nil {
			done <- result{}
			return
		}
		var resp flags.CLISettingsResponse
		if json.Unmarshal(raw, &resp) != nil {
			done <- result{}
			return
		}
		resp.FetchedAt = time.Now()
		_ = flags.SaveCachedSettings(ep, &resp) // best effort: the daemon refreshes it anyway
		done <- result{resp: &resp}
	}()
	select {
	case r := <-done:
		return r.resp
	case <-time.After(liveSettingsFetchBound):
		slog.Info("live_session_settings_fetch_timed_out", "bound_ms", liveSettingsFetchBound.Milliseconds())
		return nil
	}
}

// liveSessionBegin prepares a pending stream state for a just-started recording
// and spools its raw.jsonl header, with no network call. It is a no-op when
// streaming is off, when the session already has stream state (a re-written
// header must not reach an already-streaming spool), or on any failure.
func liveSessionBegin(projectRoot string, state *session.RecordingState, header []byte) {
	if state == nil || state.SessionID == "" || state.SessionPath == "" || state.RecordedManualPublishing() {
		return
	}
	if _, err := livesession.ReadState(state.SessionPath); !errors.Is(err, livesession.ErrNotStreaming) {
		return // already prepared, disabled or unreadable
	}
	repoID := config.GetRepoID(projectRoot)
	if repoID == "" || !liveStreamingEnabled(projectRoot) {
		return
	}
	agent := adapters.CanonicalAdapterName(state.AgentType)
	if agent == "" {
		agent = adapters.CanonicalAdapterName(state.AdapterName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveLocalBound)
	defer cancel()
	log := slog.With("session_id", state.SessionID)
	if _, err := livesession.Prepare(ctx, liveConfig(projectRoot), state.SessionPath, livesession.OpenParams{
		SessionID: state.SessionID, SessionName: session.GetSessionName(state.SessionPath),
		RepoID: repoID, Agent: agent, StartedAt: state.StartedAt,
	}); err != nil {
		log.Warn("live_session_prepare_failed", "error", err)
		return
	}
	if _, err := livesession.Spool(ctx, state.SessionPath, header, livesession.SpoolOptions{}); err != nil {
		log.Warn("live_session_header_spool_failed", "error", err)
		return
	}
	log.Info("live_session_prepared")
	liveKickDaemon(state.SessionPath)
}

// liveSessionSpool appends the bytes a just-committed AppendRecordingBatch added
// to raw.jsonl to the live spool, then asks the daemon to drain. Call it only
// after the append returned nil. Silent when the session does not stream.
func liveSessionSpool(sessionDir string, w *session.RawWriter) {
	if _, err := livesession.ReadState(sessionDir); err != nil {
		return // never prepared: streaming is off for this session
	}
	batch, ok, err := w.LastAppended()
	if err != nil {
		slog.Warn("live_session_batch_read_failed", "error", err)
		return
	}
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveLocalBound)
	defer cancel()
	res, err := livesession.Spool(ctx, sessionDir, batch.Bytes, livesession.SpoolOptions{Paused: batch.Suspended})
	if err != nil {
		if !errors.Is(err, livesession.ErrNotStreaming) {
			slog.Warn("live_session_spool_failed", "session", filepath.Base(sessionDir), "error", err)
		}
		return
	}
	if res.Appended > 0 {
		liveKickDaemon(sessionDir)
	}
}

// liveKickDaemon is fire-and-forget: a 50 ms-bounded socket write, no reply.
func liveKickDaemon(sessionDir string) {
	_ = daemon.NewClientForCurrentRepo().LiveSessionDrain(daemon.LiveSessionDrainPayload{SessionPath: sessionDir})
}

// liveSessionClose runs the stop-time open -> drain -> close within budget. A
// session that is not streaming is a no-op. If the close cannot finish the
// stream stays closing (or pending with the close remembered) for the daemon.
func liveSessionClose(projectRoot, sessionDir, summary string, budget time.Duration) {
	st, err := livesession.ReadState(sessionDir)
	if err != nil || (st.Status != livesession.StatusPending && st.Status != livesession.StatusStreaming && st.Status != livesession.StatusClosing) {
		return
	}
	res, err := livesession.Close(context.Background(), liveConfig(projectRoot), sessionDir, summary, budget)
	log := slog.With("session_id", st.SessionID, "conversation_id", st.ConversationID, "layer_id", st.LayerID)
	switch {
	case err != nil:
		log.Warn("live_session_close_deferred", "status", string(res.Status), "error", err)
	case !res.Done:
		log.Warn("live_session_close_deferred", "status", string(res.Status))
	default:
		log.Info("live_session_close_done", "status", string(res.Status), "acked", res.Acked)
	}
}

// deletedOnServerWarning is the one notice a person gets about a session
// deleted on SageOx while it streamed. Its owner or a team admin can delete it,
// so the text does not guess which.
const deletedOnServerWarning = "This session was deleted on SageOx while it was recording (by you or a team admin), so it was not shared. It stays on this machine only."

// deletedOnServerGuidance replaces the stop guidance for such a session.
const deletedOnServerGuidance = "This session was deleted on SageOx while it was recording, so it was not uploaded or shared. Tell the user once, in plain words, using upload_warning. Do not upload or publish it."

// liveDeletedOnServer reports whether the server deleted this session while it
// streamed: the live stream was rejected, and the v1 copy must stay off the
// Ledger too.
func liveDeletedOnServer(sessionDir string) bool {
	return sessionDir != "" && livesession.WasDeletedOnServer(sessionDir)
}

// holdDeletedOnServer keeps a session deleted on SageOx on this machine. The
// stream wrote the hold when it heard of the delete; writing it again covers a
// hold that failed then (WriteHoldMarker keeps an existing one). On error the
// caller must keep .recording.json, as for any hold (see holdStoppedSession).
func holdDeletedOnServer(result *agentSessionResult, sessionDir, source string) error {
	if err := writeSessionHold(sessionDir, session.HoldDeletedOnServer, source); err != nil {
		return fmt.Errorf("keep deleted session on this machine: %w", err)
	}
	result.Held = true
	result.DeletedOnServer = true
	return nil
}

// liveSessionAbort deletes the session on the server BEFORE the caller removes
// local state (the key and ids are needed). It never fails the abort.
func liveSessionAbort(projectRoot, sessionDir string) {
	if _, err := livesession.ReadState(sessionDir); err != nil {
		return
	}
	res, err := livesession.Abort(context.Background(), liveConfig(projectRoot), sessionDir, livesession.AbortBudget)
	if err != nil {
		slog.Warn("live_session_abort_failed", "session", filepath.Base(sessionDir), "error", err)
		return
	}
	slog.Info("live_session_abort", "session", filepath.Base(sessionDir), "deleted", res.Deleted, "queued", res.Queued)
}
