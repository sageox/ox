package livesession

import (
	"context"
	"testing"
	"time"

	"github.com/sageox/ox/internal/secretguard"
)

// SetCurrentRulesForTest replaces the current-rules source for one test and
// empties the cache on both sides. Tests that call it must not run in parallel.
func SetCurrentRulesForTest(t testing.TB, f func(context.Context) (*secretguard.RuleSet, error)) {
	t.Helper()
	prev := currentRules
	resetRulesCache()
	currentRules = f
	t.Cleanup(func() {
		currentRules = prev
		resetRulesCache()
	})
}

func resetRulesCache() {
	rulesCache.mu.Lock()
	defer rulesCache.mu.Unlock()
	rulesCache.delta, rulesCache.fetched = nil, time.Time{}
}
