package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Context polls provide deterministic boundaries inside a synchronous file
// stream. The faults still replace/truncate real files, without sleeps or
// product hooks that would change the reader's behavior under test.
type importDigestBoundaryContext struct {
	context.Context
	checks int
	at     int
	action func()
}

// Err injects a real filesystem change at a known hash poll, exposing races
// inside digest validation without relying on goroutine timing.
func (c *importDigestBoundaryContext) Err() error {
	c.checks++
	if c.checks == c.at {
		c.action()
	}
	return c.Context.Err()
}

// Truncation must not hash a prefix, and replacing the pathname must not pin
// the old open file even when the replacement preserves size and mtime.
func TestImportContent_DigestRejectsConcurrentFileChanges(t *testing.T) {
	for _, fault := range []string{"truncate during stream", "replace after stream"} {
		t.Run(fault, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "native.jsonl")
			data := bytes.Repeat([]byte("a"), 64*1024)
			require.NoError(t, os.WriteFile(path, data, 0o600))
			info, err := os.Stat(path)
			require.NoError(t, err)
			snapshot := importSourceSnapshot{Size: info.Size(), ModTime: info.ModTime()}
			at := 2 // before the first read, after the initial file stat
			if fault == "replace after stream" {
				at = 4
			} // two buffers have been read
			ctx := &importDigestBoundaryContext{Context: context.Background(), at: at, action: func() {
				if fault == "truncate during stream" {
					require.NoError(t, os.Truncate(path, 0))
					return
				}
				replacement := path + ".replacement"
				require.NoError(t, os.WriteFile(replacement, bytes.Repeat([]byte("b"), len(data)), 0o600))
				require.NoError(t, os.Chtimes(replacement, info.ModTime(), info.ModTime()))
				require.NoError(t, os.Rename(replacement, path))
			}}
			digest, err := importSourceDigest(ctx, path, snapshot)
			assert.ErrorIs(t, err, errImportSourceChanged)
			assert.Empty(t, digest, "never return a digest for an incomplete/replaced source")
		})
	}
}

// A canceled preview neither pins an incomplete read nor consumes a reader
// slot permanently; the same unchanged source remains reviewable afterward.
func TestImportContent_LoaderCancellationBoundaries(t *testing.T) {
	for _, boundary := range []string{"discovery check", "initial digest", "final digest"} {
		t.Run(boundary, func(t *testing.T) {
			f, env, _, c, load := reviewedImportHashFixture(t)
			ctx := importCancelDuringDigest(t, 2)
			if boundary == "initial digest" {
				ctx.cancelAt = 4
			}
			if boundary == "final digest" {
				ctx.cancelAt = -1
				env.deps.readNative = func(readCtx context.Context, agent nativeimport.Agent, path string) ([]adapters.RawEntry, error) {
					raw, err := f.readNative(readCtx, agent, path)
					// Loader cancellation check, final digest entry, then its buffer poll.
					ctx.cancelAt = ctx.checks + 3
					return raw, err
				}
			}
			preview, err := load(ctx, c.Session.NativeID)
			assert.Nil(t, preview)
			assert.ErrorIs(t, err, context.Canceled)
			assert.Empty(t, importSnapshot(c).SHA256)
			env.deps.readNative = f.readNative
			preview, err = load(context.Background(), c.Session.NativeID)
			require.NoError(t, err)
			assert.Equal(t, loginPrompt, preview.OpeningRequest)
			assert.Zero(t, f.summarizer.calls())
			assert.Zero(t, f.store.count())
		})
	}
}

