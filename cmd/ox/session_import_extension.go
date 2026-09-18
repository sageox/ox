package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/sessionprovenance"
)

// An extension replaces the entire redacted conversation. A complete prior
// snapshot, rather than a title or timestamp, proves which canonical ID to reuse.
func validateImportExtension(c *importCandidate, r *sessionprovenance.Record, meta *lfs.SessionMeta, repoID string) error {
	if r == nil || len(r.Coverage) != 1 || r.Generation != c.Generation || r.NativeSessionID != c.NativeID || r.Excludes(0, c.Size) {
		return fmt.Errorf("source extension is excluded or ambiguous")
	}
	cov := r.Coverage[0]
	if cov.Start != 0 || cov.End <= 0 || cov.End >= c.Size || cov.SessionName != c.SessionName {
		return fmt.Errorf("source is not a complete append-only extension")
	}
	if meta == nil || meta.Source == nil || meta.Source.ImportedAt == nil || meta.RepoID != repoID || meta.SessionName != c.SessionName || meta.Source.NativeSessionID != c.NativeID || meta.Source.Generation != c.Generation || len(meta.Source.Ranges) != 1 || meta.Source.Ranges[0] != (sessionprovenance.Range{Start: 0, End: cov.End}) || meta.Files["raw.jsonl"].BareOID() != cov.RawOID {
		return fmt.Errorf("prior imported source metadata does not prove ancestry")
	}
	f, err := os.Open(c.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Size() != c.Size || !before.ModTime().Equal(c.ModifiedAt) {
		return fmt.Errorf("native source changed since preview")
	}
	h := sha256.New()
	if _, err = io.CopyN(h, f, cov.End); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != meta.Source.SnapshotDigest {
		return fmt.Errorf("native source prefix changed; refusing replacement")
	}
	var last [1]byte
	if _, err = f.ReadAt(last[:], cov.End-1); err != nil || last[0] != '\n' {
		return fmt.Errorf("previous source coverage is not record-complete")
	}
	after, err := os.Stat(c.Path)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("native source changed while proving ancestry")
	}
	return nil
}

func verifyImportExtension(ctx context.Context, ledger, ref string, d importDestination, c *importCandidate, client *lfs.Client) (*lfs.SessionMeta, error) {
	r, err := readImportRemoteRecord(ctx, ledger, ref, c.NativeID)
	if err != nil {
		return nil, err
	}
	if r == nil || len(r.Coverage) == 0 {
		return nil, nil
	}
	if len(r.Coverage) != 1 {
		return nil, fmt.Errorf("multiple source mappings require reconciliation")
	}
	cov := r.Coverage[0]
	if cov.End == c.Size {
		return nil, nil
	}
	b, ok, err := importReadBlob(ctx, ledger, ref, "sessions/"+cov.SessionName+"/meta.json")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("prior session metadata missing; refusing resurrection")
	}
	var meta lfs.SessionMeta
	if err = json.Unmarshal(b, &meta); err != nil {
		return nil, err
	}
	if err = validateImportExtension(c, r, &meta, d.RepoID); err != nil {
		return nil, err
	}
	prior := *c
	prior.Size = cov.End
	prior.Digest = meta.Source.SnapshotDigest
	verified, err := verifyImportReceipt(ctx, ledger, ref, d, &prior, client)
	if err != nil {
		return nil, err
	}
	if !verified {
		return nil, fmt.Errorf("prior remote publication cannot be verified")
	}
	return &meta, nil
}

func invalidateImportProjection(r *sessionprovenance.Record, name string) error {
	if raw, ok := r.Extra["projections"]; ok {
		var projections map[string]json.RawMessage
		if err := json.Unmarshal(raw, &projections); err != nil {
			return err
		}
		delete(projections, name)
		b, err := json.Marshal(projections)
		if err != nil {
			return err
		}
		r.Extra["projections"] = b
	}
	// A global revision cannot describe a snapshot whose selected projection was
	// invalidated; other sessions' explicit projection receipts remain intact.
	r.ProjectionRevision = ""
	return nil
}

func removeImportDerivedFiles(dir string) error {
	for _, name := range append(append([]string{}, lfs.ContentFiles...), "summary.json") {
		if name == "raw.jsonl" {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// A new snapshot may supersede only a completed old journal. An interrupted
// transaction keeps its digest, publication clock, and canonical session name.
func validateImportJournalTransition(j *importJournal, c *importCandidate, prior *lfs.SessionMeta) error {
	if j.Version == 0 || j.SnapshotDigest == c.Digest {
		return nil
	}
	if prior == nil || prior.Source == nil || !j.UploadVerified || j.SnapshotDigest != prior.Source.SnapshotDigest {
		return fmt.Errorf("previous import journal is unfinished or does not prove this extension")
	}
	return nil
}
