package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBackoffClassification(t *testing.T) {
	// permanent errors should cap at clonePermanentBackoffMax (5 min)
	// transient errors should cap at cloneBackoffMax (1 hour)

	t.Run("permanent cap", func(t *testing.T) {
		// with 20 failures and production base (1min), should hit the cap exactly
		backoff := exponentialBackoff(20, time.Minute, clonePermanentBackoffMax)
		assert.Equal(t, clonePermanentBackoffMax, backoff, "permanent backoff should equal cap at high failure count")
	})

	t.Run("transient cap", func(t *testing.T) {
		backoff := exponentialBackoff(20, time.Minute, cloneBackoffMax)
		assert.Equal(t, cloneBackoffMax, backoff, "transient backoff should equal cap at high failure count")
	})

	t.Run("permanent reaches cap faster", func(t *testing.T) {
		assert.Less(t, clonePermanentBackoffMax, cloneBackoffMax)
	})
}
