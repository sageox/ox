package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parallelImportFixture exercises real discovery, redaction, staging, LFS upload,
// scoped commits and pushes. The CLI test also uses the production subprocess
// runners; only their executables and the remote LFS service are hermetic fakes.
func parallelImportFixture(t *testing.T) *importFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("parallel import integration tests use subprocesses and real Git remotes")
	}
	return newImportFixture(t)
}

// startParallelImport runs the full import with a deadline so tests can coordinate worker barriers.
func startParallelImport(t *testing.T, f *importFixture, opts importOptions, configure func(*importEnv)) <-chan importRun {
	t.Helper()
	ctx := f.ctx
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
		t.Cleanup(cancel)
	}
	env, dest := f.envFor(f.ledgerPath, opts)
	if configure != nil {
		configure(env)
	}
	done := make(chan importRun, 1)
	go func() {
		var out bytes.Buffer
		err := runSessionImportFlow(ctx, &out, opts, env, dest)
		done <- importRun{out: out.String(), err: err}
	}()
	return done
}

// finishParallelImport collects the final JSON report after every worker has drained.
func finishParallelImport(t *testing.T, done <-chan importRun) importRun {
	t.Helper()
	select {
	case r := <-done:
		require.NoError(t, json.Unmarshal([]byte(r.out), &r.report), r.out)
		return r
	case <-time.After(20 * time.Second):
		t.Fatal("parallel import did not finish")
		return importRun{}
	}
}

// awaitParallelEvent bounds a barrier wait to turn worker deadlocks into test failures.
func awaitParallelEvent[T any](t *testing.T, events <-chan T) T {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(10 * time.Second):
		t.Fatal("parallel import did not reach its barrier")
		var zero T
		return zero
	}
}

// TestImportParallelProductionRunnersAreShared verifies concurrent preparation
// initializes one runner per vendor and does not mix runners between vendors.
func TestImportParallelProductionRunnersAreShared(t *testing.T) {
	env := &importEnv{projectRoot: t.TempDir(), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	deps := productionImportDeps(context.Background(), env)
	type lookup struct {
		agent  nativeimport.Agent
		runner agentwork.Runner
	}
	results := make(chan lookup, 32)
	var workers sync.WaitGroup
	for range 16 {
		for _, agent := range []nativeimport.Agent{nativeimport.AgentClaude, nativeimport.AgentCodex} {
			workers.Go(func() { results <- lookup{agent: agent, runner: deps.runner(agent)} })
		}
	}
	workers.Wait()
	close(results)
	first := map[nativeimport.Agent]agentwork.Runner{}
	for result := range results {
		require.NotNil(t, result.runner)
		if previous := first[result.agent]; previous != nil {
			assert.Same(t, previous, result.runner, "same-vendor workers share the initialized runner")
		} else {
			first[result.agent] = result.runner
		}
	}
	assert.NotSame(t, first[nativeimport.AgentClaude], first[nativeimport.AgentCodex])
}

// parallelCLIRunners installs shell binaries for vendor capability probes and
// sends summary stdin to a local HTTP barrier. Actual summary subprocesses run
// concurrently, rather than merely proving that a mock Runner can overlap.
func parallelCLIRunners(t *testing.T, summarize func(context.Context, nativeimport.Agent, string) string) map[nativeimport.Agent]agentwork.Runner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("hermetic vendor CLI executables use POSIX shell")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prompt, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		agent := nativeimport.Agent(strings.TrimPrefix(r.URL.Path, "/"))
		output := summarize(r.Context(), agent, string(prompt))
		if agent == nativeimport.AgentClaude {
			_ = json.NewEncoder(w).Encode(map[string]string{"type": "result", "result": output})
		} else {
			_, _ = io.WriteString(w, output)
		}
	}))
	t.Cleanup(server.Close)
	bin := t.TempDir()
	for _, agent := range []nativeimport.Agent{nativeimport.AgentClaude, nativeimport.AgentCodex} {
		body := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *--help*) printf '%%s\n' '--safe-mode --tools --no-session-persistence --sandbox --ephemeral --color --config --ignore-user-config --ignore-rules --skip-git-repo-check --disable'; exit 0 ;;
  'features list') printf '%%s\n' 'shell_tool stable true' 'unified_exec stable true'; exit 0 ;;
