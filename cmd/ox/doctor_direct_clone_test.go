package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With no daemon running, `ox doctor --fix` clones a missing Ledger itself.
// A Ledger is cloned with its whole history, so how long that takes is set by
// the coworker's link, not by ox. These tests serve a real Ledger through
// git's smart HTTP backend, paced so a small fixture downloads as slowly as a
// large Ledger does on a home connection, and drive doctor's real repair
// paths end to end with the daemon down.

const (
	// directPace and directFixtureBytes make one fixture clone take ~5s.
	directPace         = 64 << 10 // bytes per second
	directFixtureBytes = 320 << 10
	directChunk        = 4 << 10 // bytes the server sends between pauses
)

// servePacedGitRepo serves a bare Ledger holding blobBytes of incompressible
// history at bytesPerSec and returns its clone URL. stallAfter > 0 makes every
// response stop after that many bytes and hold the connection open, the way a
// dead link looks to curl, until the client hangs up.
func servePacedGitRepo(t *testing.T, blobBytes, bytesPerSec, stallAfter int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("CGI Git HTTP fixture requires Unix process semantics")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	require.NoError(t, os.Mkdir(source, 0o700))
	hostedTestGit(t, source, "init", "-b", "main")
	hostedTestGit(t, source, "config", "user.name", "Test")
	hostedTestGit(t, source, "config", "user.email", "test@example.invalid")
	// incompressible, so the pack is as large as the blob; seeded, so every
	// run moves the same bytes
	blob := make([]byte, blobBytes)
	_, _ = rand.NewChaCha8([32]byte{'o', 'x'}).Read(blob)
	require.NoError(t, os.Mkdir(filepath.Join(source, "sessions"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "sessions", "history.bin"), blob, 0o600))
	hostedTestGit(t, source, "add", "--all")
	hostedTestGit(t, source, "commit", "-m", "seed Ledger history")
	cloneBareRemote(t, source, filepath.Join(root, "ledger.git"))

	backend := &cgi.Handler{
		Path: filepath.Join(hostedTestGit(t, root, "--exec-path"), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend.ServeHTTP(&pacedResponse{
			ResponseWriter: w,
			ctx:            r.Context(),
			release:        release,
			pause:          time.Duration(directChunk) * time.Second / time.Duration(bytesPerSec),
			stallAfter:     stallAfter,
		}, r)
	}))
	// cleanups run last-in first-out: unblock stalled handlers, then close
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	return server.URL + "/ledger.git"
}

// pacedResponse sends a response directChunk bytes at a time, pausing between
// chunks, and flushes each chunk so the client sees steady progress.
type pacedResponse struct {
	http.ResponseWriter
	ctx        context.Context
	release    <-chan struct{}
	pause      time.Duration
	stallAfter int
	sent       int
}

func (p *pacedResponse) Write(b []byte) (int, error) {
	written := 0
	for len(b) > 0 {
		n := min(len(b), directChunk)
		if p.stallAfter > 0 {
			n = min(n, p.stallAfter-p.sent)
		}
		if n > 0 {
			m, err := p.ResponseWriter.Write(b[:n])
			written += m
			p.sent += m
			if err != nil {
				return written, err
			}
			if f, ok := p.ResponseWriter.(http.Flusher); ok {
				f.Flush()
			}
			b = b[n:]
		}
		if p.stallAfter > 0 && p.sent >= p.stallAfter {
			// a dead link: nothing more arrives until the client gives up.
			// The error makes cgi.Handler kill git-http-backend.
			select {
			case <-p.ctx.Done():
				return written, p.ctx.Err()
			case <-p.release:
				return written, http.ErrHandlerTimeout
			}
		}
		select {
		case <-p.ctx.Done():
			return written, p.ctx.Err()
		case <-time.After(p.pause):
		}
	}
	return written, nil
}

