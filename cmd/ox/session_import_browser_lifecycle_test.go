package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A listener failure while the review waits for a callback must fail the import
// before terminal confirmation, rather than treating an interrupted review as
// an empty or successful selection.
func TestImportBrowserUnexpectedListenerExitDoesNotImport(t *testing.T) {
	f := newImportFixture(t)
	f.interactive = true
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), prompt: loginPrompt, reply: "Check the cookie."})
	env, dest := f.envFor(f.ledgerPath, importOptions{browse: true})
	head := runGit(t, f.barePath, "rev-parse", "HEAD")
	confirmed := 0
	env.deps.confirm = func(string) (bool, error) { confirmed++; return true, nil }
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	env.deps.review = func(ctx context.Context, dest importDestination, cands []*importCandidate, load importPreviewLoader, _ bool) (importReviewResult, error) {
		return serveImportBrowser(ctx, ln, dest, cands, load, func(link string) error {
			u, err := url.Parse(link)
			if err != nil {
				return err
			}
			// A real request proves Serve reached its accepting state before
			// the external listener closure interrupts it.
			u.Fragment = ""
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Get(u.String())
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			return ln.Close()
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	err = runSessionImportFlow(ctx, &out, importOptions{browse: true}, env, dest)
	require.ErrorIs(t, err, net.ErrClosed)
	assert.ErrorContains(t, err, "local session browser stopped")
	assert.Zero(t, confirmed)
	assertNothingMoved(t, f, head)
	_, err = ln.Accept()
	assert.ErrorIs(t, err, net.ErrClosed)
}

// A misbehaving reader may ignore request cancellation. The existing two-second
// graceful shutdown deadline must still force-close its socket and let Ctrl-C
// finish; no confirmation, summary, staged content or upload may follow.
func TestImportBrowserForceClosesStalledPreviewWithoutImport(t *testing.T) {
	if testing.Short() {
		t.Skip("holds a request through the two-second graceful shutdown deadline")
	}
	f := newImportFixture(t)
	f.interactive = true
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), prompt: loginPrompt, reply: "Check the cookie."})
	env, dest := f.envFor(f.ledgerPath, importOptions{browse: true})
	head := runGit(t, f.barePath, "rev-parse", "HEAD")
	confirmed := 0
	env.deps.confirm = func(string) (bool, error) { confirmed++; return true, nil }
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	started, handlerDone := make(chan struct{}), make(chan struct{})
	linkReady := make(chan string, 1)
	env.deps.review = func(ctx context.Context, dest importDestination, cands []*importCandidate, load importPreviewLoader, _ bool) (importReviewResult, error) {
		stalled := func(ctx context.Context, id string) (*importContentPreview, error) {
			close(started)
			defer close(handlerDone)
			<-release // deliberately ignores the canceled request context
			return load(ctx, id)
		}
		return serveImportBrowser(ctx, ln, dest, cands, stalled, func(link string) error { linkReady <- link; return nil })
	}
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() { done <- runSessionImportFlow(ctx, &out, importOptions{browse: true}, env, dest) }()
	var link string
	select {
	case link = <-linkReady:
	case <-time.After(5 * time.Second):
		t.Fatal("browser did not start")
	}
	u, err := url.Parse(link)
	require.NoError(t, err)
	capability, err := url.ParseQuery(u.Fragment)
	require.NoError(t, err)
	u.Fragment, u.Path, u.RawQuery = "", "/api/preview", "id="+e2eClaudeA
	requestCtx, requestCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer requestCancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, u.String(), nil)
	require.NoError(t, err)
	req.Header.Set("X-Import-Token", capability.Get("token"))
	requestDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("preview request did not enter the stalled reader")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C remained blocked after the browser shutdown deadline")
	}
	select {
	case err := <-requestDone:
		require.Error(t, err, "the stalled preview socket must be force-closed")
		assert.False(t, errors.Is(err, context.DeadlineExceeded), "socket closes before the client's independent timeout")
	case <-time.After(time.Second):
		t.Fatal("browser returned while its stalled request socket remained open")
	}
	select {
	case <-handlerDone:
		t.Fatal("the reader finished before force-close was exercised")
	default:
	}
	unblock()
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("test reader did not finish after release")
	}
	assert.Zero(t, confirmed)
	assertNothingMoved(t, f, head)
	_, err = ln.Accept()
	assert.ErrorIs(t, err, net.ErrClosed)
}
