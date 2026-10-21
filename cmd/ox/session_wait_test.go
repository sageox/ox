package main

// `ox session wait` (#1131): where the daemon uploads sessions, wait hands them
// to it and waits; elsewhere it only reports. A fake stands in for the daemon.

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

// waitFixture is a project whose Ledger is cloned where ox keeps it, from a
// bare remote, with a fake content store. The daemon is off unless a test hands
// uploads to a fake one.
type waitFixture struct {
	projectRoot string
	ledgerPath  string
	barePath    string
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
	return &waitFixture{projectRoot: projectRoot, ledgerPath: ledgerPath, barePath: barePath}
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

// fakeDaemon stands in for the daemon: it uploads what it is handed after the
// request returns, one session at a time, through ox's own publish path.
type fakeDaemon struct {
	started bool
	asked   []string
}

// daemonOwnsUploads makes the daemon own uploads here, with a fake in its
// place that uploads what it is handed only when uploads is set.
func (f *waitFixture) daemonOwnsUploads(t *testing.T, uploads bool) *fakeDaemon {
	t.Helper()
	t.Setenv("SAGEOX_DAEMON", "")
	require.True(t, daemonOutlivesSession(), "precondition: a machine whose daemon outlives the session")
	d := &fakeDaemon{}
	queue, idle := make(chan daemon.SessionFinalizeIPCPayload, 16), make(chan struct{})
	go func() {
		defer close(idle)
		for p := range queue {
			time.Sleep(100 * time.Millisecond) // the daemon summarizes first
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

// wait hands the daemon every ended session and waits until both are in the
// Ledger; the one from an earlier job is older pending.
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

// After the timeout, every session the daemon did not upload is reported with
// why. wait hands over neither a crashed recording nor a held session, and it
// publishes and pushes nothing itself.
func TestSessionWait_ReportsEverySessionTheDaemonDidNotUpload(t *testing.T) {
	f := newWaitFixture(t)
	// a session committed in the clone whose push failed, made while the CLI owns uploads
	const stranded = "2026-10-20T09-30-coworker-OxPsh1"
	dir := f.leftover(t, stranded, "OxPsh1", time.Now())
	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing.git"))
	require.Error(t, publishCachedSession(f.projectRoot, f.ledgerPath, stranded, dir))
	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", f.barePath)
	d := f.daemonOwnsUploads(t, false)
	cfg.JSON = false
	crashed := f.record(t, "OxGone1", deadOwnerPID(t))
	live := f.record(t, "OxLive1", os.Getpid())
	quarantined := f.record(t, "OxHeld1", os.Getpid())
	require.NoError(t, session.MarkSourceRejected(f.projectRoot, "OxHeld1"))
	const waiting, held = "2026-10-20T10-00-coworker-OxLeft1", "2026-10-20T09-00-coworker-OxManu1"
	f.leftover(t, waiting, "OxLeft1", time.Now())
	require.NoError(t, session.WriteHoldMarker(f.leftover(t, held, "OxManu1", time.Now()), session.HoldManualPublishing, "stop"))
	// a session the daemon is still writing into the clone reads as published there
	const writing = "2026-10-20T09-45-coworker-OxMid1"
	f.leftover(t, writing, "OxMid1", time.Now())
	require.NoError(t, os.MkdirAll(filepath.Join(f.ledgerPath, "sessions", writing), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.ledgerPath, "sessions", writing, "meta.json"), []byte(`{"session_name":"`+writing+`"}`), 0o644))
	cmd := waitCommand(time.Now().Add(-time.Hour), 200*time.Millisecond)
	var report bytes.Buffer
	cmd.SetOut(&report)

	err := runSessionWait(cmd, nil)

	assert.Equal(t, []string{waiting}, d.asked, "only a session whose recording has ended is handed to the daemon")
	tree := remoteTree(t, f.barePath)
	assert.NotContains(t, tree, "sessions/"+waiting+"/meta.json", "wait uploads nothing itself")
	assert.NotContains(t, tree, "sessions/"+stranded+"/meta.json", "wait pushes nothing itself")
	for name, reason := range map[string]string{
		crashed:     "AI coworker exited without ending the session; waiting for the daemon",
		live:        "still recording",
		quarantined: "held for ownership review",
		waiting:     "waiting for the daemon to upload it",
		held:        "held on this machine; run ox session upload " + held,
		stranded:    "in the Ledger clone but not pushed",
		writing:     "in the Ledger clone but not pushed",
	} {
		assert.Contains(t, report.String(), "Not in the Ledger: "+name+": "+reason+"\n")
	}
	assert.Contains(t, report.String(), "0 of 3 sessions finished since", "crashed, waiting and unpushed count; the rest have no stop time")
	assert.Error(t, err)
}

// With no daemon uploading, wait starts none and does not wait; it names the
// command that publishes each session left.
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
		{Session: crashed, Error: "AI coworker exited without ending the session; run ox agent OxGone2 session recover"},
		{Session: waiting, Error: "not uploaded; run ox session upload " + waiting},
	}, out.Failed)
	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+waiting+"/meta.json")
	assert.Error(t, err)
}

// A Ledger whose remote cannot be read fails the run instead of reading as
// "nothing missing".
func TestSessionWait_UnreadableLedgerRemoteFailsTheRun(t *testing.T) {
	f := newWaitFixture(t)
	runGit(t, f.ledgerPath, "branch", "--unset-upstream")

	_, err := runWait(t, time.Now().Add(-time.Hour), time.Minute)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "check the Ledger for unpushed changes")
}

// A teammate's session the clone has fetched but not merged is already on the
// remote, so wait does not report it.
func TestSessionWait_IgnoresSessionsOnlyOnTheRemote(t *testing.T) {
	f := newWaitFixture(t)
	branch := runGit(t, f.ledgerPath, "rev-parse", "--abbrev-ref", "HEAD")
	teammate := filepath.Join(t.TempDir(), "teammate")
	runGit(t, filepath.Dir(teammate), "clone", "--branch", branch, f.barePath, teammate)
	dir := filepath.Join(teammate, "sessions", "2026-10-20T08-00-teammate-OxMate1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{}`), 0o644))
	runGit(t, teammate, "add", "sessions")
	runGit(t, teammate, "commit", "--no-verify", "-m", "teammate session")
	runGit(t, teammate, "push")
	runGit(t, f.ledgerPath, "fetch", f.barePath, "+"+branch+":"+runGit(t, f.ledgerPath, "rev-parse", "--symbolic-full-name", "@{upstream}"))

	out, err := runWait(t, time.Now().Add(-time.Hour), time.Minute)

	assert.Empty(t, out.Failed)
	assert.NoError(t, err)
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
		sageox            string // "": no .sageox folder; "bare": as the legacy ~/.sageox leaves in $HOME; "initialized": no Ledger cloned yet
	}{
		{"missing --since", "", "--since needs a time", ""},
		{"malformed --since", "yesterday", "--since must be RFC3339", ""},
		{"outside a SageOx project", "2026-10-12", "not in a SageOx project", ""},
		{"under a legacy .sageox folder", "2026-10-12", "not in a SageOx project", "bare"},
		{"before the Ledger is cloned", "2026-10-12", "ox doctor --fix", "initialized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := t.TempDir()
			if tt.sageox != "" {
				require.NoError(t, os.Mkdir(filepath.Join(cwd, ".sageox"), 0o755))
			}
			if tt.sageox == "initialized" {
				require.NoError(t, os.WriteFile(filepath.Join(cwd, ".sageox", "config.json"), []byte(`{"config_version":"2","repo_id":"repo_noledger"}`), 0o644))
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
