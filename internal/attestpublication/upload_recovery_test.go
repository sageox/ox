package attestpublication

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/smithy-go"
)

type recoveryMultipart struct {
	listErr        error
	completeErr    error
	remoteComplete bool
}

func (r *recoveryMultipart) Create(context.Context, Grant) (string, error) { return "upload-1", nil }
func (r *recoveryMultipart) List(context.Context, Grant, string) ([]CompletedPart, error) {
	return nil, r.listErr
}
func (r *recoveryMultipart) Upload(_ context.Context, _ Grant, _ string, number int32, body io.Reader, size int64) (CompletedPart, error) {
	_, err := io.Copy(io.Discard, body)
	return CompletedPart{Number: number, ETag: "etag", Size: size}, err
}
func (r *recoveryMultipart) Complete(context.Context, Grant, string, []CompletedPart) error {
	return r.completeErr
}
func (r *recoveryMultipart) Completed(context.Context, Grant) (bool, error) {
	return r.remoteComplete, nil
}

func TestUploadArchiveRecoversCommittedMultipart(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "bundle.zip")
	if err := os.WriteFile(archive, []byte("frozen"), 0o600); err != nil {
		t.Fatal(err)
	}
	grant := Grant{Binding: Binding{ArchiveSizeBytes: 6}}
	for _, test := range []struct {
		name        string
		listErr     error
		completeErr error
	}{
		{"lost complete response", &smithy.GenericAPIError{Code: "NoSuchUpload", Message: "gone"}, nil},
		{"immutable object already committed", nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "exists"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			journalPath := filepath.Join(t.TempDir(), "journal.json")
			journal := Journal{Version: 1, ArchivePath: archive, Binding: grant.Binding, UploadID: "upload-1", IntentExpiresAt: time.Now().Add(time.Hour)}
			transfer := &recoveryMultipart{listErr: test.listErr, completeErr: test.completeErr, remoteComplete: true}
			if err := UploadArchive(context.Background(), transfer, grant, journalPath, &journal); err != nil {
				t.Fatal(err)
			}
			stored, err := LoadJournal(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			if !stored.Completed {
				t.Fatal("committed remote object was not reconciled into the journal")
			}
		})
	}
}
