package agentwork

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

// publishRawPending runs during deterministic detection, before the manager's
// authenticated-runner gate. A missing LLM must never strand captured content.
// Keep the cache as summary input: the Ledger copy becomes LFS pointers.
func (h *SessionFinalizeHandler) publishRawPending(payload *SessionFinalizePayload) error {
	return session.WithPublicationLock(context.Background(), payload.LedgerPath, filepath.Base(payload.SessionDir), func() error {
		return h.publishRawPendingLocked(payload)
	})
}

func (h *SessionFinalizeHandler) publishRawPendingLocked(payload *SessionFinalizePayload) error {
	if err := session.CheckImportPublication(payload.LedgerPath, payload.SessionDir); err != nil {
		return err
	}
	if err := checkNativeCaptureReady(payload.SessionDir); err != nil {
		return err
	}
	marker := filepath.Join(payload.SessionDir, ".raw-uploaded")
	digest, err := summarySourceDigest(payload.RawPath)
	if err != nil {
		return err
	}
	// Completion belongs to exact bytes, not a filename: resumed recovery can
	// replace a previously published cache. Legacy markers require verification.
	if published, readErr := os.ReadFile(marker); readErr == nil && strings.TrimSpace(string(published)) == digest {
		return nil
	}
	entryCount, err := session.CountValidatedEntries(context.Background(), payload.RawPath)
	if err != nil {
		return err
	}
	if entryCount == 0 {
		return fmt.Errorf("capture has no substantive entries")
	}
	needsSummary := len(missingArtifacts(payload.SessionDir)) > 0 || session.HasNeedsSummaryMarker(payload.SessionDir)
	if needsSummary {
		if err = session.WriteNeedsSummaryMarker(payload.SessionDir, payload.RawPath, filepath.Join(payload.LedgerPath, "sessions", filepath.Base(payload.SessionDir))); err != nil {
			return err
		}
	}
	source, err := session.ReadCaptureSource(payload.SessionDir)
	if err != nil {
		return err
	}
	// Keep provenance in the cache metadata too: a later summary copy must
	// not overwrite the published source contract with its pre-upload metadata.
	if source != nil {
		if err = lfs.MutateSessionMeta(context.Background(), payload.SessionDir, func(meta *lfs.SessionMeta) (*lfs.SessionMeta, error) {
			if meta == nil {
				meta = h.synthesizeMeta(payload.SessionDir, filepath.Base(payload.SessionDir))
			}
			meta.Source = source
			meta.RepoID = filepath.Base(payload.LedgerPath)
			meta.ProcessingStatus = "pending"
			return meta, nil
		}); err != nil {
			return err
		}
	}
	staged := *payload
	if _, err = h.stageSessionInLedger(&staged); err != nil {
		return err
	}
	var refs map[string]lfs.FileRef
	if !h.skipLFS {
		if h.projectRoot == "" {
			return fmt.Errorf("raw publication needs repository context")
		}
		client, err := lfs.NewClientFromLedger(payload.LedgerPath, endpoint.GetForProject(h.projectRoot))
		if err != nil {
			return err
		}
		refs, err = lfs.UploadSessionFiles(client, staged.SessionDir, h.logger)
		if err != nil {
			return err
		}
	}
	if err = lfs.MutateSessionMeta(context.Background(), staged.SessionDir, func(meta *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if meta == nil {
			meta = h.synthesizeMeta(staged.SessionDir, filepath.Base(staged.SessionDir))
		}
		meta.Draft = false
		meta.EntryCount = entryCount
		if needsSummary {
			meta.SummaryStatus = "pending"
		}
		now := time.Now().UTC()
		if meta.PublishedAt == nil {
			meta.PublishedAt = &now
		}
		meta.Files = h.mergeFileRefs(meta.Files, refs, filepath.Base(staged.SessionDir))
		return meta, nil
	}); err != nil {
		return err
	}
	if err = lfs.EnsureSessionsGitignore(filepath.Dir(staged.SessionDir)); err != nil {
		return err
	}
	if !h.gitCommitAndPush(&staged, refs) {
		return fmt.Errorf("raw session publication pending")
	}
	// This marker is local-only and written only after successful publication.
	current, err := summarySourceDigest(payload.RawPath)
	if err != nil {
		return err
	}
	if current != digest {
		return fmt.Errorf("raw session changed during publication")
	}
	return os.WriteFile(marker, []byte(digest+"\n"), 0600)
}
