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

// SetLostDeclareHookForTest runs f inside a spool write, after it read the
// loss totals and wrote its gap line, before it stores them as declared. Not
// safe for parallel tests.
func SetLostDeclareHookForTest(t testing.TB, f func()) {
	t.Helper()
	prev := lostDeclareHook
	lostDeclareHook = f
	t.Cleanup(func() { lostDeclareHook = prev })
}
