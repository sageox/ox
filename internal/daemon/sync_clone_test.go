package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBackoffClassification(t *testing.T) {
	// permanent errors should cap at clonePermanentBackoffMax (5 min)
	// transient errors should cap at cloneBackoffMax (1 hour)

	t.Run("permanent cap", func(t *testing.T) {
		backoff := exponentialBackoff(20, clonePermanentBackoffMax/10, clonePermanentBackoffMax)
		assert.LessOrEqual(t, backoff, clonePermanentBackoffMax, "permanent backoff should cap at %v", clonePermanentBackoffMax)
	})

	t.Run("transient cap", func(t *testing.T) {
		backoff := exponentialBackoff(20, cloneBackoffMax/60, cloneBackoffMax)
		assert.LessOrEqual(t, backoff, cloneBackoffMax, "transient backoff should cap at %v", cloneBackoffMax)
	})

	t.Run("permanent reaches cap faster", func(t *testing.T) {
		assert.Less(t, clonePermanentBackoffMax, cloneBackoffMax)
	})
}
