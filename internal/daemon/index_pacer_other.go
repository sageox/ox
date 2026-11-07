//go:build !darwin && !linux

package daemon

// defaultHostProbes has no signal sources on this platform: every signal reads
// as unknown, so the pacer stays calm and backfill runs unthrottled (still
// niced and GOMAXPROCS-capped like the rest of the daemon).
func defaultHostProbes() hostProbes {
	return hostProbes{}
}
