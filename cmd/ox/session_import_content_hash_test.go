package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rewriteImportReplyPreservingMetadata changes valid conversation content while
// retaining IDs, timestamps, counts, size and mtime. Metadata-only checks cannot
// detect this rewrite.
func rewriteImportReplyPreservingMetadata(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	after := bytes.Replace(before, []byte("Fixed."), []byte("Other."), 1)
	require.NotEqual(t, before, after)
	require.Equal(t, len(before), len(after))
	require.NoError(t, os.WriteFile(path, after, 0o600))
	require.NoError(t, os.Chtimes(path, info.ModTime(), info.ModTime()))
	updated, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, info.Size(), updated.Size())
	require.True(t, info.ModTime().Equal(updated.ModTime()))
}

// reviewedImportHashFixture discovers one native session without reading it.
// The caller must load its preview to establish the reviewed content digest.
func reviewedImportHashFixture(t *testing.T) (*importFixture, *importEnv, importDestination, *importCandidate, importPreviewLoader) {
	t.Helper()
	f := newImportFixture(t)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC), prompt: loginPrompt, reply: "Fixed."})
	env, dest := f.envFor(f.ledgerPath, importOptions{})
	cands, _, failure := planImport(context.Background(), importOptions{}, env)
	require.Nil(t, failure)
	require.Len(t, cands, 1)
	return f, env, dest, cands[0], newImportPreviewLoader(env, cands)
}

// Prevents metadata-preserving native rewrites from serving cached old content
// or being accepted as the reviewed selection. Hashes remain local and lazy.
func TestImportContent_HashPinsOnlyPreviewedFilesAndRejectsRewrites(t *testing.T) {
	f, env, _, c, _ := reviewedImportHashFixture(t)
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: c.Session.StartedAt.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded."})
	cands, _, failure := planImport(context.Background(), importOptions{}, env)
	require.Nil(t, failure)
	load := newImportPreviewLoader(env, cands)
	for _, candidate := range cands {
		assert.Empty(t, importSnapshot(candidate).SHA256, "discovery must not hash native content")
		if candidate.Session.NativeID == e2eClaudeA {
			c = candidate
		}
	}
	preview, err := load(context.Background(), e2eClaudeA)
	require.NoError(t, err)
	require.Len(t, preview.Snapshot.SHA256, 64)
	for _, candidate := range cands {
		if candidate.Session.NativeID != e2eClaudeA {
			assert.Empty(t, importSnapshot(candidate).SHA256)
		}
	}
	encoded, err := json.Marshal(preview)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), preview.Snapshot.SHA256, "review digest is not a browser/wire field")
	assert.NotContains(t, string(encoded), "SHA256")
	reads := f.readCount()
	rewriteImportReplyPreservingMetadata(t, c.Session.Path)
	_, err = load(context.Background(), e2eClaudeA)
	assert.ErrorIs(t, err, errImportSourceChanged)
	assert.Equal(t, reads, f.readCount(), "reject changed cached content before another adapter read")
	_, err = validateImportReview(context.Background(), cands, importReviewResult{IDs: []string{e2eClaudeA}})
	assert.ErrorIs(t, err, errImportSourceChanged)
	assert.Zero(t, f.summarizer.calls())
	assert.Zero(t, f.store.count())
}

// Prevents an adapter read of content that changed after the initial digest
// from producing a preview or a review pin for different native bytes.
func TestImportContent_HashRejectsRewriteDuringPreviewRead(t *testing.T) {
	f, env, _, c, load := reviewedImportHashFixture(t)
	env.deps.readNative = func(ctx context.Context, agent nativeimport.Agent, path string) ([]adapters.RawEntry, error) {
		raw, err := f.readNative(ctx, agent, path)
		rewriteImportReplyPreservingMetadata(t, path)
		return raw, err
	}
	preview, err := load(context.Background(), c.Session.NativeID)
	assert.Nil(t, preview)
	assert.ErrorIs(t, err, errImportSourceChanged)
	assert.Empty(t, importSnapshot(c).SHA256, "failed preview must not pin different content")
	assert.Zero(t, f.summarizer.calls())
	assert.Zero(t, f.store.count())
}

