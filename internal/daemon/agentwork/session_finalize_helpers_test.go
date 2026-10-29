package agentwork

import (
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
)

// Existing commit/push regressions start with already-prepared files. Exercise
// the two production phases under the same ownership as ProcessResult, without
// synthesizing new metadata that could conceal a malformed staged fixture.
func (h *SessionFinalizeHandler) gitCommitAndPush(payload *SessionFinalizePayload, refs map[string]lfs.FileRef) (bool, error) {
	if h.ledgerMu != nil {
		h.ledgerMu.Lock()
		defer h.ledgerMu.Unlock()
	}
	if err := gitutil.WithRepoLock(h.rootContext(), payload.LedgerPath, func() error {
		return h.commitSession(payload, refs)
	}); err != nil {
		return false, err
	}
	return h.pushSessionAndPrune(payload, "")
}
