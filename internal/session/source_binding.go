package session

import (
	"fmt"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// CheckNativeSourceBinding also covers paths persisted by older discovery
// code. Reject the entire recording, not just its next batch: existing raw
// entries may already have come from the unbound source.
func CheckNativeSourceBinding(state *RecordingState) error {
	if state.SourceRejected {
		return fmt.Errorf("%w: recording quarantined for manual review", adapterruntime.ErrUnboundSource)
	}
	name := state.AdapterName
	if name == "" {
		name = state.AgentType
	}
	err := adapterruntime.ValidateSessionSource(adapters.CanonicalAdapterName(name), state.SessionFile, state.AgentSessionID)
	if err == nil {
		return nil
	}
	state.SourceRejected = true
	if state.SessionPath != "" {
		if markErr := SetSourceRejectedAt(state.SessionPath, state.SessionID, true); markErr != nil {
			return fmt.Errorf("%w; persist source quarantine: %v", err, markErr)
		}
	}
	return err
}