// setDirectCloneBudgets shrinks doctor's direct-clone budgets for one test:
// fixed is the team-context budget, the fixed 60s a Ledger clone used to
// share; backstop is the Ledger's own wall-clock limit. Progress is sampled
// and printed often enough to see in a download lasting a few seconds.
func setDirectCloneBudgets(t *testing.T, fixed, backstop time.Duration) {
	t.Helper()
	prevFixed, prevBackstop := directTeamContextCloneTimeout, directLedgerCloneTimeout
	prevSample, prevPrint := directCloneSampleEvery, checkoutProgressEvery
	directTeamContextCloneTimeout, directLedgerCloneTimeout = fixed, backstop
	directCloneSampleEvery, checkoutProgressEvery = 50*time.Millisecond, time.Second
	t.Cleanup(func() {
		directTeamContextCloneTimeout, directLedgerCloneTimeout = prevFixed, prevBackstop
		directCloneSampleEvery, checkoutProgressEvery = prevSample, prevPrint
	})
}

// setupDirectCloneProject is an initialized, logged-in project with no Ledger
// on disk, a cloud API whose Ledger is served at ledgerURL, and no daemon.
func setupDirectCloneProject(t *testing.T, ledgerURL string) string {
	t.Helper()
	root := setupDoctorProject(t, nil, ledgerURL)
	require.False(t, daemon.IsRunning(), "doctor must take the direct-clone path")
	return root
}

// directLedgerRepair is one of doctor's repairs that clones a Ledger.
type directLedgerRepair struct {
	name string
	run  func(t *testing.T, root, ledgerURL string) (result checkResult, ledgerPath string)
}

func directLedgerRepairs() []directLedgerRepair {
	return []directLedgerRepair{
		{
			name: "first Ledger clone",
			run: func(t *testing.T, root, _ string) (checkResult, string) {
				localCfg := &config.LocalConfig{}
				result := fixMissingRepos(t.Context(), root, localCfg)
				ledgerPath := ""
				if localCfg.Ledger != nil {
					ledgerPath = localCfg.Ledger.Path
				}
				return result, ledgerPath
			},
		},
		{name: "configured Ledger gone missing", run: repairMissingLedger},
	}
}

// repairMissingLedger drives the repair of a configured Ledger whose directory
// is gone. The Ledger's path is known whether or not the clone succeeds.
func repairMissingLedger(t *testing.T, root, ledgerURL string) (checkResult, string) {
	missing := repoPathIssue{repoType: "ledger", path: filepath.Join(t.TempDir(), "ledger"),
		endpoint: endpoint.GetForProject(root), cloneURL: ledgerURL, issue: "missing"}
	return fixRepoPathIssues(t.Context(), root, &config.LocalConfig{}, []repoPathIssue{missing}), missing.path
}

// requireNoHalfClone asserts a failed clone left no repository at ledgerPath
// for the daemon or a later doctor run to mistake for a real Ledger.
func requireNoHalfClone(t *testing.T, ledgerPath string) {
	t.Helper()
	require.NoDirExists(t, filepath.Join(ledgerPath, ".git"), "a failed clone must not leave a half-written Ledger in place")
}

// TestDoctorFix_DirectLedgerCloneOutlivesFixedBudget verifies a direct Ledger
// clone that is still downloading is not killed by a fixed budget.
// Failure prevented: with the daemon down, `ox doctor --fix` killed every
// Ledger clone that took longer than 60s on the coworker's link (GH #994), so
// a large Ledger could never be repaired without the daemon. The fixed budget
// is scaled from 60s to 2s here; the fixture downloads for ~5s.
func TestDoctorFix_DirectLedgerCloneOutlivesFixedBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	for _, repair := range directLedgerRepairs() {
		t.Run(repair.name, func(t *testing.T) {
			setDirectCloneBudgets(t, 2*time.Second, 2*time.Minute)
			ledgerURL := servePacedGitRepo(t, directFixtureBytes, directPace, 0)
			root := setupDirectCloneProject(t, ledgerURL)

			var result checkResult
			var ledgerPath string
			start := time.Now()
			output := captureStdoutForPlanCLI(t, func() { result, ledgerPath = repair.run(t, root, ledgerURL) })
			elapsed := time.Since(start)

			require.True(t, result.passed && !result.warning, "a Ledger clone that is still downloading must not be killed: %s / %s\n%s",
				result.message, result.detail, output)
			require.Greater(t, elapsed, directTeamContextCloneTimeout,
				"the download finished inside the fixed budget, so this run proved nothing; slow the pace")
			assert.Contains(t, output, "Cloned successfully (direct)")
			assert.Contains(t, output, "Downloading: ", "a long direct clone must show how much has arrived")
			size, err := exec.Command("git", "-C", ledgerPath, "cat-file", "-s", "HEAD:sessions/history.bin").Output()
			require.NoError(t, err, "the Ledger's history must have arrived in full")
			require.Equal(t, fmt.Sprint(directFixtureBytes), strings.TrimSpace(string(size)))
		})
	}
}

