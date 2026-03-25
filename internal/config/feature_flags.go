package config

import "os"

// FeatureMurmurEnabled returns true when cross-agent murmur coordination is enabled.
// The underlying whisper infrastructure is always on (it replaced the old notification
// system), but the murmur mechanism — writing JSON files for other agents to discover —
// is gated behind FEATURE_MURMUR until we're confident in its behavior.
func FeatureMurmurEnabled() bool {
	v := os.Getenv("FEATURE_MURMUR")
	return v == "true" || v == "1"
}
