package main

// `ox session wait` (#1131) is a runner's last step on a machine that goes away
// after its job. Where the daemon outlives the session it uploads sessions, as
// on a laptop, and wait waits for it; elsewhere wait only reports. These tests
// prove what wait decides: what it hands the daemon, what it waits for and
// counts, and that it uploads nothing itself. A fake stands in for the daemon.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitFixture is a project whose Ledger is cloned where ox keeps it, on the
// data home, from a bare remote, with a fake content store. The daemon is off
// unless a test hands uploads to a fake one.
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
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
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

func deadOwnerPID(t *testing.T) int {
	t.Helper()
	owner := exec.Command("true")
	require.NoError(t, owner.Start())
	require.NoError(t, owner.Wait())
	return owner.Process.Pid
}

// fakeDaemon stands in for the daemon behind the finalize request session end
// sends. Like the daemon, it uploads after the request returns, one session at
// a time, here through ox's own publish path, which lands a session in the
// Ledger as the daemon's upload would.
type fakeDaemon struct {
	started bool
	asked   []string
}

// daemonOwnsUploads makes this a machine whose daemon outlives the session,
// with a fake in its place. uploads says whether the fake uploads what it is
// handed.
func (f *waitFixture) daemonOwnsUploads(t *testing.T, uploads bool) *fakeDaemon {
	t.Helper()
	t.Setenv("SAGEOX_DAEMON", "")
	require.True(t, daemonOutlivesSession(), "precondition: a machine whose daemon outlives the session")
	d := &fakeDaemon{}
	queue, idle := make(chan daemon.SessionFinalizeIPCPayload, 16), make(chan struct{})
	go func() {
		defer close(idle)
		for p := range queue {
			// the daemon summarizes first, so a session lands well after the request
			time.Sleep(100 * time.Millisecond)
			if err := publishCachedSession(f.projectRoot, f.ledgerPath, p.SessionName, p.CachePath); err != nil {
				t.Errorf("fake daemon: upload %s: %v", p.SessionName, err)
			}
		}
	}()
	priorEnsure, priorSend := ensureDaemonForWait, sendHookFinalizeIPC
	t.Cleanup(func() {
		close(queue)
		<-idle
		ensureDaemonForWait, sendHookFinalizeIPC = priorEnsure, priorSend
	})
	ensureDaemonForWait = func() error {
		d.started = true
		return nil
	}
	sendHookFinalizeIPC = func(p daemon.SessionFinalizeIPCPayload) error {
		d.asked = append(d.asked, p.SessionName)
		if uploads {
			queue <- p
		}
		return nil
	}
	return d
}

// waitCommand is `ox session wait --since <since> --timeout <timeout>`.
func waitCommand(since time.Time, timeout time.Duration) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("since", since.UTC().Format(time.RFC3339Nano), "")
	cmd.Flags().Duration("timeout", timeout, "")
	cmd.SetContext(context.Background())
	return cmd
}

// runWait runs `ox session wait --since <since> --timeout <timeout> --json`.
func runWait(t *testing.T, since time.Time, timeout time.Duration) (sessionWaitOutput, error) {
	t.Helper()
	cmd := waitCommand(since, timeout)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	err := runSessionWait(cmd, nil)
	var out sessionWaitOutput
	if buf.Len() > 0 {
		require.NoError(t, json.Unmarshal(buf.Bytes(), &out), buf.String())
	}
	return out, err
}

// Where the daemon outlives the session, wait starts it, hands it every
// session whose recording has ended, as session end does, and waits until the
// daemon has put them in the Ledger. One from an earlier job is older pending.
func TestSessionWait_WaitsForTheDaemonToUploadEachSession(t *testing.T) {
	f := newWaitFixture(t)
	d := f.daemonOwnsUploads(t, true)
	const recent, older = "2026-10-20T10-00-coworker-OxNew1", "2026-10-18T09-00-coworker-OxOld1"
	f.leftover(t, recent, "OxNew1", time.Now())
	f.leftover(t, older, "OxOld1", time.Now().Add(-48*time.Hour))

	out, err := runWait(t, time.Now().Add(-time.Hour), 30*time.Second)

	assert.True(t, d.started, "wait starts the daemon when it is not running")
	assert.ElementsMatch(t, []string{recent, older}, d.asked)
	tree := remoteTree(t, f.barePath)
	assert.Contains(t, tree, "sessions/"+recent+"/meta.json")
	assert.Contains(t, tree, "sessions/"+older+"/meta.json")
	assert.Equal(t, sessionWaitOutput{Since: 1, InLedger: 1, OlderPending: []string{older}, Failed: []sessionWaitFailure{}}, out)
	assert.NoError(t, err)
}