// TestDoctorFix_DirectLedgerCloneBackstopReportsTimeout verifies a direct
// Ledger clone ended by its backstop is reported as a timeout, with how much
// arrived, and leaves nothing behind.
// Failure prevented: the kill read as git's reaction to it ("fetch-pack:
// unexpected disconnect while reading sideband packet", "early EOF"), which
// looks like a server fault; and git, killed outright, left a half-written
// .git whose HEAD points at refs/heads/.invalid where the Ledger belongs.
func TestDoctorFix_DirectLedgerCloneBackstopReportsTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	setDirectCloneBudgets(t, 60*time.Second, 2500*time.Millisecond)
	ledgerURL := servePacedGitRepo(t, directFixtureBytes, directPace, 0)
	root := setupDirectCloneProject(t, ledgerURL)

	var result checkResult
	var ledgerPath string
	output := captureStdoutForPlanCLI(t, func() { result, ledgerPath = repairMissingLedger(t, root, ledgerURL) })

	require.False(t, result.passed && !result.warning, "a clone that timed out is not a repair:\n%s", output)
	require.Contains(t, output, "clone ledger timed out after 2.5s")
	require.Regexp(t, `\(\d+ KB received\)`, output, "the report must say how much of the Ledger arrived")
	for _, misleading := range []string{"unexpected disconnect", "early EOF", "exit code"} {
		require.NotContains(t, output, misleading, "git's reaction to being stopped is not the cause")
	}
	requireNoHalfClone(t, ledgerPath)
}

// TestDoctorFix_FirstLedgerCloneInterrupt verifies Ctrl-C during the first
// Ledger clone of `ox doctor --fix` ends the download at once, leaves nothing
// behind, and stops the run.
// Failure prevented: git runs in its own process group, so only the doctor
// run's context can stop it; a clone on any other context ignored Ctrl-C and
// kept downloading until its hour-long backstop.
func TestDoctorFix_FirstLedgerCloneInterrupt(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	ledgerURL := servePacedGitRepo(t, directFixtureBytes, directPace, 96<<10)
	root := setupDirectCloneProject(t, ledgerURL)
	// past the bootstrap grace period, so doctor clones instead of waiting for the daemon
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(root, ".sageox", "config.json"), old, old))
	setDirectCloneBudgets(t, time.Minute, 15*time.Second)
	projectCfg, err := config.LoadProjectConfig(root)
	require.NoError(t, err)
	target := config.DefaultLedgerPath(projectCfg.RepoID, endpoint.GetForProject(root))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var output string
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		output = captureStdoutForPlanCLI(t, func() {
			opts := doctorOptions{fix: true, fixSlugs: []string{CheckSlugGitRepoPaths}}
			_, runErr = runDoctorChecksWithState(ctx, opts, doctorState{isAuthenticated: true})
		})
	}()
	t.Cleanup(func() { <-done })
	waitForDownload(t, target, 64<<10)
	// a peer's update must survive a repair that completed nothing
	require.NoError(t, config.MutateLocalConfig(context.Background(), root, func(cfg *config.LocalConfig) error {
		cfg.SetTeamContext("team_peer", "Peer", target+".peer")
		return nil
	}))
	cancel()
	<-done

	require.ErrorIs(t, runErr, context.Canceled)
	require.Contains(t, output, "clone ledger interrupted")
	require.NotContains(t, output, "timed out", "Ctrl-C must end the clone before its backstop")
	requireNothingLeftBehind(t, target)
	saved, err := config.LoadLocalConfig(root)
	require.NoError(t, err)
	require.Len(t, saved.TeamContexts, 1)
	require.Equal(t, "team_peer", saved.TeamContexts[0].TeamID)
}

