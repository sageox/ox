package adapterprotocol

// Canonical per-bundled-adapter capability declarations.
//
// This is THE single source of truth for "which capabilities does adapter X
// declare". Before this file, the same fact
// was hand-copied into five places — each bundled adapter's own main.go,
// up to five per-adapter pin tests, internal/prime/conformance_test.go's
// adapterCaps fixture, internal/adapter/registry.yaml, and
// internal/adapter/registry_test.go's bundledCapabilities table — and it
// drifted twice, caught only by hand.
//
// Every bundled adapter's handleInfo() in cmd/ox-adapter-<name>/main.go now
// returns one of the vars below BY GO IDENTIFIER, never by a string lookup.
// A typo'd or swapped adapter is therefore a compile error at the call site,
// not a runtime surprise: there is no map keyed by adapter name for a binary
// to miss.
//
// registry.yaml is data consumed at runtime via go:embed and cannot import
// Go, so it stays a separate artifact; internal/adapter's
// TestBundledAdapters_CapabilitiesMatchBinary is the one place a "does X
// agree with Y" test is genuinely unavoidable, binding registry.yaml to
// BundledAdapterCapabilities below. Do not read that as license to add more
// such tests elsewhere — collapse to one source instead.
var (
	ClaudeCodeCapabilities = []string{
		CapSessionReader,
		CapHookInstaller,
		CapSkillsInstaller,
		CapIncrementalReader,
		CapFileWatcher,
		CapServeMode,
		CapSessionImporter,
		CapCapturePrior,
	}

	GeminiCapabilities = []string{
		CapSessionReader,
		CapHookInstaller,
		CapSkillsInstaller,
		CapIncrementalReader,
		CapFileWatcher,
		CapServeMode,
		CapSessionImporter,
	}

	CodexCapabilities = []string{
		CapSessionReader,
		CapHookInstaller,
		CapSkillsInstaller,
		CapIncrementalReader,
		CapFileWatcher,
		CapServeMode,
		CapSessionImporter,
	}

	AmpCapabilities = []string{
		CapSessionReader,
		CapSessionImporter,
		CapHookInstaller,
		CapIncrementalReader,
		CapFileWatcher,
		CapServeMode,
		CapSkillsInstaller,
	}

	// OpenCodeCapabilities carries no CapFileWatcher: OpenCode sessions live in
	// SQLite rows, so there is no filesystem path for fsnotify to watch.
	OpenCodeCapabilities = []string{
		CapSessionReader,
		CapHookInstaller,
		CapIncrementalReader,
		CapSessionImporter,
		CapServeMode,
		CapSkillsInstaller,
	}

	PiCapabilities = []string{
		CapSessionReader,
		CapHookInstaller,
		CapIncrementalReader,
		CapFileWatcher,
		CapSessionImporter,
		CapServeMode,
		CapSkillsInstaller,
	}

	OMPCapabilities = []string{
		CapSessionReader,
		CapHookInstaller,
		CapSkillsInstaller,
		CapIncrementalReader,
		CapFileWatcher,
		CapSessionImporter,
		CapServeMode,
	}

	// AiderCapabilities carries no CapSkillsInstaller: aider declares no
	// SkillTargets.
	AiderCapabilities = []string{
		CapSessionReader,
		CapHookInstaller,
		CapIncrementalReader,
		CapFileWatcher,
		CapSessionImporter,
		CapServeMode,
	}

	DroidCapabilities = []string{
		CapSessionReader,
		CapHookInstaller,
		CapIncrementalReader,
		CapFileWatcher,
		CapServeMode,
		CapSessionImporter,
		CapSkillsInstaller,
	}

	// GooseCapabilities carries no CapFileWatcher: a Goose session is a SQLite
	// row behind a virtual "goose:<id>" handle, so there is no path on disk
	// for fsnotify to watch.
	GooseCapabilities = []string{
		CapSessionReader,
		CapHookInstaller,
		CapIncrementalReader,
		CapSessionImporter,
		CapCapturePrior,
		CapServeMode,
		CapSkillsInstaller,
	}
)

// BundledAdapterCapabilities maps each bundled adapter's registry.yaml `name`
// field to its canonical capability set above. This is the only string-keyed
// lookup in the capability system, and it exists solely because
// registry.yaml (data, not Go) and test fixtures that walk it identify
// adapters by name string rather than Go identifier. Consumers: the
// registry/binary parity test (internal/adapter) and the cross-adapter
// conformance test (internal/prime).
//
// TestBundledAdapterCapabilities_AllNonEmptyAndKnown guards against a typo'd
// key silently resolving to a missing or empty set.
var BundledAdapterCapabilities = map[string][]string{
	"claude-code": ClaudeCodeCapabilities,
	"gemini":      GeminiCapabilities,
	"codex":       CodexCapabilities,
	"amp":         AmpCapabilities,
	"opencode":    OpenCodeCapabilities,
	"pi":          PiCapabilities,
	"omp":         OMPCapabilities,
	"aider":       AiderCapabilities,
	"droid":       DroidCapabilities,
	"goose":       GooseCapabilities,
}
