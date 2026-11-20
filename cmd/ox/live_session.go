package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/flags"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/sessionid"
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
// streaming is off, when this recording already has stream state (a re-written
// header must not reach an already-streaming spool), or on any failure.
//
// Live state belongs to one recording, named by its ses_ id (C4). Folder names
// have minute precision, so an AI coworker restarted within the minute reuses
// the folder of the recording before it; that recording's stream is retired
// first (retirePreviousLiveStream) and never reused.
func liveSessionBegin(projectRoot string, state *session.RecordingState, header []byte) {
	if state == nil || state.SessionID == "" || state.SessionPath == "" {
		return
	}
	// a new recording ends the AI coworker's earlier ones whatever its own
	// publishing; a manual one just never streams itself
	closeSupersededLiveStreams(state)
	if state.RecordedManualPublishing() {
		return
	}
	switch prev, err := livesession.ReadState(state.SessionPath); {
	case errors.Is(err, livesession.ErrNotStreaming):
	case err != nil:
		return // unreadable: never guess whose stream it is
	case prev.SessionID == state.SessionID:
		return // this recording's own stream: already prepared, disabled or ended
	default:
		if err := retirePreviousLiveStream(projectRoot, state.SessionPath, prev.SessionID); err != nil {
			slog.Warn("live_session_not_started", "session_id", state.SessionID, "reason", "previous_stream_kept", "previous_session_id", prev.SessionID, "error", err)
			return
		}
		slog.Info("live_session_previous_stream_retired", "session_id", state.SessionID, "previous_session_id", prev.SessionID)
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
	parent, continuedFrom := liveSessionLinks(state)
	if _, err := livesession.Prepare(ctx, liveConfig(projectRoot), state.SessionPath, livesession.OpenParams{
		SessionID: state.SessionID, SessionName: session.GetSessionName(state.SessionPath),
		RepoID: repoID, Agent: agent, StartedAt: state.StartedAt,
		ParentSessionID: parent, ContinuedFromSessionID: continuedFrom,
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

// liveSessionLinks are the links a recording's stream opens with: the ses_ id
// of the recording whose shell launched this agent, as captured when that
// parent was chosen, and of the earlier recording this one continues. An id
// that is not a ses_ id, or that names this recording itself, is left out.
func liveSessionLinks(state *session.RecordingState) (parent, continuedFrom string) {
	return ownSessionLink(state, state.ParentSessionID), ownSessionLink(state, state.ContinuedFromSessionID)
}

// ownSessionLink is id when it is a ses_ id naming another recording, else "".
func ownSessionLink(state *session.RecordingState, id string) string {
	if id == state.SessionID || !sessionid.IsValidSessionID(id) {
		return ""
	}
	return id
}

// closeSupersededLiveStreams requests the close of every stream an earlier
// recording by the same AI coworker instance left open in this project. A new
// recording means that one ended, whatever stopped it or failed to: left open,
// it read as a live session on SageOx for good. It is the normal close with no
// summary, finished by the daemon, since a start never waits on the network.
// A stream in this recording's own folder is retirePreviousLiveStream's, and
// one started after this recording is never touched.
func closeSupersededLiveStreams(state *session.RecordingState) {
	if state.AgentID == "" {
		return
	}
	sessionsDir := filepath.Dir(state.SessionPath)
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(sessionsDir, e.Name())
		if !e.IsDir() || dir == state.SessionPath {
			continue
		}
		st, err := livesession.ReadState(dir)
		if err != nil || st.CloseRequested || st.SessionID == state.SessionID || st.StartedAt.After(state.StartedAt) ||
			(st.Status != livesession.StatusPending && st.Status != livesession.StatusStreaming) {
			continue
		}
		// the raw.jsonl header names the AI coworker instance that recorded it
		if meta := session.ReadHeaderMeta(filepath.Join(dir, "raw.jsonl")); meta == nil || meta.AgentID != state.AgentID {
			continue
		}
		log := slog.With("session_id", state.SessionID, "previous_session_id", st.SessionID,
			"conversation_id", st.ConversationID, "layer_id", st.LayerID, "status", string(st.Status))
		ctx, cancel := context.WithTimeout(context.Background(), liveLocalBound)
		err = livesession.RequestCloseOf(ctx, dir, st.SessionID, liveLocalBound)
		cancel()
		switch {
		case errors.Is(err, livesession.ErrNotStreaming):
			log.Debug("live_session_superseded_close_skipped", "reason", "folder_reused")
			continue
		case err != nil:
			log.Warn("live_session_superseded_close_failed", "error", err)
			continue
		}
		log.Info("live_session_superseded_close_requested")
		liveKickDaemon(dir)
	}
}

// liveSessionSpool hands a batch just committed to raw.jsonl to the one shared
// live-spool step (livesession.SpoolCommitted), which applies the ownership
// (C4) and hold (C1) checks, then asks the daemon to drain. Every CLI capture
// path calls it: the Claude Code hook, Cursor's hook and stop, and stop's final
// drain (C8). A non-nil error means the stream could not take the batch; the
// step has declared it as a gap and logged it.
func liveSessionSpool(projectRoot string, state *session.RecordingState, batch session.AppendedBatch) error {
	ctx, cancel := context.WithTimeout(context.Background(), liveLocalBound)
	defer cancel()
	spooled, err := livesession.SpoolCommitted(ctx, projectRoot, state, batch)
	if spooled {
		liveKickDaemon(state.SessionPath)
	}
	return err
}

// liveSessionHeld reports whether a session must not stream any more: it is
// held on this machine, or its publishing is manual now or was when it started
// (stopHoldsSession). A session switched to manual mid-recording counts (C1).
func liveSessionHeld(projectRoot string, state *session.RecordingState) bool {
	return session.IsHeld(state.SessionPath) || stopHoldsSession(projectRoot, state)
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

// liveSecretWithheldWarning is the one notice a person gets when the live
// stream left part of their session out because the secret scan refused it.
// Like the viewer's marker, it never says what matched.
const liveSecretWithheldWarning = "Part of this session looked like a credential, so it was left out of the live view on SageOx. It is still saved on this machine."

// liveSecretWithheldGuidance asks the agent to pass that notice on.
const liveSecretWithheldGuidance = "Part of this session was left out of the live view on SageOx because it looked like a credential. Tell the user once, in plain words, using the data_warnings entry. Do not repeat or guess what it was."

// liveSecretWithheldEarlierWarning tells the person about an earlier
// recording's stream that withheld a secret after its stop could say so: its
// close was left to the daemon, or a hook stopped it.
const liveSecretWithheldEarlierWarning = "Part of an earlier session looked like a credential, so it was left out of its live view on SageOx. It is still saved on this machine."

// liveSecretWithheldEarlierGuidance asks the agent to pass that notice on.
const liveSecretWithheldEarlierGuidance = "Part of an earlier session was left out of its live view on SageOx because it looked like a credential. Tell the user once, in plain words, using the data_warnings entry. Do not repeat or guess what it was."

// collectLiveSecretWithheldNotices fills in, at a stop, the withheld notices
// the stop can give: this recording's own (settleLiveSecretWithheldNotice),
// and those owed by earlier recordings' streams in this project. It clears
// nothing: the folders go in LiveNoticeDirs, and only a stop that printed its
// output clears their marks (confirmLiveSecretWithheldNotices). Recovery runs
// this same processing and prints neither, so the notices stay owed.
func collectLiveSecretWithheldNotices(result *agentSessionResult, state *session.RecordingState) {
	if state == nil || state.SessionPath == "" {
		return
	}
	earlier := earlierLiveSecretWithheld(state.SessionPath, state.SessionID)
	result.EarlierLiveSecretWithheld = len(earlier) > 0
	result.LiveSecretWithheld = settleLiveSecretWithheldNotice(state)
	result.LiveNoticeDirs = earlier
	if result.LiveSecretWithheld {
		result.LiveNoticeDirs = append(result.LiveNoticeDirs, state.SessionPath)
	}
}

// settleLiveSecretWithheldNotice reports whether this recording's live stream
// withheld a batch the publication secret scan refused. A stream that withheld,
// or one still unfinished (its close left to the daemon, or never requested)
// that may withhold after the stop returns, is marked as owing the notice
// until a start or stop has said it. A hook's stop has no one to tell, and a
// stop can fail after this: either way the next session start or stop in this
// project says it. A stream left in a reused folder by another recording, or
// deleted on SageOx, does not count.
func settleLiveSecretWithheldNotice(state *session.RecordingState) bool {
	if state == nil || state.SessionPath == "" {
		return false
	}
	st, err := livesession.ReadState(state.SessionPath)
	if err != nil || st.SessionID != state.SessionID || st.DeletedOnServer || st.Status == livesession.StatusDeleted {
		return false
	}
	ended := st.Status == livesession.StatusClosed || st.Status == livesession.StatusDisabled || st.Status == livesession.StatusEmpty
	if !st.SecretWithheld && ended {
		return false
	}
	if err := livesession.OweSecretWithheldNotice(state.SessionPath); err != nil {
		slog.Warn("live_session_withheld_notice_unrecorded", "session_id", st.SessionID, "error", err)
	}
	return st.SecretWithheld
}

// earlierLiveSecretWithheld returns the folders, beside sessionDir, of earlier
// recordings' streams that owe their person the withheld notice and have
// withheld since. The recording sessionID is not earlier: its own stop says it.
func earlierLiveSecretWithheld(sessionDir, sessionID string) []string {
	if sessionDir == "" {
		return nil
	}
	return livesession.OwedSecretWithheldNotices(filepath.Dir(sessionDir), sessionID)
}

// confirmLiveSecretWithheldNotices clears the owed marks of notices a start or
// stop has just printed. Never call it before the output is written: a mark
// cleared then is a notice never given. A mark that cannot be cleared is said
// again next time: twice beats never.
func confirmLiveSecretWithheldNotices(dirs []string) {
	for _, dir := range dirs {
		if err := livesession.ClearSecretWithheldNotice(dir); err != nil {
			slog.Warn("live_session_withheld_notice_not_cleared", "session", filepath.Base(dir), "error", err)
		}
	}
}

// addLiveSecretWithheldNotice adds the withheld notices to a stop's output. A
// session deleted on SageOx already says it was not shared at all.
func addLiveSecretWithheldNotice(output *sessionStopOutput, result *agentSessionResult) {
	if result.LiveSecretWithheld && !result.DeletedOnServer {
		output.DataWarnings = append(output.DataWarnings, liveSecretWithheldWarning)
		output.Guidance += " " + liveSecretWithheldGuidance
	}
	if result.EarlierLiveSecretWithheld {
		output.DataWarnings = append(output.DataWarnings, liveSecretWithheldEarlierWarning)
		output.Guidance += " " + liveSecretWithheldEarlierGuidance
	}
}

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

// errSessionDeletedOnServer refuses publishing a session someone deleted on
// SageOx while it streamed.
var errSessionDeletedOnServer = errors.New("this session was deleted on SageOx while it was recording, so it is not published")

// refuseDeletedOnServer is the publication boundary's check (C3). Publishers
// call it immediately before their first write toward the Ledger, never with
// a value read earlier: a stream can hear of a delete at any time. It reads
// both records of a delete, the stream state's flag (written even when the hold
// marker could not be) and a deleted_on_server hold, and either one refuses.
func refuseDeletedOnServer(sessionDir string) error {
	if sessionDir == "" {
		return nil
	}
	if liveDeletedOnServer(sessionDir) {
		return errSessionDeletedOnServer
	}
	if hold, err := session.ReadHoldMarker(sessionDir); err == nil && hold.Reason == session.HoldDeletedOnServer {
		return errSessionDeletedOnServer
	}
	return nil
}

// holdRefusedAsDeleted turns a stop whose upload the publication boundary
// refused (errSessionDeletedOnServer) into a held stop: nothing left that would
// publish it later (summary prompt, .needs-summary, the doctor's retry marker),
// and the one notice for the person.
func holdRefusedAsDeleted(result *agentSessionResult, sessionDir, cacheDir string) (*agentSessionResult, error) {
	if err := holdDeletedOnServer(result, sessionDir, "session_stop"); err != nil {
		return nil, err
	}
	result.SummaryPrompt = ""
	if err := session.ClearNeedsSummaryMarker(cacheDir); err != nil {
		slog.Warn("session deleted on SageOx: summary marker not cleared", "session", filepath.Base(cacheDir), "error", err)
	}
	_ = os.Remove(filepath.Join(cacheDir, session.UploadRetryPendingFile))
	result.LedgerSessionDir = ""
	result.UploadWarning = deletedOnServerWarning
	slog.Info("session deleted on SageOx while streaming, upload refused", "session", filepath.Base(cacheDir))
	return result, nil
}

// holdIfDeletedOnServer holds a session deleted on SageOx while it streamed and
// reports whether it did. An error means the hold could not be written: the
// caller keeps .recording.json, as for any hold.
func holdIfDeletedOnServer(sessionDir, source string) (bool, error) {
	if !liveDeletedOnServer(sessionDir) {
		return false, nil
	}
	if err := writeSessionHold(sessionDir, session.HoldDeletedOnServer, source); err != nil {
		return true, fmt.Errorf("keep deleted session on this machine: %w", err)
	}
	return true, nil
}

// liveSessionAbort deletes the session's live copy on the server, bounded by
// budget. livesession.Abort records the delete in a queue outside the session
// folder before it waits on anything (C2), so a confirmed, queued or refused
// result is safe to follow with removing the folder. An error means the delete
// was neither confirmed nor recorded: the caller must keep the folder, whose
// stream state is then the only record of what to delete. raw.jsonl is never
// touched here.
func liveSessionAbort(projectRoot, sessionDir string, budget time.Duration) (livesession.AbortResult, error) {
	if sessionDir == "" {
		return livesession.AbortResult{}, nil
	}
	if _, err := livesession.ReadState(sessionDir); errors.Is(err, livesession.ErrNotStreaming) {
		return livesession.AbortResult{}, nil
	}
	res, err := livesession.Abort(context.Background(), liveConfig(projectRoot), sessionDir, budget)
	if err != nil {
		slog.Warn("live_session_abort_failed", "session", filepath.Base(sessionDir), "error", err)
		return res, fmt.Errorf("record the delete of the session's live copy on SageOx: %w", err)
	}
	slog.Info("live_session_abort", "session", filepath.Base(sessionDir), "deleted", res.Deleted, "queued", res.Queued, "refused", res.Refused, "reason", res.Reason)
	return res, nil
}

// liveDeleteRefusedWarning tells the person a live copy this machine cannot
// delete is still on SageOx, so they can delete it there.
func liveDeleteRefusedWarning(res livesession.AbortResult) string {
	if !res.Refused {
		return ""
	}
	return fmt.Sprintf("SageOx could not delete the live copy (%s); delete it on SageOx.", res.Reason)
}

// liveFinish says what a stop did with the session's live stream.
type liveFinish struct {
	// Removed: the session is held, so its live copy was deleted on SageOx, or
	// the delete is queued for the daemon.
	Removed bool
	// Abort is the held session's delete result; Abort.Refused needs the person.
	Abort livesession.AbortResult
}

// liveSessionFinish ends this recording's live stream at a stop (C1). A held
// session stops streaming and its server copy is deleted, because manual
// publishing means "not shared until I upload"; raw.jsonl stays, and 'ox
// session upload' still publishes it. Any other session's stream is closed
// within budget, and a close that cannot finish is left for the daemon. A
// stream deleted on SageOx, or one belonging to another recording, is left
// alone. A delete that could not be recorded is logged; the session is held
// either way, and abort reports it.
func liveSessionFinish(projectRoot string, state *session.RecordingState, summary string, budget time.Duration) liveFinish {
	if state == nil || state.SessionPath == "" {
		return liveFinish{}
	}
	st, err := livesession.ReadState(state.SessionPath)
	if err != nil || st.SessionID != state.SessionID || st.DeletedOnServer {
		return liveFinish{}
	}
	if !liveSessionHeld(projectRoot, state) {
		liveSessionClose(projectRoot, state.SessionPath, summary, budget)
		return liveFinish{}
	}
	if st.Status == livesession.StatusDeleted {
		return liveFinish{} // already let go
	}
	res, err := liveSessionAbort(projectRoot, state.SessionPath, budget)
	if err != nil {
		return liveFinish{}
	}
	return liveFinish{Removed: res.Deleted || res.Queued || res.Refused, Abort: res}
}

// heldLiveWarning is the stop message for a held session whose live copy the
// stop deleted on SageOx: said once, at the stop that did it.
func heldLiveWarning(base string, fin liveFinish) string {
	switch {
	case fin.Abort.Refused:
		return base + " " + liveDeleteRefusedWarning(fin.Abort)
	case fin.Removed:
		return base + " Its live copy on SageOx was deleted, since publishing is manual."
	}
	return base
}

// liveCloseRequestFileName is livesession's close-request marker; ReadState
// reports it as State.CloseRequested.
const liveCloseRequestFileName = "close-requested"

// retirePreviousLiveStream clears the live files an earlier recording left in a
// session folder a new recording now reuses (C4). A stream that ended is
// removed. One that still owes the server an open, a drain or a close moves,
// with its close requested, to its own folder beside this one: the daemon's
// tick scans every folder there and finishes it, so none of its bytes are lost
// and none reach the new recording's stream. An ended stream that still owes
// its person the withheld notice moves the same way, so the notice survives
// for the next start or stop to say (an automatic start says none). prevSessionID is the stream's
// owner as first read; a different owner by the time the lock is held is an
// error, never a guess. A hold placed for that recording is released too (see
// releasePreviousHold).
func retirePreviousLiveStream(projectRoot, dir, prevSessionID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), liveLocalBound)
	defer cancel()
	// livesession's own per-session lock: holding it means no drain, open or
	// close is mid-way through the session's live files
	return fileutil.WithFileLockTimeout(ctx, livesession.LockTarget(dir), liveLocalBound, func() error {
		st, err := livesession.ReadState(dir)
		if errors.Is(err, livesession.ErrNotStreaming) {
			return nil
		}
		if err != nil {
			return err
		}
		if st.SessionID != prevSessionID {
			return fmt.Errorf("live stream changed owner while it was retired")
		}
		names, err := liveFileNames(dir)
		if err != nil {
			return err
		}
		switch {
		case st.Status == livesession.StatusPending, st.Status == livesession.StatusStreaming, st.Status == livesession.StatusClosing,
			owesWithheldNotice(dir, st):
			if err := moveLiveStream(ctx, liveConfig(projectRoot), dir, st, names); err != nil {
				return err
			}
		default:
			for _, name := range names {
				if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("remove previous live file %s: %w", name, err)
				}
			}
		}
		return releasePreviousHold(ctx, dir, prevSessionID)
	})
}

// owesWithheldNotice reports whether the stream in dir withheld a secret and
// its person has not been told yet.
func owesWithheldNotice(dir string, st livesession.State) bool {
	if !st.SecretWithheld || st.DeletedOnServer || st.Status == livesession.StatusDeleted {
		return false
	}
	return livesession.OwesSecretWithheldNotice(dir)
}

// releasePreviousHold removes a hold placed for the recording prevSessionID,
// which this folder no longer holds: the new recording's raw.jsonl header has
// replaced its transcript. StartRecording refuses a held folder, but that
// recording's stream can hear SageOx's delete after that check and before this
// retire, and write its deleted_on_server hold here. Left in place it held the
// new recording, and the daemon deleted the new stream on SageOx (C1). A hold
// that names no recording, or another one, is never released: fail closed.
func releasePreviousHold(ctx context.Context, dir, prevSessionID string) error {
	hold, err := session.ReadHoldMarker(dir)
	if err != nil || hold.SessionID == "" || hold.SessionID != prevSessionID {
		return nil
	}
	if err := session.ClearHoldMarker(dir); err != nil {
		return fmt.Errorf("release the previous recording's hold: %w", err)
	}
	slog.InfoContext(ctx, "live_session_previous_hold_released", "previous_session_id", prevSessionID, "reason", string(hold.Reason), "held_at", hold.HeldAt)
	return nil
}

// liveStateFileName is livesession's stream state, which says whose stream a
// folder holds. Retiring handles it last, so a retire cut short leaves the
// folder still owned by the previous stream and the next start finishes it.
const liveStateFileName = "stream-state.json"

// liveFileNames lists the live-stream files in dir, the stream state last.
func liveFileNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read session folder: %w", err)
	}
	var names []string
	hasState := false
	for _, e := range entries {
		switch {
		case e.IsDir() || !livesession.IsLocalFile(e.Name()):
		case e.Name() == liveStateFileName:
			hasState = true
		default:
			names = append(names, e.Name())
		}
	}
	if hasState {
		names = append(names, liveStateFileName)
	}
	return names, nil
}

