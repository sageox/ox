package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/glance"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/lfs"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostedReaderFixture is a real ledger served over Git smart HTTP, materialized
// into the caller's own data home by the shipped `ox sync --read-only` command.
// Readers then run against the checkout that command published — not a
// hand-assembled directory that could satisfy a reader the real one would not.
type hostedReaderFixture struct {
	path        string
	sessionName string
	now         time.Time
	corruptHour time.Time
}

// runOxInProc runs one shipped command through the real root hooks, so a reader
// that accidentally leaves the headless path starts daemon or human-auth work
// and fails here rather than in a hosted runtime.
func runOxInProc(t *testing.T, target *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	cmd := &cobra.Command{
		Use:                target.Name(),
		RunE:               target.RunE,
		PersistentPreRunE:  rootCmd.PersistentPreRunE,
		PersistentPostRunE: rootCmd.PersistentPostRunE,
		SilenceErrors:      true,
		SilenceUsage:       true,
	}
	cmd.Flags().AddFlagSet(target.Flags())
	// Persistent, not local: the readers resolve --json through
	// cmd.Root().PersistentFlags(), so a local copy would leave them reading an
	// empty flag set and falling back to agent detection.
	cmd.PersistentFlags().AddFlagSet(rootCmd.PersistentFlags())
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		value, changed := flag.Value.String(), flag.Changed
		t.Cleanup(func() {
			_ = flag.Value.Set(value)
			flag.Changed = changed
		})
		require.NoError(t, flag.Value.Set(flag.DefValue))
		flag.Changed = false
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func hostedTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func newHostedReaderFixture(t *testing.T) *hostedReaderFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("short: native Git clone and checkout lifecycle")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	require.NoError(t, os.Mkdir(source, 0700))
	hostedTestGit(t, source, "init", "-b", "main")
	hostedTestGit(t, source, "config", "user.name", "Test")
	hostedTestGit(t, source, "config", "user.email", "test@example.invalid")

	now := time.Now().UTC()
	session := now.Format("2006-01-02T15-04") + "-devon-ses01"
	murmur, err := json.Marshal(ledger.MurmurFile{
		SchemaVersion: "1",
		ID:            "mur_01",
		Timestamp:     now,
		AgentID:       "Ox0001",
		PrincipalID:   "devon",
		Topic:         "wip",
		Importance:    "normal",
		Content:       "materializing the hosted ledger",
	})
	require.NoError(t, err)
	// A synced ledger session is a directory with meta.json plus its content.
	// Without meta.json the store skips the directory entirely, so a fixture
	// missing it would test a reader against nothing.
	meta, err := json.Marshal(lfs.SessionMeta{
		Version:     "1.0",
		SessionName: session,
		Username:    "devon",
		AgentID:     "Ox0001",
		AgentType:   "claude-code",
		Title:       "hosted ledger read",
		Summary:     "materialized by ox sync --read-only",
		CreatedAt:   now,
		EntryCount:  2,
	})
	require.NoError(t, err)
	// A malformed murmur, committed so it is part of the verified checkout
	// rather than a local edit the guard would refuse as dirty. Parked three
	// hours back so only the test that asks for that hour ever scans it.
	corruptHour := now.Add(-3 * time.Hour)
	files := map[string]string{
		filepath.Join("sessions", session, "meta.json"):                     string(meta),
		filepath.Join("sessions", session, "raw.jsonl"):                     "{\"type\":\"user\"}\n{\"type\":\"assistant\"}\n",
		filepath.Join("data", "plans", "p1", "plan.md"):                     "# plan\n",
		filepath.Join(ledger.MurmurDateHourDir(now), "mur_01.json"):         string(murmur),
		filepath.Join(ledger.MurmurDateHourDir(corruptHour), "bad_01.json"): "{not json",
	}
	for name, body := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(source, name)), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(source, name), []byte(body), 0600))
	}
	hostedTestGit(t, source, "add", "--all")
	hostedTestGit(t, source, "commit", "-m", "ledger")

	bare := filepath.Join(root, "ledger.git")
	hostedTestGit(t, root, "clone", "--bare", source, bare)
	hostedTestGit(t, bare, "config", "uploadpack.allowFilter", "true")
	hostedTestGit(t, bare, "config", "uploadpack.allowAnySHA1InWant", "true")

	const token = "oxt_test_1ljPfr"
	repoRoute := "/api/v1/cli/repos/" + readSyncTestRepoID
	backend := &cgi.Handler{
		Path: filepath.Join(hostedTestGit(t, root, "--exec-path"), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == repoRoute {
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"ledger":{"status":"ready","read_url":%q}}`, server.URL+repoRoute+"/ledger.git")
			return
		}
		if u, p, ok := r.BasicAuth(); !ok || u != "ox" || p != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="ledger"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && !strings.HasSuffix(r.URL.Path, "/git-upload-pack") {
			t.Error("hosted read attempted a write endpoint")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, repoRoute)
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	// Trust the fixture CA at Git's invocation boundary and for discovery only;
	// production TLS validation and transport policy still run unchanged.
	cert := filepath.Join(root, "ca.pem")
	require.NoError(t, os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600))
	gitBin, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0700))
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"),
		[]byte("#!/bin/sh\nexec "+quote(gitBin)+" -c "+quote("http.sslCAInfo="+cert)+" \"$@\"\n"), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	oldHelper := gitserver.DefaultHelperCommand()
	gitserver.SetHelperCommand(`!f() { printf 'username=ox\npassword=%s\n\n' "$SAGEOX_TOKEN"; }; f`)
	t.Cleanup(func() { gitserver.SetHelperCommand(oldHelper) })

	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("SAGEOX_TOKEN", token)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data-home"))
	t.Setenv("OX_XDG_DISABLE", "")

	synced, _, err := runReadSyncInProc(t, "--read-only", "--repo", readSyncTestRepoID, "--timeout", "2m", "--json")
	require.NoError(t, err)
	require.True(t, synced.Ready, "%+v", synced)
	return &hostedReaderFixture{path: synced.Path, sessionName: session, now: now, corruptHour: corruptHour}
}

// Failure prevented: a hosted caller with no source checkout cannot read the
// ledger ox just materialized for it, or reads it without the guard and can be
// handed a checkout a refresh is midway through replacing.
func TestHostedReadersServeTheMaterializedLedger(t *testing.T) {
	f := newHostedReaderFixture(t)

	stdout, stderr, err := runOxInProc(t, sessionListCmd, "--repo", readSyncTestRepoID, "--json")
	require.NoError(t, err, stderr)
	var listed sessionListOutput
	require.NoError(t, json.Unmarshal([]byte(stdout), &listed), stdout)
	require.True(t, listed.LedgerAvailable)
	require.Equal(t, readSyncTestRepoID, listed.RepoID)
	require.Len(t, listed.Sessions, 1)
	require.Equal(t, f.sessionName, listed.Sessions[0].Name)

	// --all drops the 7-day window and the row cap; the ledger is the same.
	stdout, stderr, err = runOxInProc(t, sessionListCmd, "--repo", readSyncTestRepoID, "--all", "--json")
	require.NoError(t, err, stderr)
	require.NoError(t, json.Unmarshal([]byte(stdout), &listed), stdout)
	require.Equal(t, "all", listed.Window)
	require.Len(t, listed.Sessions, 1)

	// Absolute UTC bounds, the way a hosted consumer calls it: the murmur
	// partition under data/murmurs is keyed by UTC hour, so a relative window
	// resolved in local time would scan a neighboring hour's directory.
	stdout, stderr, err = runOxInProc(t, glanceCmd, "--repo", readSyncTestRepoID,
		"--since", f.now.Add(-time.Hour).Format(time.RFC3339),
		"--until", f.now.Add(time.Hour).Format(time.RFC3339))
	require.NoError(t, err, stderr)
	var activity glance.ActivityData
	require.NoError(t, json.Unmarshal([]byte(stdout), &activity), stdout)
	require.Equal(t, readSyncTestRepoID, activity.Repo)
	require.Equal(t, 1, activity.Stats.TotalMurmurs)
	require.Equal(t, 1, activity.Stats.TotalSessions)

	// Neither reader may leave the checkout in a state that no longer verifies:
	// a read that writes is a read that can invalidate its own receipt.
	checked, _, err := runReadSyncInProc(t, "--read-only", "--repo", readSyncTestRepoID, "--check", "--timeout", "30s", "--json")
	require.NoError(t, err)
	require.True(t, checked.Ready, "%+v", checked)
}

// Failure prevented: the guard verifies readiness, releases the lock, and only
// then reads — leaving a window a refresh can mutate. That is the `--check`
// followed by an unguarded read shape, and it is indistinguishable from a
// correct reader until something mutates inside the window.
//
// A second acquisition of the same lock from inside the read must time out.
// This is the discriminating proof: every other assertion here also passes when
// the read merely checks readiness first, because CheckReadiness takes the same
// lock on its way in and gives it straight back.
func TestHostedGuardHoldsTheLockForTheWholeRead(t *testing.T) {
	f := newHostedReaderFixture(t)
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	read := false
	require.NoError(t, withHostedLedger(cmd, readSyncTestRepoID, func(path string) error {
		read = true
		require.Equal(t, f.path, path)
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		require.Error(t, gitutil.WithRepoLock(ctx, path, func() error { return nil }),
			"a refresh must not be able to take the checkout lock while a read is in flight")
		return nil
	}))
	require.True(t, read, "the guard must run the read on a verified checkout")
}

// Failure prevented: a reader runs while a refresh is replacing the worktree and
// returns half a ledger — the race the shared checkout lock exists to stop.
// Proves the shipped command contends on that lock and observes the refresh's
// finished state; TestHostedGuardHoldsTheLockForTheWholeRead proves it keeps the
// lock past the readiness check.
func TestHostedReaderSerializesAgainstConcurrentRefresh(t *testing.T) {
	f := newHostedReaderFixture(t)

	// Hold the lock the way a refresh does, then empty the worktree underneath
	// it. A reader that ignores the guard observes zero sessions.
	// require.* in a non-test goroutine calls runtime.Goexit, which would skip
	// close(held) and hang the test instead of failing it. Every check in the
	// refresh worker is therefore assert.* plus an explicit return, and
	// refreshDone lets the main goroutine notice a worker that died early.
	held, release, refreshDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var refresh sync.WaitGroup
	refresh.Add(1)
	go func() {
		defer refresh.Done()
		defer close(refreshDone)
		_ = gitutil.WithRepoLock(t.Context(), f.path, func() error {
			dir := filepath.Join(f.path, "sessions", f.sessionName)
			saved := map[string][]byte{}
			entries, err := os.ReadDir(dir)
			if !assert.NoError(t, err) {
				return err
			}
			for _, e := range entries {
				body, err := os.ReadFile(filepath.Join(dir, e.Name()))
				if !assert.NoError(t, err) {
					return err
				}
				saved[e.Name()] = body
			}
			if !assert.NoError(t, os.RemoveAll(dir)) {
				return errors.New("remove failed")
			}
			close(held)
			<-release
			// Put the ledger back before the reader is allowed to look at it.
			if !assert.NoError(t, os.MkdirAll(dir, 0700)) {
				return errors.New("restore failed")
			}
			for name, body := range saved {
				assert.NoError(t, os.WriteFile(filepath.Join(dir, name), body, 0600))
			}
			return nil
		})
	}()
	select {
	case <-held:
	case <-refreshDone:
		t.Fatal("refresh worker failed before it reached the midpoint")
	}

	listed := make(chan sessionListOutput, 1)
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		stdout, stderr, err := runOxInProc(t, sessionListCmd, "--repo", readSyncTestRepoID, "--json")
		if !assert.NoError(t, err, stderr) {
			return
		}
		var out sessionListOutput
		if assert.NoError(t, json.Unmarshal([]byte(stdout), &out), stdout) {
			listed <- out
		}
	}()

	// The reader must still be waiting: nothing can be read while the refresh
	// holds the lock.
	select {
	case out := <-listed:
		t.Fatalf("reader returned during refresh: %+v", out)
	case <-time.After(250 * time.Millisecond):
	}
	close(release)
	refresh.Wait()
	reader.Wait()

	// Non-blocking: the reader goroutine reports its own failures through
	// assert.*, and a reader that failed never sent. Receiving blindly here
	// would turn that into a hang.
	var out sessionListOutput
	select {
	case out = <-listed:
	default:
		t.Fatal("reader produced no output")
	}
	require.Len(t, out.Sessions, 1, "reader must see the restored ledger, never the refresh midpoint")
	require.Equal(t, f.sessionName, out.Sessions[0].Name)
}

// Failure prevented: an unverified, missing, or foreign checkout is reported as
// a ledger that is simply empty, and a reader's refusal is indistinguishable
// from a successful read of nothing.
func TestHostedReadersRefuseUnverifiedCheckouts(t *testing.T) {
	t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		args []string
		code int
	}{
		{"session list no checkout", sessionListCmd, []string{"--repo", readSyncTestRepoID, "--json"}, 1},
		{"glance no checkout", glanceCmd, []string{"--repo", readSyncTestRepoID}, 1},
		{"glance rejects a path", glanceCmd, []string{"--repo", t.TempDir()}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := runOxInProc(t, tc.cmd, tc.args...)
			require.Equal(t, tc.code, exitCodeOf(t, err))
			require.Empty(t, stdout, "a refused read must not look like an empty ledger")
			require.Contains(t, stderr, "Ledger read failed: ")
		})
	}
}

// Failure prevented: an unsafe endpoint or a shared data home reaches a reader,
// or a value that only looks like a repo ID diverts a project read.
func TestHostedLedgerSelection(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "")
	for _, tc := range []struct{ name, endpoint, dataHome, class string }{
		{"userinfo", "https://user:secret@sageox.ai", t.TempDir(), "invalid_arguments"},
		{"http", "http://sageox.ai", t.TempDir(), "invalid_arguments"},
		{"path", "https://sageox.ai/other", t.TempDir(), "invalid_arguments"},
		{"relative data home", "https://sageox.ai", "relative", "invalid_arguments"},
		{"query", "https://sageox.ai?token=secret", t.TempDir(), "invalid_arguments"},
		{"fragment", "https://sageox.ai#secret", t.TempDir(), "invalid_arguments"},
		{"no host", "https:///ledger", t.TempDir(), "invalid_arguments"},
		{"accepted", "https://sageox.ai", t.TempDir(), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SAGEOX_ENDPOINT", tc.endpoint)
			t.Setenv("XDG_DATA_HOME", tc.dataHome)
			path, _, class := selectHostedLedger(readSyncTestRepoID)
			require.Equal(t, tc.class, class)
			require.NotContains(t, path, "secret")
		})
	}

	// The legacy path override moves the checkout out from under the data home
	// the caller selected, which is the whole isolation contract.
	t.Run("legacy override", func(t *testing.T) {
		t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		t.Setenv("OX_XDG_DISABLE", "1")
		_, _, class := selectHostedLedger(readSyncTestRepoID)
		require.Equal(t, "invalid_arguments", class)
	})

	// A value that is not a canonical repo ID never reaches path resolution.
	t.Run("not a repo id", func(t *testing.T) {
		t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		for _, bad := range []string{"", "../../secret", "repo_not-a-uuid", "/path/to/repo"} {
			path, ep, class := selectHostedLedger(bad)
			require.Equal(t, "invalid_arguments", class, bad)
			require.Empty(t, path)
			require.Empty(t, ep)
		}
	})

	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"session", "list", "--repo", readSyncTestRepoID}, true},
		{[]string{"session", "list", "--repo=" + readSyncTestRepoID}, true},
		{[]string{"glance", "--repo", readSyncTestRepoID}, true},
		{[]string{"session", "list", "--repo", "/path/to/repo"}, false},
		{[]string{"session", "list"}, false},
		{[]string{"glance", "--since", "3d"}, false},
		{[]string{"session", "list", "--", "--repo", readSyncTestRepoID}, false},
		{[]string{"query", "--repo", readSyncTestRepoID}, false},
		// Cobra binds the LAST --repo. If preflight read the first, a repeated
		// flag would run the project prelude — loading dotenv from the working
		// directory — and only then select the hosted checkout.
		{[]string{"session", "list", "--repo", "/local/path", "--repo", readSyncTestRepoID}, true},
		{[]string{"glance", "--repo", "/local/path", "--repo=" + readSyncTestRepoID}, true},
		{[]string{"glance", "--repo", readSyncTestRepoID, "--repo", "/local/path"}, false},
		// pflag hands "--" to the first --repo as its VALUE, then binds the
		// last one. Reading "--" as the positional separator would stop the
		// scan and route a hosted read through the project prelude.
		{[]string{"session", "list", "--repo", "--", "--repo", readSyncTestRepoID}, true},
		{[]string{"glance", "--repo=--", "--repo", readSyncTestRepoID}, true},
	} {
		require.Equal(t, tc.want, headlessLedgerReadRequested(tc.args), "%v", tc.args)
	}
}

// Failure prevented: the human-readable form of a hosted read renders nothing,
// or renders a row for a ledger that has none — a coworker debugging a hosted
// runtime reads this table, not the JSON.
func TestHostedSessionListRendersATable(t *testing.T) {
	f := newHostedReaderFixture(t)
	t.Setenv("AGENT_ENV", "")

	out := captureStdoutForPlanCLI(t, func() {
		_, stderr, err := runOxInProc(t, sessionListCmd, "--repo", readSyncTestRepoID, "--json=false")
		require.NoError(t, err, stderr)
	})
	require.Contains(t, out, "SESSION")
	require.Contains(t, out, f.sessionName)

	// Locally deleted ledger content is a dirty checkout, not a ledger that has
	// no sessions. Reporting the latter would let a hosted coworker answer "no
	// sessions" from a checkout it should have refused.
	require.NoError(t, os.RemoveAll(filepath.Join(f.path, "sessions", f.sessionName)))
	stdout, stderr, err := runOxInProc(t, sessionListCmd, "--repo", readSyncTestRepoID, "--json")
	require.Equal(t, 1, exitCodeOf(t, err))
	require.Empty(t, stdout)
	require.Equal(t, "Ledger read failed: dirty\n", stderr)
}

// Failure prevented: isHeadlessLedgerRead dispatches on the command NAME, so a
// new `<something> list --repo` elsewhere in the tree would silently start
// skipping the ordinary prelude the moment its value looked like a repo ID.
func TestOnlySessionListAmongListCommandsTakesARepoFlag(t *testing.T) {
	var found []string
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Name() == "list" && cmd.Flags().Lookup("repo") != nil {
			found = append(found, cmd.CommandPath())
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)
	require.Equal(t, []string{sessionListCmd.CommandPath()}, found)
}

// Failure prevented: a window with nothing in it renders as null arrays a
// consumer has to special-case, or — worse — as the same shape a failed harvest
// would produce. An honest empty window is a real answer and must look like one.
func TestHostedGlanceReportsAnEmptyWindowHonestly(t *testing.T) {
	f := newHostedReaderFixture(t)
	quiet := f.now.Add(-30 * 24 * time.Hour)

	stdout, stderr, err := runOxInProc(t, glanceCmd, "--repo", readSyncTestRepoID,
		"--since", quiet.Format(time.RFC3339), "--until", quiet.Add(time.Hour).Format(time.RFC3339))
	require.NoError(t, err, stderr)

	var activity glance.ActivityData
	require.NoError(t, json.Unmarshal([]byte(stdout), &activity), stdout)
	require.Equal(t, readSyncTestRepoID, activity.Repo)
	require.Equal(t, 0, activity.Stats.TotalMurmurs)
	require.Equal(t, 0, activity.Stats.TotalSessions)
	// Not null: the arrays are part of the schema whether or not they have
	// members, and a consumer decoding them must not have to branch.
	require.NotNil(t, activity.Authors)
	require.NotNil(t, activity.Conflicts)
	require.NotNil(t, activity.Overlap)
	require.Empty(t, activity.Authors)
}

// Failure prevented: an unreadable murmur is skipped and the window comes back
// looking quiet. Under-reporting activity is worse than failing: a coworker
// reads "nobody touched this" and goes ahead.
func TestHostedGlanceFailsOnUnreadableActivity(t *testing.T) {
	f := newHostedReaderFixture(t)

	stdout, stderr, err := runOxInProc(t, glanceCmd, "--repo", readSyncTestRepoID,
		"--since", f.corruptHour.Format(time.RFC3339),
		"--until", f.corruptHour.Add(30*time.Minute).Format(time.RFC3339))
	require.Equal(t, 1, exitCodeOf(t, err))
	require.Empty(t, stdout, "a failed harvest must not render as a quiet window")
	require.Equal(t, "Ledger read failed: unavailable\n", stderr)
}

// Failure prevented: the project-scoped `ox glance` path regressed while the
// hosted one was added. It had no test at all before the harvest/analyze body
// moved into glanceActivity, so nothing held its behavior in place.
func TestProjectGlanceStillReportsItsOwnLedger(t *testing.T) {
	projectRoot, ledgerPath := setupLedgerProject(t)
	t.Chdir(projectRoot)

	now := time.Now().UTC()
	body, err := json.Marshal(ledger.MurmurFile{
		SchemaVersion: "1",
		ID:            "mur_project",
		Timestamp:     now,
		AgentID:       "Ox0002",
		PrincipalID:   "avery",
		Topic:         "wip",
		Importance:    "normal",
		Content:       "project-scoped glance",
	})
	require.NoError(t, err)
	dir := filepath.Join(ledgerPath, ledger.MurmurDateHourDir(now))
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mur_project.json"), body, 0600))

	stdout, stderr, err := runOxInProc(t, glanceCmd,
		"--since", now.Add(-time.Hour).Format(time.RFC3339),
		"--until", now.Add(time.Hour).Format(time.RFC3339))
	require.NoError(t, err, stderr)

	var activity glance.ActivityData
	require.NoError(t, json.Unmarshal([]byte(stdout), &activity), stdout)
	require.Equal(t, filepath.Base(projectRoot), activity.Repo, "project reads label by directory, not repo ID")
	require.Equal(t, 1, activity.Stats.TotalMurmurs)

	// Reading advances the checkpoint, so a later bare invocation resumes from
	// here. The hosted path deliberately does not do this.
	require.False(t, glance.GetSince(ledgerPath).Before(now), "MarkRead must advance the checkpoint")

	// A malformed window is rejected before the ledger is consulted.
	_, _, err = runOxInProc(t, glanceCmd, "--since", "not-a-window")
	require.Error(t, err)
	_, _, err = runOxInProc(t, glanceCmd, "--until", "not-a-window")
	require.Error(t, err)

	// The default invocation resolves its own window from the checkpoint the
	// read above advanced, so it reports nothing new rather than replaying.
	stdout, stderr, err = runOxInProc(t, glanceCmd)
	require.NoError(t, err, stderr)
	require.NoError(t, json.Unmarshal([]byte(stdout), &activity), stdout)
	require.Equal(t, 0, activity.Stats.TotalMurmurs)
}

// Failure prevented: a project read that cannot see its activity reports a
// quiet ledger instead of failing. Each of these is a different way the ledger
// is unreadable, and every one of them must be louder than "nothing happened".
func TestProjectGlanceFailsRatherThanUnderReport(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name   string
		break_ func(t *testing.T, ledgerPath string)
	}{
		{"no ledger on disk", func(t *testing.T, ledgerPath string) {
			require.NoError(t, os.RemoveAll(ledgerPath))
		}},
		{"unreadable murmur", func(t *testing.T, ledgerPath string) {
			dir := filepath.Join(ledgerPath, ledger.MurmurDateHourDir(now))
			require.NoError(t, os.MkdirAll(dir, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0600))
		}},
		{"sessions is not a directory", func(t *testing.T, ledgerPath string) {
			// A regular file where a directory belongs fails on every platform,
			// unlike a chmod, and is what a bad extraction actually leaves behind.
			require.NoError(t, os.WriteFile(filepath.Join(ledgerPath, "sessions"), []byte("x"), 0600))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, ledgerPath := setupLedgerProject(t)
			t.Chdir(projectRoot)
			tc.break_(t, ledgerPath)

			stdout, _, err := runOxInProc(t, glanceCmd,
				"--since", now.Add(-time.Hour).Format(time.RFC3339),
				"--until", now.Add(time.Hour).Format(time.RFC3339))
			require.Error(t, err)
			require.Empty(t, stdout, "a failed harvest must not render as a quiet ledger")
		})
	}
}

// Failure prevented: a reader accepts an endpoint or data home that `ox sync
// --read-only` refuses, so the selection rules diverge between the command that
// writes the checkout and the commands that read it.
func TestHostedReadersRefuseTheSameUnsafeSelection(t *testing.T) {
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("SAGEOX_ENDPOINT", "http://sageox.ai") // not HTTPS

	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"session list", sessionListCmd, []string{"--repo", readSyncTestRepoID, "--json"}},
		{"glance", glanceCmd, []string{"--repo", readSyncTestRepoID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := runOxInProc(t, tc.cmd, tc.args...)
			require.Equal(t, 2, exitCodeOf(t, err), "unsafe selection is an invocation error, not a read failure")
			require.Empty(t, stdout)
			require.Equal(t, "Ledger read failed: invalid_arguments\n", stderr)
		})
	}
}

