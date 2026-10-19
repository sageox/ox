package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/daemon/testutil"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `ox doctor --fix` clones a Ledger while other ox processes may want the
// same Ledger: the daemon (started by any ox command mid-download), its
// background clone, or a second doctor run in another terminal. These tests
// drive doctor's real repairs against real paced git servers and the daemon's
// real Checkout, and check that there is only ever one clone of a Ledger path,
// and that nothing at that path looks like a Ledger until the clone is whole.

// startDoctorRepair runs doctor's repair of the missing Ledger at ledgerPath
// in the background. wait returns its result and what it printed.
func startDoctorRepair(t *testing.T, root, ledgerURL, ledgerPath string) (wait func() (checkResult, string)) {
	t.Helper()
	missing := repoPathIssue{repoType: "ledger", path: ledgerPath,
		endpoint: endpoint.GetForProject(root), cloneURL: ledgerURL, issue: "missing"}
	var result checkResult
	var output string
	done := make(chan struct{})
	go func() {
		defer close(done)
		output = captureStdoutForPlanCLI(t, func() {
			result = fixRepoPathIssues(root, &config.LocalConfig{}, []repoPathIssue{missing})
		})
	}()
	t.Cleanup(func() { <-done })
	return func() (checkResult, string) { <-done; return result, output }
}

// waitForDownload returns once at least bytes of the Ledger have arrived on
// disk, wherever the clone is writing them.
func waitForDownload(t *testing.T, ledgerPath string, bytes int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		received := dirSize(ledgerPath)
		temps, _ := filepath.Glob(ledgerPath + ".tmp-*")
		for _, tmp := range temps {
			received += dirSize(tmp)
		}
		return received >= bytes
	}, 10*time.Second, 20*time.Millisecond, "the Ledger clone never started downloading")
}

// halfCloneHead describes what a clone left at ledgerPath, for failure output.
func halfCloneHead(ledgerPath string) string {
	head, err := os.ReadFile(filepath.Join(ledgerPath, ".git", "HEAD"))
	if err != nil {
		return "(no .git/HEAD)"
	}
	return "HEAD: " + strings.TrimSpace(string(head))
}

// TestDoctorFix_DirectLedgerCloneIsNoLedgerUntilComplete verifies a Ledger
// that doctor is still cloning without the daemon is not at the Ledger's path,
// and that a daemon starting mid-download sees a clone in progress.
// Failure prevented: doctor cloned straight into the Ledger's path, so a
// daemon spawned meanwhile found a .git there and reported the Ledger as
// already cloned (or pulled on it) while the download was half done.
func TestDoctorFix_DirectLedgerCloneIsNoLedgerUntilComplete(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	setDirectCloneBudgets(t, 60*time.Second, 2*time.Minute)
	ledgerURL := servePacedGitRepo(t, directFixtureBytes, directPace, 0)
	root := setupDirectCloneProject(t, ledgerURL)
	ledgerPath := filepath.Join(t.TempDir(), "ledger")

	wait := startDoctorRepair(t, root, ledgerURL, ledgerPath)
	waitForDownload(t, ledgerPath, 64<<10)

	require.NoDirExists(t, filepath.Join(ledgerPath, ".git"),
		"a Ledger still downloading must not be where the Ledger belongs (%s)", halfCloneHead(ledgerPath))
	scheduler := daemon.NewSyncScheduler(daemon.DefaultConfig(), slog.New(slog.DiscardHandler))
	seen, err := scheduler.Checkout(daemon.CheckoutPayload{CloneURL: ledgerURL, RepoPath: ledgerPath, RepoType: "ledger"}, nil)
	require.NoError(t, err)
	require.True(t, seen.InProgress, "a daemon starting mid-download must see the clone in progress, not a Ledger: %+v", seen)

	result, output := wait()
	require.True(t, result.passed && !result.warning, "%s / %s\n%s", result.message, result.detail, output)
	assert.Contains(t, output, "Cloned successfully (direct)")
	size, err := exec.Command("git", "-C", ledgerPath, "cat-file", "-s", "HEAD:sessions/history.bin").Output()
	require.NoError(t, err, "the Ledger's history must have arrived in full")
	require.Equal(t, fmt.Sprint(directFixtureBytes), strings.TrimSpace(string(size)))
	temps, _ := filepath.Glob(ledgerPath + ".tmp-*")
	require.Empty(t, temps, "the download's temp clone must be renamed into place, not left behind")

	seen, err = scheduler.Checkout(daemon.CheckoutPayload{CloneURL: ledgerURL, RepoPath: ledgerPath, RepoType: "ledger"}, nil)
	require.NoError(t, err)
	require.True(t, seen.AlreadyExists, "once it lands, the daemon must take it for the Ledger: %+v", seen)
}

