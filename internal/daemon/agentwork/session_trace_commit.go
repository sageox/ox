package agentwork

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/pipeline"
)

// prepareSessionPointers runs under the Ledger repo lock. An optional trace
// write cannot roll back successful ordinary pointer preparation or prevent its
// publication. Trace paths are omitted from the index on failure; local files
// are left intact for recovery, including malformed directories or symlinks.
func (h *SessionFinalizeHandler) prepareSessionPointers(payload *SessionFinalizePayload, refs map[string]lfs.FileRef) error {
	ordinary := make(map[string]lfs.FileRef)
	traces := make(map[string]lfs.FileRef)
	for name, ref := range refs {
		if pipeline.IsTraceFile(name) {
			traces[name] = ref
		} else {
			ordinary[name] = ref
		}
	}
	if _, err := lfs.WritePointerFiles(payload.SessionDir, lfs.AssertUploadedManifest(ordinary)); err != nil {
		return err
	}
	if !payload.omitTraces {
		if _, err := lfs.WritePointerFiles(payload.SessionDir, lfs.AssertUploadedManifest(traces)); err != nil {
			h.logger.Warn("trace pointer preparation skipped", "session", filepath.Base(payload.SessionDir), "error", err)
			payload.omitTraces = true
		}
	}
	if !payload.omitTraces {
		return nil
	}
	if err := lfs.MutateSessionMeta(context.Background(), payload.SessionDir, func(meta *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if meta == nil {
			return nil, nil
		}
		delete(meta.Files, pipeline.LedgerFileTraceSpans)
		delete(meta.Files, pipeline.LedgerFileTraceEvents)
		meta.Trace = nil
		return meta, nil
	}); err != nil {
		return fmt.Errorf("remove omitted trace references: %w", err)
	}
	relDir, err := filepath.Rel(payload.LedgerPath, payload.SessionDir)
	if err != nil {
		return err
	}
	args := []string{"rm", "--sparse", "--cached", "--ignore-unmatch", "-r", "-f", "--"}
	for _, name := range []string{pipeline.LedgerFileTraceSpans, pipeline.LedgerFileTraceEvents} {
		args = append(args, ":(literal)"+filepath.ToSlash(filepath.Join(relDir, name)))
	}
	if err := h.runGit(payload.LedgerPath, args...); err != nil {
		return fmt.Errorf("omit trace paths from index: %w", err)
	}
	return nil
}
