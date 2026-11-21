package main

import (
	"net/http"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented (review): the rollout flag turned off during an admitted
// session, the SessionEnd hook's probe open was refused feature_off, and the
// stream was disabled with no close requested, so SageOx showed it live until
// the server's auto-finish.
func TestLiveSessionEndAfterTheFlagTurnsOffStillClosesTheStream(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the flag turned off")
	st := f.openAndDrain()
	f.srv.RefuseNextOpens(1, http.StatusForbidden, sessionstream.ReasonFeatureOff, sessionstream.ActionStop)

	liveSessionFinishAtHook(f.projectRoot, f.state, liveEndCloseBudget)

	require.Equal(t, livesession.StatusStreaming, f.liveState().Status, "an admitted stream is never disabled by a refused probe")
	f.daemonClose("")
	_, closed := f.srv.ClosedWith(st.ConversationID)
	assert.True(t, closed, "the daemon's close reaches the server")
}
