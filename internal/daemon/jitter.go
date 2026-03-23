package daemon

import (
	"math/rand/v2"
	"time"
)

// jitteredDuration returns a duration with random jitter applied.
// jitterFraction of 0.10 means +-10% (e.g., 15s becomes 13.5s-16.5s).
func jitteredDuration(base time.Duration, jitterFraction float64) time.Duration {
	if jitterFraction <= 0 || base <= 0 {
		return base
	}
	jitter := float64(base) * jitterFraction
	offset := rand.Float64()*2*jitter - jitter
	return time.Duration(float64(base) + offset)
}
