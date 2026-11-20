package livesession

// SetPrepareReopenStateHook makes PrepareReopen's state write fail (or run fn
// in its place) until the returned restore is called.
func SetPrepareReopenStateHook(fn func() error) (restore func()) {
	prepareReopenStateHook = fn
	return func() { prepareReopenStateHook = nil }
}