// TestDoctorFix_RepoPathRepairInterrupt verifies Ctrl-C during the Ledger
// repair of `ox doctor --fix` starts no repair for the team context after it.
// Failure prevented: the loop went on to the next repair after the coworker
// asked doctor to stop.
func TestDoctorFix_RepoPathRepairInterrupt(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	ledgerURL := servePacedGitRepo(t, directFixtureBytes, directPace, 96<<10)
	root := setupDirectCloneProject(t, ledgerURL)
	setDirectCloneBudgets(t, time.Minute, 15*time.Second)
	// the Ledger and a team context are configured, and both directories are gone
	target := filepath.Join(t.TempDir(), "ledger")
	localCfg := &config.LocalConfig{Ledger: &config.LedgerConfig{Path: target}}
	localCfg.SetTeamContext("team_next", "Next", filepath.Join(t.TempDir(), "team_next"))
	require.NoError(t, config.SaveLocalConfig(root, localCfg))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var output string
	done := make(chan struct{})
	go func() {
		defer close(done)
		output = captureStdoutForPlanCLI(t, func() { gitRepoPathsCheck(ctx, true) })
	}()
	t.Cleanup(func() { <-done })
	waitForDownload(t, target, 64<<10)
	cancel()
	<-done

	require.Contains(t, output, "clone ledger interrupted")
	require.NotContains(t, output, "timed out", "Ctrl-C must end the clone before its backstop")
	require.NotContains(t, output, "Team context: Next", "Ctrl-C must stop the repair loop")
	requireNothingLeftBehind(t, target)
}

// TestDoctorFix_InterruptedLookupMovesNothingAside verifies Ctrl-C while
// doctor looks up a repo's clone URL leaves the existing directory in place.
// Failure prevented: the repair moved the coworker's directory aside after
// they asked doctor to stop, then failed the clone, leaving the path empty.
func TestDoctorFix_InterruptedLookupMovesNothingAside(t *testing.T) {
	root := setupDoctorProject(t, nil, "")
	dir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep\n"), 0o600))
	cli.SetAssumeYes(true)
	t.Cleanup(func() { cli.SetAssumeYes(false) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // Ctrl-C arrived while the cloud lookup ran

	var err error
	captureStdoutForPlanCLI(t, func() {
		err = cloneRepoForFix(ctx, repoPathIssue{repoType: "ledger", path: dir, endpoint: endpoint.GetForProject(root), issue: "not-git-repo"})
	})
	require.ErrorIs(t, err, context.Canceled)
	require.FileExists(t, filepath.Join(dir, "notes.txt"), "the directory must stay where it was")
	backups, err := filepath.Glob(dir + "_backup_*")
	require.NoError(t, err)
	require.Empty(t, backups)
}

// TestRunDoctorChecks_StopsOnceInterrupted verifies a canceled doctor run
// starts no repair and returns context.Canceled from the git repo paths check
// or the Ledger git checks, before any later category runs.
// Failure prevented: Ctrl-C stopped only the repair it landed in, and doctor
// went on to run every later category's repairs before exiting.
func TestRunDoctorChecks_StopsOnceInterrupted(t *testing.T) {
	if testing.Short() {
		t.Skip("short: full doctor pipeline shells out to git")
	}
	for _, tc := range []struct {
		name          string
		authenticated bool
		lastCategory  string
	}{
		{"after the repo path repairs", true, "Git Status"},
		{"after the Ledger git repairs", false, "Git Repository Health"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// a logged-in project whose configured Ledger is missing: a repair is pending
			root := setupDoctorProject(t, nil, "")
			require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{
				Ledger: &config.LedgerConfig{Path: filepath.Join(t.TempDir(), "ledger")},
			}))
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			var categories []checkCategory
			var err error
			output := captureStdoutForPlanCLI(t, func() {
				opts := doctorOptions{fix: true, fixSlugs: []string{CheckSlugGitRepoPaths}}
				categories, err = runDoctorChecksWithState(ctx, opts, doctorState{isAuthenticated: tc.authenticated})
			})
			require.ErrorIs(t, err, context.Canceled)
			require.NotContains(t, output, "Cloning", "a canceled run must start no repair")
			require.NotEmpty(t, categories)
			require.Equal(t, tc.lastCategory, categories[len(categories)-1].name)
		})
	}
}

