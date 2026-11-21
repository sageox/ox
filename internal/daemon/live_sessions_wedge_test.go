package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/require"
)

// A live session's spool, stream state and raw.jsonl sit inside the Ledger
// checkout (<ledger>/.sageox/cache/sessions/<name>/). These tests wedge that
// checkout every way it wedges in the field and require that every turn a hook
// spools still reaches SageOx, in order, with nothing declared lost.

// wedgeLedgerName is in every wedged Ledger's path, so the git shim can tell a
// Ledger call from any other git the test process runs.
const wedgeLedgerName = "ledger-wedged"

// sandboxLiveHome points every per-user directory at a temp dir: nothing these
// tests run may read or write the real ~/.local/share/sageox.
func sandboxLiveHome(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	for name, dir := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache", "XDG_STATE_HOME": "state"} {
		t.Setenv(name, filepath.Join(home, dir))
	}
	userConfig := filepath.Join(home, "user-config.yaml")
	t.Setenv("OX_USER_CONFIG", userConfig)
	// no AI coworker CLI probe at setup: it execs claude, codex and gemini,
	// which is not what these tests time, and must not reach a real login
	require.NoError(t, os.WriteFile(userConfig, []byte("agent_worker:\n  agent: none\n"), 0o600))
	t.Setenv("DO_NOT_TRACK", "1")
	t.Setenv("SAGEOX_TELEMETRY", "false")
	t.Setenv(endpoint.EnvVar, "")
	t.Setenv("SAGEOX_TOKEN", "")
	isolateCredentialsWithDir(t)
	return home
}

// syncBuffer is a log sink safe for the daemon's concurrent writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// wedgedGit is a git shim placed first on PATH.
type wedgedGit struct {
	log, release string
}

// installWedgedGit puts a git shim first on PATH. mode "hang" blocks every
// call made in or about a Ledger path until release(), then fails it the way a
// killed git does. mode "network" leaves local git alone but hangs every
// fetch or pull and rejects every push as non-fast-forward.
func installWedgedGit(t *testing.T, mode string) *wedgedGit {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	dir := t.TempDir()
	g := &wedgedGit{log: filepath.Join(dir, "calls.log"), release: filepath.Join(dir, "release")}
	// values are baked in: ox runs some git with its own environment
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s :: %%s\n' "$PWD" "$*" >> '%[1]s'
hang() {
  i=0
  while [ ! -f '%[2]s' ] && [ $i -lt 2400 ]; do sleep 0.05; i=$((i+1)); done
  echo 'fatal: the hung git was killed' >&2
  exit 128
}
case '%[3]s' in
hang)
  case "$PWD :: $*" in *'%[4]s'*) hang ;; esac ;;
network)
  for a in "$@"; do
    case "$a" in
    fetch|pull) hang ;;
    push) echo ' ! [rejected]        main -> main (non-fast-forward)' >&2; echo 'error: failed to push some refs' >&2; exit 1 ;;
    esac
  done ;;
