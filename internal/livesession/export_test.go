package livesession

import (
	"context"
	"log/slog"
	"testing"
)

// SetCurrentCatalogForTest replaces the upstream catalog source for one test
// and forgets the rules this process loaded, on both sides. Tests that call it
// must not run in parallel.
func SetCurrentCatalogForTest(t testing.TB, f func(context.Context) ([]byte, error)) {
	t.Helper()
	prev := currentCatalog
	loadedRules.Store(nil)
	currentCatalog = f
	t.Cleanup(func() {
		currentCatalog = prev
		loadedRules.Store(nil)
	})
}

// ForgetLoadedRulesForTest drops the rules this process loaded or refreshed,
// as a new process (a CLI stop) starts without them.
func ForgetLoadedRulesForTest() { loadedRules.Store(nil) }

// RulesPathForTest is where the refreshed secret rules are kept for cfg.
func RulesPathForTest(cfg Config) string { return rulesPath(cfg.DataDir) }

// CheckPublicationForTest is the scan every batch passes before it is sent,
// with the rules persisted in cfg's data dir.
func CheckPublicationForTest(cfg Config, content []byte) error {
	return checkPublication(loadRules(context.Background(), cfg.DataDir, slog.Default()), content)
}

// SetSpoolRewriteHookForTest runs f while a drain replacing a withheld batch
// holds the spool's swap lock, after it cut the spool and before it writes the
// new tail; an error f returns fails that write. Not safe for parallel tests.
func SetSpoolRewriteHookForTest(t testing.TB, f func() error) {
	t.Helper()
	prev := spoolRewriteHook
	spoolRewriteHook = f
	t.Cleanup(func() { spoolRewriteHook = prev })
}

// SetSpoolSwapWaitHookForTest runs f when an append that found live.lock busy
// is about to wait for the swap lock. Not safe for parallel tests.
func SetSpoolSwapWaitHookForTest(t testing.TB, f func()) {
	t.Helper()
	prev := spoolSwapWaitHook
	spoolSwapWaitHook = f
	t.Cleanup(func() { spoolSwapWaitHook = prev })
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

// SetSpoolUnlockedHookForTest runs f when Spool's attempt under live.lock has
// returned and released it. Not safe for parallel tests.
func SetSpoolUnlockedHookForTest(t testing.TB, f func()) {
	t.Helper()
	prev := spoolUnlockedHook
	spoolUnlockedHook = f
	t.Cleanup(func() { spoolUnlockedHook = prev })
}
