package lfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/stretchr/testify/require"
)

// An upload failure must never leave compressed trace content in the git tree,
// even while the first blob succeeds and the second is being transferred.
func TestTracePublishNeverCopiesContentIntoLedger(t *testing.T) {
	for _, fail := range []int{0, 1, 2} {
		t.Run(string(rune('0'+fail)), func(t *testing.T) {
			cache, ledger := t.TempDir(), t.TempDir()
			names := []string{pipeline.LedgerFileTraceSpans, pipeline.LedgerFileTraceEvents}
			for _, name := range names {
				require.NoError(t, os.WriteFile(filepath.Join(cache, name), []byte("compressed private capture "+name), 0600))
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				for _, name := range names {
					_, err := os.Stat(filepath.Join(ledger, name))
					if !os.IsNotExist(err) {
						t.Errorf("trace path exists before upload confirmation: %s", name)
					}
				}
				var req struct {
					Objects []BatchObject `json:"objects"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				response := BatchResponse{Transfer: "basic"}
				for _, obj := range req.Objects {
					result := BatchResponseObject{OID: obj.OID, Size: obj.Size}
					if calls == fail {
						result.Error = &ObjectError{Code: 403, Message: "upload refused"}
					}
					response.Objects = append(response.Objects, result)
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			client := NewClient(server.URL, "test", "test")
			refs, err := PublishTraceFiles(cache, ledger, func(data []byte) (UploadedRef, error) { return UploadBlob(client, data) })
			if fail != 0 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Len(t, refs, 2)
			}
			for _, name := range names {
				content, err := os.ReadFile(filepath.Join(cache, name))
				require.NoError(t, err)
				if fail != 0 {
					_, err = os.Stat(filepath.Join(ledger, name))
					require.True(t, os.IsNotExist(err))
					continue
				}
				pointer, err := os.ReadFile(filepath.Join(ledger, name))
				require.NoError(t, err)
				oid, size, err := ParsePointer(string(pointer))
				require.NoError(t, err)
				require.Equal(t, NewFileRef(content).OID, oid)
				require.EqualValues(t, len(content), size)
			}
		})
	}
}

// A second pointer-write failure must restore an existing attachment, or remove
// the first new pointer when this is the session's first trace publication.
func TestTracePublishRollsBackPointerPair(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			cache, ledger := t.TempDir(), t.TempDir()
			names := []string{pipeline.LedgerFileTraceSpans, pipeline.LedgerFileTraceEvents}
			old := make(map[string][]byte)
			for _, name := range names {
				require.NoError(t, os.WriteFile(filepath.Join(cache, name), []byte("new private capture "+name), 0600))
				if existing {
					ref := NewFileRef([]byte("previous capture " + name))
					old[name] = []byte(FormatPointer(ref.OID, ref.Size))
					require.NoError(t, os.WriteFile(filepath.Join(ledger, name), old[name], 0640))
				}
			}
			failure := errors.New("second pointer write failed")
			refs, err := publishTraceFiles(cache, ledger,
				func(data []byte) (UploadedRef, error) { return AssertUploaded(NewFileRef(data)), nil },
				func(path string, ref UploadedRef) error {
					if filepath.Base(path) == pipeline.LedgerFileTraceEvents {
						return failure
					}
					return WritePointerFile(path, ref)
				})
			require.ErrorIs(t, err, failure)
			require.Nil(t, refs, "partial refs must never reach metadata")
			for _, name := range names {
				path := filepath.Join(ledger, name)
				if existing {
					got, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, old[name], got)
					info, err := os.Stat(path)
					require.NoError(t, err)
					require.Equal(t, os.FileMode(0640), info.Mode().Perm())
				} else {
					require.NoFileExists(t, path)
				}
				got, err := os.ReadFile(filepath.Join(cache, name))
				require.NoError(t, err)
				require.Equal(t, "new private capture "+name, string(got))
			}
		})
	}
}

func TestTracePublishRefusesUnsafeOrMissingArtifacts(t *testing.T) {
	for _, shape := range []string{"missing cache", "directory cache", "existing content", "existing directory", "existing symlink"} {
		t.Run(shape, func(t *testing.T) {
			cache, ledger := t.TempDir(), t.TempDir()
			for _, name := range []string{pipeline.LedgerFileTraceSpans, pipeline.LedgerFileTraceEvents} {
				require.NoError(t, os.WriteFile(filepath.Join(cache, name), []byte("private trace"), 0600))
			}
			cacheEvent := filepath.Join(cache, pipeline.LedgerFileTraceEvents)
			ledgerEvent := filepath.Join(ledger, pipeline.LedgerFileTraceEvents)
			switch shape {
			case "missing cache", "directory cache":
				require.NoError(t, os.Remove(cacheEvent))
				if shape == "directory cache" {
					require.NoError(t, os.Mkdir(cacheEvent, 0700))
				}
			case "existing content":
				require.NoError(t, os.WriteFile(ledgerEvent, []byte("preserve existing content"), 0600))
			case "existing directory":
				require.NoError(t, os.Mkdir(ledgerEvent, 0700))
			case "existing symlink":
				if err := os.Symlink(cacheEvent, ledgerEvent); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			refs, err := PublishTraceFiles(cache, ledger, func(data []byte) (UploadedRef, error) { return AssertUploaded(NewFileRef(data)), nil })
			require.Error(t, err)
			require.Nil(t, refs)
			require.NoFileExists(t, filepath.Join(ledger, pipeline.LedgerFileTraceSpans), "preflight must prevent partial publication")
			if shape == "existing content" {
				got, err := os.ReadFile(ledgerEvent)
				require.NoError(t, err)
				require.Equal(t, "preserve existing content", string(got))
			}
			got, err := os.ReadFile(filepath.Join(cache, pipeline.LedgerFileTraceSpans))
			require.NoError(t, err)
			require.Equal(t, "private trace", string(got))
		})
	}
}
