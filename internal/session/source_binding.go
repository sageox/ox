package session

import (
	"fmt"
	"path/filepath"
	"strings"

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
	adapter := adapters.CanonicalAdapterName(name)
	nativeID := state.AgentSessionID
	if adapter == "amp" {
		if state.SessionFile == "" && nativeID == "" {
			// The thread ID has not arrived yet (prime before SessionStart).
			// Nothing native has been read, so wait for it instead of
			// quarantining a recording a later hook will bind.
			return nil
		}
		nativeID = boundAmpThreadID(state)
	}
	err := adapterruntime.ValidateSessionSource(adapter, state.SessionFile, nativeID)
	if err == nil {
		return nil
	}
	state.SourceRejected = true
	if state.SessionPath != "" {
		if markErr := SetSourceRejectedAt(state.SessionPath, state.SessionID, true); markErr != nil {
			return fmt.Errorf("%w; persist source quarantine: %w", err, markErr)
		}
	}
	return err
}

// boundAmpThreadID returns the thread the saved sidecar belongs to when the
// recording observed that thread locally. A later thread switch updates
// AgentSessionID but not the sidecar, so the sidecar's own thread must be
// matched against every thread this recording recorded.
func boundAmpThreadID(state *RecordingState) string {
	if state.SessionFile == "" {
		return state.AgentSessionID
	}
	sidecarID := strings.TrimSuffix(filepath.Base(state.SessionFile), ".jsonl")
	ids := []string{state.AgentSessionID}
	for _, native := range state.NativeSessions {
		ids = append(ids, native.ID)
	}
	for _, id := range ids {
		if adapterruntime.AmpThreadID(id) == sidecarID {
			return id
		}
	}
	return state.AgentSessionID
}
