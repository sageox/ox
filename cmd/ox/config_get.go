package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/spf13/cobra"
)

var configGetCmd = &cobra.Command{
	Use:   "get <setting>",
	Short: "Get a config setting value",
	Long: `Get the current value of a config setting with its source.

Shows the effective value and the override chain (user > repo > team > default).

Examples:
  ox config get session_recording
  ox config get telemetry
  ox config get session_recording --json`,
	Args: cobra.ExactArgs(1),
	RunE: runConfigGet,
}

func init() {
	configGetCmd.Flags().Bool("json", false, "Output in JSON format")
}

func runConfigGet(cmd *cobra.Command, args []string) error {
	key := args[0]
	jsonOutput, _ := cmd.Flags().GetBool("json")

	projectRoot, _ := findProjectRoot()

	cv, err := ResolveConfigValue(key, projectRoot)
	if err != nil {
		return err
	}

	if jsonOutput {
		out, err := json.MarshalIndent(cv, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}

	// human-readable output with override chain
	fmt.Printf("%s: %s\n", cli.StyleBold.Render(key), cli.StyleSuccess.Render(cv.Value))
	fmt.Println()

	// show description
	setting := GetSetting(key)
	if setting != nil && setting.LongDescription != "" {
		fmt.Println(cli.StyleDim.Render(setting.LongDescription))
		fmt.Println()
	}

	// show override chain
	fmt.Println(cli.StyleDim.Render("Override chain:"))

	// default
	defaultMark := " "
	if cv.Source == ConfigLevelDefault {
		defaultMark = "→"
	}
	fmt.Printf("  %s default: %s\n", defaultMark, cv.Default)

	// team (if setting supports it)
	if setting != nil && containsLevel(setting.Levels, ConfigLevelTeam) {
		teamMark := " "
		if cv.Source == ConfigLevelTeam {
			teamMark = "→"
		}
		teamVal := cv.TeamVal
		if teamVal == "" {
			teamVal = cli.StyleDim.Render("(not set)")
		}
		fmt.Printf("  %s team:    %s\n", teamMark, teamVal)
	}

	// repo (if setting supports it)
	if setting != nil && containsLevel(setting.Levels, ConfigLevelRepo) {
		repoMark := " "
		if cv.Source == ConfigLevelRepo {
			repoMark = "→"
		}
		repoVal := cv.RepoVal
		if repoVal == "" {
			repoVal = cli.StyleDim.Render("(not set)")
		}
		fmt.Printf("  %s repo:    %s\n", repoMark, repoVal)
	}

	// user (if setting supports it)
	if setting != nil && containsLevel(setting.Levels, ConfigLevelUser) {
		userMark := " "
		if cv.Source == ConfigLevelUser {
			userMark = "→"
		}
		userVal := cv.UserVal
		if userVal == "" {
			userVal = cli.StyleDim.Render("(not set)")
		}
		fmt.Printf("  %s user:    %s\n", userMark, userVal)
	}

	fmt.Println()
	fmt.Printf(cli.StyleDim.Render("Effective: %s (from %s)\n"), cv.Value, cv.Source)

	return nil
}

func containsLevel(levels []ConfigLevel, target ConfigLevel) bool {
	for _, l := range levels {
		if l == target {
			return true
		}
	}
	return false
}

var configSetCmd = &cobra.Command{
	Use:   "set <setting> <value>",
	Short: "Set a config setting value",
	Long: `Set a config setting at a specific level.

By default, sets at user level (overrides all other levels).
Use --repo or --team to set at those levels instead.

Examples:
  ox config set session_recording auto
  ox config set session_recording disabled --user
  ox config set session_recording manual --repo
  ox config set telemetry off`,
	Args: cobra.ExactArgs(2),
	RunE: runConfigSet,
}

func init() {
	configSetCmd.Flags().Bool("user", false, "Set at user level (default)")
	configSetCmd.Flags().Bool("repo", false, "Set at repo level")
	configSetCmd.Flags().Bool("team", false, "Set at team level")
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	key := args[0]
	value := strings.ToLower(args[1])

	userLevel, _ := cmd.Flags().GetBool("user")
	repoLevel, _ := cmd.Flags().GetBool("repo")
	teamLevel, _ := cmd.Flags().GetBool("team")

	// default to user level
	level := ConfigLevelUser
	if repoLevel {
		level = ConfigLevelRepo
	} else if teamLevel {
		level = ConfigLevelTeam
	} else if userLevel {
		level = ConfigLevelUser
	}

	projectRoot, _ := findProjectRoot()

	if err := SetConfigValue(key, value, level, projectRoot); err != nil {
		return err
	}

	fmt.Printf("%s Set %s = %s (at %s level)\n",
		cli.StyleSuccess.Render("✓"),
		key,
		value,
		level)

	return nil
}

var configUnsetCmd = &cobra.Command{
	Use:   "unset <setting>",
	Short: "Clear a config setting at a specific level",
	Long: `Clear a config override, falling back to the next level in the chain.

Priority: user > repo > team > default.
Clearing a user-level override lets the repo or team value take effect.

Examples:
  ox config unset session_recording           # clears user override
  ox config unset session_recording --repo    # clears repo override
  ox config unset murmur_send --user          # clears user override`,
	Args: cobra.ExactArgs(1),
	RunE: runConfigUnset,
}

func init() {
	configUnsetCmd.Flags().Bool("user", false, "Clear at user level (default)")
	configUnsetCmd.Flags().Bool("repo", false, "Clear at repo level")
	configUnsetCmd.Flags().Bool("team", false, "Clear at team level")
}

func runConfigUnset(cmd *cobra.Command, args []string) error {
	key := args[0]

	userLevel, _ := cmd.Flags().GetBool("user")
	repoLevel, _ := cmd.Flags().GetBool("repo")
	teamLevel, _ := cmd.Flags().GetBool("team")

	level := ConfigLevelUser
	if repoLevel {
		level = ConfigLevelRepo
	} else if teamLevel {
		level = ConfigLevelTeam
	} else if userLevel {
		level = ConfigLevelUser
	}

	projectRoot, _ := findProjectRoot()

	if err := UnsetConfigValue(key, level, projectRoot); err != nil {
		return err
	}

	// show what the effective value is now
	cv, _ := ResolveConfigValue(key, projectRoot)
	effectiveInfo := ""
	if cv != nil {
		effectiveInfo = fmt.Sprintf(" (now: %s %s %s)",
			cli.StyleSuccess.Render(cv.Value),
			cli.StyleDim.Render("←"),
			cli.StyleDim.Render(string(cv.Source)))
	}

	fmt.Printf("%s Cleared %s at %s level%s\n",
		cli.StyleSuccess.Render("✓"),
		key,
		level,
		effectiveInfo)

	return nil
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all config settings",
	Long: `List all config settings with their current values and sources.

Examples:
  ox config list
  ox config list --json`,
	RunE: runConfigList,
}

func init() {
	configListCmd.Flags().Bool("json", false, "Output in JSON format")
}

func runConfigList(cmd *cobra.Command, args []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	projectRoot, _ := findProjectRoot()

	var values []*ConfigValue
	for _, setting := range AllSettings {
		cv, err := ResolveConfigValue(setting.Key, projectRoot)
		if err != nil {
			continue
		}
		values = append(values, cv)
	}

	if jsonOutput {
		out, err := json.MarshalIndent(values, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}

	// group by category
	categories := make(map[string][]ConfigSetting)
	for _, s := range AllSettings {
		categories[s.Category] = append(categories[s.Category], s)
	}

	fmt.Println(cli.StyleBold.Render("Configuration Settings"))
	fmt.Println()

	for category, settings := range categories {
		fmt.Println(cli.StyleBold.Render(category))
		for _, setting := range settings {
			cv, _ := ResolveConfigValue(setting.Key, projectRoot)
			if cv == nil {
				continue
			}

			sourceIndicator := ""
			switch cv.Source {
			case ConfigLevelUser:
				sourceIndicator = cli.StyleDim.Render(" (user)")
			case ConfigLevelRepo:
				sourceIndicator = cli.StyleDim.Render(" (repo)")
			case ConfigLevelTeam:
				sourceIndicator = cli.StyleDim.Render(" (team)")
			case ConfigLevelDefault:
				sourceIndicator = cli.StyleDim.Render(" (default)")
			}

			fmt.Printf("  %-25s %s%s\n",
				setting.Key+":",
				cli.StyleSuccess.Render(cv.Value),
				sourceIndicator)
		}
		fmt.Println()
	}

	fmt.Println(cli.StyleDim.Render("Use 'ox config get <setting>' for details"))
	fmt.Println(cli.StyleDim.Render("Use 'ox config set <setting> <value>' to change"))

	return nil
}
