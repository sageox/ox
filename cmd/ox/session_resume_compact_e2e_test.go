package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Customer promise (#1398): a Claude Code session I resume, or one that
// compacts, is recorded like any other, and `ox agent <id> session stop`
// publishes it to the Ledger. No `session recover`, no empty session.
//
// Everything below is real except Claude Code itself: the claude-code adapter
// binary discovers and reads the transcript, the PostToolUse hook captures it,
// stop finalizes and uploads it over LFS, and the Ledger pushes to a bare
// remote. The transcripts are shaped like the ones Claude Code writes: a
// resumed session's file opens with a copy of the conversation it continues,
// still under that conversation's session ID.

// Failure prevented: a resumed session's hooks all reported
// session-file-not-found, and stop published nothing.
func TestResumedClaudeSession_IsRecordedAndPublishedOnStop(t *testing.T) {
	r := newClaudeRecordingFixture(t)
	const (
		parent  = "a1969243-7a0d-4f80-bc97-b71beefcb3e6"
		resumed = "230457d4-c384-4e38-90b5-3289ff44be62"
	)
	before := time.Now().UTC().Add(-time.Hour)
	// the parent's own transcript, and the copy Claude opens the resumed file with
	parentTurns := []string{
		r.turn(parent, "user", "INHERITED-PROMPT: add retries to the ledger push", before),
		r.turn(parent, "assistant", "INHERITED-REPLY: retries added", before.Add(time.Second)),
	}
	r.write(parent, parentTurns...)
	r.write(resumed, append(parentTurns, r.title(resumed))...)

	agentID := r.prime(resumed)
	now := time.Now().UTC()
	r.append(resumed,
		r.turn(resumed, "user", "RESUMED-PROMPT: now make the backoff exponential", now.Add(time.Second)),
		r.turn(resumed, "assistant", "RESUMED-REPLY: backoff is exponential with jitter", now.Add(2*time.Second)))
	r.afterTool(agentID)

	r.requireCaptured(agentID, []string{"RESUMED-PROMPT", "RESUMED-REPLY"}, []string{"INHERITED-PROMPT", "INHERITED-REPLY"})
	r.stopAndRequirePublished(agentID, 2)
}

// A session that was resumed and later compacted keeps the inherited copy at
// the top of its file; compaction appends a boundary and a summary, and the
// SessionStart that follows records it afresh. Failure prevented: that
// recording's hooks all reported session-file-not-found (6 of 8 live
// recordings on one machine).
func TestCompactedClaudeSession_IsRecordedAndPublishedOnStop(t *testing.T) {
	r := newClaudeRecordingFixture(t)
	const (
		parent    = "a1969243-7a0d-4f80-bc97-b71beefcb3e6"
		compacted = "045c4e3b-1162-45ed-8ece-f0217e4bcead"
	)
	before := time.Now().UTC().Add(-2 * time.Hour)
	r.write(compacted,
		r.turn(parent, "user", "INHERITED-PROMPT: sketch the sync scheduler", before),
		r.title(compacted),
		r.turn(compacted, "user", "PRE-COMPACT-PROMPT: wire it into the daemon", before.Add(time.Minute)),
		r.turn(compacted, "assistant", "PRE-COMPACT-REPLY: wired in", before.Add(2*time.Minute)),
		r.record(compacted, map[string]any{"type": "system", "subtype": "compact_boundary", "timestamp": time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)}),
		r.record(compacted, map[string]any{"type": "user", "isCompactSummary": true, "timestamp": time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano),
			"message": map[string]any{"role": "user", "content": "This session is being continued from a previous conversation that ran out of context."}}))

	agentID := r.prime(compacted)
	now := time.Now().UTC()
	r.append(compacted,
		r.turn(compacted, "user", "POST-COMPACT-PROMPT: add a test for the scheduler", now.Add(time.Second)),
		r.turn(compacted, "assistant", "POST-COMPACT-REPLY: added TestScheduler", now.Add(2*time.Second)))
	r.afterTool(agentID)
	r.append(compacted, r.turn(compacted, "user", "POST-COMPACT-FOLLOWUP: run it", now.Add(3*time.Second)))
	r.afterTool(agentID)

	r.requireCaptured(agentID,
		[]string{"POST-COMPACT-PROMPT", "POST-COMPACT-REPLY", "POST-COMPACT-FOLLOWUP"},
		[]string{"INHERITED-PROMPT", "PRE-COMPACT-PROMPT", "PRE-COMPACT-REPLY"})
	r.stopAndRequirePublished(agentID, 3)
}

