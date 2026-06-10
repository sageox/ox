// Package adapter provides the embedded adapter registry and lookup functions.
//
// The registry is a static YAML file embedded in the ox binary via go:embed.
// It lists all known adapters (bundled and external) with their metadata,
// capabilities, and source repositories. Phase 1 is embedded-only; future
// phases will fetch updates from sageox/ox-adapters.
package adapter

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed registry.yaml
var registryYAML []byte

// Registry is the top-level adapter registry structure.
type Registry struct {
	SchemaVersion int              `yaml:"schema_version" json:"schema_version"`
	Adapters      []AdapterEntry   `yaml:"adapters" json:"adapters"`
	Community     []CommunityEntry `yaml:"community,omitempty" json:"community,omitempty"`
}

// AdapterEntry describes a known adapter in the registry.
type AdapterEntry struct {
	Name         string   `yaml:"name" json:"name"`
	DisplayName  string   `yaml:"display_name" json:"display_name"`
	Description  string   `yaml:"description" json:"description"`
	Type         string   `yaml:"type" json:"type"` // session, vcs, indexer
	Bundled      bool     `yaml:"bundled" json:"bundled"`
	Binary       string   `yaml:"binary" json:"binary"`
	Repo         string   `yaml:"repo" json:"repo"`
	Capabilities []string `yaml:"capabilities" json:"capabilities"`
	DetectCmds   []string `yaml:"detect_commands,omitempty" json:"detect_commands,omitempty"`

	// Curated-path integrity fields (ADR-022, ox-5ihl). For the short-name
	// install path SageOx is the trust anchor, so a code-reviewed release tag
	// pin plus per-platform sha256 in this registry IS the authenticity control.
	// The download is verified against these BEFORE the binary is made
	// executable or run (see runAdapterInstall). A missing pin fails closed
	// unless the user opts into --allow-unverified.
	//
	// Tag pins the exact GitHub release (e.g. "v1.2.3"); installs fetch
	// releases/tags/<Tag>, never releases/latest. Checksums maps a platform key
	// ("darwin_arm64") to the lowercase sha256 hex of that platform's asset.
	Tag       string            `yaml:"tag,omitempty" json:"tag,omitempty"`
	Checksums map[string]string `yaml:"checksums,omitempty" json:"checksums,omitempty"`
}

// RequireInstallable returns an error if a non-bundled entry lacks the curated
// integrity pins (Tag + Checksums) that ADR-022 requires before SageOx can
// vouch for a short-name install. Bundled adapters ship with ox and are exempt.
//
// Callers use this to drive fail-closed behavior on the curated install path:
// an entry that is not installable can only be installed with an explicit
// --allow-unverified opt-in. It is NOT called automatically at registry load,
// so `ox adapter list`/`info` still surface entries that lack a pin.
func (e *AdapterEntry) RequireInstallable() error {
	if e.Bundled {
		return nil
	}
	if e.Tag == "" {
		return fmt.Errorf("adapter %q has no pinned release tag in the registry", e.Name)
	}
	if len(e.Checksums) == 0 {
		return fmt.Errorf("adapter %q has no per-platform checksums in the registry", e.Name)
	}
	return nil
}

// CommunityEntry describes a community-contributed adapter.
type CommunityEntry struct {
	Name         string   `yaml:"name" json:"name"`
	DisplayName  string   `yaml:"display_name" json:"display_name"`
	Description  string   `yaml:"description" json:"description"`
	Repo         string   `yaml:"repo" json:"repo"`
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
}

// LoadEmbeddedRegistry parses the embedded registry YAML and returns
// the registry. This is the Phase 1 registry source.
func LoadEmbeddedRegistry() (*Registry, error) {
	var reg Registry
	if err := yaml.Unmarshal(registryYAML, &reg); err != nil {
		return nil, fmt.Errorf("parse embedded registry: %w", err)
	}
	return &reg, nil
}

// Lookup finds an adapter entry by name. Returns nil if not found.
func (r *Registry) Lookup(name string) *AdapterEntry {
	for i := range r.Adapters {
		if r.Adapters[i].Name == name {
			return &r.Adapters[i]
		}
	}
	return nil
}

// BundledAdapters returns only adapters that ship with the ox binary.
func (r *Registry) BundledAdapters() []AdapterEntry {
	var result []AdapterEntry
	for _, a := range r.Adapters {
		if a.Bundled {
			result = append(result, a)
		}
	}
	return result
}

// ExternalAdapters returns only adapters that must be installed separately.
func (r *Registry) ExternalAdapters() []AdapterEntry {
	var result []AdapterEntry
	for _, a := range r.Adapters {
		if !a.Bundled {
			result = append(result, a)
		}
	}
	return result
}

// GitHubRepo returns the full github.com URL for an adapter entry.
func (e *AdapterEntry) GitHubRepo() string {
	return "github.com/" + e.Repo
}
