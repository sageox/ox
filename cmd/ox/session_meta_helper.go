package main

import (
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
)

// sessionMetaBase creates a SessionMetaBuilder pre-populated with identity
// fields (UserID, RepoID) resolved from the project root. Callers chain
// additional optional setters before calling Build().
func sessionMetaBase(sessionName, username, agentID, agentType string, createdAt time.Time, projectRoot string) *lfs.SessionMetaBuilder {
	ep := endpoint.GetForProject(projectRoot)
	return lfs.NewSessionMeta(sessionName, username, agentID, agentType, createdAt).
		UserID(auth.GetUserID(ep)).
		RepoID(getRepoIDOrDefault(projectRoot))
}