// A third request can cancel while both native-reader slots are occupied.
// It must not enter the adapter, and releasing the other requests must restore
// capacity for a later preview.
func TestImportContent_CanceledQueueDoesNotEnterNativeReader(t *testing.T) {
	f, env, _, c, load := reviewedImportHashFixture(t)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	env.deps.readNative = func(ctx context.Context, agent nativeimport.Agent, path string) ([]adapters.RawEntry, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return f.readNative(ctx, agent, path)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := load(context.Background(), c.Session.NativeID); results <- err }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("native readers did not occupy both slots")
		}
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	checked := make(chan struct{})
	resume := make(chan struct{})
	ctx := &importDigestBoundaryContext{Context: parent, at: 2, action: func() { close(checked); <-resume }}
	// Capture the pre-cancellation result at the discovery boundary so the
	// pending request reaches the select while cancellation races with enqueue.
	queueCtx := &importQueueCancelContext{importDigestBoundaryContext: ctx}
	queued := make(chan error, 1)
	go func() { _, err := load(queueCtx, c.Session.NativeID); queued <- err }()
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Fatal("queued request did not reach discovery check")
	}
	cancel()
	close(resume)
	select {
	case err := <-queued:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("queued cancellation blocked")
	}
	assert.Empty(t, entered, "canceled queued request never enters native reader")
	close(release)
	for range 2 {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("active preview did not finish")
		}
	}
	preview, err := load(context.Background(), c.Session.NativeID)
	require.NoError(t, err)
	assert.Equal(t, loginPrompt, preview.OpeningRequest)
	assert.Equal(t, 2, f.readCount(), "successful cached request requires no extra adapter read")
}

type importQueueCancelContext struct{ *importDigestBoundaryContext }

// Err intentionally returns the state from before cancellation. The queued
// reader must observe Done even when its immediately preceding Err check was nil.
func (c *importQueueCancelContext) Err() error {
	before := c.Context.Err()
	_ = c.importDigestBoundaryContext.Err()
	return before
}

// Cache eviction bounds retained conversation memory while preserving the
// immutable review pin and allowing evicted sessions to be read again.
func TestImportContent_LargeSessionCacheEvictsAndReloads(t *testing.T) {
	if testing.Short() {
		t.Skip("large retained-session cache exercise exceeds the short-test budget")
	}
	f := newImportFixture(t)
	start := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	for i, id := range []string{e2eClaudeA, e2eClaudeB} {
		f.add(t, pastSession{agent: nativeimport.AgentClaude, id: id, start: start.Add(time.Duration(i) * time.Hour), prompt: strings.Repeat("review this request ", 350000), reply: id})
	}
	env, _ := f.envFor(f.ledgerPath, importOptions{})
	cands, _, failure := planImport(context.Background(), importOptions{}, env)
	require.Nil(t, failure)
	load := newImportPreviewLoader(env, cands)
	first, err := load(context.Background(), e2eClaudeA)
	require.NoError(t, err)
	second, err := load(context.Background(), e2eClaudeB)
	require.NoError(t, err)
	require.Greater(t, importPreviewBytes(first)+importPreviewBytes(second), 32<<20)
	require.Less(t, importPreviewBytes(first), 32<<20)
	cached, err := load(context.Background(), e2eClaudeB)
	require.NoError(t, err)
	assert.Same(t, second, cached)
	reloaded, err := load(context.Background(), e2eClaudeA)
	require.NoError(t, err)
	assert.NotSame(t, first, reloaded)
	assert.Equal(t, first, reloaded)
	assert.Equal(t, 3, f.readCount())
	assert.Zero(t, importPreviewBytes(nil))
}