// Prevents a same-size/restored-mtime rewrite during terminal confirmation
// from passing the under-lock replan and reaching summarization or upload.
func TestImportReview_HashRechecksAfterFinalConfirmation(t *testing.T) {
	f, env, dest, c, _ := reviewedImportHashFixture(t)
	f.interactive = true
	head := runGit(t, f.barePath, "rev-parse", "HEAD")
	env.deps.review = func(ctx context.Context, _ importDestination, _ []*importCandidate, load importPreviewLoader, _ bool) (importReviewResult, error) {
		_, err := load(ctx, e2eClaudeA)
		require.NoError(t, err)
		return importReviewResult{IDs: []string{e2eClaudeA}}, nil
	}
	env.deps.confirm = func(string) (bool, error) {
		rewriteImportReplyPreservingMetadata(t, c.Session.Path)
		return true, nil
	}
	var out bytes.Buffer
	err := runSessionImportFlow(context.Background(), &out, importOptions{}, env, dest)
	require.Error(t, err)
	assert.Equal(t, importErrNativeUnreadable, errkind.DetailOf(err))
	assertNothingMoved(t, f, head)
	staged, err := filepath.Glob(filepath.Join(env.stagingRoot, "*", "raw.jsonl"))
	require.NoError(t, err)
	assert.Empty(t, staged)
}

// Prevents a native rewrite before or during adapter conversion from writing
// retained content to staging, despite size/mtime remaining unchanged.
func TestImportPrepare_HashChecksBeforeAndAfterNativeRead(t *testing.T) {
	for _, boundary := range []string{"before native read", "during native read"} {
		t.Run(boundary, func(t *testing.T) {
			f, env, _, c, load := reviewedImportHashFixture(t)
			_, err := load(context.Background(), c.Session.NativeID)
			require.NoError(t, err)
			head := runGit(t, f.barePath, "rev-parse", "HEAD")
			reads := f.readCount()
			if boundary == "before native read" {
				rewriteImportReplyPreservingMetadata(t, c.Session.Path)
			} else {
				env.deps.readNative = func(ctx context.Context, agent nativeimport.Agent, path string) ([]adapters.RawEntry, error) {
					raw, err := f.readNative(ctx, agent, path)
					rewriteImportReplyPreservingMetadata(t, path)
					return raw, err
				}
			}
			prepared, skip, err := prepareImport(context.Background(), env, c)
			require.NoError(t, err)
			assert.Nil(t, prepared)
			assert.Contains(t, skip, "changed since the content review")
			assertNothingMoved(t, f, head)
			_, err = os.Stat(filepath.Join(env.stagingRoot, c.Name))
			assert.True(t, os.IsNotExist(err), "no staged retained bytes may survive")
			if boundary == "before native read" {
				assert.Equal(t, reads, f.readCount())
			} else {
				assert.Equal(t, reads+1, f.readCount())
			}
		})
	}
}

// Prevents a rewrite after preparation from leaking a reviewed session through
// the later publication boundary after it no longer matches the source.
func TestImportPublish_HashRechecksPreparedSource(t *testing.T) {
	f, env, _, c, load := reviewedImportHashFixture(t)
	_, err := load(context.Background(), c.Session.NativeID)
	require.NoError(t, err)
	prepared, skip, err := prepareImport(context.Background(), env, c)
	require.NoError(t, err)
	require.Empty(t, skip)
	require.NotNil(t, prepared)
	defer prepared.cleanup()
	rewriteImportReplyPreservingMetadata(t, c.Session.Path)
	skip, err = publishPreparedImport(context.Background(), env, c, prepared)
	require.NoError(t, err)
	assert.Contains(t, skip, "changed since the content review")
	assert.Zero(t, f.store.count())
}

// Proves the streaming digest covers data spanning multiple buffers and rejects
// unreadable/non-file/changed-metadata sources without exposing local paths.
func TestImportContent_StreamingDigestAndSafeFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.jsonl")
	content := bytes.Repeat([]byte("session content\n"), 32*1024)
	require.NoError(t, os.WriteFile(path, content, 0o600))
	info, err := os.Stat(path)
	require.NoError(t, err)
	snapshot := importSourceSnapshot{Size: info.Size(), ModTime: info.ModTime()}
	digest, err := importSourceDigest(context.Background(), path, snapshot)
	require.NoError(t, err)
	expected := sha256.Sum256(content)
	assert.Equal(t, hex.EncodeToString(expected[:]), digest)
	for _, tc := range []struct {
		path     string
		snapshot importSourceSnapshot
	}{
		{path + ".missing", snapshot},
		{filepath.Dir(path), snapshot},
		{path, importSourceSnapshot{Size: snapshot.Size + 1, ModTime: snapshot.ModTime}},
		{path, importSourceSnapshot{Size: snapshot.Size, ModTime: snapshot.ModTime.Add(time.Second)}},
	} {
		_, err := importSourceDigest(context.Background(), tc.path, tc.snapshot)
		assert.ErrorIs(t, err, errImportSourceChanged)
		assert.NotContains(t, err.Error(), tc.path)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = importSourceDigest(ctx, path, snapshot)
	assert.ErrorIs(t, err, context.Canceled)
}

