package main

import (
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Prime and doctor retry registration directly, without the Stop hook's draft
// gate. The recording's saved hold must govern those requests as well.
func TestNotifySessionStartedAsync_PersistedManualSuppressesRetry(t *testing.T) {
	for _, status := range []string{"deferred", "pending"} {
		for _, mode := range []string{config.SessionPublishingManual, config.SessionPublishingAuto} {
			t.Run(status+"/"+mode, func(t *testing.T) {
				projectRoot, requests := newNotifyManualFixture(t, config.SessionPublishingAuto)
				t.Setenv(config.EnvSessionPublishing, config.SessionPublishingAuto)
				state := newRegisteredTurnState(t, "OxHeldRetry")
				state.AdapterName = "cursor"
				state.PublishingMode = mode
				state.LifecycleRegistrationState = status
				require.NoError(t, session.SaveRecordingState(projectRoot, state))
				require.Equal(t, config.SessionPublishingAuto, config.GetSessionPublishing(projectRoot))

				notifySessionStartedAsync(projectRoot, state)

				persisted, err := session.ReadRecordingStateFile(state.SessionPath)
				require.NoError(t, err)
				require.NotNil(t, persisted)
				assert.Equal(t, mode, persisted.PublishingMode)
				if mode == config.SessionPublishingManual {
					assert.Zero(t, atomic.LoadInt32(requests), "a registration retry must not reveal a held session")
					assert.Equal(t, status, state.LifecycleRegistrationState)
					assert.Equal(t, status, persisted.LifecycleRegistrationState)
				} else {
					assert.Equal(t, int32(1), atomic.LoadInt32(requests))
					assert.Equal(t, "confirmed", state.LifecycleRegistrationState)
					assert.Equal(t, "confirmed", persisted.LifecycleRegistrationState)
				}
			})
		}
	}
}
