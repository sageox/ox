package main

// `ox session wait` (#1131) is a runner's last step on a machine that goes away
// after its job. These tests prove what wait itself decides: which recordings
// it finishes, which sessions it publishes or pushes, what it counts, and that
// it leaves uploads to a daemon that outlives the session. The session-end stop
// it runs for a crashed AI coworker has its own end-to-end test
// (TestSessionEnd_AmpOrbUploadsBeforeTheOrbPauses), so here it is stubbed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/runtime"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitFixture is a project whose Ledger is cloned where ox keeps it, on the
// data home, from a bare remote, with a fake content store. The daemon is off,
// so the CLI owns uploads.
type waitFixture struct {
	projectRoot string
	ledgerPath  string
	barePath    string
	store       *fakeLFSStore
}

func newWaitFixture(t *testing.T) *waitFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("short: real git remote")
	}
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	clearFinalizeEnv(t)
	t.Setenv("SAGEOX_DAEMON", "false")
	priorDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	t.Cleanup(func() { gitserver.TestSetConfigDirOverride(priorDir) })
	priorStorage := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() { gitserver.TestSetForceFileStorage(priorStorage) })
	setTestCfg(t)
	cfg.JSON = true

	store := newFakeLFSStore(t)
	t.Setenv("SAGEOX_ENDPOINT", store.url)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(store.url, gitserver.GitCredentials{
		Username: "coworker", Token: "test-token", ServerURL: store.url, ExpiresAt: time.Now().Add(time.Hour),
	}))

	projectRoot := t.TempDir()
	runGit(t, projectRoot, "init")
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_waitproject","endpoint":"`+store.url+`"}`), 0o644))

	barePath := filepath.Join(t.TempDir(), "ledger.git")
	initBareRemote(t, barePath)
	ledgerPath := paths.LedgersDataDir("repo_waitproject", store.url)
	require.NoError(t, os.MkdirAll(filepath.Dir(ledgerPath), 0o755))
	runGit(t, filepath.Dir(ledgerPath), "clone", barePath, ledgerPath)
	runGit(t, ledgerPath, "config", "gc.auto", "0")
	runGit(t, ledgerPath, "config", "user.email", "coworker@example.com")
	runGit(t, ledgerPath, "config", "user.name", "Coworker")
	require.NoError(t, os.WriteFile(filepath.Join(ledgerPath, ".gitkeep"), nil, 0o644))
	runGit(t, ledgerPath, "add", ".gitkeep")
	runGit(t, ledgerPath, "commit", "--no-verify", "-m", "initial")
	runGit(t, ledgerPath, "push")
	// the content store URL derives from the fetch remote; pushes land on the bare repo
	runGit(t, ledgerPath, "remote", "set-url", "origin", store.url+"/ledger.git")
	runGit(t, ledgerPath, "remote", "set-url", "--push", "origin", barePath)

	t.Chdir(projectRoot)
	return &waitFixture{projectRoot: projectRoot, ledgerPath: ledgerPath, barePath: barePath, store: store}
}

// record starts a recording owned by ownerPID, with two captured turns.
func (f *waitFixture) record(t *testing.T, agentID string, ownerPID int) string {
	t.Helper()
	state, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
		AgentID: agentID, AdapterName: "codex", Username: "coworker", ParentPID: ownerPID,
	})
	require.NoError(t, err)
	writeWaitTranscript(t, state.SessionPath, agentID, time.Now())
	return session.GetSessionName(state.SessionPath)
}

// leftover writes a session that stopped at `at` and is still only in the
// cache, stamped the way session end stamps it.
func (f *waitFixture) leftover(t *testing.T, name, agentID string, at time.Time) string {
	t.Helper()
	dir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
	writeWaitTranscript(t, dir, agentID, at)
	require.NoError(t, session.StampRawCarrier(filepath.Join(dir, "raw.jsonl"), session.CarrierStamp{StoppedAt: at}))
	return dir
}

// stranded publishes a leftover whose push fails, so its commit is only in the
// Ledger clone.
func (f *waitFixture) stranded(t *testing.T, name, agentID string) {
	t.Helper()
	dir := f.leftover(t, name, agentID, time.Now())
	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing.git"))
	require.Error(t, publishCachedSession(f.projectRoot, f.ledgerPath, name, dir))
	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", f.barePath)
	require.NotContains(t, remoteTree(t, f.barePath), "sessions/"+name+"/meta.json", "precondition")
}