// Failure prevented: a reader whose flags fail to parse emits the sync
// command's receipt JSON, which a consumer would decode as a plausible — and
// wrong — result for the command it actually ran.
func TestReaderParseFailureDoesNotEmitASyncReceipt(t *testing.T) {
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	args := []string{"session", "list", "--repo", readSyncTestRepoID, "--bogus", "--json"}
	require.Equal(t, 2, writeReadSyncUsageError(cmd, args))
	require.Empty(t, out.String(), "stdout must stay empty; a receipt here decodes as a session list that failed open")
	require.Equal(t, "Ledger read failed: invalid_arguments\n", errOut.String())
}

// Failure prevented: a signal arriving after the checkout lock is taken does not
// stop the reader — the ledger readers walk the filesystem through context-free
// APIs — so the traversal runs to completion and the command reports success for
// a read the caller already abandoned.
func TestHostedReadCanceledMidReadIsNotReportedAsSuccess(t *testing.T) {
	f := newHostedReaderFixture(t)
	require.NotEmpty(t, f.path)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&stderr)

	// The read succeeds; cancellation lands while it is in flight, exactly as a
	// hosted runtime's tool deadline would deliver SIGTERM.
	err := withHostedLedger(cmd, readSyncTestRepoID, func(string) error {
		cancel()
		return nil
	})
	require.Equal(t, 1, exitCodeOf(t, err))
	require.Equal(t, "Ledger read failed: interrupted\n", stderr.String())
}