esac
exec '%[5]s' "$@"
`, g.log, g.release, mode, wedgeLedgerName, realGit)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(g.Release) // never leave a shim spinning after the test
	return g
}

func (g *wedgedGit) Release() { _ = os.WriteFile(g.release, nil, 0o600) }

func (g *wedgedGit) calls() []string {
	b, err := os.ReadFile(g.log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// waitForCall blocks until some git call mentions want: the wedge is in place.
func (g *wedgedGit) waitForCall(t *testing.T, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, c := range g.calls() {
			if strings.Contains(c, want) {
				return true
			}
		}
		return false
	}, 20*time.Second, 20*time.Millisecond, "no git call mentioning %q", want)
}

// holdRepoLock holds the Ledger's cross-process repo lock, as a CLI commit or
// push stalled on the network does, until the returned func is called.
func holdRepoLock(t *testing.T, ledgerDir string) (release func()) {
	t.Helper()
	held, done, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		_ = gitutil.WithRepoLock(context.Background(), ledgerDir, func() error {
			close(held)
			<-done
			return nil
		})
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("could not take the repo lock")
	}
	var once bool
	release = func() {
		if !once {
			once = true
			close(done)
			<-exited
		}
	}
	t.Cleanup(release)
	return release
}

// newWedgeLedgerRepo is a Ledger checkout with one commit, cloned from a bare
// origin, whose path carries wedgeLedgerName.
func newWedgeLedgerRepo(t *testing.T) (bare, ledgerDir string) {
	t.Helper()
	ledgerDir = filepath.Join(t.TempDir(), wedgeLedgerName)
	return newLedgerRepoAt(t, ledgerDir), ledgerDir
}

// newLedgerRepoAt is newWedgeLedgerRepo at a path the caller picks.
func newLedgerRepoAt(t *testing.T, ledgerDir string) (bare string) {
	t.Helper()
	root := t.TempDir()
	bare = filepath.Join(root, "origin.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(ledgerDir), 0o755))
	mustGitIn(t, root, "init", "--bare", "--initial-branch=main", bare)
	mustGitIn(t, root, "clone", "--quiet", bare, ledgerDir)
	mustGitIn(t, ledgerDir, "symbolic-ref", "HEAD", "refs/heads/main")
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "sessions", "index.md"), []byte("base\n"), 0o644))
	mustGitIn(t, ledgerDir, "add", "sessions/index.md")
	mustGitIn(t, ledgerDir, "commit", "-q", "-m", "base")
	mustGitIn(t, ledgerDir, "push", "-q", "-u", "origin", "main")
	return bare
}

func mustGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := runGitOut(t, dir, args...)
	require.NoError(t, err, "git %v: %s", args, out)
}

// newLiveEnvUnder is newLiveEnv with its sessions folder where a real one
// lives: inside a Ledger checkout's cache.
func newLiveEnvUnder(t *testing.T, sessionsDir string) *liveEnv {
	t.Helper()
	srv := livesessiontest.New(t)
	e := &liveEnv{t: t, srv: srv, cfg: srv.Config(t), sessionsDir: sessionsDir, clock: time.Now()}
	e.dir = filepath.Join(sessionsDir, "2026-11-19T09-15-person-a-OxWdg1")
	require.NoError(t, os.MkdirAll(e.dir, 0o755))
	logger := slog.New(slog.NewTextHandler(testLogWriter{t}, nil))
	e.live = NewLiveSessions(logger, sessionsDir, srv.URL)
	e.live.config = func(ep string) livesession.Config { c := e.cfg; c.Endpoint = ep; return c }
	e.live.now = func() time.Time { return e.clock }
	e.params = livesession.OpenParams{SessionID: sessionid.GenerateSessionID(), SessionName: filepath.Base(e.dir), RepoID: "repo_wedge", Agent: "claude-code", StartedAt: time.Now()}
	_, err := livesession.Prepare(t.Context(), e.cfg, e.dir, e.params)
	require.NoError(t, err)
	return e
}

func ledgerSessionsDir(ledgerDir string) string {
	return filepath.Join(ledgerDir, ".sageox", "cache", "sessions")
}

// turn spools one turn the way a hook does, then lets the daemon's tick run
// past any backoff, and records the turn in want.
func (e *liveEnv) turn(want *strings.Builder, n int) {
	e.t.Helper()
	line := lineN(n)
	want.WriteString(line)
	e.spool(line)
	e.clock = e.clock.Add(liveBackoffCap + liveBackoffSlack)
	e.live.Tick(e.t.Context())
	e.live.Wait()
}

// requireDelivered: SageOx holds every turn spooled so far, in order, the
// spool holds nothing else, and no turn was declared lost.
func requireDelivered(t *testing.T, e *liveEnv, want string) {
	t.Helper()
	require.Equal(t, want, e.spooled(), "the spool does not hold every turn in order")
	st := e.state()
	require.Equal(t, livesession.StatusStreaming, st.Status)
	require.EqualValues(t, len(want), st.Acked, "turns spooled but not acknowledged")
	require.Equal(t, want, e.stored(), "SageOx does not hold every turn, in order")
	require.NoFileExists(t, filepath.Join(e.dir, "spool-lost"), "a turn was declared lost")
}

// Mode a + c + d (self-heal): the Ledger's index.lock is held, a stalled CLI
// push holds the repo lock, and the daemon's own Ledger sync hangs inside git.
// When that git finally dies, the sync calls the Ledger corrupt and moves the
// checkout aside. Streaming must not notice any of it.
func TestLiveStreamWedge_LedgerSyncHungInGitThenMovedAside(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git Ledger and a hung git on PATH")
	}
	sandboxLiveHome(t)
	_, ledgerDir := newWedgeLedgerRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, ".git", "index.lock"), nil, 0o644))
	e := newLiveEnvUnder(t, ledgerSessionsDir(ledgerDir))
	git := installWedgedGit(t, "hang")

	var want strings.Builder
	e.turn(&want, 1)
	requireDelivered(t, e, want.String())
	require.Empty(t, git.calls(), "the streaming path ran git")

	releaseLock := holdRepoLock(t, ledgerDir)
	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	sched := NewSyncScheduler(cfg, discardLogger())
	pulled := make(chan error, 1)
	go func() { pulled <- sched.doPull(context.Background(), nil, true, true) }()
	git.waitForCall(t, wedgeLedgerName)

	for n := 2; n <= 4; n++ {
		e.turn(&want, n)
	}
	requireDelivered(t, e, want.String())
	select {
	case <-pulled:
		t.Fatal("precondition: the Ledger sync should still be hung in git")
	default:
	}

	git.Release()
	require.NoError(t, <-pulled)
	require.False(t, pathIsGitRepo(ledgerDir), "precondition: the self-heal moved the hung Ledger aside as corrupt")
	for n := 5; n <= 6; n++ {
		e.turn(&want, n)
	}
	requireDelivered(t, e, want.String())
	releaseLock()
}

// Mode c: the network half of the Ledger is broken. Every fetch and pull
// hangs and every push is rejected non-fast-forward; the daemon's sync sits
// in a hung pull holding its Ledger mutex and the repo lock.
func TestLiveStreamWedge_FetchHangsAndPushIsRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git Ledger and a hung git on PATH")
	}
	sandboxLiveHome(t)
	_, ledgerDir := newWedgeLedgerRepo(t)
	e := newLiveEnvUnder(t, ledgerSessionsDir(ledgerDir))
	git := installWedgedGit(t, "network")

	out, err := exec.Command("git", "-C", ledgerDir, "push", "origin", "HEAD:main").CombinedOutput()
	require.Error(t, err, "precondition: pushes are rejected")
	require.Contains(t, string(out), "non-fast-forward")

	var want strings.Builder
	e.turn(&want, 1)

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	sched := NewSyncScheduler(cfg, discardLogger())
	pulled := make(chan error, 1)
	go func() { pulled <- sched.doPull(context.Background(), nil, true, true) }()
	require.Eventually(t, func() bool {
		for _, c := range git.calls() {
			if strings.Contains(c, " fetch") || strings.Contains(c, " pull") {
				return true
			}
		}
		return false
	}, 20*time.Second, 20*time.Millisecond, "the sync never reached the network")

	for n := 2; n <= 4; n++ {
		e.turn(&want, n)
	}
	requireDelivered(t, e, want.String())
	select {
	case <-pulled:
		t.Fatal("precondition: the Ledger sync should still be hung in a fetch")
	default:
	}
	git.Release()
	<-pulled
	e.turn(&want, 5)
	requireDelivered(t, e, want.String())
}

// Mode b: the Ledger stopped mid-rebase on a conflict, conflict markers in its
// working tree, and the daemon's sync runs into it.
func TestLiveStreamWedge_LedgerMidRebaseWithConflictMarkers(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git rebase")
	}
	sandboxLiveHome(t)
	bare, ledgerDir := newWedgeLedgerRepo(t)
	other := filepath.Join(t.TempDir(), "teammate")
	mustGitIn(t, filepath.Dir(other), "clone", "--quiet", bare, other)
	require.NoError(t, os.WriteFile(filepath.Join(other, "sessions", "index.md"), []byte("teammate\n"), 0o644))
	mustGitIn(t, other, "commit", "-q", "-am", "teammate")
	mustGitIn(t, other, "push", "-q", "origin", "HEAD:main")
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "sessions", "index.md"), []byte("mine\n"), 0o644))
	mustGitIn(t, ledgerDir, "commit", "-q", "-am", "mine")
	mustGitIn(t, ledgerDir, "fetch", "-q", "origin")
	_, err := runGitOut(t, ledgerDir, "rebase", "origin/main")
	require.Error(t, err, "precondition: the rebase stops on a conflict")
	require.True(t, gitutil.IsRebaseInProgress(ledgerDir))
	marked, err := os.ReadFile(filepath.Join(ledgerDir, "sessions", "index.md"))
	require.NoError(t, err)
	require.Contains(t, string(marked), "<<<<<<<", "precondition: conflict markers in the tree")

	e := newLiveEnvUnder(t, ledgerSessionsDir(ledgerDir))
	var want strings.Builder
	e.turn(&want, 1)
	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	_ = NewSyncScheduler(cfg, discardLogger()).doPull(context.Background(), nil, true, true)
	for n := 2; n <= 3; n++ {
		e.turn(&want, n)
	}
	requireDelivered(t, e, want.String())
}

// Mode d (reclone): a wedged Ledger is recloned blue-green while the session
// streams, and a hook spools a turn inside the swap.
func TestLiveStreamWedge_GCRecloneWhileStreaming(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Ledger clones")
	}
	sandboxLiveHome(t)
	bare, clone, scheduler := setupClonedLedger(t)
	e := newLiveEnvUnder(t, ledgerSessionsDir(clone))
	var want strings.Builder
	e.turn(&want, 1)
	requireDelivered(t, e, want.String())

	scheduler.gcSwapWindowTestHook = func() {
		line := lineN(2)
		want.WriteString(line)
		e.spool(line)
	}
	ws := WorkspaceState{ID: "ledger", Type: WorkspaceTypeLedger, Path: clone, CloneURL: "file://" + bare, Exists: true}
	require.Equal(t, gcSuccess, scheduler.runBlueGreenGC(context.Background(), ws))

	e.turn(&want, 3)
	requireDelivered(t, e, want.String())
}

// Mode d (reclone, adversarial): the Ledger is a repo anyone on the team can
// push to. One tracked file under .sageox/cache, sorting before sessions/,
// must not leave every live session behind in the clone the reclone sets
// aside.
func TestLiveStreamWedge_GCRecloneWhenTheLedgerTracksACacheFile(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Ledger clones")
	}
	sandboxLiveHome(t)
	bare, clone, scheduler := setupClonedLedger(t)
	planted := filepath.Join(clone, ".sageox", "cache", "codedb", "planted.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(planted), 0o755))
	require.NoError(t, os.WriteFile(planted, []byte("pushed by a teammate\n"), 0o644))
	mustGitIn(t, clone, "add", "-f", ".sageox/cache/codedb/planted.txt")
	mustGitIn(t, clone, "-c", "user.name=Person A", "-c", "user.email=person@test.sageox.ai", "commit", "-q", "-m", "plant")
	mustGitIn(t, clone, "push", "-q", "origin", "HEAD:main")

	e := newLiveEnvUnder(t, ledgerSessionsDir(clone))
	var want strings.Builder
	e.turn(&want, 1)

	gitBefore, err := os.Stat(filepath.Join(clone, ".git"))
	require.NoError(t, err)
	ws := WorkspaceState{ID: "ledger", Type: WorkspaceTypeLedger, Path: clone, CloneURL: "file://" + bare, Exists: true}
	require.Equal(t, gcSuccess, scheduler.runBlueGreenGC(context.Background(), ws))
	gitAfter, err := os.Stat(filepath.Join(clone, ".git"))
	require.NoError(t, err)
	require.False(t, os.SameFile(gitBefore, gitAfter), "GC never published a replacement clone, so the carry was not exercised")
	old := gcBackupPath(clone)
	require.FileExists(t, filepath.Join(old, ".sageox", "cache", "codedb", "planted.txt"),
		"the conflicting file stays in the retained old clone: nothing is deleted")
	require.NoDirExists(t, filepath.Join(old, ".sageox", "cache", "sessions", filepath.Base(e.dir)),
		"the live session was left behind in the set-aside clone")
	require.DirExists(t, e.dir)
	e.turn(&want, 2)
	requireDelivered(t, e, want.String())
}

// Mode f: the Ledger was never cloned (a fresh repo, or a clone that keeps
// failing). The session streams from the cache-only skeleton, and keeps
// streaming when the clone finally lands and adopts that cache.
func TestLiveStreamWedge_LedgerNotClonedYetThenClonedUnderneath(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone")
	}
	sandboxLiveHome(t)
	bare, seed := newWedgeLedgerRepo(t)
	require.NoError(t, os.RemoveAll(seed))
	ledgerDir := filepath.Join(t.TempDir(), wedgeLedgerName)
	e := newLiveEnvUnder(t, ledgerSessionsDir(ledgerDir))
	require.False(t, pathIsGitRepo(ledgerDir), "precondition: no clone yet")

	var want strings.Builder
	e.turn(&want, 1)
	e.turn(&want, 2)
	requireDelivered(t, e, want.String())

	temp := ledgerDir + ".tmp-clone"
	mustGitIn(t, filepath.Dir(ledgerDir), "clone", "--quiet", bare, temp)
	require.NoError(t, gitserver.MoveCloneIntoPlace(temp, ledgerDir))
	require.True(t, pathIsGitRepo(ledgerDir), "precondition: the clone landed")

	e.turn(&want, 3)
	requireDelivered(t, e, want.String())
}

// startupBudget is how long daemon setup may take before the live-session
// drainer starts. Far under ADR-247 D6's 60 s from write to visible.
const startupBudget = 5 * time.Second

// runWithin runs fn and fails the test if it takes longer than budget. On a
// timeout it releases the wedge first, so fn can return and the test can end.
func runWithin(t *testing.T, budget time.Duration, release func(), what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(budget):
		release()
		<-done
		t.Fatalf("%s waited more than %s on the wedged Ledger", what, budget)
	}
}

// startupProject is a project whose config names repoID.
func startupProject(t *testing.T, repoID string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"), []byte(`{"config_version":"2","repo_id":"`+repoID+`"}`), 0o644))
	return root
}

// Mode a at daemon start: a daemon restarted (upgrade, inactivity exit, crash)
// while the Ledger is wedged must still stream. Setup used to refresh the
// Ledger's sparse checkout synchronously, before the drainer was even built:
// up to 2 minutes behind a held repo lock, with no bound at all when git
// hangs. This runs the real setup and the real workers with the Ledger still
// wedged and requires a spooled turn to reach SageOx before the wedge clears.
func TestLiveStreamWedge_DaemonSetupDoesNotWaitOnTheLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git Ledger and daemon workers")
	}
	for _, tc := range []struct {
		name  string
		wedge func(t *testing.T, ledgerDir string) (release func())
	}{
		{"a peer holds the Ledger repo lock", holdRepoLock},
		{"git hangs in the Ledger", func(t *testing.T, _ string) func() { return installWedgedGit(t, "hang").Release }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := sandboxLiveHome(t)
			_, ledgerDir := newWedgeLedgerRepo(t)
			release := tc.wedge(t, ledgerDir)

			cfg := DefaultConfig()
			cfg.ProjectRoot = startupProject(t, "repo_wedge_startup")
			cfg.LedgerPath = ledgerDir
			cfg.VersionCheckInterval, cfg.GitHubSyncInterval = 0, 0
			d := New(cfg, discardLogger())
			d.ctx, d.cancel = context.WithCancel(context.Background())
			started := false
			// registered after every Setenv, so it runs first: no daemon
			// goroutine may outlive the sandboxed environment
			t.Cleanup(func() {
				d.cancel()
				release()
				done := make(chan struct{})
				go func() { d.wg.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(60 * time.Second):
					t.Error("daemon workers outlived the test")
				}
				if started {
					d.telemetry.Stop()
					d.friction.Stop()
				}
				if d.whisperRegistry != nil {
					_ = d.whisperRegistry.Close()
				}
				d.releaseGlobalSyncLease()
			})

			runWithin(t, startupBudget, release, "daemon setup", func() { d.initComponents() })
			require.NotNil(t, d.liveSessions, "no live-session drainer was built")

			srv := livesessiontest.New(t)
			lcfg := srv.Config(t)
			d.liveSessions.endpoint = srv.URL
			d.liveSessions.config = func(ep string) livesession.Config { c := lcfg; c.Endpoint = ep; return c }
			dir := filepath.Join(d.liveSessions.sessionsDir, "2026-11-19T09-20-person-a-OxWdg2")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			_, err := livesession.Prepare(t.Context(), lcfg, dir, livesession.OpenParams{
				SessionID: sessionid.GenerateSessionID(), SessionName: filepath.Base(dir), RepoID: "repo_wedge_startup", Agent: "claude-code", StartedAt: time.Now(),
			})
			require.NoError(t, err)
			_, err = livesession.Spool(t.Context(), dir, []byte(lineN(1)), livesession.SpoolOptions{})
			require.NoError(t, err)

			// Workers that call out or outlive their context stay off: the
			// settings and GitHub fetches sleep, then use whatever HOME is then;
			// the code index runs untracked. Live sessions need none of them.
			d.config.ProjectRoot = ""
			d.codedb, d.agentWorker = nil, nil
			d.scheduler.settingsFetcher, d.scheduler.githubSync = nil, nil
			d.service = &daemonServiceImpl{d}
			d.server = NewServerWithService(d.logger, d.service)
			for _, p := range []string{d.liveSessions.sessionsDir, SocketPath(), paths.DataDir(), paths.ConfigDir()} {
				require.Truef(t, strings.HasPrefix(p, home) || strings.HasPrefix(p, os.Getenv("XDG_RUNTIME_DIR")), "%s is outside the sandbox", p)
			}

			started = true
			runWithin(t, startupBudget, release, "starting the daemon workers", d.startWorkers)
			require.Eventually(t, func() bool {
				st, err := livesession.ReadState(dir)
				return err == nil && st.Acked == int64(len(lineN(1)))
			}, 10*time.Second, 20*time.Millisecond, "the turn never reached SageOx while the Ledger was wedged")
			st, err := livesession.ReadState(dir)
			require.NoError(t, err)
			require.Equal(t, lineN(1), string(srv.Stored(t, st.ConversationID, st.LayerID)))
			require.NoFileExists(t, filepath.Join(ledgerDir, ".git", "info", "sparse-checkout"),
				"precondition: the wedge held, so the sparse refresh had not run")
		})
	}
}

// Thread from review: setup must not open the Ledger's whisper database
// through a tracked symlink, whether or not git can neutralize the link in
// time. The Ledger is a repo anyone on the team can push to.
func TestLiveStreamWedge_SetupRefusesASymlinkedWhisperCache(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git Ledger and daemon setup")
	}
	for _, tc := range []struct {
		name string
		hang bool
	}{
		{"git neutralizes the link", false},
		{"git hangs in the Ledger", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := sandboxLiveHome(t)
			const repoID = "repo_" + wedgeLedgerName + "_whisper"
			root := startupProject(t, repoID)
			ledgerDir := paths.LedgersDataDir(repoID, endpoint.GetForProject(root))
			require.True(t, strings.HasPrefix(ledgerDir, home), "the Ledger must be in the sandbox: %s", ledgerDir)
			newLedgerRepoAt(t, ledgerDir)
			outside := t.TempDir()
			link := filepath.Join(ledgerDir, ".sageox", "cache", "whisper")
			require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
			require.NoError(t, os.Symlink(outside, link))
			mustGitIn(t, ledgerDir, "add", "-f", ".sageox/cache/whisper")
			mustGitIn(t, ledgerDir, "commit", "-q", "-m", "plant a cache symlink")
			release := func() {}
			if tc.hang {
				release = installWedgedGit(t, "hang").Release
			}

			logs := &syncBuffer{}
			cfg := DefaultConfig()
			cfg.ProjectRoot = root
			cfg.LedgerPath = ledgerDir
			d := New(cfg, slog.New(slog.NewTextHandler(logs, nil)))
			d.ctx, d.cancel = context.WithCancel(context.Background())
			t.Cleanup(func() {
				d.cancel()
				if d.whisperRegistry != nil {
					_ = d.whisperRegistry.Close()
				}
				d.releaseGlobalSyncLease()
			})
			runWithin(t, startupBudget, release, "daemon setup", func() { d.initComponents() })

			opened, err := os.ReadDir(outside)
			require.NoError(t, err)
			require.Empty(t, opened, "setup opened the whisper database through a tracked symlink, outside the Ledger")
			require.Nil(t, d.whisperRegistry)
			require.Contains(t, logs.String(), "level=ERROR msg=whisper_store_refused")
		})
	}
}