esac
test "$OX_SESSION_RECORDING" = disabled || exit 2
test "$SAGEOX_DAEMON" = false || exit 2
exec /usr/bin/curl --silent --show-error --fail --data-binary @- '%s/%s'
`, server.URL, agent)
		require.NoError(t, os.WriteFile(filepath.Join(bin, string(agent)), []byte(body), 0o755))
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return map[nativeimport.Agent]agentwork.Runner{
		nativeimport.AgentClaude: agentwork.NewClaudeRunner(logger),
		nativeimport.AgentCodex:  agentwork.NewCodexRunner(logger),
	}
}

// TestImportE2E_ParallelCLISummariesHonorGlobalLimit checks that real subprocess
// summaries share one global capacity across both vendors, including capacity one.
func TestImportE2E_ParallelCLISummariesHonorGlobalLimit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		agents   []nativeimport.Agent
		parallel int
	}{
		{"same Claude CLI", []nativeimport.Agent{nativeimport.AgentClaude, nativeimport.AgentClaude, nativeimport.AgentClaude, nativeimport.AgentClaude}, 2},
		{"same Codex CLI", []nativeimport.Agent{nativeimport.AgentCodex, nativeimport.AgentCodex, nativeimport.AgentCodex, nativeimport.AgentCodex}, 2},
		{"mixed CLIs", []nativeimport.Agent{nativeimport.AgentClaude, nativeimport.AgentCodex, nativeimport.AgentClaude, nativeimport.AgentCodex}, 2},
		{"sequential escape hatch", []nativeimport.Agent{nativeimport.AgentClaude, nativeimport.AgentCodex, nativeimport.AgentClaude, nativeimport.AgentCodex}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := parallelImportFixture(t)
			release := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			started := make(chan nativeimport.Agent, len(tc.agents))
			var mu sync.Mutex
			active, maximum, calls := 0, 0, 0
			runners := parallelCLIRunners(t, func(ctx context.Context, agent nativeimport.Agent, prompt string) string {
				mu.Lock()
				active++
				calls++
				if active > maximum {
					maximum = active
				}
				mu.Unlock()
				defer func() { mu.Lock(); active--; mu.Unlock() }()
				started <- agent
				select {
				case <-release:
				case <-ctx.Done():
				}
				return e2eSummaryFor(prompt)
			})
			start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
			for i, agent := range tc.agents {
				f.add(t, pastSession{agent: agent, id: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), start: start.Add(time.Duration(i) * time.Hour), prompt: loginPrompt, reply: "Fixed the cookie."})
			}
			done := startParallelImport(t, f, importOptions{yes: true, jsonOut: true, parallel: tc.parallel}, func(env *importEnv) {
				env.deps.runner = func(agent nativeimport.Agent) agentwork.Runner { return runners[agent] }
			})
			for range tc.parallel {
				awaitParallelEvent(t, started)
			}
			select {
			case agent := <-started:
				t.Errorf("another %s summary started while the entire pool was blocked", agent)
			case <-time.After(100 * time.Millisecond):
			}
			releaseOnce.Do(func() { close(release) })
			r := finishParallelImport(t, done)
			require.NoError(t, r.err, r.out)
			mu.Lock()
			assert.Equal(t, tc.parallel, maximum, "the configured capacity is used and never exceeded across vendors")
			assert.Equal(t, len(tc.agents), calls)
			mu.Unlock()
			assert.Len(t, remoteSessionDirs(t, f.barePath), len(tc.agents))
		})
	}
}

// observeParallelPublication records prior commits at each LFS batch. Each
// session must finish publication, including its commit, before the next batch
// begins; per-file blob upload concurrency inside one session remains allowed.
func observeParallelPublication(t *testing.T, f *importFixture, gate <-chan struct{}, entered chan<- struct{}) (*lfs.Client, func() []string) {
	t.Helper()
	upstream, err := url.Parse(f.store.url)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var proxyURL string
	proxy.ModifyResponse = func(response *http.Response) error {
		if !strings.HasSuffix(response.Request.URL.Path, "/objects/batch") {
			return nil
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			return err
		}
		// Keep every action on the client's trusted batch host, including
		// uploads and verifies that the upstream fake otherwise names directly.
		data = bytes.ReplaceAll(data, []byte(f.store.url), []byte(proxyURL))
		response.Body = io.NopCloser(bytes.NewReader(data))
		response.ContentLength = int64(len(data))
		response.Header.Set("Content-Length", fmt.Sprint(len(data)))
		return nil
	}
	var mu sync.Mutex
	var priorCommits []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/objects/batch") {
			count, err := gitutil.RunGit(r.Context(), f.ledgerPath, "rev-list", "--count", "@{upstream}..HEAD")
			if err != nil {
				count = err.Error()
			}
			mu.Lock()
			priorCommits = append(priorCommits, strings.TrimSpace(count))
			first := len(priorCommits) == 1
			mu.Unlock()
			if first {
				entered <- struct{}{}
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	proxyURL = server.URL
	return lfs.NewClient(server.URL+"/ledger.git", "oauth2", "test-token"), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), priorCommits...)
	}
}

// TestImportE2E_ParallelPublicationWaitsForPriorCommit prevents workers from
// uploading another session before the single committer has finished the prior one.
func TestImportE2E_ParallelPublicationWaitsForPriorCommit(t *testing.T) {
	f := parallelImportFixture(t)
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(gate) }) })
	client, counts := observeParallelPublication(t, f, gate, entered)
	finished := make(chan struct{}, 3)
	f.summarizer.reply = func(prompt string) string { finished <- struct{}{}; return e2eSummaryFor(prompt) }
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	for i, id := range []string{e2eClaudeA, e2eClaudeB, e2eClaudeC} {
		f.add(t, pastSession{agent: nativeimport.AgentClaude, id: id, start: start.Add(time.Duration(i) * time.Hour), prompt: loginPrompt, reply: "Fixed the cookie."})
	}
	done := startParallelImport(t, f, importOptions{yes: true, jsonOut: true, parallel: 3}, func(env *importEnv) {
		env.deps.lfsClient = func() (*lfs.Client, error) { return client, nil }
	})
	awaitParallelEvent(t, entered)
	for range 3 {
		awaitParallelEvent(t, finished)
	}
	once.Do(func() { close(gate) })
	r := finishParallelImport(t, done)
	require.NoError(t, r.err, r.out)
	assert.Equal(t, []string{"0", "1", "2"}, counts(), "LFS publication never overlaps another session's uncommitted publication")
	assert.Len(t, remoteSessionDirs(t, f.barePath), 3)
}

// TestImportE2E_ParallelReportsKeepPreviewOrder verifies completion order can
// drive publication without reordering the coworker's final report.
func TestImportE2E_ParallelReportsKeepPreviewOrder(t *testing.T) {
	f := parallelImportFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f.ctx, f.pushBatch = ctx, 1
	laterPublished := make(chan struct{})
	var once sync.Once
	f.summarizer.before = func(prompt string) {
		if strings.Contains(prompt, loginPrompt) {
			select {
			case <-laterPublished:
			case <-ctx.Done():
			}
		}
	}
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	for i, s := range []pastSession{
		{agent: nativeimport.AgentClaude, id: e2eClaudeA, prompt: loginPrompt, reply: "Fixed the cookie."},
		{agent: nativeimport.AgentCodex, id: e2eCodexA, prompt: pushPrompt, reply: "Re-uploaded the object."},
		{agent: nativeimport.AgentClaude, id: e2eClaudeB, prompt: tokenPrompt, reply: "The deploy bot."},
	} {
		s.start = start.Add(time.Duration(i) * time.Hour)
		f.add(t, s)
	}
	var published []string
	done := startParallelImport(t, f, importOptions{yes: true, jsonOut: true, parallel: 2}, func(env *importEnv) {
		env.deps.notify = func(meta *lfs.SessionMeta, _ string) {
			published = append(published, meta.Title)
			if meta.Title == e2eTitleFor(pushPrompt) {
				once.Do(func() { close(laterPublished) })
			}
		}
	})
	r := finishParallelImport(t, done)
	require.NoError(t, r.err, r.out)
	require.Len(t, published, 3)
	assert.Equal(t, e2eTitleFor(pushPrompt), published[0], "a completed later session is published before the blocked earlier summary")
	var ids []string
	for _, s := range r.report.Sessions {
		ids = append(ids, s.NativeID)
		assert.Equal(t, "uploaded", s.Outcome)
	}
	assert.Equal(t, []string{e2eClaudeA, e2eCodexA, e2eClaudeB}, ids, "JSON order follows the preview, not worker completion")
}

// TestImportE2E_ParallelVerdictsAreAllRemembered prevents concurrent local-only
// decisions from overwriting each other and triggering unnecessary summaries on rerun.
func TestImportE2E_ParallelVerdictsAreAllRemembered(t *testing.T) {
	f := parallelImportFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f.ctx = ctx
	barrier := make(chan struct{})
	var mu sync.Mutex
	arrived := 0
	f.summarizer.reply = func(prompt string) string {
		mu.Lock()
		arrived++
		if arrived == 3 {
			close(barrier)
		}
		mu.Unlock()
		select {
		case <-barrier:
		case <-ctx.Done():
			return ""
		}
		if strings.Contains(prompt, pushPrompt) {
			return `{"quality_category":"skip","score_reason":"No useful work to share."}`
		}
		return `{"title":"Keep account research local","summary":"Checked a local account setup.","key_actions":["Checked the account"],"outcome":"success","quality_category":"local_only","score_reason":"Personal local account setup."}`
	}
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	ids := []string{e2eClaudeA, e2eCodexA, e2eClaudeB}
	for i, s := range []pastSession{
		{agent: nativeimport.AgentClaude, id: ids[0], prompt: loginPrompt, reply: "Fixed the cookie."},
		{agent: nativeimport.AgentCodex, id: ids[1], prompt: pushPrompt, reply: "Re-uploaded the object."},
		{agent: nativeimport.AgentClaude, id: ids[2], prompt: tokenPrompt, reply: "The deploy bot."},
	} {
		s.start = start.Add(time.Duration(i) * time.Hour)
		f.add(t, s)
	}
	r := finishParallelImport(t, startParallelImport(t, f, importOptions{yes: true, jsonOut: true, parallel: 3}, nil))
	require.NoError(t, r.err, r.out)
	for _, id := range ids {
		assert.Equal(t, "skipped", r.session(t, id).Outcome)
	}
	assert.Empty(t, remoteSessionDirs(t, f.barePath))
	assert.Zero(t, f.store.count(), "local verdicts upload nothing")
	again := f.run(t, importOptions{yes: true, jsonOut: true, parallel: 3})
	require.NoError(t, again.err, again.out)
	for _, id := range ids {
		assert.Equal(t, string(stateNotShared), again.session(t, id).State, "every concurrent verdict survives on disk")
	}
	assert.Equal(t, 3, f.summarizer.calls(), "remembered verdicts avoid another summary")
}

// TestImportE2E_ParallelWaitingPublicationRechecksNativeFile prevents uploading
// a session that resumed while its prepared artifacts waited for publication.
func TestImportE2E_ParallelWaitingPublicationRechecksNativeFile(t *testing.T) {
	f := parallelImportFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f.ctx = ctx
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(gate) }) })
	client, counts := observeParallelPublication(t, f, gate, entered)
	firstPublishing := make(chan struct{})
	f.summarizer.before = func(prompt string) {
		if strings.Contains(prompt, pushPrompt) {
			select {
			case <-firstPublishing:
			case <-ctx.Done():
			}
		}
	}
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	later := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeB, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded the object."})
	preview := f.run(t, importOptions{jsonOut: true})
	require.NoError(t, preview.err, preview.out)
	stagedMeta := filepath.Join(importStagingRoot(f.ledgerPath), preview.session(t, e2eClaudeB).SessionName, "meta.json")
	done := startParallelImport(t, f, importOptions{yes: true, jsonOut: true, parallel: 2}, func(env *importEnv) {
		env.deps.lfsClient = func() (*lfs.Client, error) { return client, nil }
	})
	awaitParallelEvent(t, entered)
	close(firstPublishing)
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(stagedMeta)
		if err != nil {
			return false
		}
		var meta lfs.SessionMeta
		return json.Unmarshal(data, &meta) == nil && meta.Title != ""
	}, 5*time.Second, 10*time.Millisecond, "the later summary is prepared while the committer is occupied")
	appendClaudePrompt(t, later, e2eClaudeB, f.projectRoot, "one more question", start.Add(2*time.Hour))
	once.Do(func() { close(gate) })
	r := finishParallelImport(t, done)
	require.NoError(t, r.err, r.out)
	assert.Equal(t, "uploaded", r.session(t, e2eClaudeA).Outcome)
	changed := r.session(t, e2eClaudeB)
	assert.Equal(t, "skipped", changed.Outcome)
	assert.Equal(t, "became active since the preview", changed.Detail)
	assert.Equal(t, []string{"0"}, counts(), "the queued session is held before any LFS batch")
	assert.Zero(t, f.store.holding(pushPrompt), "the changed native session never leaves the machine")
	assert.Equal(t, []string{r.session(t, e2eClaudeA).SessionName}, remoteSessionDirs(t, f.barePath))
	assert.Empty(t, strings.TrimSpace(runGit(t, f.ledgerPath, "status", "--porcelain")), "all abandoned staging and committed publication leave a clean Ledger")
}

// TestImportE2E_ParallelInterruptRetriesCommittedPushWithLiveContext verifies
// cancellation stops new summaries while committed sessions still reach the remote.
func TestImportE2E_ParallelInterruptRetriesCommittedPushWithLiveContext(t *testing.T) {
	f := parallelImportFixture(t)
	deadline, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(deadline)
	defer cancel()
	f.ctx, f.pushBatch = ctx, 1
	blocked := make(chan struct{}, 2)
	f.summarizer.before = func(prompt string) {
		if strings.Contains(prompt, loginPrompt) {
			for range 2 {
				select {
				case <-blocked:
				case <-ctx.Done():
				}
			}
			return
		}
		blocked <- struct{}{}
		<-ctx.Done()
	}
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	for i, s := range []pastSession{
		{agent: nativeimport.AgentClaude, id: e2eClaudeA, prompt: loginPrompt, reply: "Fixed the cookie."},
		{agent: nativeimport.AgentCodex, id: e2eCodexA, prompt: pushPrompt, reply: "Re-uploaded the object."},
		{agent: nativeimport.AgentClaude, id: e2eClaudeB, prompt: tokenPrompt, reply: "The deploy bot."},
		{agent: nativeimport.AgentClaude, id: e2eClaudeC, prompt: tokenPrompt, reply: "The deploy bot."},
	} {
		s.start = start.Add(time.Duration(i) * time.Hour)
		f.add(t, s)
	}
	pushes := 0
	var finalPushContextErr error
	done := startParallelImport(t, f, importOptions{yes: true, jsonOut: true, parallel: 3}, func(env *importEnv) {
		push := env.deps.push
		env.deps.push = func(pushCtx context.Context, ledger string) error {
			pushes++
			if pushes == 1 {
				cancel() // first commit exists; Ctrl-C aborts the batch push
				return context.Canceled
			}
			finalPushContextErr = pushCtx.Err()
			return push(pushCtx, ledger)
		}
	})
	r := finishParallelImport(t, done)
	assert.ErrorIs(t, r.err, cli.ErrSilent)
	assert.Equal(t, "interrupted", postHogErrorKind(r.err, 1))
	assert.Equal(t, 2, pushes, "the failed interrupted batch is retried at shutdown")
	assert.NoError(t, finalPushContextErr, "shutdown push is bounded but detached from the canceled summary context")
	doneSession := r.session(t, e2eClaudeA)
	assert.Equal(t, "uploaded", doneSession.Outcome)
	assert.Equal(t, []string{doneSession.SessionName}, remoteSessionDirs(t, f.barePath))
	assert.Empty(t, strings.TrimSpace(runGit(t, f.ledgerPath, "log", "--oneline", "@{upstream}..HEAD")), "the completed session is never stranded locally")
	for _, id := range []string{e2eCodexA, e2eClaudeB, e2eClaudeC} {
		s := r.session(t, id)
		assert.Equal(t, "failed", s.Outcome)
		assert.Contains(t, s.Detail, "interrupted")
		assert.Contains(t, s.Retry, id)
	}
	assert.Contains(t, r.session(t, e2eClaudeC).Detail, "not started")
	assert.Equal(t, 3, f.summarizer.calls(), "queued work does not start after cancellation")
}

// TestImportE2E_ParallelCapturedMathBlitzFixture sends captured Desktop formats
// through real adapter executables and publication at both capacities.
// Summary models, LFS and the Git
// remote are hermetic: this is correctness evidence, not live CLI timing.
func TestImportE2E_ParallelCapturedMathBlitzFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("captured-session import builds and runs real adapter executables")
	}
	adapterDir := t.TempDir()
	for _, name := range []string{"claude-code", "codex"} {
		binary := filepath.Join(adapterDir, "ox-adapter-"+name)
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		build := exec.Command("go", "build", "-o", binary, "./cmd/ox-adapter-"+name)
		build.Dir = repoPath("..", "..")
		output, err := build.CombinedOutput()
		require.NoError(t, err, "build %s: %s", name, output)
		adapter, err := adapters.NewExternalAdapter(binary)
		require.NoError(t, err)
		var prior adapters.Adapter
		for _, registered := range adapters.ListAdapters() {
			if registered == name {
				prior, err = adapters.GetAdapter(name)
				require.NoError(t, err)
				break
			}
		}
		adapters.Unregister(name)
		adapters.Register(adapter)
		t.Cleanup(func() {
			adapters.Unregister(name)
			if prior != nil {
				adapters.Register(prior)
			}
		})
	}
	for _, parallel := range []int{1, 3} {
		t.Run(fmt.Sprintf("parallel%d", parallel), func(t *testing.T) {
			f := parallelImportFixture(t)
			testData := seedCapturedSessions(t, repoPath("testdata", "session-import", "math-blitz"), f.projectRoot)
			opts := importOptions{yes: true, jsonOut: true, parallel: parallel, testData: testData}
			env, dest := f.envFor(f.ledgerPath, opts)
			env.deps.readNative = readNativeWithAdapter
			run := func() importRun {
				var out bytes.Buffer
				r := importRun{err: runSessionImportFlow(context.Background(), &out, opts, env, dest)}
				r.out = out.String()
				require.NoError(t, json.Unmarshal(out.Bytes(), &r.report), r.out)
				return r
			}
			r := run()
			require.NoError(t, r.err, r.out)
			require.Len(t, r.report.Sessions, 7)
			assert.Equal(t, 1, r.report.Ignored.InternalThreads)
			assert.Zero(t, r.report.Ignored.Unreadable)
			var reportedIDs, committedNames []string
			for _, s := range r.report.Sessions {
				reportedIDs = append(reportedIDs, s.NativeID)
				assert.True(t, s.Selected, s.NativeID)
				switch s.Outcome {
				case "uploaded":
					committedNames = append(committedNames, s.SessionName)
					meta := remoteMeta(t, f.barePath, s.SessionName)
					require.Len(t, meta.NativeSessions, 1, s.NativeID)
					assert.Equal(t, s.NativeID, meta.NativeSessions[0].ID)
					assert.Equal(t, s.SessionID, meta.EffectiveSessionID())
					assert.NotEmpty(t, meta.Title)
				case "skipped":
					assert.Contains(t, s.Detail, "not worth sharing", "only the brief-session prefilter may skip this fixture")
				default:
					t.Errorf("captured session %s was neither uploaded nor judged brief: %s (%s)", s.NativeID, s.Outcome, s.Detail)
				}
			}
			assert.ElementsMatch(t, []string{
				"01a0f957-40ec-7192-b67b-71c990b28f23",
				"01a0f968-60ef-7500-aaa4-13cbb34e5557",
				"21bb267b-4585-4b4c-b7ca-062f545259f0",
				"8fa7dffa-45e9-4298-91be-05a4ea0df37b",
				"077ecc79-bdf7-4708-b558-7ff5dd82fe5b",
				"01a0f8f3-d483-7643-b43d-e3fa4d8cee96",
				"185a01e8-9b38-46b6-a686-b6a4e3509c3d",
			}, reportedIDs)
			assert.ElementsMatch(t, committedNames, remoteSessionDirs(t, f.barePath))
			assert.NotEmpty(t, committedNames, "substantial captured sessions must be published")
			calls, objects := f.summarizer.calls(), f.store.count()
			head := runGit(t, f.barePath, "rev-parse", "HEAD")
			again := run()
			require.NoError(t, again.err, again.out)
			for _, s := range again.report.Sessions {
				assert.False(t, s.Selected, "imports and brief verdicts are both remembered")
				assert.Contains(t, []string{string(stateAlreadyImported), string(stateNotShared)}, s.State)
			}
			assert.Equal(t, calls, f.summarizer.calls(), "rerun never summarizes captured sessions again")
			assert.Equal(t, objects, f.store.count(), "rerun uploads no duplicate objects")
			assert.Equal(t, head, runGit(t, f.barePath, "rev-parse", "HEAD"), "rerun adds no duplicate commit")
		})
	}
}
