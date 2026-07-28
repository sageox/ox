package main

import (
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
)

// sessionMetaBase creates a SessionMetaBuilder pre-populated with identity
// fields (UserID, RepoID, SessionID) resolved from the project root.
// Callers chain additional optional setters before calling Build().
//
// sessionID is REQUIRED and must already be fully resolved by the caller via
// session.ResolveOrMintSessionID(preserved, startMinted) — preferring a
// preserved meta.json ID, then a start-minted/header-carried ID, and only
// minting fresh as a last resort. This constructor deliberately does not
// mint on its own: a prior version defaulted to a fresh mint here and relied
// on every caller remembering to override it, which one call site (doctor's
// orphan retry-upload) silently failed to do, rotating SessionIDs on every
// retry. Requiring the value up front makes that class of bug impossible.
// Never regenerated on later mutates: MutateSessionMeta paths preserve the
// value via JSON round-trip.
func sessionMetaBase(sessionName, username, agentID, agentType string, createdAt time.Time, projectRoot, sessionID string) *lfs.SessionMetaBuilder {
	ep := endpoint.GetForProject(projectRoot)
	return lfs.NewSessionMeta(sessionName, username, agentID, agentType, createdAt).
		UserID(auth.GetUserID(ep)).
		RepoID(getRepoIDOrDefault(projectRoot)).
		SessionID(sessionID)
}