// requireNothingLeftBehind asserts an interrupted clone left neither the
// target nor its temporary clone.
func requireNothingLeftBehind(t *testing.T, target string) {
	t.Helper()
	require.NoDirExists(t, target)
	temps, err := filepath.Glob(target + ".tmp-*")
	require.NoError(t, err)
	require.Empty(t, temps, "interruption must remove the temporary clone")
}

// TestDoctorFix_DirectLedgerCloneStallIsAborted verifies a direct Ledger clone
// over a dead link is aborted by curl's stall check, long before the backstop,
// and reported as a stall.
// Failure prevented: replacing the fixed budget with a long backstop turns a
// dead link into a doctor run that hangs for an hour, then reports git's
// "unexpected disconnect" instead of the stall.
func TestDoctorFix_DirectLedgerCloneStallIsAborted(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	setDirectCloneBudgets(t, 60*time.Second, time.Minute)
	// curl gives up on a dead link after 2s instead of production's 15. git
	// reads this after its -c flags, but it only shortens the window: curl
	// checks speed at all only because doctor passes http.lowSpeedLimit.
	t.Setenv("GIT_HTTP_LOW_SPEED_TIME", "2")
	// the pack stops after 96 KiB and the connection stays open
	ledgerURL := servePacedGitRepo(t, directFixtureBytes, directPace, 96<<10)
	root := setupDirectCloneProject(t, ledgerURL)

	var result checkResult
	var ledgerPath string
	start := time.Now()
	output := captureStdoutForPlanCLI(t, func() { result, ledgerPath = repairMissingLedger(t, root, ledgerURL) })
	elapsed := time.Since(start)

	require.False(t, result.passed && !result.warning, "a clone over a dead link is not a repair:\n%s", output)
	// curl averages speed over ~5s before its 2s window starts counting
	require.Less(t, elapsed, 30*time.Second, "the stall check, not the backstop, must end a dead clone")
	require.Contains(t, output, "clone ledger stalled")
	require.NotContains(t, output, "unexpected disconnect", "git's reaction to the abort is not the cause")
	requireNoHalfClone(t, ledgerPath)
}

// TestDirectLedgerCloneError_SaysWhatEndedTheClone verifies a failed direct
// Ledger clone is reported by its cause, not by git's reaction to it.
// Failure prevented: "fetch-pack: unexpected disconnect" for a clone our own
// backstop or curl's stall check ended, which reads like a server fault.
func TestDirectLedgerCloneError_SaysWhatEndedTheClone(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	cloneErr := fmt.Errorf("clone failed: exit status 128")

	for _, tt := range []struct {
		name      string
		ctx       context.Context
		gitOutput string
		want      string
	}{
		{name: "backstop expired", ctx: expired, gitOutput: "fetch-pack: unexpected disconnect while reading sideband packet", want: "clone ledger timed out after 1h0m0s (2 MB received)"},
		{name: "curl stall abort", ctx: context.Background(), gitOutput: "error: RPC failed; curl 28 Operation too slow. Less than 1000 bytes/sec transferred the last 15 seconds", want: "clone ledger stalled: the download stopped making progress (2 MB received)"},
		{name: "git's own failure", ctx: context.Background(), gitOutput: "fatal: repository 'http://127.0.0.1/ledger.git/' not found\n", want: "git clone failed: fatal: repository 'http://127.0.0.1/ledger.git/' not found"},
		{name: "no git output", ctx: context.Background(), want: cloneErr.Error()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := directLedgerCloneError(tt.ctx, time.Hour, 2<<20, tt.gitOutput, cloneErr)
			require.Error(t, err)
			assert.Equal(t, tt.want, err.Error())
		})
	}
}