// Cancels on a chosen context poll so a test can exercise cancellation inside
// a synchronous digest without a timing-dependent sleep or a product hook.
type importDigestCancelContext struct {
	context.Context
	cancel   context.CancelFunc
	checks   int
	cancelAt int
}

// Err cancels at a chosen digest poll, making cancellation inside a streamed
// hash deterministic without sleeps or a production-only test hook.
func (c *importDigestCancelContext) Err() error {
	c.checks++
	if c.checks == c.cancelAt {
		c.cancel()
	}
	return c.Context.Err()
}

// importCancelDuringDigest chooses the poll that interrupts hashing and owns
// context cleanup, allowing tests to distinguish pre-read and digest cancellation.
func importCancelDuringDigest(t *testing.T, cancelAt int) *importDigestCancelContext {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &importDigestCancelContext{Context: ctx, cancel: cancel, cancelAt: cancelAt}
}

// Cancellation in a digest is an interrupt, never a persisted hold verdict
// claiming that an unchanged session was rewritten. Staging still drains.
func TestImportPrepare_HashCancellationPreservesInterrupt(t *testing.T) {
	for _, boundary := range []string{"before native read", "after native read"} {
		t.Run(boundary, func(t *testing.T) {
			f, env, _, c, load := reviewedImportHashFixture(t)
			_, err := load(context.Background(), c.Session.NativeID)
			require.NoError(t, err)
			require.Less(t, c.Session.Size, int64(32*1024), "fixture digest spans one buffer")
			// One entry poll precedes the digest's initial, buffer, and final
			// polls. Cancel at the last poll, after bytes were streamed.
			ctx := importCancelDuringDigest(t, 4)
			if boundary == "after native read" {
				ctx.cancelAt = -1
				env.deps.readNative = func(readCtx context.Context, agent nativeimport.Agent, path string) ([]adapters.RawEntry, error) {
					raw, err := f.readNative(readCtx, agent, path)
					ctx.cancelAt = ctx.checks + 3
					return raw, err
				}
			}
			prepared, skip, err := prepareImport(ctx, env, c)
			assert.Nil(t, prepared)
			assert.Empty(t, skip, "interrupt must not turn into a changed-source hold")
			assert.ErrorIs(t, err, context.Canceled)
			assert.Zero(t, f.summarizer.calls())
			assert.Zero(t, f.store.count())
			_, err = os.Stat(filepath.Join(env.stagingRoot, c.Name))
			assert.True(t, os.IsNotExist(err))
		})
	}
}

// An interrupt during the final source digest must return context.Canceled,
// not a changed-source skip, and must prevent every upload.
func TestImportPublish_HashCancellationPreservesInterrupt(t *testing.T) {
	f, env, _, c, load := reviewedImportHashFixture(t)
	_, err := load(context.Background(), c.Session.NativeID)
	require.NoError(t, err)
	prepared, skip, err := prepareImport(context.Background(), env, c)
	require.NoError(t, err)
	require.Empty(t, skip)
	require.NotNil(t, prepared)
	defer prepared.cleanup()
	ctx := importCancelDuringDigest(t, 4)
	skip, err = publishPreparedImport(ctx, env, c, prepared)
	assert.Empty(t, skip)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, f.store.count())
}

// Review validation must retain cancellation identity during hashing instead
// of treating an interrupted read as a changed native source.
func TestImportReview_HashCancellationPreservesInterrupt(t *testing.T) {
	_, _, _, c, load := reviewedImportHashFixture(t)
	_, err := load(context.Background(), c.Session.NativeID)
	require.NoError(t, err)
	ctx := importCancelDuringDigest(t, 3)
	_, err = validateImportReview(ctx, []*importCandidate{c}, importReviewResult{IDs: []string{c.Session.NativeID}})
	assert.ErrorIs(t, err, context.Canceled)
}

// Proves concurrent row/detail requests publish one immutable content pin
// safely, and every returned preview describes the same native bytes.
func TestImportContent_ConcurrentPreviewsShareImmutableHashPin(t *testing.T) {
	_, _, _, c, load := reviewedImportHashFixture(t)
	var wg sync.WaitGroup
	results := make(chan *importContentPreview, 8)
	errors := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := load(context.Background(), c.Session.NativeID)
			results <- p
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	for preview := range results {
		require.NotNil(t, preview)
		assert.Equal(t, importSnapshot(c), preview.Snapshot)
		assert.Len(t, preview.Snapshot.SHA256, 64)
	}
}