// Two overlapping reads may observe different native bytes with identical
// discovery metadata. Only the first accepted preview may establish a pin;
// the other version must not silently replace it or enter the cache.
func TestImportContent_ConcurrentVersionsCannotReplaceAcceptedPin(t *testing.T) {
	f, env, _, c, load := reviewedImportHashFixture(t)
	info, err := os.Stat(c.Session.Path)
	require.NoError(t, err)
	original, err := os.ReadFile(c.Session.Path)
	require.NoError(t, err)
	changed := bytes.Replace(original, []byte("Fixed."), []byte("Other."), 1)
	require.NotEqual(t, original, changed)
	rewrite := func(data []byte) {
		require.NoError(t, os.WriteFile(c.Session.Path, data, 0o600))
		require.NoError(t, os.Chtimes(c.Session.Path, info.ModTime(), info.ModTime()))
	}
	entered := make(chan int, 2)
	gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var reads atomic.Int32
	env.deps.readNative = func(ctx context.Context, agent nativeimport.Agent, path string) ([]adapters.RawEntry, error) {
		n := int(reads.Add(1)) - 1
		raw, err := f.readNative(ctx, agent, path)
		if n == 1 {
			raw[len(raw)-1].Content = "Other."
		}
		entered <- n
		select {
		case <-gates[n]:
			return raw, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		p   *importContentPreview
		err error
	}
	results := []chan result{make(chan result, 1), make(chan result, 1)}
	launch := func(n int) { go func() { p, err := load(ctx, c.Session.NativeID); results[n] <- result{p, err} }() }
	launch(0)
	select {
	case n := <-entered:
		require.Zero(t, n)
	case <-time.After(5 * time.Second):
		t.Fatal("first adapter did not start")
	}
	rewrite(changed)
	launch(1)
	select {
	case n := <-entered:
		require.Equal(t, 1, n)
	case <-time.After(5 * time.Second):
		t.Fatal("second adapter did not start")
	}
	rewrite(original)
	close(gates[0])
	var first result
	select {
	case first = <-results[0]:
		require.NoError(t, first.err)
	case <-time.After(5 * time.Second):
		t.Fatal("first preview did not finish")
	}
	rewrite(changed)
	close(gates[1])
	select {
	case second := <-results[1]:
		assert.Nil(t, second.p)
		assert.ErrorIs(t, second.err, errImportSourceChanged)
	case <-time.After(5 * time.Second):
		t.Fatal("second preview did not finish")
	}
	assert.Equal(t, first.p.Snapshot, importSnapshot(c))
	rewrite(original)
	cached, err := load(context.Background(), c.Session.NativeID)
	require.NoError(t, err)
	assert.Same(t, first.p, cached)
	assert.Equal(t, "Fixed.", cached.LastReply)
	assert.Zero(t, f.store.count())
}

// A request that streamed another version while the first preview populated
// the cache must reject that cache hit, even before entering its own adapter.
func TestImportContent_ConcurrentCacheFillRejectsDifferentDigest(t *testing.T) {
	f, env, _, c, load := reviewedImportHashFixture(t)
	info, err := os.Stat(c.Session.Path)
	require.NoError(t, err)
	original, err := os.ReadFile(c.Session.Path)
	require.NoError(t, err)
	changed := bytes.Replace(original, []byte("Fixed."), []byte("Other."), 1)
	require.NotEqual(t, original, changed)
	require.Less(t, info.Size(), int64(32*1024))
	rewrite := func(data []byte) {
		require.NoError(t, os.WriteFile(c.Session.Path, data, 0o600))
		require.NoError(t, os.Chtimes(c.Session.Path, info.ModTime(), info.ModTime()))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	env.deps.readNative = func(ctx context.Context, agent nativeimport.Agent, path string) ([]adapters.RawEntry, error) {
		raw, err := f.readNative(ctx, agent, path)
		close(entered)
		select {
		case <-release:
			return raw, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	type result struct {
		p   *importContentPreview
		err error
	}
	firstDone := make(chan result, 1)
	go func() { p, err := load(ctx, c.Session.NativeID); firstDone <- result{p, err} }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first read did not enter adapter")
	}
	rewrite(changed)
	streamed, resume := make(chan struct{}), make(chan struct{})
	secondCtx := &importDigestBoundaryContext{Context: ctx, at: 5, action: func() {
		close(streamed)
		select {
		case <-resume:
		case <-ctx.Done():
		}
	}}
	secondDone := make(chan result, 1)
	go func() { p, err := load(secondCtx, c.Session.NativeID); secondDone <- result{p, err} }()
	select {
	case <-streamed:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent request did not finish its stream")
	}
	rewrite(original)
	close(release)
	var first result
	select {
	case first = <-firstDone:
		require.NoError(t, first.err)
	case <-time.After(5 * time.Second):
		t.Fatal("first read did not populate cache")
	}
	rewrite(changed)
	close(resume)
	select {
	case second := <-secondDone:
		assert.Nil(t, second.p)
		assert.ErrorIs(t, second.err, errImportSourceChanged)
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent cache lookup did not finish")
	}
	assert.Equal(t, 1, f.readCount(), "different digest is refused before another adapter read")
	assert.Equal(t, first.p.Snapshot, importSnapshot(c))
	rewrite(original)
	cached, err := load(context.Background(), c.Session.NativeID)
	require.NoError(t, err)
	assert.Same(t, first.p, cached)
}
