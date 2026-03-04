package config

// Environment variable names for configuration overrides.
// Each variable is consumed in exactly one resolver function.
const (
	// EnvProjectRoot overrides project root discovery (walk-up from cwd).
	// Consumed by: ResolveProjectRootOverride()
	EnvProjectRoot = "OX_PROJECT_ROOT"

	// EnvSessionRecording overrides the session recording mode.
	// Consumed by: ResolveSessionRecording()
	EnvSessionRecording = "OX_SESSION_RECORDING"

	// EnvUserConfig overrides the user config file path.
	// Consumed by: LoadUserConfig()
	EnvUserConfig = "OX_USER_CONFIG"
)
