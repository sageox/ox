package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/daemon/testutil"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/stretchr/testify/assert"
)

// `ox doctor --fix` clones a missing Ledger through the daemon and waits on it
// over IPC. A Ledger is cloned with its whole history, so on a slow link the
// clone runs for many minutes, and the daemon keeps going whether or not
// doctor is still waiting. These tests drive doctor's real repair paths
// against a fake cloud API and a daemon served over the real IPC socket, with
// doctor's 60s window scaled to idle.

const doctorCloneIdle = 750 * time.Millisecond

// doctorCloneScenario is what the daemon does with doctor's checkout.
type doctorCloneScenario struct {
	name     string
	checkout func(daemon.CheckoutPayload, *daemon.ProgressWriter) (*daemon.CheckoutResult, error)

	cloneFails  bool   // the daemon reports the clone failed
	wantFailed  bool   // the check fails
	wantWarning bool   // the check passes with a warning
	wantMessage string // in the check's message
	wantDetail  string // in the check's detail
	wantOutput  string // in what doctor printed
}

func doctorCloneScenarios() []doctorCloneScenario {
	cloned := func(p daemon.CheckoutPayload) *daemon.CheckoutResult {
		return &daemon.CheckoutResult{Path: p.RepoPath, Cloned: true}
	}
	return []doctorCloneScenario{
		{
			// Failure prevented: a clone still downloading when doctor's
			// fixed 60s ran out was reported as "read: i/o timeout" and a
			// failed check, though the daemon went on to finish it.
			name: "download outlasts doctor's window",
			checkout: func(p daemon.CheckoutPayload, pw *daemon.ProgressWriter) (*daemon.CheckoutResult, error) {
				start := time.Now()
				for received := 1; time.Since(start) < 4*doctorCloneIdle; received++ {
					_ = pw.WriteStage(daemon.CheckoutStageDownloading, fmt.Sprintf("Downloading: %d.0 MB received", received))
					time.Sleep(50 * time.Millisecond)
				}
				return cloned(p), nil
			},
			wantOutput: "Downloading: 1.0 MB received",
		},
		{
			// Failure prevented: doctor gave up on a daemon that was still
			// working (here, waiting out the daemon's own background clone of
			// the same Ledger) and reported the repair as failed.
			name: "daemon still working after doctor stops waiting",
			checkout: func(p daemon.CheckoutPayload, _ *daemon.ProgressWriter) (*daemon.CheckoutResult, error) {
				time.Sleep(3 * doctorCloneIdle)
				return cloned(p), nil
			},
			wantWarning: true,
			wantMessage: "still cloning in the background",
			wantDetail:  "ox status",
			wantOutput:  "Still cloning in the ox daemon",
		},
		{
			// Negative control: a clone the daemon reports as failed is still
			// a failed repair, not something cloning in the background.
			name: "daemon reports the clone failed",
			checkout: func(daemon.CheckoutPayload, *daemon.ProgressWriter) (*daemon.CheckoutResult, error) {
				return nil, errors.New("git clone failed: Authentication failed")
			},
			cloneFails: true,
			wantFailed: true,
			wantOutput: "Clone failed: git clone failed: Authentication failed",
		},
	}
}

// serveLedgerAPI is the cloud API for a project whose Ledger is ready.
func serveLedgerAPI(t *testing.T, repoID string) string {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ledgerURL := srv.URL + "/ledger.git"
		switch r.URL.Path {
		case auth.IntrospectEndpoint:
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "principal_kind": auth.PrincipalKindTeamService})
		case "/api/v1/cli/repos":
			_ = json.NewEncoder(w).Encode(api.ReposResponse{Token: "doctor-pat", ServerURL: srv.URL, ExpiresAt: time.Now().Add(time.Hour)})
		case "/api/v1/cli/repos/" + repoID:
			_ = json.NewEncoder(w).Encode(api.RepoDetailResponse{Ledger: &api.RepoDetailLedger{Status: "ready", RepoURL: ledgerURL}})
		case "/api/v1/repos/" + repoID + "/ledger-status":
			_ = json.NewEncoder(w).Encode(api.LedgerStatusResponse{Status: "ready", RepoURL: ledgerURL})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// setupDoctorCloneProject is an initialized, logged-in project with no Ledger