// TestDoctorFix_DirectLedgerCloneKilledMidDownloadLeavesNoLedger verifies a
// direct Ledger clone that dies without cleaning up leaves nothing at the
// Ledger's path.
// Failure prevented: git killed outright (kill -9, the OOM killer, a laptop
// that never wakes the process) left a half-written .git where the Ledger
// belongs, HEAD pointing at refs/heads/.invalid, which the daemon and every
// CLI command then treated as a real Ledger whose every commit fails.
func TestDoctorFix_DirectLedgerCloneKilledMidDownloadLeavesNoLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("needs pgrep to find the clone's git process")
	}
	setDirectCloneBudgets(t, 60*time.Second, 2*time.Minute)
	ledgerURL := servePacedGitRepo(t, directFixtureBytes, directPace, 0)
	root := setupDirectCloneProject(t, ledgerURL)
	ledgerPath := filepath.Join(t.TempDir(), "ledger")

	wait := startDoctorRepair(t, root, ledgerURL, ledgerPath)
	waitForDownload(t, ledgerPath, 64<<10)

	out, err := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid()), "-f", "clone --quiet").Output()
	require.NoError(t, err, "the clone's git process must be running")
	pids := strings.Fields(string(out))
	require.Len(t, pids, 1, "exactly one git clone: %q", out)
	pid, err := strconv.Atoi(pids[0])
	require.NoError(t, err)
	proc, err := os.FindProcess(pid)
	require.NoError(t, err)
	require.NoError(t, proc.Kill(), "kill -9 the clone, so git cannot clean up after itself")

	result, output := wait()
	require.False(t, result.passed && !result.warning, "a killed clone is not a repair:\n%s", output)
	require.NoDirExists(t, filepath.Join(ledgerPath, ".git"),
		"a killed clone must leave nothing where the Ledger belongs (%s)", halfCloneHead(ledgerPath))
}

// TestDoctorFix_LedgerAlreadyBeingClonedIsReportedAsCloning verifies doctor,
// with or without the daemon, reports a Ledger another ox process is already
// cloning as cloning in the background, at once, and starts no clone of its
// own. The test holds the Ledger's pre-clone lock the way that clone does.
// Failure prevented: without the daemon, doctor cloned into the same path
// as the running clone; through the daemon, it waited out its whole window
// before saying anything.
func TestDoctorFix_LedgerAlreadyBeingClonedIsReportedAsCloning(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git server")
	}
	for _, viaDaemon := range []bool{false, true} {
		name := "no daemon"
		if viaDaemon {
			name = "through the daemon"
		}
		t.Run(name, func(t *testing.T) {
			ledgerURL := servePacedGitRepo(t, 16<<10, 1<<20, 0)
			var root string
			if viaDaemon {
				scheduler := daemon.NewSyncScheduler(daemon.DefaultConfig(), slog.New(slog.DiscardHandler))
				root = setupDoctorProject(t, &testutil.MockService{CheckoutFunc: scheduler.Checkout}, ledgerURL)
				prev := doctorCheckoutIdle
				doctorCheckoutIdle = 3 * time.Second
				t.Cleanup(func() { doctorCheckoutIdle = prev })
			} else {
				root = setupDirectCloneProject(t, ledgerURL)
			}
			ledgerPath := filepath.Join(t.TempDir(), "ledger")
			holdPreCloneLock(t, ledgerPath)

			missing := repoPathIssue{repoType: "ledger", path: ledgerPath,
				endpoint: endpoint.GetForProject(root), cloneURL: ledgerURL, issue: "missing"}
			var result checkResult
			start := time.Now()
			output := captureStdoutForPlanCLI(t, func() {
				result = fixRepoPathIssues(root, &config.LocalConfig{}, []repoPathIssue{missing})
			})
			elapsed := time.Since(start)

			require.True(t, result.passed && result.warning, "%s / %s\n%s", result.message, result.detail, output)
			assert.Contains(t, result.message, "still cloning in the background")
			assert.Contains(t, output, "Already being cloned by another ox process")
			assert.NotContains(t, output, "Clone failed")
			require.Less(t, elapsed, time.Second, "doctor must be told at once, not wait out the running clone")
			require.NoDirExists(t, ledgerPath, "doctor must not clone a Ledger another process is cloning")
		})
	}
}

