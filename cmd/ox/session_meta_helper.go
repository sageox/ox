package main

import (
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/sessionid"
)

// sessionMetaBase creates a SessionMetaBuilder pre-populated with identity
// fields (UserID, RepoID, SessionID) resolved from the project root.
// Callers chain additional optional setters before calling Build().
//
// SessionID is a fresh ses_<UUIDv7> stamped here at recording-creation
// time. Never regenerated on later mutates: MutateSessionMeta paths
// preserve the value via JSON round-trip.
func sessionMetaBase(sessionName, username, agentID, agentType string, createdAt time.Time, projectRoot string) *lfs.SessionMetaBuilder {
	ep := endpoint.GetForProject(projectRoot)
	return lfs.NewSessionMeta(sessionName, username, agentID, agentType, createdAt).
		UserID(auth.GetUserID(ep)).
		RepoID(getRepoIDOrDefault(projectRoot)).
		SessionID(sessionid.GenerateSessionID())
}
