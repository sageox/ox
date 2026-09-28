package agentwork

import (
	"path/filepath"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/trace/model"
)

func (h *SessionFinalizeHandler) prepareTraces(payload *SessionFinalizePayload, stored *session.StoredSession) (string, *model.Metadata) {
	if stored == nil {
		var err error
		stored, err = session.ReadSessionFromPath(payload.RawPath)
		if err != nil {
			h.logger.Debug("trace carrier unavailable", "error", err)
			return "", nil
		}
	}
	// The raw header alone may predate pauses. Without a durable stop carrier,
	// materialization must fail closed rather than synthesizing a wider range.
	if stored.Meta == nil {
		return "", nil
	}
	cache, meta, err := session.MaterializeTraces(payload.LedgerPath, filepath.Base(payload.SessionDir), stored.Meta.TraceCapture)
	if err != nil {
		h.logger.Warn("trace materialization skipped", "error", err)
		return "", nil
	}
	return cache, meta
}

func (h *SessionFinalizeHandler) uploadPreparedTraces(client *lfs.Client, cache string, meta *model.Metadata, refs map[string]lfs.FileRef) *model.Metadata {
	if meta == nil {
		return nil
	}
	uploaded, err := lfs.UploadTraceFiles(cache, func(data []byte) (lfs.UploadedRef, error) {
		return lfs.UploadBlob(client, data)
	})
	if err != nil {
		h.logger.Warn("trace upload skipped", "error", err)
		return nil
	}
	for name, ref := range uploaded {
		refs[name] = ref.Ref()
	}
	return meta
}
