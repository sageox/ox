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
// even though none of the three needs an agent — attribution has its own
// fallback chain (OAuth token → git config → OS username) and RecordingState
// has a workspace-scoped loader. The cost was permanent, not cosmetic: with
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
	// Stamp the author ONLY when attribution genuinely resolved. DisplayName is
	// documented always-non-empty (it bottoms out at the literal "Anonymous"),
	// so writing it unconditionally would make an unattributable plan
	// indistinguishable from an attributed one in the artifact — the same
	// category error as the bug above, and it would silently destroy the only
	// way to measure whether attribution works ("% of plans with an author"
	// would read 100% forever). Empty here means "we could not attribute",
	// which is honest, queryable, and what the unlinked-plan contract below
	// depends on.
	if attr := identity.ResolveAttribution(ep, ""); !attr.IsAnonymous() {
		prov.AuthorName = attr.DisplayName
	}

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

// loadPlanRecordingState resolves the live recording backing a plan save:
// agent-scoped when an agent id is present, workspace-scoped otherwise (the
// fallback that lets a plan saved outside a detected agent still carry its
// session), then subagent parent-preference so the plan links the main session.
//
// This is the SINGLE resolver for the plan path — liveSessionConversationURL
// calls it rather than reimplementing it. That matters because LintSessionLink
// compares the provenance session against the /c/ id stamped into the render:
// two implementations that must agree, kept in step by a comment, had already
// drifted (parent-preference was applied to both branches here and only to the
// agent branch there), which false-warns on every subagent render resolved by
// workspace. One function means the divergence has nowhere to live.
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
	if st.ParentAgentID != "" {
		if parentState, _ := session.LoadRecordingStateForAgent(gitRoot, st.ParentAgentID); parentState != nil {
			return parentState
		}
	}
	return st
}

// planSessionLink resolves, from ONE decision, both halves of a plan's session
// link: the ses_ id that must appear in the rendered artifact and the /c/ URL
// carrying it. Returns ("", "") when no link is expected — no live recording, a
// recording predating start-minted ids, an unlinked project, or session
// attribution turned off.
//
// Callers must gate LintSessionLink on THIS id rather than on the provenance
// they stamped. The two are not the same predicate: provenance records the
// session for the ledger even when no link is expected in the page, so gating
// the lint on provenance warns "the render carries no /c/ link" about renders
// that were never supposed to carry one.
func planSessionLink(gitRoot string) (sessionID, url string) {
	if attr := loadResolvedAttribution(); attr.Session == "" {
		return "", "" // session attribution disabled — no link expected
	}
	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil || cfg == nil {
		return "", ""
	}
	agentID, _ := detectAgentContext()
	state := loadPlanRecordingState(gitRoot, agentID)
	if state == nil || state.SessionID == "" {
		return "", ""
	}
	return state.SessionID, buildConversationURL(cfg, state.SessionID)
}

// liveSessionConversationURL returns the /c/ conversation link of the current
// live recording, or "" when no link is expected. Used to stamp a deterministic
// session link into rendered plan artifacts — zero agent tokens.
func liveSessionConversationURL(gitRoot string) string {
	_, url := planSessionLink(gitRoot)
	return url
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

// appendProducedPlan records the plan slug on the live recording (the
// reverse-link accumulator folded into session meta at stop). Best-effort: no
// live recording → no-op.
//
// Takes the RecordingState the caller already resolved, but passes only its
// SessionPath down to session.AppendProducedPlan, which reloads from disk
// immediately before writing. Both halves matter and they solve different
// problems:
//
//   - Passing the caller's state (not an agent id) is what fixed the original
//     bug: re-deriving by agent id gated this reverse link on agent detection a
//     second time, so it stayed empty on exactly the saves the provenance fix
//     rescues — and two independent loads could name different sessions for a
//     subagent, breaking the guarantee that the forward and reverse links agree.
//   - Passing a PATH rather than the struct is what keeps that fix from
//     introducing a worse one: writing back a struct captured before plan.Save
//     resurrects recordings that `ox session stop` deleted, and clobbers every
//     field a concurrent hook updated in between. See session.AppendProducedPlan.
func appendProducedPlan(projectRoot string, st *session.RecordingState, slug string) error {
	if st == nil {
		return nil
	}
	return session.AppendProducedPlan(projectRoot, st.SessionPath, slug)
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
