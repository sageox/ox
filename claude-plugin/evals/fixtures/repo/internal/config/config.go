// Package config loads runtime configuration for the acme service.
package config

import "os"

// Config is the resolved runtime configuration.
type Config struct {
	ArtifactStoreURL string
	MaxChunkBytes    int
}

// Load returns the configuration for this process.
func Load() Config {
	cfg := Config{ArtifactStoreURL: "https://artifacts.acme.example", MaxChunkBytes: 4 << 20}
	if v := os.Getenv("ACME_ARTIFACT_STORE_URL"); v != "" {
		cfg.ArtifactStoreURL = v
	}
	return cfg
}
