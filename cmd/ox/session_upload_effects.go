package main

import (
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/lfs"
)

// sessionUploadEffects is the narrow orchestration boundary between durable
// local session state and the fallible external effects used to publish it.
// Filesystem preparation and metadata writes deliberately remain real: tests
// exercise the same cache/ledger artifacts production writes while replacing
// only network and git effects that cannot be made deterministic in-process.
//
// Keep this value explicit (rather than a package global) so parallel tests do
// not race by swapping process-wide hooks.
type sessionUploadEffects struct {
	uploadLFS       func(projectRoot, sessionDir string) (map[string]lfs.FileRef, error)
	commitInitial   func(ledgerPath, sessionName string) error
	commitRetry     func(ledgerPath, sessionName string, includeSummary bool) error
	reconcilePlans  func(projectRoot string, slugs []string, sessionName, sessionID string)
	finalizeLinkage func(projectRoot, sessionDir string, meta *lfs.SessionMeta, sessionName string) []api.PRLinkMiss
}

func productionSessionUploadEffects() sessionUploadEffects {
	return sessionUploadEffects{
		uploadLFS:       uploadSessionLFS,
		commitInitial:   commitAndPushLedger,
		commitRetry:     commitAndPushLedgerWithExtras,
		reconcilePlans:  reconcileProducedPlansAtStop,
		finalizeLinkage: finalizeLinkageAfterPush,
	}
}