// moveLiveStream moves an unfinished stream's files to .live-<ses id> beside
// dir and requests its close there: the recording it belongs to has ended. The
// close request goes in first, so the daemon never finds the moved stream
// without it. The dot keeps the folder out of listings that skip dotfiles.
//
// A stream is trusted only in the folder its conversation is bound to, so the
// renames run inside livesession.MoveStream, which moves that binding with
// them. If the binding cannot move, nothing does: the stream stays in dir,
// still trusted there, and this start does not stream.
func moveLiveStream(ctx context.Context, cfg livesession.Config, dir string, st livesession.State, names []string) error {
	sessionID := st.SessionID
	if !sessionid.IsValidSessionID(sessionID) {
		// it names a folder: never let a damaged state file pick the path
		return fmt.Errorf("previous live stream has no valid session id")
	}
	dest := filepath.Join(filepath.Dir(dir), ".live-"+sessionID)
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return fmt.Errorf("make a folder for the previous live stream: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dest, liveStateFileName)); err == nil {
		return fmt.Errorf("a live stream is already kept at %s", filepath.Base(dest))
	}
	if err := fileutil.AtomicWriteBytes(filepath.Join(dest, liveCloseRequestFileName), nil, 0o600); err != nil {
		return fmt.Errorf("request the previous live stream's close: %w", err)
	}
	// names lists the stream state last, as MoveStream requires: a move cut
	// short leaves the state, and so the binding, in dir
	return livesession.MoveStream(ctx, cfg, st, dir, dest, func() error {
		for i, name := range names {
			if err := os.Rename(filepath.Join(dir, name), filepath.Join(dest, name)); err != nil {
				// cut short, the stream is split across two folders: the daemon may
				// find a stream state with no spool, or a spool with no state. The
				// next start in this folder tries again; this line says where it stands.
				slog.ErrorContext(ctx, "live_session_retire_move_failed", "session_id", sessionID, "conversation_id", st.ConversationID,
					"layer_id", st.LayerID, "status", string(st.Status), "file", name, "moved", i, "files", len(names),
					"from", dir, "to", dest, "error", err)
				return fmt.Errorf("move previous live file %s: %w", name, err)
			}
		}
		return nil
	})
}