// on disk, a daemon that handles checkout, and doctor's window set to idle.
func setupDoctorCloneProject(t *testing.T, checkout func(daemon.CheckoutPayload, *daemon.ProgressWriter) (*daemon.CheckoutResult, error)) string {
	t.Helper()
	const repoID = "repo_doctor_clone"
	startSyncTestDaemon(t, &testutil.MockService{CheckoutFunc: checkout})
	setupAuthRenderEnv(t, serveLedgerAPI(t, repoID), validTeamToken)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: repoID})
	hostedTestGit(t, root, "init")
	t.Chdir(root)

	prev := doctorCheckoutIdle
	doctorCheckoutIdle = doctorCloneIdle
	t.Cleanup(func() { doctorCheckoutIdle = prev })
	return root
}

func requireDoctorCloneOutcome(t *testing.T, sc doctorCloneScenario, result checkResult, output string) {
	t.Helper()
	assert.Equal(t, !sc.wantFailed, result.passed, "passed: %s / %s\n%s", result.message, result.detail, output)
	assert.Equal(t, sc.wantWarning, result.warning, "warning: %s / %s\n%s", result.message, result.detail, output)
	assert.Contains(t, result.message, sc.wantMessage)
	assert.Contains(t, result.detail, sc.wantDetail)
	assert.Contains(t, output, sc.wantOutput)
	if !sc.cloneFails {
		assert.NotContains(t, output, "Clone failed", "a clone the daemon is still working on is not a failed clone")
		assert.NotContains(t, output, "i/o timeout", "doctor's own wait running out is not the clone's result")
	}
}

// TestDoctorFix_FirstLedgerCloneWaitsOnProgress drives the first-time Ledger
// clone (`ox doctor --fix` with no repos configured).
func TestDoctorFix_FirstLedgerCloneWaitsOnProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("short: waits out doctor's clone window")
	}
	for _, sc := range doctorCloneScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			root := setupDoctorCloneProject(t, sc.checkout)
			var result checkResult
			output := captureStdoutForPlanCLI(t, func() { result = fixMissingRepos(root, &config.LocalConfig{}) })
			requireDoctorCloneOutcome(t, sc, result, output)
			if sc.wantOutput == "Downloading: 1.0 MB received" {
				// the daemon reports every few seconds; doctor must not print
				// a line for each report of a download lasting many minutes
				assert.Equal(t, 1, strings.Count(output, "MB received"), "download progress must be throttled:\n%s", output)
			}
		})
	}
}

// TestDoctorFix_MissingLedgerCloneWaitsOnProgress drives the repair of a
// configured Ledger whose directory is gone.
func TestDoctorFix_MissingLedgerCloneWaitsOnProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("short: waits out doctor's clone window")
	}
	for _, sc := range doctorCloneScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			root := setupDoctorCloneProject(t, sc.checkout)
			missing := repoPathIssue{repoType: "ledger", path: filepath.Join(t.TempDir(), "ledger"), endpoint: endpoint.GetForProject(root), issue: "missing"}
			var result checkResult
			output := captureStdoutForPlanCLI(t, func() { result = fixRepoPathIssues(root, &config.LocalConfig{}, []repoPathIssue{missing}) })
			if sc.cloneFails {
				// this path has never failed the check outright: an
				// unrepaired issue is a warning that asks for another run
				sc.wantFailed, sc.wantWarning, sc.wantMessage = false, true, "1 repo(s) with issues"
			}
			requireDoctorCloneOutcome(t, sc, result, output)
		})
	}
}
