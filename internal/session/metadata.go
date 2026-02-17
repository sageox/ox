package session

import (
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/version"
)

// SessionSchemaVersion is the current session schema version.
// Increment when making breaking changes to the session format.
const SessionSchemaVersion = "1"

// SessionMeta is the first line of each session JSONL file.
// It captures context about the recording session for provenance and replay.
type SessionMeta struct {
	// OxVersion is the version of ox that created this session
	OxVersion string `json:"ox_version"`

	// OxUsername is the authenticated SageOx username (if logged in)
	OxUsername string `json:"ox_username,omitempty"`

	// SchemaVersion identifies the session format version
	SchemaVersion string `json:"schema_version"`

	// AgentType identifies the AI agent (e.g., "claude-code", "cursor", "copilot")
	AgentType string `json:"agent_type"`

	// AgentVersion is the version of the AI agent (if known)
	AgentVersion string `json:"agent_version,omitempty"`

	// Model is the LLM model used (e.g., "claude-sonnet-4-20250514")
	Model string `json:"model,omitempty"`

	// SessionID is the short agent ID (e.g., "Oxa7b3")
	SessionID string `json:"session_id"`

	// OxSID is the full session ID (e.g., "oxsid_01JEYQ9Z8X...")
	OxSID string `json:"oxsid"`

	// StartedAt is when the recording session began
	StartedAt time.Time `json:"started_at"`

	// EndedAt is when the recording session ended (set on close)
	EndedAt time.Time `json:"ended_at,omitempty"`

	// ProjectRemote is the git remote URL for provenance tracking
	ProjectRemote string `json:"project_remote,omitempty"`
}

// SessionFooter is the last line of each session JSONL (optional).
// It provides summary statistics for the recording session.
type SessionFooter struct {
	// EndedAt is when the recording session ended
	EndedAt time.Time `json:"ended_at"`

	// DurationMins is the total duration in minutes
	DurationMins int `json:"duration_minutes"`

	// EntryCount is the total number of entries recorded
	EntryCount int `json:"entry_count"`
}

// NewSessionMeta creates metadata with current ox version and user.
// sessionID is the short agent ID (e.g., "Oxa7b3").
// oxsid is the full session ID (e.g., "oxsid_01JEYQ9Z8X...").
// agentType identifies the AI agent (e.g., "claude-code").
// projectRemote is the git remote URL for provenance.
// endpointURL is the project endpoint for auth lookup (empty = default).
func NewSessionMeta(sessionID, oxsid, agentType, projectRemote, endpointURL string) *SessionMeta {
	return &SessionMeta{
		OxVersion:     version.Version,
		OxUsername:    getOxUsername(endpointURL),
		SchemaVersion: SessionSchemaVersion,
		AgentType:     agentType,
		SessionID:     sessionID,
		OxSID:         oxsid,
		StartedAt:     time.Now(),
		ProjectRemote: projectRemote,
	}
}

// NewSessionMetaWithVersion creates metadata with a specific agent version.
func NewSessionMetaWithVersion(sessionID, oxsid, agentType, agentVersion, projectRemote, endpointURL string) *SessionMeta {
	meta := NewSessionMeta(sessionID, oxsid, agentType, projectRemote, endpointURL)
	meta.AgentVersion = agentVersion
	return meta
}

// NewSessionFooter creates a footer with session statistics.
func NewSessionFooter(startedAt time.Time, entryCount int) *SessionFooter {
	endedAt := time.Now()
	durationMins := int(endedAt.Sub(startedAt).Minutes())

	return &SessionFooter{
		EndedAt:      endedAt,
		DurationMins: durationMins,
		EntryCount:   entryCount,
	}
}

// getOxUsername returns the authenticated SageOx username.
// If ep is non-empty, looks up the token for that specific endpoint.
// Falls back to the default endpoint token when ep is empty.
// Returns empty string if not authenticated or on error.
func getOxUsername(ep string) string {
	var token *auth.StoredToken
	var err error
	if ep != "" {
		token, err = auth.GetTokenForEndpoint(ep)
	} else {
		token, err = auth.GetToken()
	}
	if err != nil || token == nil {
		return ""
	}

	// prefer email as username, fall back to name
	if token.UserInfo.Email != "" {
		return token.UserInfo.Email
	}
	return token.UserInfo.Name
}

// Close finalizes the metadata with end time.
func (m *SessionMeta) Close() {
	m.EndedAt = time.Now()
}

// Duration returns the session duration.
// Returns zero duration if EndedAt is not set.
func (m *SessionMeta) Duration() time.Duration {
	if m.EndedAt.IsZero() {
		return 0
	}
	return m.EndedAt.Sub(m.StartedAt)
}
