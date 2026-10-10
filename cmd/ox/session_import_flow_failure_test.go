package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failed review, output and confirmation must never authorize upload. An
// interrupt during selection validation must remain context cancellation,
// rather than telling the coworker that an unchanged source was rewritten.
func TestImportReview_FailureBoundariesLeaveLedgerUntouched(t *testing.T) {
	for _, boundary := range []string{"discovery", "review", "selection cancellation", "output", "short output", "confirmation"} {
		t.Run(boundary, func(t *testing.T) {
			f, env, dest, c, _ := reviewedImportHashFixture(t)
			f.interactive = true
			head := runGit(t, f.barePath, "rev-parse", "HEAD")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("review boundary failed")
			var out bytes.Buffer
			var writer io.Writer = &out
			opts := importOptions{browse: true}
			env.deps.review = func(ctx context.Context, _ importDestination, _ []*importCandidate, load importPreviewLoader, _ bool) (importReviewResult, error) {
				if boundary == "review" {
					return importReviewResult{}, sentinel
				}
				_, err := load(ctx, c.Session.NativeID)
				require.NoError(t, err)
				if boundary == "selection cancellation" {
					cancel()
				}
				return importReviewResult{IDs: []string{c.Session.NativeID}}, nil
			}
			confirmations := 0
			env.deps.confirm = func(string) (bool, error) { confirmations++; return false, sentinel }
			switch boundary {
			case "discovery":
				opts.sessions = []string{"missing-session"}
			case "output":
				writer = importFailWriter{sentinel}
			case "short output":
				writer = importShortWriter{}
			}
			err := runSessionImportFlow(ctx, writer, opts, env, dest)
			switch boundary {
			case "discovery":
				assert.Equal(t, importErrBadFlag, errkind.DetailOf(err))
			case "selection cancellation":
				assert.ErrorIs(t, err, context.Canceled)
			case "short output":
				assert.ErrorIs(t, err, io.ErrShortWrite)
			default:
				assert.ErrorIs(t, err, sentinel)
			}
			if boundary == "confirmation" {
				assert.Equal(t, 1, confirmations)
			} else {
				assert.Zero(t, confirmations)
			}
			assertNothingMoved(t, f, head)
			_, err = os.Stat(env.stagingRoot)
			assert.True(t, os.IsNotExist(err), "all failures happen before staging")
		})
	}
}

type importShortWriter struct{}

func (importShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

type importFailWriter struct{ err error }

func (w importFailWriter) Write([]byte) (int, error) { return 0, w.err }

// Browser selection does not authorize an unattended/JSON reader to upload.
// Its resulting preview and rerun command must include only the chosen IDs.
func TestImportReview_BrowserJSONSelectionStillRequiresAuthorization(t *testing.T) {
	f, env, dest, c, _ := reviewedImportHashFixture(t)
	f.interactive = true
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: c.Session.StartedAt, prompt: pushPrompt, reply: "Re-uploaded."})
	env.deps.review = func(ctx context.Context, _ importDestination, _ []*importCandidate, load importPreviewLoader, browse bool) (importReviewResult, error) {
		require.True(t, browse)
		_, err := load(ctx, c.Session.NativeID)
		require.NoError(t, err)
		return importReviewResult{IDs: []string{c.Session.NativeID}}, nil
	}
	env.deps.confirm = func(string) (bool, error) {
		t.Fatal("JSON preview must not ask for upload confirmation")
		return false, nil
	}
	head := runGit(t, f.barePath, "rev-parse", "HEAD")
	var out bytes.Buffer
	err := runSessionImportFlow(context.Background(), &out, importOptions{browse: true, jsonOut: true}, env, dest)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "ox session import --yes --session "+c.Session.NativeID)
	assert.NotContains(t, out.String(), "--session "+e2eCodexA)
	assertNothingMoved(t, f, head)
}

// A cancellation after the under-lock discovery pass must reject the reviewed
// source before workers start. This covers the second authorization boundary,
// independent of the browser/terminal validation pass.
func TestImportReview_UnderLockCancellationStartsNoWorkers(t *testing.T) {
	f, env, dest, c, load := reviewedImportHashFixture(t)
	preview, err := load(context.Background(), c.Session.NativeID)
	require.NoError(t, err)
	ctx := importCancelDuringDigest(t, -1)
	env.deps.usable = func(nativeimport.Agent) bool {
		// Discovery has finished all Git operations; the next polls are the
		// reviewed-source digest entry and buffer reads.
		ctx.cancelAt = ctx.checks + 2
		return true
	}
	head := runGit(t, f.barePath, "rev-parse", "HEAD")
	reads := f.readCount()
	var out bytes.Buffer
	err = runLockedImport(ctx, &out, importOptions{yes: true, reviewed: map[string]importSourceSnapshot{c.Session.NativeID: preview.Snapshot}}, env, dest)
	assert.ErrorIs(t, err, context.Canceled)
	assertNothingMoved(t, f, head)
	assert.Equal(t, reads, f.readCount())
	staged, err := filepath.Glob(filepath.Join(env.stagingRoot, "*", "raw.jsonl"))
	require.NoError(t, err)
	assert.Empty(t, staged)
}

// A summarizer failure after raw conversion removes that session's staging
// tree and cannot publish partial artifacts or turn the failure into a skip.
func TestImportPrepare_SummarizerFailureCleansConvertedSession(t *testing.T) {
	f, env, _, c, _ := reviewedImportHashFixture(t)
	sentinel := errors.New("summarizer transport disconnected")
	f.summarizer.fail = func(string) error { return sentinel }
	head := runGit(t, f.barePath, "rev-parse", "HEAD")
	prepared, skip, err := prepareImport(context.Background(), env, c)
	assert.Nil(t, prepared)
	assert.Empty(t, skip)
	require.Error(t, err)
	assert.Contains(t, err.Error(), sentinel.Error())
	assert.Greater(t, f.summarizer.calls(), 0)
	_, err = os.Stat(filepath.Join(env.stagingRoot, c.Name))
	assert.True(t, os.IsNotExist(err))
	assert.Zero(t, f.store.count())
	assert.Equal(t, head, runGit(t, f.barePath, "rev-parse", "HEAD"))
}