// What the daemon has not uploaded when the timeout passes is reported with
// why, and fails the run. wait hands the daemon neither a crashed recording,
// whose last entries the daemon's dead-agent check drains first, nor a held
// session, and it publishes and pushes nothing itself.
func TestSessionWait_ReportsEverySessionTheDaemonDidNotUpload(t *testing.T) {
	f := newWaitFixture(t)
	const stranded = "2026-10-20T09-30-coworker-OxPsh1"
	f.stranded(t, stranded, "OxPsh1") // while the CLI owns uploads, so no daemon is reached
	d := f.daemonOwnsUploads(t, false)
	cfg.JSON = false
	crashed := f.record(t, "OxGone1", deadOwnerPID(t))
	live := f.record(t, "OxLive1", os.Getpid())
	quarantined := f.record(t, "OxHeld1", os.Getpid())
	require.NoError(t, session.MarkSourceRejected(f.projectRoot, "OxHeld1"))
	const waiting, held = "2026-10-20T10-00-coworker-OxLeft1", "2026-10-20T09-00-coworker-OxManu1"
	f.leftover(t, waiting, "OxLeft1", time.Now())
	require.NoError(t, session.WriteHoldMarker(f.leftover(t, held, "OxManu1", time.Now()), session.HoldManualPublishing, "stop"))
	cmd := waitCommand(time.Now().Add(-time.Hour), 200*time.Millisecond)
	var report bytes.Buffer
	cmd.SetOut(&report)

	err := runSessionWait(cmd, nil)

	assert.Equal(t, []string{waiting}, d.asked, "only a session whose recording has ended is handed to the daemon")
	tree := remoteTree(t, f.barePath)
	assert.NotContains(t, tree, "sessions/"+waiting+"/meta.json", "wait uploads nothing itself")
	assert.NotContains(t, tree, "sessions/"+stranded+"/meta.json", "wait pushes nothing itself")
	for name, reason := range map[string]string{
		crashed:     "its AI coworker exited without ending the session; waiting for the daemon to finish it",
		live:        "still recording",
		quarantined: "held for ownership review",
		waiting:     "waiting for the daemon to upload it",
		held:        "held on this machine; publish it with ox session upload " + held,
		stranded:    "committed on this machine but not pushed",
	} {
		assert.Contains(t, report.String(), "Not in the Ledger: "+name+": "+reason+"\n")
	}
	assert.Error(t, err)
}

// Where no daemon outlives the session, session end has uploaded what it
// could. wait starts no daemon and does not wait; it names what will publish
// each session that is left.
func TestSessionWait_OnlyReportsWhereNoDaemonUploads(t *testing.T) {
	f := newWaitFixture(t)
	priorEnsure, priorSend := ensureDaemonForWait, sendHookFinalizeIPC
	t.Cleanup(func() { ensureDaemonForWait, sendHookFinalizeIPC = priorEnsure, priorSend })
	ensureDaemonForWait = func() error {
		t.Error("wait started a daemon where none uploads")
		return nil
	}
	sendHookFinalizeIPC = func(daemon.SessionFinalizeIPCPayload) error {
		t.Error("wait handed a session to a daemon where none uploads")
		return nil
	}
	crashed := f.record(t, "OxGone2", deadOwnerPID(t))
	const waiting = "2026-10-20T10-00-coworker-OxLeft2"
	f.leftover(t, waiting, "OxLeft2", time.Now())

	start := time.Now()
	out, err := runWait(t, time.Now().Add(-time.Hour), time.Minute)

	assert.Less(t, time.Since(start), 30*time.Second, "wait has nothing to wait for")
	assert.Equal(t, []sessionWaitFailure{
		{Session: crashed, Error: "its AI coworker exited without ending the session; recover it with ox agent OxGone2 session recover"},
		{Session: waiting, Error: "not uploaded; publish it with ox session upload " + waiting},
	}, out.Failed)
	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+waiting+"/meta.json")
	assert.Error(t, err)
}

// A Ledger whose remote state cannot be read is not reported as empty: the
// run fails, so a runner never reads "nothing missing" off a check that did
// not happen.
func TestSessionWait_UnreadableLedgerRemoteFailsTheRun(t *testing.T) {
	f := newWaitFixture(t)
	runGit(t, f.ledgerPath, "branch", "--unset-upstream")

	_, err := runWait(t, time.Now().Add(-time.Hour), time.Minute)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "check the Ledger for unpushed commits")
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
