package config

// Session draft placeholders — see docs/adr/ADR-029-session-draft-placeholder.md.
//
// A draft is a meta.json-only placeholder committed to the ledger partway
// through a recording, so https://<endpoint>/c/<session_id> resolves for links
// already circulating in PR bodies and commit trailers. It carries no turn
// data and is superseded wholesale at session stop.
const (
	// SessionDraftOn publishes a draft placeholder at DraftPublishTurn.
	SessionDraftOn = "on"
	// SessionDraftOff never publishes one. The server-visibility path then
	// rests entirely on the (silent, best-effort) session-started
	// notification, which is the pre-draft behavior.
	SessionDraftOff = "off"
)

// ValidSessionDraftModes lists the modes accepted by user configuration.
var ValidSessionDraftModes = []string{SessionDraftOn, SessionDraftOff}

const (
	// DraftPublishTurn is the response turn at which the placeholder is
	// published.
	//
	// 2, not 1: turn 1 is very often a one-shot question that never becomes
	// work worth linking, so publishing there would put a ledger commit into
	// the critical path of every trivial interaction. By turn 2 the agent has
	// almost always been asked to do something whose PR body will carry a
	// /c/ link.
	//
	// Not user-configurable. A numeric public config key is permanent API for
	// a knob nobody has asked for, and the failure mode of a bad value
	// (publish on every single turn) is ledger churn for the whole team.
	DraftPublishTurn = 2

	// DraftRefreshEveryTurns is how often the draft's counters are refreshed
	// after the initial publish.
	//
	// Refreshing at all is what makes the feature verifiable: a climbing
	// turn_count on the /c/ page is the signal that distinguishes "recording
	// is working" from "recording is silently broken", which is the whole
	// problem drafts exist to solve. 10 keeps a 100-turn session to roughly
	// 10 tiny meta.json commits — visible forward motion without turning the
	// ledger into a commit firehose.
	DraftRefreshEveryTurns = 10
)

// SessionDraftSource indicates where the resolved value came from.
type SessionDraftSource string

const (
	// SessionDraftSourceDefault — no config set, returning the built-in default (on).
	SessionDraftSourceDefault SessionDraftSource = "default"
	// SessionDraftSourceUserConfig — set via `ox config set session.draft ...`.
	SessionDraftSourceUserConfig SessionDraftSource = "user"
)

// ResolvedSessionDraft is the effective draft policy plus its provenance.
type ResolvedSessionDraft struct {
	Enabled      bool
	PublishTurn  int
	RefreshEvery int
	Source       SessionDraftSource
}

// IsValidSessionDraft reports whether mode is accepted by `ox config set`.
// Empty is valid (unset takes the default).
func IsValidSessionDraft(mode string) bool {
	switch mode {
	case SessionDraftOn, SessionDraftOff, "":
		return true
	}
	return false
}

// NormalizeSessionDraft returns the canonical mode string. Unrecognized values
// fall back to the default (on).
func NormalizeSessionDraft(mode string) string {
	if mode == SessionDraftOff {
		return SessionDraftOff
	}
	return SessionDraftOn
}

// ResolveSessionDraft determines the effective draft policy. Resolution order
// is user config (session.draft), then the built-in default of "on".
//
// No environment variable. Customer-facing env vars require human review, and
// this is a user preference rather than a deployment knob.
func ResolveSessionDraft() *ResolvedSessionDraft {
	resolved := &ResolvedSessionDraft{
		Enabled:      true,
		PublishTurn:  DraftPublishTurn,
		RefreshEvery: DraftRefreshEveryTurns,
		Source:       SessionDraftSourceDefault,
	}
	if userCfg, err := LoadUserConfig(); err == nil && userCfg != nil && userCfg.SessionDraft != "" {
		resolved.Enabled = NormalizeSessionDraft(userCfg.SessionDraft) == SessionDraftOn
		resolved.Source = SessionDraftSourceUserConfig
	}
	return resolved
}
