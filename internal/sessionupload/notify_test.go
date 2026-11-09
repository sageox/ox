package sessionupload

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sageox/ox/internal/lfs"
)

// The session ID is interpolated into the signal's URL path, so an ID that is
// not a canonical ses_ UUID must never be sent.
func TestCanNotify_RejectsNonCanonicalSessionIDs(t *testing.T) {
	for _, id := range []string{
		"ses_x/aborted?",
		"ses_0198a2b4-7c1d-7e3f-9a2b-1c3d5e7f9a0b/../aborted",
		"ses_0198A2B4-7C1D-7E3F-9A2B-1C3D5E7F9A0B",
	} {
		assert.False(t, canNotify(&lfs.SessionMeta{SessionID: id}), id)
	}
	assert.False(t, canNotify(nil))
	assert.True(t, canNotify(&lfs.SessionMeta{SessionID: "ses_0198a2b4-7c1d-7e3f-9a2b-1c3d5e7f9a0b"}))
	// a pre-ses_ session gets a synthesized canonical ID and is still sent
	assert.True(t, canNotify(&lfs.SessionMeta{RepoID: "repo_01abc", SessionName: "2026-01-01T00-00-user-OxA1b2"}))
}
