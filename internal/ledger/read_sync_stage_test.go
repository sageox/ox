package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a cold sync that fails after materializing most of the
// ledger reports coverage.files 0 and hydration "unknown", so its result reads
// exactly like an attempt that did nothing, and nothing in it says the next
// attempt resumes the stage instead of cloning again (ox #983). Every way
// hydration can end short must report what the stage holds, leaving ready,
// error_class, and error_detail as they were.
func TestReadSyncColdFailureReportsTheStageItKept(t *testing.T) {
	names := []string{"a", "b", "c"}
	contents, byOID := map[string][]byte{}, map[string]string{}
	for _, name := range names {
		contents[name] = []byte("cold stage object " + name + "\n")
		byOID[lfs.ComputeOID(contents[name])] = name
	}
	refused := func(code int) *ReadFailureDetail {
		return &ReadFailureDetail{Reason: "download_refused", Path: "sessions/cold/a.md",
			OID: lfs.ComputeOID(contents["a"]), ServerCode: code}
	}
	for _, tc := range []struct {
		name, errorClass string
		// status answers a's download; 0 cancels the attempt while a is in flight.
		status int
		detail *ReadFailureDetail
	}{
		{"object refused", "missing_hydration", http.StatusNotFound, refused(http.StatusNotFound)},
		{"access revoked", "denied", http.StatusUnauthorized, refused(http.StatusUnauthorized)},
		{"budget expired", "interrupted", 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transfers := map[string]*atomic.Int32{}
			for _, name := range names {
				transfers[name] = new(atomic.Int32)
			}
			var holding atomic.Bool
			holding.Store(true)
			release := make(chan struct{})
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/batch") {
					grantReadLFSBatch(t, w, r)
					return
				}
				name, ok := byOID[filepath.Base(r.URL.Path)]
				if !assert.True(t, ok, "only a requested object may be downloaded") {
					http.NotFound(w, r)
					return
				}
				transfers[name].Add(1)
				if name == "a" && holding.Load() {
					select {
					case <-release:
						w.WriteHeader(tc.status)
					case <-r.Context().Done():
					}
					return
				}
				_, _ = w.Write(contents[name])
			})
			for _, name := range names {
				commitReadLFSPointer(t, f, "sessions/cold/"+name+".md", contents[name])
			}
			stage := readStagePath(f.opts.Path)
			landed := func(name string) bool {
				actual, err := os.ReadFile(filepath.Join(stage, "sessions/cold", name+".md"))
				return err == nil && bytes.Equal(contents[name], actual)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan ReadSyncResult, 1)
			go func() { finished <- ReadSync(ctx, f.opts) }()
			// a sorts first in its batch, so ending the attempt there — after b
			// and c are on disk — leaves progress only an honest result reports.
			require.Eventually(t, func() bool { return landed("b") && landed("c") }, 30*time.Second, 5*time.Millisecond)
			if tc.status == 0 {
				cancel()
			} else {
				close(release)
			}
			result := <-finished

			require.False(t, result.Ready, "%+v", result)
			require.Equal(t, tc.errorClass, result.ErrorClass, "%+v", result)
			require.Equal(t, tc.detail, result.ErrorDetail)
			require.NoDirExists(t, f.opts.Path, "a failed cold clone is never published")
			require.Equal(t, ReadHydration{State: "missing", Required: 3, Completed: 2}, result.Hydration, "%+v", result)
			require.True(t, result.Resumable, "%+v", result)
			transport, err := gitserver.NewReadTransport(f.opts.Endpoint, f.opts.RepoID, f.opts.ReadURL)
			require.NoError(t, err)
			onDisk := verifyReadCheckout(context.Background(), f.opts, transport, stage, result.Coverage.Paths)
			require.Positive(t, onDisk.Coverage.Files)
			require.Equal(t, onDisk.Coverage, result.Coverage, "coverage counts the stage as it is on disk")
			require.Equal(t, onDisk.Hydration, result.Hydration, "hydration counts the stage as it is on disk")
			data, err := json.Marshal(result)
			require.NoError(t, err)
			require.Contains(t, string(data), `"resumable":true`)

			// The claim is only worth making if it holds: the next attempt
			// continues from the stage instead of transferring b and c again.
			holding.Store(false)
			resumed := ReadSync(context.Background(), f.opts)
			require.True(t, resumed.Ready, "%+v", resumed)
			require.False(t, resumed.Resumable, "publishing consumes the stage")
			require.Equal(t, int32(2), transfers["a"].Load())
			require.Equal(t, int32(1), transfers["b"].Load(), "a landed object is never transferred again")
			require.Equal(t, int32(1), transfers["c"].Load())
		})
	}
}
