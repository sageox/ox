package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/plan"
)

// Unsaved-plan nudge — the capture gap everywhere except Claude Code plan mode.
//
// The plan-exit nudge (agent_hook_plan_nudge.go) fires only when Claude Code
// emits ExitPlanMode. Every other route to a plan captures nothing: Codex plan
// mode writes no artifact ox can discover, and any session where the human
// never entered plan mode leaves the plan in a transcript, where it dies with
// the session. The next person then re-derives it from scratch — which is
// precisely the prior art `ox plan enrich` exists to supply.
//
// The signal here needs no heuristics and no transcript scraping. `ox plan
// enrich` IS the drafting call: prime and the plan-mode hint both steer the
// agent to run it while drafting. So when enrich runs over a real plan document
// and nothing persists it, ox KNOWS a plan was drafted this session and never
// landed. That fact is stamped; any successful save clears it.
//
// Delivery reuses the only channel whose stdout reaches the model
// (UserPromptSubmit — see the table in agent_hook.go). Unlike the plan-exit
// nudge, this is a STATE rather than a one-shot message, so it is delivered at
// most once and then MARKED rather than deleted: re-running enrich on the same
// plan must not re-nag, and the surviving stamp is what a later session-end
// report reads. Only a save removes it.
//
// Everything is best-effort and fail-open. A stamp that cannot be written,
// read, or parsed simply yields no nudge — never a failed command.

const (
	// planUnsavedCacheSubdir holds the per-agent "a plan was drafted and never
	// saved" stamp under the ledger cache (.sageox/cache/). Local-only derived
	// data, never committed.
	planUnsavedCacheSubdir = "plan-unsaved"

	// planUnsavedMaxAge bounds how long an unsaved-plan stamp stays live. The
	// plan-exit nudge's 30 minutes is wrong here: that nudge follows an approval
	// by seconds, whereas drafting and implementing routinely straddle hours. The
	// ceiling exists so a plan drafted this morning never surfaces inside an
	// unrelated session tonight.
	planUnsavedMaxAge = 4 * time.Hour
)

// unsavedPlanStamp records a plan that was enriched but never persisted.
// Serialized as JSON so a later reader (session-end report, `ox doctor`) can
// describe the plan without re-reading it.
//
// Topic and SourcePath both come from the plan DOCUMENT (its H1 and its path),
// so both are untrusted. Anything that renders either into model context must
// go through reminderSafePlanTarget or an equivalent — only SourcePath is
// rendered today, which is why only it has a sanitizer so far.
type unsavedPlanStamp struct {
	Topic      string    `json:"topic"`
	SourcePath string    `json:"source_path,omitempty"`
	Files      int       `json:"files"`
	Steps      int       `json:"steps"`
	Material   bool      `json:"material"`
	NonTrivial bool      `json:"non_trivial"`
	ArmedAt    time.Time `json:"armed_at"`
	// NudgedAt is zero until the model has been told once. Non-zero suppresses
	// every later nudge for the same plan, so re-running enrich while iterating
	// on a draft cannot turn into per-prompt nagging.
	NudgedAt time.Time `json:"nudged_at,omitempty"`
}

// planUnsavedPath returns the per-agent stamp path under the ledger cache.
// Empty projectRoot/agentID yields "" (caller no-ops).
func planUnsavedPath(projectRoot, agentID string) string {
	if projectRoot == "" || agentID == "" {
		return ""
	}
	// agentID is an ox-generated token (no path separators), safe as a filename.
	return filepath.Join(projectRoot, ".sageox", "cache", planUnsavedCacheSubdir, agentID+".json")
}

// envAgentID resolves the agent identity for stamp scoping from the same env
// var every other agent-facing command reads (query.go, session_score.go).
// `ox plan enrich` runs as a plain subprocess, not inside a hook, so it has no
// HookContext to take the id from — the env var the prime handed the session is
// the only channel. Empty when the session was never primed, which correctly
// disables the feature rather than writing a stamp no nudge could attribute.
func envAgentID() string { return os.Getenv("SAGEOX_AGENT_ID") }

