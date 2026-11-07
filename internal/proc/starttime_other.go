//go:build !linux && !darwin

package proc

// startTime is unknown here, so IsReused keeps callers on PID liveness alone.
func startTime(int) (int64, bool) { return 0, false }
