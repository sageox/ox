package nativeimport

import (
	"time"

	"github.com/sageox/ox/internal/lfs"
)

// nameTimeFormat matches live session names, so parsers that read the first 16
// characters as the start time keep working.
const nameTimeFormat = "2006-01-02T15-04"

// Name is an imported session's Ledger directory:
// <native start, UTC minute>-import-<agent>-<native session UUID>.
//
// Every input is a fact of the native session, so every machine and every
// retry computes the same name. It can never equal a live name, which ends in
// the ox instance ID (Ox plus four characters).
func Name(agent Agent, nativeID string, startedAt time.Time) string {
	return startedAt.UTC().Format(nameTimeFormat) + "-import-" + string(agent) + "-" + nativeID
}

// SessionID is the ses_ ID for an imported session: the same UUIDv5 over
// repo_id + "/" + name that ox and the server already derive for recordings
// without a stored session_id.
func SessionID(repoID, name string) string {
	meta := lfs.SessionMeta{RepoID: repoID, SessionName: name}
	return meta.EffectiveSessionID()
}