// When the transcript cannot be found at all, what the recording already holds
// (here, history an AI coworker reconstructed with capture-prior) is still the
// session, under either publishing mode. Failure prevented: stop returned
// success but published nothing and left the recording marked live, so
// `ox session upload` refused it as "still recording" and only
// `session recover` could publish it.
func TestSessionStop_PublishesCapturedHistoryWhenTheTranscriptIsMissing(t *testing.T) {
	t.Run("automatic publishing: stop publishes it", func(t *testing.T) {
		r := newClaudeRecordingFixture(t)
		r.setSessionPublishing(config.SessionPublishingAuto)
		agentID := r.captureHistoryWithoutTranscript()
		r.stopAndRequirePublished(agentID, 2)
	})

	t.Run("manual publishing: stop holds it, upload publishes it", func(t *testing.T) {
		r := newClaudeRecordingFixture(t)
		r.setSessionPublishing(config.SessionPublishingManual)
		agentID := r.captureHistoryWithoutTranscript()
		name := session.GetSessionName(r.recording(agentID).SessionPath)

		stop := r.stop(agentID)
		remoteBefore := runGit(t, r.barePath, "rev-parse", "HEAD")
		require.NoError(t, sessionUploadCmd.RunE(sessionUploadCmd, []string{name}), "upload must publish what stop saved")
		assert.True(t, stop.Held, "manual publishing holds the stopped session: %+v", stop)
		assert.Nil(t, r.recording(agentID), "stop must not leave the recording live")
		assert.NotEqual(t, remoteBefore, runGit(t, r.barePath, "rev-parse", "HEAD"), "the upload reached the remote")
		meta, err := lfs.ReadSessionMeta(filepath.Join(r.ledgerPath, "sessions", name))
		require.NoError(t, err)
		assert.False(t, meta.IsDraft())
		assert.Equal(t, 2, meta.EntryCount, "the captured history was published, not an empty session")
	})
}

func (r *claudeRecordingFixture) setSessionPublishing(mode string) {
	r.t.Helper()
	projectCfg, err := config.LoadProjectConfig(r.projectRoot)
	require.NoError(r.t, err)
	projectCfg.SessionPublishing = mode
	require.NoError(r.t, config.SaveProjectConfig(r.projectRoot, projectCfg))
	r.t.Setenv("OX_SESSION_PUBLISHING", "")
}

// captureHistoryWithoutTranscript primes a recording whose transcript does not
// exist and gives it two turns an AI coworker reconstructed with capture-prior.
func (r *claudeRecordingFixture) captureHistoryWithoutTranscript() string {
	t := r.t
	t.Helper()
	agentID := r.prime("5a6f0cd0-f4e9-4fbb-8d6b-6c60ccea1613") // no such transcript
	r.afterTool(agentID)
	state, err := session.LoadRecordingStateForAgent(r.projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, "session-file-not-found", state.LastHookStatus, "precondition: the transcript is not findable")

	started := time.Now().UTC().Format(time.RFC3339)
	prior := filepath.Join(t.TempDir(), "prior.jsonl")
	require.NoError(t, os.WriteFile(prior, []byte(strings.Join([]string{
		fmt.Sprintf(`{"_meta":{"schema_version":"1","source":"agent_reconstruction","agent_id":%q,"agent_type":"claude-code","started_at":%q}}`, agentID, started),
		fmt.Sprintf(`{"seq":1,"type":"user","content":"CAPTURED-PROMPT: fix the flaky upload retry","ts":%q,"source":"planning_history"}`, started),
		fmt.Sprintf(`{"seq":2,"type":"assistant","content":"CAPTURED-REPLY: the retry now waits for the lock","ts":%q,"source":"planning_history"}`, started),
	}, "\n")+"\n"), 0o600))
	captureStdoutForPlanCLI(t, func() {
		require.NoError(t, runAgentSessionCapturePrior(&agentinstance.Instance{AgentID: agentID, AgentType: "claude-code"}, []string{"--file", prior}))
	})
	return agentID
}

