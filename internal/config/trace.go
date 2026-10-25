package config

// TraceConfig controls the local trace receiver. Removing this block disables
// automatic startup. Claude Code exporter settings are configured separately.
type TraceConfig struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port,omitempty"`
}
