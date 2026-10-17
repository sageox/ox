package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
)

// configSchemaVersion is bumped only on a breaking change to the
// 'ox config schema --json' shape. Adding a field is not breaking.
const configSchemaVersion = 1

// Setting types in 'ox config schema --json'.
const (
	configSchemaTypeEnum = "enum" // values lists every value 'ox config set' accepts
	configSchemaTypeText = "text" // free text; values is null
)

// configSchema is the machine-readable description of every 'ox config'
// setting. Apps (e.g. SageOx desktop) render settings controls from it instead
// of keeping their own copy of the list, so it is built from AllSettings — the
// same registry 'ox config', 'ox config list' and 'ox config set' use.
type configSchema struct {
	SchemaVersion int                   `json:"schema_version"`
	Settings      []configSchemaSetting `json:"settings"`
}

type configSchemaSetting struct {
	Key         string        `json:"key"`
	Label       string        `json:"label"`
	Description string        `json:"description"`
	Type        string        `json:"type"`
	Values      []string      `json:"values"` // nil (JSON null) for text settings
	Scopes      []ConfigLevel `json:"scopes"`
	Deprecated  bool          `json:"deprecated"`
	// Value, Source and Default mean exactly what they mean in 'ox config --json'.
	Value   string      `json:"value"`
	Source  ConfigLevel `json:"source"`
	Default string      `json:"default"`
}

var configSchemaCmd = &cobra.Command{
	Use:   "schema",
	Short: "Describe every config setting (for apps)",
	Long: `Describe every config setting: its label, description, type, allowed
values, the levels it can be set at, and its current value.

Use --json for the machine-readable form apps render settings from. It works
outside a repository, where values come from user config and defaults.

Examples:
  ox config schema
  ox config schema --json`,
	Args: cobra.NoArgs,
	RunE: runConfigSchema,
}

func init() {
	configCmd.AddCommand(configSchemaCmd)
}

func runConfigSchema(cmd *cobra.Command, _ []string) error {
	// config.FindProjectRoot returns "" outside a repo (the desktop app runs
	// this from /), so values resolve from user config and defaults only.
	// findProjectRoot would fall back to cwd and log a warning instead.
	projectRoot := config.FindProjectRoot()
	schema := buildConfigSchema(projectRoot)

	if cfg != nil && cfg.JSON {
		return cli.PrintJSONTo(cmd.OutOrStdout(), schema)
	}

	out := cmd.OutOrStdout()
	keyWidth := 0
	for _, s := range schema.Settings {
		keyWidth = max(keyWidth, len(s.Key))
	}
	for _, s := range schema.Settings {
		scopes := make([]string, len(s.Scopes))
		for i, l := range s.Scopes {
			scopes[i] = string(l)
		}
		fmt.Fprintf(out, "%-*s  %s\n", keyWidth, s.Key, cli.StyleBold.Render(s.Label))
		fmt.Fprintf(out, "%-*s  %s\n", keyWidth, "", cli.StyleDim.Render(fmt.Sprintf(
			"%s · set at: %s · now: %s (%s)",
			settingValuesSummary(GetSetting(s.Key)), strings.Join(scopes, ", "), s.Value, s.Source)))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, cli.StyleDim.Render("Use 'ox config schema --json' for the machine-readable schema"))
	return nil
}

// buildConfigSchema describes every registered setting, resolving current
// values against projectRoot ("" = no repo: user and default levels only).
func buildConfigSchema(projectRoot string) configSchema {
	schema := configSchema{
		SchemaVersion: configSchemaVersion,
		Settings:      make([]configSchemaSetting, 0, len(AllSettings)),
	}
	for _, s := range AllSettings {
		entry := configSchemaSetting{
			Key:         s.Key,
			Label:       s.Label,
			Description: s.Description,
			Type:        configSchemaTypeText,
			Scopes:      append([]ConfigLevel{}, s.Levels...),
			Deprecated:  s.Deprecated,
			Default:     s.Default,
		}
		if len(s.ValidValues) > 0 {
			entry.Type = configSchemaTypeEnum
			entry.Values = append([]string{}, s.ValidValues...)
		}
		if cv, err := ResolveConfigValue(s.Key, projectRoot); err == nil {
			entry.Value, entry.Source, entry.Default = cv.Value, cv.Source, cv.Default
		}
		schema.Settings = append(schema.Settings, entry)
	}
	return schema
}

// settingValuesSummary is the one-line "what can I set this to" hint shared by
// 'ox config --help' and 'ox config schema'.
func settingValuesSummary(s *ConfigSetting) string {
	switch {
	case s == nil:
		return ""
	case len(s.ValidValues) > 0:
		return strings.Join(s.ValidValues, " | ")
	case s.Key == "attribution.score_threshold":
		return "0.0 to 1.0"
	default:
		return `any text, "" to disable`
	}
}

// configAvailableSettingsHelp renders the "Available settings" block of
// 'ox config --help' from AllSettings, so the help can never list a key that
// 'ox config set' rejects or omit one it accepts.
func configAvailableSettingsHelp() string {
	keyWidth := 0
	for _, s := range AllSettings {
		keyWidth = max(keyWidth, len(s.Key))
	}
	var b strings.Builder
	b.WriteString("Available settings:\n")
	for i := range AllSettings {
		s := &AllSettings[i]
		line := fmt.Sprintf("  %-*s  %s", keyWidth, s.Key, settingValuesSummary(s))
		if s.Deprecated {
			line += "  (deprecated)"
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}
