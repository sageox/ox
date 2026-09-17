// Package sessionpublication verifies durable remote receipts independently of
// best-effort indexing notifications and summary completion.
package sessionpublication

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/sessionprovenance"
)

// Verify refreshes the remote and verifies one immutable tree plus its referenced
// LFS bytes. Callers retain their recovery cache until this succeeds.
func Verify(ctx context.Context, ledger, name string, expected *lfs.SessionMeta, client *lfs.Client) error {
	if !sessionprovenance.ValidSessionName(name) || expected == nil || expected.Source == nil || client == nil {
		return fmt.Errorf("invalid publication verification input")
	}
	if _, err := gitutil.RunGit(ctx, ledger, "fetch", "--quiet", "origin"); err != nil {
		return fmt.Errorf("refresh publication receipt: %w", err)
	}
	ref, err := gitutil.RunGit(ctx, ledger, "rev-parse", "@{upstream}")
	if err != nil {
		return err
	}
	ref = strings.TrimSpace(ref)
	metaBytes, err := readReceipt(ctx, ledger, ref, "sessions/"+name+"/meta.json")
	if err != nil {
		return fmt.Errorf("remote session metadata unavailable: %w", err)
	}
	var meta lfs.SessionMeta
	if err = json.Unmarshal(metaBytes, &meta); err != nil {
		return err
	}
	path, err := sessionprovenance.Path(expected.Source.NativeSessionID)
	if err != nil {
		return err
	}
	recordBytes, err := readReceipt(ctx, ledger, ref, path)
	if err != nil {
		return fmt.Errorf("remote source receipt unavailable: %w", err)
	}
	var record sessionprovenance.Record
	if err = json.Unmarshal(recordBytes, &record); err != nil {
		return err
	}
	if err = validateReceipt(name, expected, &meta, &record); err != nil {
		return err
	}
	for filename, file := range meta.Files {
		if !sessionprovenance.ValidSessionName(filename) {
			return fmt.Errorf("invalid published filename")
		}
		artifactPath := "sessions/" + name + "/" + filename
		if file.EffectiveStorage() == lfs.StorageGit {
			if err := verifyInlineBlob(ctx, ledger, ref, artifactPath, file.Size); err != nil {
				return err
			}
			continue
		}
		pointer, err := readReceipt(ctx, ledger, ref, artifactPath)
		if err != nil {
			return err
		}
		oid, size, err := lfs.ParsePointer(string(pointer))
		if err != nil || strings.TrimPrefix(oid, "sha256:") != file.BareOID() || size != file.Size {
			return fmt.Errorf("remote pointer differs from receipt")
		}
		batch, err := client.BatchDownloadContext(ctx, []lfs.BatchObject{{OID: file.BareOID(), Size: file.Size}})
		if err != nil {
			return err
		}
		if len(batch.Objects) != 1 || batch.Objects[0].Error != nil || batch.Objects[0].Actions == nil || batch.Objects[0].Actions.Download == nil {
			return fmt.Errorf("published blob is unavailable")
		}
		if err = lfs.DownloadToFileContext(ctx, batch.Objects[0].Actions.Download, io.Discard, true, file.OID); err != nil {
			return fmt.Errorf("published blob verification failed: %w", err)
		}
	}
	return nil
}

func validateReceipt(name string, expected, actual *lfs.SessionMeta, record *sessionprovenance.Record) error {
	if err := record.Validate(); err != nil {
		return err
	}
	source := expected.Source
	if source == nil || !reflect.DeepEqual(source, actual.Source) || actual.RepoID != expected.RepoID || record.NativeSessionID != source.NativeSessionID || record.Generation != source.Generation {
		return fmt.Errorf("remote provenance differs from publication")
	}
	raw, ok := actual.Files["raw.jsonl"]
	if !ok || raw.OID == "" || raw.EffectiveStorage() != lfs.StorageLFS || raw != expected.Files["raw.jsonl"] {
		return fmt.Errorf("remote transcript differs from publication")
	}
	for _, span := range source.Ranges {
		if record.Excludes(span.Start, span.End) {
			return fmt.Errorf("published source has been excluded")
		}
		found := false
		for _, coverage := range record.Coverage {
			if coverage.Start == span.Start && coverage.End == span.End && coverage.SessionName == name && coverage.RawOID == raw.BareOID() {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("remote source coverage is incomplete")
		}
	}
	return nil
}

func readReceipt(ctx context.Context, ledger, ref, path string) ([]byte, error) {
	object := ref + ":" + path
	size, err := gitutil.RunGit(ctx, ledger, "cat-file", "-s", object)
	if err != nil {
		return nil, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	// Only content-free receipts/pointers belong here; transcripts are streamed
	// from LFS. Bound malformed metadata before git show can allocate its body.
	if err != nil || n < 0 || n > 4*1024*1024 {
		return nil, fmt.Errorf("publication receipt exceeds metadata limit")
	}
	data, err := gitutil.RunGit(ctx, ledger, "show", object)
	return []byte(data), err
}

// Inline artifacts are already in the immutable Git tree. Prove their size
// without loading content into the metadata/pointer reader's bounded buffer.
func verifyInlineBlob(ctx context.Context, ledger, ref, path string, expectedSize int64) error {
	tree, err := gitutil.RunGit(ctx, ledger, "ls-tree", ref, "--", path)
	if err != nil {
		return err
	}
	fields := strings.Fields(strings.SplitN(tree, "\t", 2)[0])
	if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return fmt.Errorf("published inline artifact missing or not a regular blob")
	}
	size, err := gitutil.RunGit(ctx, ledger, "cat-file", "-s", fields[2])
	if err != nil {
		return err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	if err != nil || n < 0 || n != expectedSize {
		return fmt.Errorf("published inline artifact size differs from receipt")
	}
	return nil
}