// writeWaitTranscript writes a recorded session's raw.jsonl: a header and two
// turns at the given time.
func writeWaitTranscript(t *testing.T, dir, agentID string, at time.Time) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	ts := at.UTC().Format(time.RFC3339)
	raw := `{"type":"header","metadata":{"version":"1.0","agent_id":"` + agentID + `","agent_type":"codex","username":"coworker","created_at":"` + ts + `","session_id":"` + sessionid.GenerateSessionID() + `"}}` + "\n" +
		`{"type":"user","content":"Make the pod upload its sessions before it goes away.","timestamp":"` + ts + `"}` + "\n" +
		`{"type":"assistant","content":"Every pending session now reaches the Ledger first.","timestamp":"` + ts + `"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw), 0o644))
}

// stubSessionEnd stands in for the session-end stop and records whom it ran
// for. A failure is the upload's: like the real stop, it ends the recording
// first, leaving the session in the cache.
func stubSessionEnd(t *testing.T, uploadErr error) *[]string {
	t.Helper()
	var ran []string
	prior := runSessionStopAtEnd
	runSessionStopAtEnd = func(projectRoot, agentID string) error {
		ran = append(ran, agentID)
		if uploadErr != nil {
			require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, agentID))
		}
		return uploadErr
	}
	t.Cleanup(func() { runSessionStopAtEnd = prior })
	return &ran
}

func deadOwnerPID(t *testing.T) int {
	t.Helper()
	owner := exec.Command("true")
	require.NoError(t, owner.Start())
	require.NoError(t, owner.Wait())
	return owner.Process.Pid
}

// runWait runs `ox session wait --since <since> --json`.
func runWait(t *testing.T, since time.Time) (sessionWaitOutput, error) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().String("since", since.UTC().Format(time.RFC3339Nano), "")
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	err := runSessionWait(cmd, nil)
	var out sessionWaitOutput
	if buf.Len() > 0 {
		require.NoError(t, json.Unmarshal(buf.Bytes(), &out), buf.String())
	}
	return out, err
}

// wait runs session end for an AI coworker that died before it could, and
// only for that one: a live recording, a quarantined one and a held session
// are reported, never finished or published. A session whose upload failed is
// tried once per run and reported once.
func TestSessionWait_FinishesCrashedRecordingsAndLeavesTheRestAlone(t *testing.T) {
	f := newWaitFixture(t)
	ran := stubSessionEnd(t, errors.New("session upload failed: content store unavailable"))
	crashed := f.record(t, "OxGone1", deadOwnerPID(t))
	live := f.record(t, "OxLive1", os.Getpid())
	quarantined := f.record(t, "OxHeld1", os.Getpid())
	require.NoError(t, session.MarkSourceRejected(f.projectRoot, "OxHeld1"))
	const held = "2026-10-20T09-00-coworker-OxManu1"
	require.NoError(t, session.WriteHoldMarker(f.leftover(t, held, "OxManu1", time.Now()), session.HoldManualPublishing, "stop"))

	out, err := runWait(t, time.Now().Add(-time.Hour))

	assert.Equal(t, []string{"OxGone1"}, *ran, "session end runs for the crashed AI coworker only")
	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+held+"/meta.json", "a held session is never published")
	assert.ElementsMatch(t, []sessionWaitFailure{
		{Session: crashed, Error: "session upload failed: content store unavailable"},
		{Session: live, Error: "still recording"},
		{Session: quarantined, Error: "held for ownership review"},
		{Session: held, Error: "held on this machine; publish it with ox session upload " + held},
	}, out.Failed)
	assert.Error(t, err)
}

// Sessions still waiting in the cache are published, and the report counts
// them; one from an earlier job is listed as older.
func TestSessionWait_PublishesWhatIsLeftAndCountsIt(t *testing.T) {
	f := newWaitFixture(t)
	const recent, older = "2026-10-20T10-00-coworker-OxNew1", "2026-10-18T09-00-coworker-OxOld1"
	f.leftover(t, recent, "OxNew1", time.Now())
	f.leftover(t, older, "OxOld1", time.Now().Add(-48*time.Hour))

	out, err := runWait(t, time.Now().Add(-time.Hour))

	tree := remoteTree(t, f.barePath)
	assert.Contains(t, tree, "sessions/"+recent+"/meta.json")
	assert.Contains(t, tree, "sessions/"+older+"/meta.json")
	assert.Equal(t, sessionWaitOutput{Since: 1, InLedger: 1, OlderPending: []string{older}, Failed: []sessionWaitFailure{}}, out)
	assert.NoError(t, err)
}

// An upload whose push failed is committed in the Ledger clone and reads as
// published there, so nothing else retries it; wait pushes it. While that push
// fails too, the report still comes out and names the session.
func TestSessionWait_PushesAnUploadWhosePushFailed(t *testing.T) {
	f := newWaitFixture(t)
	const name = "2026-10-20T09-30-coworker-OxPsh1"
	f.stranded(t, name, "OxPsh1")
	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing.git"))

	out, err := runWait(t, time.Now().Add(-time.Hour))

	require.Len(t, out.Failed, 1, "the report is printed when the push fails")
	assert.Equal(t, name, out.Failed[0].Session)
	assert.Contains(t, out.Failed[0].Error, "push the Ledger: ")
	assert.Error(t, err)

	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", f.barePath)

	out, err = runWait(t, time.Now().Add(-time.Hour))

	assert.Contains(t, remoteTree(t, f.barePath), "sessions/"+name+"/meta.json", "the stranded commit reaches the Ledger")
	assert.Equal(t, sessionWaitOutput{Since: 1, InLedger: 1, OlderPending: []string{}, Failed: []sessionWaitFailure{}}, out)
	assert.NoError(t, err)
}

// A session that cannot be published fails the run and stays on the machine.
// The coworker reading the report sees which session and why.
func TestSessionWait_FailedUploadExitsNonZero(t *testing.T) {
	f := newWaitFixture(t)
	cfg.JSON = false
	const name = "2026-10-20T10-00-coworker-OxFail1"
	dir := f.leftover(t, name, "OxFail1", time.Now())
	f.store.failBatch = true
	cmd := &cobra.Command{}
	since := time.Now().Add(-time.Hour)
	cmd.Flags().String("since", since.UTC().Format(time.RFC3339Nano), "")
	cmd.SetContext(context.Background())
	var report bytes.Buffer
	cmd.SetOut(&report)

	err := runSessionWait(cmd, nil)

	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+name+"/meta.json")
	assert.FileExists(t, filepath.Join(dir, "raw.jsonl"), "the session stays for the next try")
	assert.Contains(t, report.String(), "0 of 1 sessions finished since")
	assert.Contains(t, report.String(), "Not in the Ledger: "+name+": ")
	assert.Error(t, err)
}

// A Ledger whose remote state cannot be read is not reported as empty: the
// run fails, so a runner never reads "nothing missing" off a check that did
// not happen.
func TestSessionWait_UnreadableLedgerRemoteFailsTheRun(t *testing.T) {
	f := newWaitFixture(t)
	runGit(t, f.ledgerPath, "branch", "--unset-upstream")

	_, err := runWait(t, time.Now().Add(-time.Hour))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "check the Ledger for unpushed commits")
}

// Where the daemon outlives the session it owns uploads, so wait only reports:
// two uploaders on one session is how a Ledger wedges. It pushes nothing either.
func TestSessionWait_LeavesUploadsToADaemonThatOutlivesTheSession(t *testing.T) {
	f := newWaitFixture(t)
	const stranded = "2026-10-20T09-30-coworker-OxPsh3"
	f.stranded(t, stranded, "OxPsh3") // while the CLI owns uploads, so no daemon is reached
	t.Setenv("SAGEOX_DAEMON", "")
	runtime.Reset()
	require.True(t, daemonOutlivesSession(), "precondition: a laptop with a working daemon")
	ran := stubSessionEnd(t, nil)
	crashed := f.record(t, "OxGone2", deadOwnerPID(t))
	const waiting = "2026-10-20T10-00-coworker-OxLeft2"
	f.leftover(t, waiting, "OxLeft2", time.Now())

	out, err := runWait(t, time.Now().Add(-time.Hour))

	assert.Empty(t, *ran, "wait never runs session end where the daemon owns it")
	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+waiting+"/meta.json", "wait never publishes where the daemon owns uploads")
	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+stranded+"/meta.json", "wait never pushes where the daemon owns uploads")
	assert.ElementsMatch(t, []sessionWaitFailure{
		{Session: crashed, Error: "its AI coworker is gone; the daemon finishes it"},
		{Session: waiting, Error: "waiting for the daemon to upload it"},
		{Session: stranded, Error: "committed on this machine but not pushed"},
	}, out.Failed)
	assert.Error(t, err)
}

func TestSessionWait_RejectsUnusableInvocation(t *testing.T) {
	// A test earlier in this package can leave the process inside a real project,
	// and wait uploads whatever it finds there; one full run did exactly that.
	// Pin every way to reach real data.
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv(config.EnvProjectRoot, "")

	tests := []struct {
		name, since, want string
		legacyDir         bool // a bare .sageox folder, as the legacy ~/.sageox leaves in $HOME
	}{
		{"missing --since", "", "--since needs a time", false},
		{"malformed --since", "yesterday", "--since must be RFC3339", false},
		{"outside a SageOx project", "2026-10-12", "not in a SageOx project", false},
		{"under a legacy .sageox folder", "2026-10-12", "not in a SageOx project", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := t.TempDir()
			if tt.legacyDir {
				require.NoError(t, os.Mkdir(filepath.Join(cwd, ".sageox"), 0o755))
			}
			t.Chdir(cwd)
			cmd := &cobra.Command{}
			cmd.Flags().String("since", tt.since, "")
			cmd.SetContext(context.Background())

			err := runSessionWait(cmd, nil)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestPrintSessionWait_NamesEverySessionOutsideTheLedger(t *testing.T) {
	var buf bytes.Buffer
	printSessionWait(&buf, sessionWaitOutput{
		Since: 2, InLedger: 1, OlderPending: []string{"2026-10-10T09-00-coworker-OxOld01"},
		Failed: []sessionWaitFailure{{Session: "2026-10-12T19-00-coworker-OxFail1", Error: "content store refused the upload"}},
	}, time.Date(2026, 10, 12, 19, 0, 0, 0, time.UTC))

	assert.Equal(t, `1 of 2 sessions finished since 2026-10-12T19:00:00Z are in the Ledger.
Older pending session: 2026-10-10T09-00-coworker-OxOld01
Not in the Ledger: 2026-10-12T19-00-coworker-OxFail1: content store refused the upload
`, buf.String())
}
