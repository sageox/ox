package config

import "os"

// FeatureWhisperEnabled returns true when the whisper/murmur system is enabled.
// Gated behind FEATURE_WHISPER env var until launch.
func FeatureWhisperEnabled() bool {
	v := os.Getenv("FEATURE_WHISPER")
	return v == "true" || v == "1"
}
