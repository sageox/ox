package livesession

import (
	"context"
	"testing"

	"github.com/sageox/ox/internal/secretguard"
)

// SetCurrentRulesForTest replaces the current-rules source for one test and
// forgets the last good delta on both sides. Tests that call it must not run
// in parallel.
func SetCurrentRulesForTest(t testing.TB, f func(context.Context) (*secretguard.RuleSet, error)) {
	t.Helper()
	prev := currentRules
	lastDelta.Store(nil)
	currentRules = f
	t.Cleanup(func() {
		currentRules = prev
		lastDelta.Store(nil)
	})
}

// CheckPublicationForTest is the scan every batch passes before it is sent.
func CheckPublicationForTest(content []byte) error { return checkPublication(content) }

// SetSpoolRewriteHookForTest runs f while a drain replacing a withheld batch
// holds the spool's swap lock, after it read the old spool and before it
// renames the new one. Not safe for parallel tests.
func SetSpoolRewriteHookForTest(t testing.TB, f func()) {
	t.Helper()
	prev := spoolRewriteHook
	spoolRewriteHook = f
	t.Cleanup(func() { spoolRewriteHook = prev })
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
