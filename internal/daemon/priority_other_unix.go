//go:build unix && !darwin

package daemon

// enterBackgroundBand is a no-op off macOS: no other unix has a background QoS
// band, and nice already applies.
func enterBackgroundBand() error { return nil }
