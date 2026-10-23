package lfs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/secretguard"
	"github.com/stretchr/testify/require"
)

func TestEveryUploadBoundaryRefusesSecretsBeforeNetwork(t *testing.T) {
	for _, name := range []string{"single-blob", "single-object", "batch-upload", "batch-existing", "recovery", "recovery-direct"} {
		t.Run(name, func(t *testing.T) {
			useBundledSessionRules(t)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			content := []byte("MAILLAB_API_KEY=syntheticOpaqueLFSBoundarySentinel39xyAZ")
			ref := NewFileRef(content)
			client := NewClient(server.URL, "oauth2", "test-token")
			action := &Action{Href: server.URL}
			var err error
			switch name {
			case "single-blob":
				_, err = UploadBlob(client, content)
			case "single-object":
				err = UploadObject(action, content)
			case "batch-upload", "batch-existing":
				obj := BatchResponseObject{OID: ref.BareOID(), Size: ref.Size}
				if name == "batch-upload" {
					obj.Actions = &Actions{Upload: action}
				}
				results := UploadAll(&BatchResponse{Objects: []BatchResponseObject{obj}}, map[string][]byte{ref.BareOID(): content}, 1)
				require.Len(t, results, 1)
				err = results[0].Error
			case "recovery":
				err = uploadRecoveryBlob(context.Background(), client, recoveryUpload{entry: pointerEntry{relPath: "sessions/example/raw.jsonl", ref: ref}, content: content})
			case "recovery-direct":
				err = putRecoveryObject(context.Background(), action, content)
			}
			require.Error(t, err)
			require.NotContains(t, err.Error(), "Sentinel39")
			require.Zero(t, requests.Load(), "no batch request or PUT may occur for refused bytes")
		})
	}
}
func TestEveryUploadBoundaryWaitsForFreshRules(t *testing.T) {
	original := currentSessionRules
	currentSessionRules = func(context.Context) (*secretguard.RuleSet, error) {
		return nil, errors.New("current rules unavailable")
	}
	t.Cleanup(func() { currentSessionRules = original })
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	content := []byte("benign content")
	ref := NewFileRef(content)
	action := &Action{Href: server.URL}
	client := NewClient(server.URL, "oauth2", "test-token")
	_, err := UploadBlob(client, content)
	require.Error(t, err)
	require.Error(t, UploadObject(action, content))
	results := UploadAll(&BatchResponse{Objects: []BatchResponseObject{{OID: ref.BareOID(), Size: ref.Size}}}, map[string][]byte{ref.BareOID(): content}, 1)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	require.Error(t, uploadRecoveryBlob(context.Background(), client, recoveryUpload{entry: pointerEntry{relPath: "sessions/example/raw.jsonl", ref: ref}, content: content}))
	require.Error(t, putRecoveryObject(context.Background(), action, content))
	require.Zero(t, requests.Load())
}
func TestFreshRulesCoverDirectUploadAndAlreadyPresentBytes(t *testing.T) {
	rules, err := secretguard.NewRules([]byte("[[rules]]\nid='future-provider'\nregex='futurekey_[A-Z0-9]{12}'\n"))
	require.NoError(t, err)
	original := currentSessionRules
	var fetches atomic.Int32
	currentSessionRules = func(context.Context) (*secretguard.RuleSet, error) { fetches.Add(1); return rules, nil }
	t.Cleanup(func() { currentSessionRules = original })
	content := []byte("futurekey_ABCDEFGHIJ12")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	require.Error(t, UploadObject(&Action{Href: server.URL}, content))
	ref := NewFileRef(content)
	results := UploadAll(&BatchResponse{Objects: []BatchResponseObject{{OID: ref.BareOID(), Size: ref.Size}}}, map[string][]byte{ref.BareOID(): content}, 1)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	require.EqualValues(t, 2, fetches.Load())
	require.Zero(t, requests.Load())
}
func TestUploadProofOwnsExactBufferAndRefreshesOncePerBatch(t *testing.T) {
	useBundledSessionRules(t)
	original := currentSessionRules
	var fetches atomic.Int32
	currentSessionRules = func(ctx context.Context) (*secretguard.RuleSet, error) { fetches.Add(1); return original(ctx) }
	t.Cleanup(func() { currentSessionRules = original })
	input := []byte("benign buffer")
	proofs, err := prepareUploads(context.Background(), map[string][]byte{"first": input, "second": []byte("another benign buffer")})
	require.NoError(t, err)
	copy(input, strings.Repeat("X", len(input)))
	require.Equal(t, "benign buffer", string(proofs["first"].content))
	require.EqualValues(t, 1, fetches.Load())
}
