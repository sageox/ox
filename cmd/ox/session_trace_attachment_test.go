package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/require"
)

// Trace capture is optional: a trace upload or materialization failure cannot
// block publishing the ordinary recording, nor copy trace content into git.
func TestTraceAttachmentAndFailureIsolation(t *testing.T) {
	for _, door := range []string{"stop", "doctor"} {
		t.Run(door, func(t *testing.T) {
			for _, mode := range []string{"success", "upload-failure", "partial-refs-error", "partial-refs-nil", "invalid-boundary"} {
				t.Run(mode, func(t *testing.T) {
					f := newSessionUploadFixture(t)
					const id = "12345678-1234-4234-8234-123456789abc"
					data := []byte("{\"resourceSpans\":[{\"scopeSpans\":[{\"spans\":[{\"name\":\"selected\",\"attributes\":[{\"key\":\"user.email\",\"value\":{\"stringValue\":\"private@example.com\"}}]}]}]}]}\n")
					dir := filepath.Join(paths.TraceSpoolDir(), id)
					require.NoError(t, os.MkdirAll(dir, 0700))
					require.NoError(t, os.WriteFile(filepath.Join(dir, "traces.jsonl"), data, 0600))
					f.state.Trace = &model.Capture{Boundaries: []model.Boundary{
						{Action: "start", At: time.Now(), Offsets: map[string]model.Offsets{id: {}}},
						{Action: "stop", At: time.Now(), Offsets: map[string]model.Offsets{id: {Spans: int64(len(data))}}},
					}}
					if mode == "invalid-boundary" {
						f.state.Trace.Boundaries[1].Offsets[id] = model.Offsets{Spans: -1}
					}
					var calls []string
					effects := scriptedSessionUploadEffects(&calls, f.refs, "")
					traceCalled := false
					effects.uploadTraces = func(_, cache, ledger string) (map[string]lfs.FileRef, error) {
						traceCalled = true
						for _, name := range []string{pipeline.LedgerFileTraceSpans, pipeline.LedgerFileTraceEvents} {
							_, err := os.Stat(filepath.Join(ledger, name))
							require.True(t, os.IsNotExist(err))
						}
						if mode == "upload-failure" {
							return nil, errors.New("trace-only outage")
						}
						if mode == "partial-refs-error" || mode == "partial-refs-nil" {
							refs := map[string]lfs.FileRef{pipeline.LedgerFileTraceSpans: lfs.NewFileRef([]byte("partial"))}
							if mode == "partial-refs-error" {
								return refs, errors.New("second pointer failed")
							}
							return refs, nil
						}
						// Stand-in upload service confirms bytes; real Batch API behavior is
						// covered by lfs.TestTracePublishNeverCopiesContentIntoLedger.
						return lfs.PublishTraceFiles(cache, ledger, func(b []byte) (lfs.UploadedRef, error) { return lfs.AssertUploaded(lfs.NewFileRef(b)), nil })
					}
					if door == "doctor" {
						require.NoError(t, session.StampRawCarrier(f.result.RawPath, session.CarrierStamp{TraceCapture: f.state.Trace, StoppedAt: time.Now()}))
						meta, count, err := readCacheSessionMeta(f.result.RawPath)
						require.NoError(t, err)
						require.Equal(t, f.state.Trace.Boundaries[1].Offsets, meta.TraceCapture.Boundaries[1].Offsets)
						orphan := f.orphan()
						orphan.Meta, orphan.EntryCount = meta, count
						updatedRaw, err := os.ReadFile(f.result.RawPath)
						require.NoError(t, err)
						f.refs[ledgerFileRaw] = lfs.NewFileRef(updatedRaw)
						require.NoError(t, retrySessionUploadWithEffects(f.projectRoot, f.ledgerPath, orphan, effects))
						require.Contains(t, calls, "commit_retry")
					} else {
						require.NoError(t, uploadSessionToLedgerWithEffects(f.projectRoot, f.result, f.state, f.ledgerPath, f.sessionName, effects))
						require.Contains(t, calls, "commit_initial")
					}
					require.Equal(t, mode != "invalid-boundary", traceCalled)
					meta, err := lfs.ReadSessionMeta(filepath.Join(f.ledgerPath, "sessions", f.sessionName))
					require.NoError(t, err)
					if mode == "success" {
						require.NotNil(t, meta.Trace)
						require.EqualValues(t, 1, meta.Trace.Scrubbed["user.email"])
					} else {
						require.Nil(t, meta.Trace)
					}
					for _, name := range []string{pipeline.LedgerFileTraceSpans, pipeline.LedgerFileTraceEvents} {
						path := filepath.Join(f.ledgerPath, "sessions", f.sessionName, name)
						if mode == "success" {
							require.True(t, lfs.IsPointerFile(path))
							require.Equal(t, "lfs", meta.Files[name].Storage)
						} else {
							_, err := os.Stat(path)
							require.True(t, os.IsNotExist(err))
							require.NotContains(t, meta.Files, name)
						}
					}
				})
			}

		})
	}
}
