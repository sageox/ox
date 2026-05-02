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
	"discussions/", // all discussion artifacts synced by cloud pipeline
	"agent-context/",
}

// FallbackConfig returns a ManifestConfig with hardcoded control-plane
// paths and sensible defaults. Used when .sageox/sync.manifest is
// missing, unparseable, or has an unknown version.
func FallbackConfig() *ManifestConfig {
	includes := make([]string, len(fallbackIncludes))
	copy(includes, fallbackIncludes)

	rules := make([]ResolveRule, len(DefaultResolveRules))
	copy(rules, DefaultResolveRules)

	return &ManifestConfig{
		Version:         SupportedVersion,
		Includes:        includes,
		Denies:          nil,
		SyncIntervalMin: DefaultSyncIntervalMin,
		GCIntervalDays:  DefaultGCIntervalDays,
		ResolveRules:    rules,
	}
}