// TestDoctorFix_TeamContextAlreadyBeingClonedIsNotAFailure verifies a team
// context the daemon is already cloning is reported as cloning, not as a
// clone that did not finish.
// Failure prevented: the daemon's answer read as a quiet window, which for a
// team context doctor reports as a failed clone.
func TestDoctorFix_TeamContextAlreadyBeingClonedIsNotAFailure(t *testing.T) {
	scheduler := daemon.NewSyncScheduler(daemon.DefaultConfig(), slog.New(slog.DiscardHandler))
	root := setupDoctorProject(t, &testutil.MockService{CheckoutFunc: scheduler.Checkout}, "")
	teamPath := filepath.Join(t.TempDir(), "team")
	holdPreCloneLock(t, teamPath)

	var err error
	output := captureStdoutForPlanCLI(t, func() {
		err = cloneViaDaemon("http://127.0.0.1:1/team.git", teamPath, "team-context", endpoint.GetForProject(root))
	})
	require.ErrorIs(t, err, daemon.ErrCheckoutInProgress, "callers count it as cloning in the background")
	assert.NotContains(t, err.Error(), "did not finish")
	assert.Contains(t, output, "Already being cloned by another ox process")
}

// holdPreCloneLock holds path's pre-clone lock for the rest of the test, as a
// clone of path in another ox process does.
func holdPreCloneLock(t *testing.T, path string) {
	t.Helper()
	held, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		_ = gitutil.WithPreCloneLock(context.Background(), path, func() error { close(held); <-release; return nil })
	})
	<-held
	t.Cleanup(func() { close(release); wg.Wait() })
}

// TestDirectCloneLedger_TargetStates verifies what the no-daemon Ledger clone
// does with each state its target can be in, before anything downloads.
// Failure prevented: a target the clone could never be renamed onto is found
// only after the whole Ledger has downloaded; a finished Ledger is cloned
// over; a lock that cannot be tried reads as another clone running.
func TestDirectCloneLedger_TargetStates(t *testing.T) {
	// No credentials are saved, so a clone that gets as far as downloading
	// fails there, after the target was checked.
	const unreachable = "http://127.0.0.1:1/ledger.git"
	const downloadRan = "git credentials not available"
	for _, tt := range []struct {
		name       string
		prepare    func(t *testing.T, target string)
		wantErr    string // empty: no error
		wantCloned bool
		wantTarget bool // target still exists afterwards
	}{
		{
			name: "finished Ledger already there: reported, not cloned over",
			prepare: func(t *testing.T, target string) {
				require.NoError(t, os.MkdirAll(filepath.Join(target, ".git"), 0o755))
			},
			wantTarget: true,
		},
		{
			name: "empty directory: cleared, then the download runs",
			prepare: func(t *testing.T, target string) {
				require.NoError(t, os.MkdirAll(target, 0o755))
			},
			wantErr: downloadRan,
		},
		{
			name: "CodeDB cache only: kept for the clone to adopt, then the download runs",
			prepare: func(t *testing.T, target string) {
				require.NoError(t, os.MkdirAll(filepath.Join(target, ".sageox", "cache", "codedb"), 0o755))
			},
			wantErr:    downloadRan,
			wantTarget: true,
		},
		{
			name: "someone else's files: refused before anything downloads",
			prepare: func(t *testing.T, target string) {
				require.NoError(t, os.MkdirAll(target, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(target, "notes.txt"), []byte("mine"), 0o600))
			},
			wantErr:    "path already exists and is not empty",
			wantTarget: true,
		},
		{
			name: "a file where the Ledger belongs: refused",
			prepare: func(t *testing.T, target string) {
				require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))
			},
			wantErr:    "read clone target",
			wantTarget: true,
		},
		{
			name: "lock cannot be tried: an error, not another clone running",
			prepare: func(t *testing.T, _ string) {
				notADir := filepath.Join(t.TempDir(), "not-a-directory")
				require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o600))
				t.Setenv("TMPDIR", notADir)
				if os.TempDir() != notADir {
					t.Skip("TMPDIR does not set os.TempDir on this platform (Windows reads TMP and TEMP)")
				}
			},
			wantErr: "open lock file",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setDirectCloneBudgets(t, 60*time.Second, 10*time.Second)
			setupDirectCloneProject(t, "")
			target := filepath.Join(t.TempDir(), "ledger")
			tt.prepare(t, target)

			cloned, err := directCloneLedger(unreachable, target, endpoint.GetForProject(""))
			assert.Equal(t, tt.wantCloned, cloned)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
				require.NotErrorIs(t, err, daemon.ErrCheckoutInProgress, "nothing else holds this Ledger")
			}
			_, statErr := os.Lstat(target)
			assert.Equal(t, tt.wantTarget, statErr == nil, "target exists afterwards")
			temps, _ := filepath.Glob(target + ".tmp-*")
			assert.Empty(t, temps, "no partial download left behind")
		})
	}
}