// claudeRecordingFixture is a project with a Ledger on a bare remote, the real
// claude-code adapter, and a fake ~/.claude/projects bucket for the project.
type claudeRecordingFixture struct {
	t *testing.T
	*downloadLedgerFixture
	repo   string // the project root as Claude records cwd: symlinks resolved
	bucket string
}

func newClaudeRecordingFixture(t *testing.T) *claudeRecordingFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("short: builds the claude-code adapter and pushes to a bare remote")
	}
	// built before the fixture moves HOME, so the build uses the real Go cache
	adapterDir := buildClaudeAdapterForTest(t)
	for _, key := range []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "AMP_THREAD_URL", "PI_SESSION_ID", "OMP_SESSION_ID", "GC_RUN_ID"} {
		t.Setenv(key, "") // the test binary may itself run inside an AI coworker
	}
	f := newDownloadLedgerFixture(t)
	// Prime records only once it finds the Ledger where it clones one, and
	// the session caches are keyed by the Ledger directory's name, the repo ID:
	// put the fixture's Ledger there.
	defaultLedger, err := ledger.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(defaultLedger), 0o755))
	require.NoError(t, os.Symlink(f.ledgerPath, defaultLedger))
	require.NoError(t, config.SaveLocalConfig(f.projectRoot, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: defaultLedger}}))
	f.ledgerPath = defaultLedger
	oxConfigSetRepo(t, "session_recording", "auto")
	t.Setenv("OX_ADAPTER_PATH", adapterDir)
	adapters.Unregister("claude-code")
	require.NoError(t, adapters.RegisterExternalAdapters())
	t.Cleanup(func() { adapters.Unregister("claude-code") })
	adapter, err := adapters.GetAdapter("claude")
	require.NoError(t, err)
	require.IsType(t, &adapters.ExternalAdapter{}, adapter, "the real adapter binary must serve Claude lookups")

	repo, err := filepath.EvalSymlinks(f.projectRoot)
	require.NoError(t, err)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	hash := strings.NewReplacer(string(os.PathSeparator), "-", "_", "-", ".", "-").Replace(repo)
	bucket := filepath.Join(home, ".claude", "projects", hash)
	require.NoError(t, os.MkdirAll(bucket, 0o700))
	return &claudeRecordingFixture{t: t, downloadLedgerFixture: f, repo: repo, bucket: bucket}
}

func buildClaudeAdapterForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "ox-adapter-claude-code"), "./cmd/ox-adapter-claude-code")
	cmd.Dir = filepath.Join(packageDir, "..", "..")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "build ox-adapter-claude-code: %s", out)
	return dir
}

// record is one native line carrying the identity Claude stamps on every record.
func (r *claudeRecordingFixture) record(sessionID string, fields map[string]any) string {
	r.t.Helper()
	fields["sessionId"], fields["cwd"] = sessionID, r.repo
	line, err := json.Marshal(fields)
	require.NoError(r.t, err)
	return string(line)
}

func (r *claudeRecordingFixture) turn(sessionID, role, text string, at time.Time) string {
	var content any = text
	if role == "assistant" {
		content = []map[string]any{{"type": "text", "text": text}}
	}
	return r.record(sessionID, map[string]any{"type": role, "timestamp": at.Format(time.RFC3339Nano),
		"message": map[string]any{"role": role, "content": content}})
}

func (r *claudeRecordingFixture) title(sessionID string) string {
	r.t.Helper()
	line, err := json.Marshal(map[string]any{"type": "custom-title", "customTitle": "ledger work", "sessionId": sessionID})
	require.NoError(r.t, err)
	return string(line)
}