// Failure prevented: the activity checkpoint advances before the activity is
// delivered, so a failed write silently skips that window forever — the next
// bare `ox glance` resumes past murmurs the consumer never received.
func TestProjectGlanceKeepsTheCheckpointWhenOutputFails(t *testing.T) {
	projectRoot, ledgerPath := setupLedgerProject(t)
	t.Chdir(projectRoot)

	now := time.Now().UTC()
	body, err := json.Marshal(ledger.MurmurFile{
		SchemaVersion: "1", ID: "mur_x", Timestamp: now,
		AgentID: "Ox0003", PrincipalID: "riley", Topic: "wip",
		Importance: "normal", Content: "must survive a failed write",
	})
	require.NoError(t, err)
	dir := filepath.Join(ledgerPath, ledger.MurmurDateHourDir(now))
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mur_x.json"), body, 0600))

	cmd := &cobra.Command{Use: "glance", RunE: glanceCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	cmd.Flags().AddFlagSet(glanceCmd.Flags())
	cmd.PersistentFlags().AddFlagSet(rootCmd.PersistentFlags())
	// A closed pipe is what a consumer that hung up actually leaves behind.
	r, w := io.Pipe()
	require.NoError(t, r.Close())
	defer w.Close()
	cmd.SetOut(w)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--since", now.Add(-time.Hour).Format(time.RFC3339)})
	require.Error(t, cmd.Execute(), "a failed write must surface")

	// With no checkpoint recorded, GetSince falls back to DefaultWindow ago.
	// MarkRead would have stored ~now instead, so the distance from now is what
	// separates "never advanced" from "advanced past undelivered activity" —
	// comparing against a pre-run GetSince cannot, since the fallback itself
	// moves with the clock.
	resume := glance.GetSince(ledgerPath)
	require.Greater(t, time.Since(resume), time.Hour,
		"checkpoint advanced to %v despite the write failing; the next glance would skip that window", resume)
}