// TestDoctorFix_DirectCloneOfAFinishedLedgerSaysSo verifies doctor without the
// daemon reports a Ledger that landed meanwhile as already there.
// Failure prevented: doctor reported "Cloned successfully" for a clone it
// never ran, or failed with "path already exists".
func TestDoctorFix_DirectCloneOfAFinishedLedgerSaysSo(t *testing.T) {
	root := setupDirectCloneProject(t, "")
	target := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, os.MkdirAll(filepath.Join(target, ".git"), 0o755))

	var err error
	output := captureStdoutForPlanCLI(t, func() {
		err = cloneViaDaemon("http://127.0.0.1:1/ledger.git", target, "ledger", endpoint.GetForProject(root))
	})
	require.NoError(t, err)
	assert.Contains(t, output, "Repository already exists.")
	assert.NotContains(t, output, "Cloned successfully")
}

// TestDoctorFix_FirstRunTeamContextAlreadyBeingClonedIsCloning drives the
// first-time setup (`ox doctor --fix` with no repos configured) for a team
// context the daemon is already cloning.
// Failure prevented: the daemon's "already running" answer was printed as
// "Clone failed" and the check failed, for a clone that lands on its own.
func TestDoctorFix_FirstRunTeamContextAlreadyBeingClonedIsCloning(t *testing.T) {
	const repoID, teamID = "repo_doctor_team", "team_doctor_clone"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case auth.IntrospectEndpoint:
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "principal_kind": auth.PrincipalKindTeamService})
		case "/api/v1/cli/repos":
			_ = json.NewEncoder(w).Encode(api.ReposResponse{Token: "doctor-pat", ServerURL: srv.URL, ExpiresAt: time.Now().Add(time.Hour)})
		case "/api/v1/cli/repos/" + repoID:
			_ = json.NewEncoder(w).Encode(api.RepoDetailResponse{TeamContexts: []api.RepoDetailTeamContext{
				{TeamID: teamID, Name: "Platform", RepoURL: srv.URL + "/team.git"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	scheduler := daemon.NewSyncScheduler(daemon.DefaultConfig(), slog.New(slog.DiscardHandler))
	startSyncTestDaemon(t, &testutil.MockService{CheckoutFunc: scheduler.Checkout})
	setupAuthRenderEnv(t, srv.URL, validTeamToken)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: repoID})
	hostedTestGit(t, root, "init")
	t.Chdir(root)
	holdPreCloneLock(t, paths.TeamContextDir(teamID, endpoint.GetForProject(root)))

	var result checkResult
	output := captureStdoutForPlanCLI(t, func() { result = fixMissingRepos(root, &config.LocalConfig{}) })

	require.True(t, result.passed && result.warning, "%s / %s\n%s", result.message, result.detail, output)
	assert.Contains(t, result.message, "1 repo(s) still cloning in the background")
	assert.Contains(t, output, "Already being cloned by another ox process")
	assert.NotContains(t, output, "Clone failed")
}
