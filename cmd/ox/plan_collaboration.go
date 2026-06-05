package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/internal/session"
)

// Plan provenance + collaboration signals (cmd/ox side).
//
// internal/plan is a pure data layer with no session/agent-instance deps, so
// the bits that touch the recording state and raw.jsonl transcript live here
// and hand internal/plan plain structs. Everything is best-effort and
// fail-open: a plan saved outside a primed/recording session simply carries no
// provenance and no collaboration block, never an error.

// resolvePlanProvenance builds the forward link for a plan being saved in
// gitRoot. Returns the provenance to stamp and the live recording state (so the
// caller can derive collaboration signals from the same load). Returns
// (nil, nil) when no agent context is detected — the plan is then unlinked,
// which renders fine.
func resolvePlanProvenance(gitRoot string) (*plan.Provenance, *session.RecordingState) {
	agentID, agentType := detectAgentContext()
	if agentID == "" {
		return nil, nil
	}

	prov := &plan.Provenance{AgentID: agentID, AgentType: agentType}

	// instance store: authoritative agent type + model snapshot.
	if inst, err := resolveInstance(agentID); err == nil && inst != nil {
		if inst.AgentType != "" {
			prov.AgentType = inst.AgentType
		}
		prov.Model = inst.Model
	}

	// repo id + author snapshot (denormalized so the plan renders standalone).
	ep := ""
	if ctx, err := config.LoadProjectContext(gitRoot); err == nil && ctx != nil {
		ep = ctx.Endpoint()
	}
	if cfg, err := config.LoadProjectConfig(gitRoot); err == nil && cfg != nil {
		prov.RepoID = cfg.RepoID
	}
	prov.AuthorName = identity.AttributionDisplayName(ep, "")

	// live recording → session name (available now) + active outcome. The
	// canonical ses_ SessionID is minted at stop and backfilled there.
	var st *session.RecordingState
	if rs, err := session.LoadRecordingStateForAgent(gitRoot, agentID); err == nil && rs != nil {
		st = rs
		prov.SessionName = session.GetSessionName(rs.SessionPath)
		prov.SessionOutcome = plan.SessionOutcomeActive
	}

	return prov, st
}

// deriveCollabSignals counts deterministic collaboration-effort proxies from the
// live recording's raw.jsonl (local cache full content — no LFS hydration).
// Returns nil when there is no live recording or no countable entries, so the
// plan's collaboration block is omitted rather than written all-zero.
func deriveCollabSignals(st *session.RecordingState) *plan.CollabSignals {
	if st == nil || st.SessionPath == "" {
		return nil
	}
	rawPath := filepath.Join(st.SessionPath, "raw.jsonl")
	if _, err := os.Stat(rawPath); err != nil {
		return nil
	}
	stored, err := session.ReadSessionFromPath(rawPath)
	if err != nil || stored == nil || len(stored.Entries) == 0 {
		return nil
	}

	sig := &plan.CollabSignals{}
	var firstUserTS, lastTS time.Time
	for _, e := range stored.Entries {
		switch entryString(e, "type") {
		case "user":
			sig.UserPrompts++
			if ts := entryTime(e); !ts.IsZero() && firstUserTS.IsZero() {
				firstUserTS = ts
			}
		case "tool":
			sig.ToolCalls++
			if strings.Contains(entryString(e, "tool_name"), "AskUserQuestion") {
				sig.AgentQuestions++
			}
		}
		if ts := entryTime(e); !ts.IsZero() {
			lastTS = ts
		}
	}

	if !firstUserTS.IsZero() && lastTS.After(firstUserTS) {
		sig.DurationSeconds = int(lastTS.Sub(firstUserTS).Seconds())
	}

	// all-zero counts carry no signal — omit the block entirely.
	if sig.UserPrompts == 0 && sig.ToolCalls == 0 && sig.AgentQuestions == 0 && sig.DurationSeconds == 0 {
		return nil
	}
	return sig
}

// appendProducedPlan records the plan slug on the agent's live recording state
// (the reverse-link accumulator folded into session meta at stop). Dedups by
// slug. Best-effort: no live recording → no-op.
func appendProducedPlan(projectRoot, agentID, slug string) error {
	if agentID == "" || slug == "" {
		return nil
	}
	st, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || st == nil {
		return nil
	}
	for _, existing := range st.ProducedPlans {
		if existing == slug {
			return nil // already recorded
		}
	}
	st.ProducedPlans = append(st.ProducedPlans, slug)
	return session.SaveRecordingState(projectRoot, st)
}

// entryString reads a string field from a raw.jsonl entry map, tolerating
// absent/typed-otherwise values.
func entryString(e map[string]any, key string) string {
	if v, ok := e[key].(string); ok {
		return v
	}
	return ""
}

// entryTime parses an entry's timestamp, accepting both the recording format
// ("ts") and the import format ("timestamp"). Returns zero time on any miss.
func entryTime(e map[string]any) time.Time {
	for _, key := range []string{"ts", "timestamp"} {
		if s := entryString(e, key); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}