func (r *claudeRecordingFixture) write(id string, lines ...string) {
	r.t.Helper()
	require.NoError(r.t, os.WriteFile(filepath.Join(r.bucket, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

func (r *claudeRecordingFixture) append(id string, lines ...string) {
	r.t.Helper()
	f, err := os.OpenFile(filepath.Join(r.bucket, id+".jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(r.t, err)
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	require.NoError(r.t, err)
	require.NoError(r.t, f.Close())
}

// prime starts the recording the way `ox agent prime` does on a Claude
// SessionStart, which names the agent "claude".
func (r *claudeRecordingFixture) prime(nativeID string) string {
	r.t.Helper()
	const agentID = "OxRsm1"
	status := startSessionRecording(r.projectRoot, agentID, "claude", "", "", nativeID)
	require.NotNil(r.t, status)
	require.True(r.t, status.Recording, "prime must start recording: %+v", status)
	state := r.recording(agentID)
	require.NotNil(r.t, state)
	assert.Equal(r.t, "claude-code", state.AdapterName, "the recording names the adapter its Claude-only checks look for")
	return agentID
}

func (r *claudeRecordingFixture) recording(agentID string) *session.RecordingState {
	r.t.Helper()
	state, err := session.LoadRecordingStateForAgent(r.projectRoot, agentID)
	require.NoError(r.t, err)
	return state
}

func (r *claudeRecordingFixture) afterTool(agentID string) {
	r.t.Helper()
	require.NoError(r.t, handleAfterTool(&HookContext{Phase: phaseAfterTool, AgentType: "claude-code", ProjectRoot: r.projectRoot, Marker: &SessionMarker{AgentID: agentID}}))
}

func (r *claudeRecordingFixture) requireCaptured(agentID string, want, notWant []string) {
	r.t.Helper()
	state := r.recording(agentID)
	require.NotNil(r.t, state)
	require.Equal(r.t, "ok", state.LastHookStatus, "every hook must find and read the transcript")
	raw, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
	require.NoError(r.t, err)
	for _, text := range want {
		assert.Contains(r.t, string(raw), text, "the recording captured this turn")
	}
	for _, text := range notWant {
		assert.NotContains(r.t, string(raw), text, "turns from before the recording are not part of it")
	}
}

func (r *claudeRecordingFixture) stop(agentID string) pipeline.StopOutput {
	r.t.Helper()
	out := captureStdoutForPlanCLI(r.t, func() {
		require.NoError(r.t, runAgentSessionStop(&agentinstance.Instance{AgentID: agentID, AgentType: "claude-code"}))
	})
	var stop pipeline.StopOutput
	start := strings.Index(out, "{")
	require.NotEqual(r.t, -1, start, "stop must print its JSON result: %s", out)
	require.NoError(r.t, json.Unmarshal([]byte(out[start:]), &stop), "stop JSON: %s", out)
	return stop
}

func (r *claudeRecordingFixture) stopAndRequirePublished(agentID string, entries int) {
	r.t.Helper()
	state := r.recording(agentID)
	require.NotNil(r.t, state)
	name := session.GetSessionName(state.SessionPath)
	remoteBefore := runGit(r.t, r.barePath, "rev-parse", "HEAD")

	stop := r.stop(agentID)
	require.True(r.t, stop.Success, "%+v", stop)
	assert.Empty(r.t, stop.UploadWarning)
	assert.Equal(r.t, entries, stop.EntryCount)
	assert.Nil(r.t, r.recording(agentID), "stop must not leave the recording live")

	assert.NotEqual(r.t, remoteBefore, runGit(r.t, r.barePath, "rev-parse", "HEAD"), "stop's upload reached the remote")
	assert.Contains(r.t, runGit(r.t, r.barePath, "ls-tree", "-r", "--name-only", "HEAD"), "sessions/"+name+"/meta.json")
	meta, err := lfs.ReadSessionMeta(filepath.Join(r.ledgerPath, "sessions", name))
	require.NoError(r.t, err)
	assert.False(r.t, meta.IsDraft())
	assert.Equal(r.t, entries, meta.EntryCount)
	assert.True(r.t, lfs.IsPointerFile(filepath.Join(r.ledgerPath, "sessions", name, "raw.jsonl")), "the shared tree carries a pointer, never the bytes")
}
