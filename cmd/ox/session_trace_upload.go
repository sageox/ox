package main

import (
	"log/slog"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/trace/model"
)

func uploadSessionTraces(projectRoot, cacheDir, sessionDir string) (map[string]lfs.FileRef, error) {
	client, err := getLFSClient(projectRoot)
	if err != nil {
		return nil, err
	}
	return lfs.PublishTraceFiles(cacheDir, sessionDir, func(data []byte) (lfs.UploadedRef, error) {
		return lfs.UploadBlob(client, data)
	})
}

// Raw-only recovery must not infer missing pause history or a stop boundary.
// Doctor stamps complete marker state before handing an orphan to this path.
func prepareRetryTraces(ledgerPath string, orphan orphanedSession) (string, *model.Metadata) {
	if orphan.Meta == nil {
		return "", nil
	}
	cache, meta, err := session.MaterializeTraces(ledgerPath, orphan.SessionName, orphan.Meta.TraceCapture)
	if err != nil {
		slog.Warn("trace retry materialization skipped", "error", err)
		return "", nil
	}
	return cache, meta
}
