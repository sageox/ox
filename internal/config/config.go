package config

import (
	"os"

	"github.com/sageox/ox/internal/runtime"
)

type Config struct {
	Verbose       bool
	Quiet         bool
	JSON          bool
	Text          bool // human-readable text output (overrides JSON default)
	Review        bool // security audit mode: both human summary and machine output
	NoInteractive bool // disable spinners and TUI elements (auto-enabled in CI/ephemeral)
}

// Load creates a Config from environment variables only.
// Runtime flags (--verbose, --json, etc.) are applied by the cobra layer in cli/context.go.
func Load() *Config {
	// Seed the user-config ephemeral preference before any subsystem
	// consults the runtime capability probe. Commands like `ox distill
	// --sync` decide about daemon startup during PersistentPreRun, so
	// the persisted preference must be visible here even if no later
	// code explicitly loads user config.
	_, _ = LoadUserConfig()

	return &Config{
		Verbose: os.Getenv("OX_VERBOSE") == "1",
		Quiet:   os.Getenv("OX_QUIET") == "1",
		JSON:    os.Getenv("OX_JSON") == "1",
		Text:    os.Getenv("OX_TEXT") == "1",
		Review:  os.Getenv("OX_REVIEW") == "1",
		// NoInteractive turns off spinners and TUI elements. The honest
		// question is "is there a human watching the terminal?" — which
		// is what runtime.Caps().Browser probes (sandboxes, CI, and
		// OX_BROWSER=0 all collapse it). The legacy isCI() check stays
		// as a second guard in case the Browser probe ever loosens its
		// CI heuristics; both signals agree on the CI case today, but
		// the redundancy is cheap.
		NoInteractive: os.Getenv("OX_NO_INTERACTIVE") == "1" || isCI() || !runtime.Caps().Browser,
	}
}

// IsCI returns true if running in a CI environment.
// Checks standard CI environment variables used by major CI providers.
// Kept as a separate predicate from runtime.Caps() because some callers
// want the narrower "is CI specifically" answer (e.g. test fixtures
// that behave differently under a buildkite agent vs a Claude Cloud
// sandbox).
func IsCI() bool {
	return isCI()
}

// isCI returns true if running in a CI environment.
// Checks standard CI environment variables used by major CI providers.
// Keep this list in sync with internal/ephemeral/mode.go (envCI).
func isCI() bool {
	ciVars := []string{"CI", "GITHUB_ACTIONS", "GITLAB_CI", "JENKINS_URL", "BUILDKITE", "CODEBUILD_BUILD_ID"}
	for _, v := range ciVars {
		if val := os.Getenv(v); val != "" && val != "false" && val != "0" {
			return true
		}
	}
	return false
}
