package manifest

// fallbackIncludes is the hardcoded sparse set used when the manifest
// is missing or unparseable.
var fallbackIncludes = []string{
	".sageox/",
	"SOUL.md",
	"TEAM.md",
	"MEMORY.md",
	"AGENTS.md",
	"memory/",
	"docs/",
	"coworkers/",
}

// FallbackConfig returns a ManifestConfig with hardcoded control-plane
// paths and sensible defaults. Used when .sageox/sync.manifest is
// missing, unparseable, or has an unknown version.
func FallbackConfig() *ManifestConfig {
	includes := make([]string, len(fallbackIncludes))
	copy(includes, fallbackIncludes)

	return &ManifestConfig{
		Version:         SupportedVersion,
		Includes:        includes,
		Denies:          nil,
		SyncIntervalMin: DefaultSyncIntervalMin,
		GCIntervalDays:  DefaultGCIntervalDays,
	}
}