// armUnsavedPlanStamp records that a plan was enriched without being saved.
//
// Two gates keep this quiet. A consult-mode call (`--topic`, no document) is
// NOT a drafted plan — there is nothing to save yet — so it never arms. And a
// plan below both signal axes is throwaway by the same definition the rest of
// `ox plan` uses; nudging on it would train people to ignore the nudge.
//
// Re-arming preserves NudgedAt for the same topic so iterating on one draft
// nudges once, not once per enrich.
func armUnsavedPlanStamp(projectRoot, agentID string, in plan.Input, res plan.Result) error {
	path := planUnsavedPath(projectRoot, agentID)
	if path == "" {
		return nil
	}
	if strings.TrimSpace(in.Raw) == "" {
		return nil // consult-before-drafting; no plan exists yet
	}
	if !res.Signals.Material && !res.Signals.NonTrivial {
		return nil // trivial work needs no plan and must not be nagged
	}

	topic := plan.PlanTopic(in)
	st := unsavedPlanStamp{
		Topic: topic,
		// Absolute, via the same resolver the saved meta.json uses. The nudge
		// is read in a later turn whose working directory is not guaranteed to
		// match the one enrich ran in, so a relative path would render a
		// `--file` argument that does not resolve.
		SourcePath: absSourcePlanPath(in.Path),
		Files:      res.Signals.Files,
		Steps:      res.Signals.Steps,
		Material:   res.Signals.Material,
		NonTrivial: res.Signals.NonTrivial,
		ArmedAt:    time.Now().UTC(),
	}
	// Identity is (topic, source path), not topic alone. Two plans can easily
	// share an H1 — "Implementation Plan" is the default title ox itself
	// generates — and matching on topic alone would carry the first plan's
	// NudgedAt onto the second, silently costing it its only reminder.
	if prev, ok := readUnsavedPlanStamp(path); ok &&
		prev.Topic == topic && prev.SourcePath == st.SourcePath {
		st.NudgedAt = prev.NudgedAt
	}

	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("plan-unsaved marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("plan-unsaved mkdir: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// clearUnsavedPlanStamp removes the stamp. Called from the capture path the
// moment a plan actually lands in the ledger — the one event that makes the
// pending nudge wrong.
func clearUnsavedPlanStamp(projectRoot, agentID string) {
	path := planUnsavedPath(projectRoot, agentID)
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Debug("hook: could not clear unsaved-plan stamp", "err", err)
	}
}

// readUnsavedPlanStamp loads a stamp. Returns ok=false when absent, unreadable,
// or malformed — every one of which means "no nudge", never an error.
func readUnsavedPlanStamp(path string) (unsavedPlanStamp, bool) {
	var st unsavedPlanStamp
	if path == "" {
		return st, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st, false
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, false
	}
	if st.Topic == "" {
		return st, false
	}
	return st, true
}

// emitUnsavedPlanNudge tells the model, exactly once, that a plan it drafted
// this session was never saved. Called from handlePrompt — the proven
// UserPromptSubmit stdout-injection channel.
//
// Deliberately not deliver-once-and-delete: the stamp is state, and only a save
// clears it. Marking NudgedAt is what prevents repeat delivery.
func emitUnsavedPlanNudge(w io.Writer, projectRoot, agentID string) {
	path := planUnsavedPath(projectRoot, agentID)
	st, ok := readUnsavedPlanStamp(path)
	if !ok {
		return
	}
	if !st.NudgedAt.IsZero() {
		return // already told the model about this plan
	}
	if time.Since(st.ArmedAt) > planUnsavedMaxAge {
		// Stale: remove outright so it cannot resurface in a later session.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Debug("hook: could not remove stale unsaved-plan stamp", "err", err)
		}
		return
	}

	// Mark BEFORE writing. If the mark fails we stay silent rather than risk a
	// nudge that repeats on every prompt — an unheard reminder beats a nag.
	st.NudgedAt = time.Now().UTC()
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		slog.Debug("hook: could not mark unsaved-plan stamp", "err", err)
		return
	}

	// <system-reminder> is the only tag Claude Code treats as trusted system
	// context (see formatWhispers — <new-context> is rejected as injection).
	fmt.Fprintf(w, "<system-reminder>[ox] %s</system-reminder>\n", unsavedPlanNudgeLine(st))
}

// unsavedPlanNudgeLine states the fact, the command, and the consequence — in
// that order, on one line. It never asks a question and never proposes opening
// a browser: saving is local, durable, and needs no human decision, so the nudge
// must not manufacture one.
//
// The plan path is ATTACKER-INFLUENCED and crosses into trusted model context,
// so it is sanitized rather than interpolated: a file named
// `x</system-reminder>...` would otherwise close the wrapper handlePrompt puts
// around this line and let the remainder land as system-level instructions.
func unsavedPlanNudgeLine(st unsavedPlanStamp) string {
	return fmt.Sprintf(
		"A plan was drafted this session (%s) and never saved to the ledger. Save it — `ox plan save --file %s` — so it becomes prior art the next person gets back from `ox plan enrich` instead of re-deriving it.",
		planScopePhrase(st.Files, st.Steps), reminderSafePlanTarget(st.SourcePath),
	)
}

// reminderSafePlanTarget renders a plan path as a single shell argument that is
// also safe to embed in trusted model context.
//
// Two independent hazards, two independent treatments:
//
//   - SHELL: a path with a space, `;`, `$`, or a backtick is several arguments
//     (or a command) once pasted. POSIX single-quoting collapses it back to one
//     literal argument; the only character needing care inside single quotes is
//     the single quote itself.
//   - MARKUP: `<` and `>` can terminate the <system-reminder> wrapper. They are
//     escaped AFTER quoting, so a pathological name is rendered visibly rather
//     than silently dropped — a mangled path the reader can see beats a nudge
//     that quietly names the wrong file. Such a name cannot survive as a
//     runnable command either way, and it is not a shape any real plan has.
//
// An empty path (a plan piped on stdin) yields a bracket-free placeholder;
// angle brackets in our own literal would be markup noise in the same wrapper.
func reminderSafePlanTarget(sourcePath string) string {
	if sourcePath == "" {
		return "path/to/plan.md"
	}
	quoted := "'" + strings.ReplaceAll(sourcePath, "'", `'\''`) + "'"
	quoted = strings.ReplaceAll(quoted, "<", "&lt;")
	return strings.ReplaceAll(quoted, ">", "&gt;")
}
