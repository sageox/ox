package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: hydration walks past every object it cannot materialize
// but reports only the first, so a cause shared by thousands of objects reads
// as one bad object and sends an operator to repair it (ox #984). The result
// must count every failure walked past, tally them by reason, and list a bounded
// sample of each reason — while error_class and error_detail still describe the
// first failure exactly as they did, and a lone failure is reported exactly as
// it was before the summary existed.
func TestReadSyncLFSSkippedAccountsForEveryFailureWalkedPast(t *testing.T) {
	const objects = 10
	paths := make([]string, 0, objects)
	contents := make(map[string][]byte, objects)
	oids := make(map[string]string, objects)
	for i := range objects {
		path := fmt.Sprintf("sessions/skipped/object-%03d.md", i)
		content := []byte(fmt.Sprintf("skipped object %03d\n", i))
		paths = append(paths, path)
		oids[path] = lfs.ComputeOID(content)
		contents[oids[path]] = content
	}
	grantRefused := func(path string) ReadFailureDetail {
		return ReadFailureDetail{Reason: "object_refused", Path: path, OID: oids[path], ServerCode: http.StatusNotFound}
	}
	downloadRefused := func(path string) ReadFailureDetail {
		return ReadFailureDetail{Reason: "download_refused", Path: path, OID: oids[path], ServerCode: http.StatusNotFound}
	}
	// Seven refused grants are more than a sample of one reason holds. The two
	// refused downloads are a second reason, met only after the first has filled
	// its share, and the sample still lists them.
	manyFailures := &ReadSkipped{
		Total:   9,
		Reasons: map[string]int{"object_refused": 7, "download_refused": 2},
		Sample: []ReadFailureDetail{
			grantRefused(paths[0]), grantRefused(paths[1]), grantRefused(paths[2]),
			grantRefused(paths[3]), grantRefused(paths[4]),
			downloadRefused(paths[7]), downloadRefused(paths[8]),
		},
	}
	for _, tc := range []struct {
		name string
		// Paths whose grant, or whose download, the server refuses with a 404,
		// and paths whose download it denies with a 403. Grants are judged before
		// any download starts, so a refused grant is met first even when its path
		// sorts later.
		refuseGrant, refuseDownload, denyDownload []string
		// cold makes this sync a first clone. Its unpublished stage reports a
		// hydration failure before verification runs, which is the second of
		// the two places such a failure reaches the result.
		cold    bool
		class   string
		detail  ReadFailureDetail
		skipped *ReadSkipped
	}{
		{
			name:        "one failure",
			refuseGrant: paths[7:8],
			class:       "missing_hydration",
			detail:      grantRefused(paths[7]),
		},
		{
			name:           "many failures",
			refuseGrant:    paths[:7],
			refuseDownload: paths[7:9],
			class:          "missing_hydration",
			detail:         grantRefused(paths[0]),
			skipped:        manyFailures,
		},
		{
			name:           "many failures in a cold clone",
			refuseGrant:    paths[:7],
			refuseDownload: paths[7:9],
			cold:           true,
			class:          "missing_hydration",
			detail:         grantRefused(paths[0]),
			skipped:        manyFailures,
		},
		{
			// Hydration walks past both refused grants, then stops at the denial.
			// The result reports the denial, so a summary of the refusals beside
			// it would pair "denied" with objects that were never denied.
			name:         "stopped after failures",
			refuseGrant:  paths[7:9],
			denyDownload: paths[2:3],
			class:        "denied",
			detail:       ReadFailureDetail{Reason: "download_refused", Path: paths[2], OID: oids[paths[2]], ServerCode: http.StatusForbidden},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusedGrants, downloadStatus := map[string]bool{}, map[string]int{}
			for _, path := range tc.refuseGrant {
				refusedGrants[oids[path]] = true
			}
			for _, path := range tc.refuseDownload {
				downloadStatus[oids[path]] = http.StatusNotFound
			}
			for _, path := range tc.denyDownload {
				downloadStatus[oids[path]] = http.StatusForbidden
			}
			var refuse atomic.Bool
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/batch") {
					oid := filepath.Base(r.URL.Path)
					if status := downloadStatus[oid]; refuse.Load() && status != 0 {
						w.WriteHeader(status)
						return
					}
					_, _ = w.Write(contents[oid])
					return
				}
				var request struct {
					Objects []lfs.BatchObject `json:"objects"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				response := lfs.BatchResponse{}
				for _, object := range request.Objects {
					granted := lfs.BatchResponseObject{OID: object.OID, Size: object.Size}
					if refuse.Load() && refusedGrants[object.OID] {
						granted.Error = &lfs.ObjectError{Code: http.StatusNotFound, Message: "refused"}
					} else {
						granted.Actions = &lfs.Actions{Download: &lfs.Action{
							Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + object.OID,
						}}
					}
					response.Objects = append(response.Objects, granted)
				}
				assert.NoError(t, json.NewEncoder(w).Encode(response))
			})
			if !tc.cold {
				require.True(t, ReadSync(context.Background(), f.opts).Ready)
			}
			refuse.Store(true)
			for _, path := range paths {
				commitReadLFSPointer(t, f, path, contents[oids[path]])
			}

			result := ReadSync(context.Background(), f.opts)
			require.False(t, result.Ready, "%+v", result)
			require.Equal(t, tc.class, result.ErrorClass, "%+v", result)
			require.Equal(t, &tc.detail, result.ErrorDetail, "error_detail still names the failure it named before")
			require.Equal(t, tc.skipped, result.Skipped)
			rendered, err := json.Marshal(result)
			require.NoError(t, err)
			if tc.skipped == nil {
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(rendered, &fields))
				require.NotContains(t, fields, "skipped", "the result reads exactly as it did before skipped existed")
			} else {
				require.Contains(t, string(rendered), `"skipped":{"total":9,"reasons":{"download_refused":2,"object_refused":7},"sample":[{"reason":"object_refused","path":"sessions/skipped/object-000.md",`,
					"the summary's wire names are the contract a consumer parses")
			}
			switch {
			case tc.cold:
				require.NoDirExists(t, f.opts.Path, "a cold clone with a missing object is never published")
			case len(tc.denyDownload) == 0:
				// A denial cancels the downloads beside it, so how many of them
				// finished first is timing; the counts are exact everywhere else.
				failed := len(tc.refuseGrant) + len(tc.refuseDownload)
				require.Equal(t, ReadHydration{State: "missing", Required: objects, Completed: objects - failed}, result.Hydration)
			}

			refuse.Store(false)
			recovered := ReadSync(context.Background(), f.opts)
			require.True(t, recovered.Ready, "%+v", recovered)
			require.Nil(t, recovered.Skipped, "a recovered sync carries no stale summary")
			for _, path := range paths {
				actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
				require.NoError(t, err)
				require.Equal(t, contents[oids[path]], actual, path)
			}
		})
	}
}

// Failure prevented: the summary miscounts what it summarizes. A failure that
// carries no reason — a failed batch request, a local write failure — vanishes
// from the total, so the count understates the damage; or it drags a reason
// into error_detail that error_detail never carried, changing the first
// failure's report for consumers that read only that.
func TestReadSkippedCountsEveryFailureAndTalliesTheReasonedOnes(t *testing.T) {
	refused := func(path string) error {
		return &readFailure{err: &lfs.HTTPError{StatusCode: http.StatusNotFound},
			detail: ReadFailureDetail{Reason: "object_refused", Path: path, ServerCode: http.StatusNotFound}}
	}
	batchFailed := &lfs.HTTPError{StatusCode: http.StatusServiceUnavailable}
	for _, tc := range []struct {
		name     string
		failures []error
		class    string
		detail   *ReadFailureDetail
		skipped  *ReadSkipped
		wire     string
	}{
		{
			name:     "a lone failure is reported by class and detail alone",
			failures: []error{refused("sessions/a.md")},
			class:    "missing_hydration",
			detail:   &ReadFailureDetail{Reason: "object_refused", Path: "sessions/a.md", ServerCode: http.StatusNotFound},
		},
		{
			name:     "a failure without a reason is counted but neither tallied nor listed",
			failures: []error{batchFailed, refused("sessions/a.md")},
			class:    "missing_hydration",
			// A failed batch request named no object before the summary existed,
			// and it still names none.
			skipped: &ReadSkipped{Total: 2, Reasons: map[string]int{"object_refused": 1},
				Sample: []ReadFailureDetail{{Reason: "object_refused", Path: "sessions/a.md", ServerCode: http.StatusNotFound}}},
		},
		{
			name:     "failures without any reason still render a tally and a sample",
			failures: []error{os.ErrPermission, batchFailed},
			class:    "git_failed",
			skipped:  &ReadSkipped{Total: 2, Reasons: map[string]int{}, Sample: []ReadFailureDetail{}},
			wire:     `"skipped":{"total":2,"reasons":{},"sample":[]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var skips readSkips
			for _, err := range tc.failures {
				require.True(t, skips.skip(context.Background(), err))
			}
			var result ReadSyncResult
			recordReadFailure(context.Background(), &result, skips.err())
			require.Equal(t, tc.class, result.ErrorClass)
			require.Equal(t, tc.detail, result.ErrorDetail)
			require.Equal(t, tc.skipped, result.Skipped)
			if tc.wire != "" {
				rendered, err := json.Marshal(result)
				require.NoError(t, err)
				require.Contains(t, string(rendered), tc.wire, "an empty tally or sample is empty, never null")
			}
		})
	}
}

// Failure prevented: the sample republishes an identifier the result's
// redaction rule drops everywhere else. An unrequested object in a batch
// response and a committed pointer's oid line are arbitrary text, and the
// summary carries up to several of them where error_detail carries one.
func TestReadSkippedCarriesOnlyCanonicalIdentifiers(t *testing.T) {
	canonical := lfs.ComputeOID(nil)
	credential := "https://ox:" + readTestToken + "@ledger.invalid/repo.git?sig=" + readTestToken
	var skips readSkips
	for _, detail := range []ReadFailureDetail{
		{Reason: "batch_object_unrequested", OID: canonical},
		{Reason: "batch_object_unrequested", OID: credential},
		{Reason: "empty_object_oid_mismatch", Path: "sessions/empty.md", OID: credential, ExpectedOID: canonical},
	} {
		require.True(t, skips.skip(context.Background(), missingHydration(detail)))
	}
	var result ReadSyncResult
	recordReadFailure(context.Background(), &result, skips.err())
	require.Equal(t, []ReadFailureDetail{
		{Reason: "batch_object_unrequested", OID: canonical},
		{Reason: "batch_object_unrequested"},
		{Reason: "empty_object_oid_mismatch", Path: "sessions/empty.md", ExpectedOID: canonical},
	}, result.Skipped.Sample)
	rendered, err := json.Marshal(result)
	require.NoError(t, err)
	for _, forbidden := range []string{readTestToken, "ledger.invalid", "?sig="} {
		require.NotContains(t, string(rendered), forbidden)
	}
}

// Failure prevented: a refresh walks past several objects and then cannot write
// its receipt. The result reports "interrupted" while still naming and
// summarizing those objects, so its class and its objects describe two
// different failures.
func TestReadSyncFailedReceiptDropsWhatHydrationWalkedPast(t *testing.T) {
	var receipt atomic.Pointer[string]
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if !assert.True(t, strings.HasSuffix(r.URL.Path, "/batch"), "every object is refused before any download") {
			http.NotFound(w, r)
			return
		}
		// A directory where the receipt belongs fails the write that would
		// publish this refresh, on every platform, after hydration has run.
		path := *receipt.Load()
		assert.NoError(t, os.Remove(path))
		assert.NoError(t, os.Mkdir(path, 0o700))
		var request struct {
			Objects []lfs.BatchObject `json:"objects"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		response := lfs.BatchResponse{}
		for _, object := range request.Objects {
			response.Objects = append(response.Objects, lfs.BatchResponseObject{OID: object.OID, Size: object.Size,
				Error: &lfs.ObjectError{Code: http.StatusNotFound, Message: "refused"}})
		}
		assert.NoError(t, json.NewEncoder(w).Encode(response))
	})
	path := filepath.Join(f.opts.Path, readReceiptRelative)
	receipt.Store(&path)
	require.True(t, ReadSync(context.Background(), f.opts).Ready)
	for _, name := range []string{"a", "b"} {
		commitReadLFSPointer(t, f, "sessions/receipt/"+name+".md", []byte("refused object "+name+"\n"))
	}

	result := ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready, "%+v", result)
	require.Equal(t, "interrupted", result.ErrorClass, "%+v", result)
	require.Nil(t, result.ErrorDetail, "the class and the detail must describe one failure")
	require.Nil(t, result.Skipped, "and so must the class and the summary")
}
