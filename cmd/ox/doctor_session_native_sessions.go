package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/lfs"
)

// checkSessionNativeSessions is the report-only guard for the native session
// id list every new recording is expected to carry. A finalized session
// whose meta.json has stopped_at (so it was written by a binary that knows
// the field) but no native_sessions, for an agent that does expose a session
// id, means the SessionStart path that records the id did not run — a hook
// not installed, a marker that never got the id, a prime that bypassed it.
// Nothing else makes that visible: the recording still finalizes and uploads
// normally, it just cannot be matched to the agent's own transcript or
// trace afterwards.
//
// Report-only on purpose. The ids live in the agent's own session files and
// hook payloads at recording time; doctor cannot recover them after the fact,
// so there is nothing safe to auto-fix. The check tells the coworker which
// sessions are affected and what to look at.
//
// Scoping rules, each of which keeps a legitimate session out of the count:
//   - meta.json without stopped_at: written before the field existed. The
//     absence proves nothing.
//   - agent type that agentx says exposes no session id (Droid, Gemini,
//     Goose, OpenCode...): an empty list is the correct record.
//   - draft placeholders: not finalized, not expected to carry the list yet.

// CheckSlugSessionNativeSessions is the slug for this check.
const CheckSlugSessionNativeSessions = "session-native-ids"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugSessionNativeSessions,
		Name:     "Session native ids",
		Category: "Sessions",
		FixLevel: FixLevelCheckOnly,
		Description: "Detects finalized sessions recorded without the agent's native session ids " +
			"(a broken SessionStart path) — report-only, the ids cannot be recovered after the fact",
		Run: func(fix bool) checkResult { return checkSessionNativeSessions() },
	})
}

func checkSessionNativeSessions() checkResult {
	const name = "Session native ids"

	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger found", "")
	}

	checked, missing, err := scanSessionNativeSessions(filepath.Join(ledgerPath, "sessions"))
	if err != nil {
		return SkippedCheck(name, "no sessions directory", "")
	}

	if checked == 0 {
		return SkippedCheck(name, "no finalized sessions from agents with native session ids", "")
	}
	if len(missing) == 0 {
		return PassedCheck(name, fmt.Sprintf("%d session(s) carry their native session ids", checked))
	}

	shown := missing
	if len(shown) > 5 {
		shown = shown[:5]
	}
	msg := fmt.Sprintf("%d/%d session(s) recorded without native session ids: %s", len(missing), checked, strings.Join(shown, ", "))
	if len(missing) > len(shown) {
		msg += fmt.Sprintf(" (+%d more)", len(missing)-len(shown))
	}
	fix := "The SessionStart path that records the agent's session id did not run for these recordings. " +
		"Check that the ox hooks are installed for this agent (ox hooks list) and that 'ox agent prime' runs at session start. " +
		"The ids cannot be recovered after the fact; new recordings will carry them once the path is fixed."
	return WarningCheck(name, msg, fix)
}

// scanSessionNativeSessions walks sessionsDir and returns how many finalized
// sessions were expected to carry native session ids and, sorted, the names
// of those that do not. See checkSessionNativeSessions for the scoping rules.
func scanSessionNativeSessions(sessionsDir string) (checked int, missing []string, err error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return 0, nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, readErr := lfs.ReadSessionMeta(filepath.Join(sessionsDir, e.Name()))
		if readErr != nil || meta == nil || meta.IsDraft() || meta.StoppedAt == nil {
			continue
		}
		if !agentTypeExposesNativeSessionID(meta.AgentType) {
			continue
		}
		checked++
		if len(meta.NativeSessions) == 0 {
			missing = append(missing, e.Name())
		}
	}
	sort.Strings(missing)
	return checked, missing, nil
}

// nativeSessionIDAgents are the agent types whose session id reaches ox on
// every session start — Claude Code on SessionStart hook stdin, the rest
// through an environment variable agentx reads (CODEX_THREAD_ID,
// AMP_THREAD_URL, PI_SESSION_ID, OMP_SESSION_ID, GC_RUN_ID). Agents that
// only resolve their id later, inside the adapter (Droid, Gemini, Goose,
// OpenCode), are deliberately absent: an empty list is their correct record
// until adapters report the id they resolved (tracked separately). This is
// an allowlist rather than agentx's SupportsSession, which is true for
// several agents that expose no id in their environment.
var nativeSessionIDAgents = map[string]bool{
	string(agentx.AgentTypeClaudeCode): true,
	string(agentx.AgentTypeCodex):      true,
	string(agentx.AgentTypeAmp):        true,
	string(agentx.AgentTypePi):         true,
	string(agentx.AgentTypeOMP):        true,
	string(agentx.AgentTypeGasCity):    true,
}

// agentTypeExposesNativeSessionID reports whether recordings from agentType
// are expected to carry native session ids. Unknown and generic agent types
// are not expected to, so a custom adapter never trips the warning.
func agentTypeExposesNativeSessionID(agentType string) bool {
	return nativeSessionIDAgents[canonicalAgentType(agentType)]
}
