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
// (nil, nil) only when NOTHING resolved — the plan is then unlinked, which
// renders fine.
//
// Agent detection is NOT a precondition. It used to be: an `agentID == ""`
// early return here discarded the author, the repo id, and the session too,
// even though none of the three needs an agent — AttributionDisplayName falls
// back through OAuth token, git config, and OS username to a literal
// "anonymous" and so can never come back empty, and RecordingState has a
// workspace-scoped loader. The cost was permanent, not cosmetic: with
// SessionName blank, plan.BackfillSessionID (which matches on SessionName) can
// never attach the canonical ses_ id afterwards, so those plans stayed
// unattributed forever. Measured over 233 real plans, author was populated on
// 5.2% and session_id on 4.7% — one failure, not two.
func resolvePlanProvenance(gitRoot string) (*plan.Provenance, *session.RecordingState) {
	agentID, agentType := detectAgentContext()

	prov := &plan.Provenance{AgentID: agentID, AgentType: agentType}

	// instance store: authoritative agent type + model snapshot. The only
	// genuinely agent-keyed lookup in this function.
	if agentID != "" {
		if inst, err := resolveInstance(agentID); err == nil && inst != nil {
			if inst.AgentType != "" {
				prov.AgentType = inst.AgentType
			}
			prov.Model = inst.Model
		}
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

	// live recording → session name + start-minted ses_ SessionID (both
	// available now) + active outcome. Stop-time reconciliation remains the
	// backstop for recordings started under an older binary (empty ID here).
	st := loadPlanRecordingState(gitRoot, agentID)
	if st != nil {
		prov.SessionName = session.GetSessionName(st.SessionPath)
		prov.SessionID = st.SessionID
		prov.SessionOutcome = plan.SessionOutcomeActive
	}

	// Nothing resolved at all — keep the historical unlinked-plan contract so
	// callers that branch on a nil provenance behave as before.
	if prov.AgentID == "" && prov.RepoID == "" && prov.AuthorName == "" && prov.SessionName == "" {
		return nil, nil
	}
	return prov, st
}

// loadPlanRecordingState resolves the live recording backing a plan save.
// Mirrors liveSessionConversationURL's resolution exactly — including the
// workspace-scoped fallback when no agent id is present, which is the whole
// reason a plan saved outside a detected agent can still carry its session.
// Keeping the two in step matters: LintSessionLink compares the provenance
// session against the URL that function stamps into the rendered artifact, so
// if they disagree every render false-warns.
func loadPlanRecordingState(gitRoot, agentID string) *session.RecordingState {
	var st *session.RecordingState
	if agentID != "" {
		st, _ = session.LoadRecordingStateForAgent(gitRoot, agentID)
	} else {
		st, _ = session.LoadRecordingStateForWorkspace(gitRoot, gitRoot)
	}
	if st == nil {
		return nil
	}
	// subagent: prefer the parent session so provenance matches the footer
	// link (liveSessionConversationURL parent-prefers too) — otherwise
	// LintSessionLink false-warns on every subagent render.
	if st.ParentAgentID != "" {
		if parentState, _ := session.LoadRecordingStateForAgent(gitRoot, st.ParentAgentID); parentState != nil {
			return parentState
		}
	}
	return st
}

// liveSessionConversationURL returns the /c/ conversation link of the current
// live recording, or "" when there is no recording, the recording predates
// start-minted IDs, or session attribution is disabled. Used to stamp a
// deterministic session link into rendered plan artifacts — zero agent tokens.
func liveSessionConversationURL(gitRoot string) string {
	attr := loadResolvedAttribution()
	if attr.Session == "" {
		return ""
	}
	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil || cfg == nil {
		return ""
	}
	agentID, _ := detectAgentContext()
	var state *session.RecordingState
	if agentID != "" {
		state, _ = session.LoadRecordingStateForAgent(gitRoot, agentID)
		// subagent: prefer parent session so the plan links the main session
		if state != nil && state.ParentAgentID != "" {
			if parentState, _ := session.LoadRecordingStateForAgent(gitRoot, state.ParentAgentID); parentState != nil {
				state = parentState
			}
		}
	} else {
		state, _ = session.LoadRecordingStateForWorkspace(gitRoot, gitRoot)
	}
	if state == nil {
		return ""
	}
	return buildConversationURL(cfg, state.SessionID)
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

// appendProducedPlan records the plan slug on the live recording state (the
// reverse-link accumulator folded into session meta at stop). Dedups by slug.
// Best-effort: no live recording → no-op.
//
// Takes the RecordingState the caller already resolved rather than re-loading
// it by agent id. The old signature re-derived the state via
// LoadRecordingStateForAgent, which meant this reverse link was gated on agent
// detection a second time even after resolvePlanProvenance had successfully
// found the session by workspace — so the accumulator stayed empty on exactly
// the saves the provenance fix is meant to rescue. Reusing the caller's state
// also guarantees the forward link (Provenance.SessionName) and the reverse
// link land on the SAME session; two independent loads could disagree for a
// subagent, whose parent-preference only one of them applied.
func appendProducedPlan(projectRoot string, st *session.RecordingState, slug string) error {
	if st == nil || slug == "" {
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
