package main

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
)

type versionInfo struct {
	Version   string `json:"version"`
	BuildDate string `json:"build_date"`
	GitCommit string `json:"git_commit"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Long:  "Display version, build date, git commit, and Go version information for ox CLI",
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOutput, _ := cmd.Flags().GetBool("json")

		info := versionInfo{
			Version:   version.Version,
			BuildDate: version.BuildDate,
			GitCommit: version.GitCommit,
			GoVersion: runtime.Version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
		}

		if jsonOutput {
			return cli.PrintJSONTo(cmd.OutOrStdout(), info)
		}

		printVersionStyled(info)
		return nil
	},
}

// printVersionStyled renders version info with semantic colors
func printVersionStyled(info versionInfo) {
	// check if this is a dirty build (uncommitted changes)
	isDirty := strings.Contains(info.Version, "-dirty")

	// version line: prominent, warn if dirty
	fmt.Print(cli.StyleBrand.Render("ox") + " ")
	if isDirty {
		// strip -dirty suffix for cleaner display, show warning separately
		cleanVersion := strings.TrimSuffix(info.Version, "-dirty")
		fmt.Print(cli.StyleWarning.Render(cleanVersion))
		fmt.Print(" " + cli.StyleWarning.Render("(dirty)"))
	} else {
		fmt.Print(cli.StyleSuccess.Render(info.Version))
	}
	fmt.Println()

	// format build date nicely if valid
	buildDateDisplay := info.BuildDate
	if t, err := time.Parse(time.RFC3339, info.BuildDate); err == nil {
		buildDateDisplay = t.Format("2006-01-02 15:04 MST")
	}

	// aligned key-value pairs with semantic colors
	fmt.Printf("%s %s\n",
		cli.StyleDim.Render("Built:"),
		cli.StyleDim.Render(buildDateDisplay))
	fmt.Printf("%s %s\n",
		cli.StyleDim.Render("Commit:"),
		cli.StyleInfo.Render(info.GitCommit))
	fmt.Printf("%s %s\n",
		cli.StyleDim.Render("Go:"),
		cli.StyleDim.Render(info.GoVersion))
	fmt.Printf("%s %s\n",
		cli.StyleDim.Render("Platform:"),
		cli.StyleDim.Render(info.OS+"/"+info.Arch))
}

func init() {
	versionCmd.Flags().Bool("json", false, "output version information as JSON (default: false)")
}
