package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/pet"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
)

// pet.go — `ox pet`, Oxy in Claude Code's thinking line (terminal) and under
// each of Claude's replies (Claude desktop app's Code tab), and as Codex's
// terminal pet.
//
// Claude Code's startup mascot is compiled in and cannot be swapped; its
// thinking line can, through a Claude Code mod. `ox pet enable` writes that
// mod (with frames rendered from the sprite sheet embedded in this binary)
// and installs it at user scope through the claude CLI. For Codex it writes
// a custom pet (spritesheet.png + pet.json) and selects it in config.toml.

// Hosts `--host` accepts.
const (
	petHostClaude = "claude-code"
	petHostCodex  = "codex"
)

var petHosts = []string{petHostClaude, petHostCodex}

// newPetInstaller is swapped in tests to inject a fake claude CLI.
var newPetInstaller = func() pet.Installer {
	return pet.Installer{Dir: pet.Dir(), Version: version.Version, Claude: pet.FindClaude()}
}

// newCodexPetInstaller is swapped in tests to point at a temp Codex home.
// An unresolvable home leaves Home empty, which the installer reports.
var newCodexPetInstaller = func() pet.CodexInstaller {
	home, _ := nativeimport.CodexHome()
	return pet.CodexInstaller{Home: home, Version: version.Version}
}

// petReport is the single --json document every pet subcommand prints.
// A host that was not acted on is null.
type petReport struct {
	ClaudeCode *pet.Result      `json:"claude_code"`
	Codex      *pet.CodexResult `json:"codex"`
	Guidance   string           `json:"guidance"`
}

const petNoHostGuidance = "Neither Claude Code nor Codex was found on this machine. " +
	"Pass --host claude-code or --host codex to set Oxy up anyway."

var petCmd = &cobra.Command{
	Use:   "pet",
	Short: "Show Oxy, SageOx's mascot, in Claude Code (terminal and desktop app) and as Codex's terminal pet",
	Long: `Show Oxy, SageOx's mascot, in Claude Code's thinking line, under each of
Claude's replies in the Claude desktop app's Code tab, and as Codex's terminal
pet.

In Claude Code, Oxy animates beside the thinking word while Claude works and
follows what it is doing: thinking, typing at his laptop, chattering out an
answer, and his own routines for ox agent prime, ox query, ox plan, ox murmur,
and ox doctor. When a turn ends, Oxy throws confetti on the closing line, or
falls flat if it failed. He is installed as a Claude Code plugin
(ox-cli-oxy@ox-cli-pets) at user scope; /oxy shows him, /oxy off hides him,
and /oxy small|medium|large sizes him.

The Claude desktop app loads the same plugin, but its thinking row is one
text line tall, so Oxy stands under the last block of each of Claude's
replies instead. Under the newest reply he works while Claude works, even
while it runs commands before writing anything, celebrates when the turn
ends, then rests; under older replies he stands still. /oxy pane moves him to a
narrow side pane beside the transcript and /oxy reply moves him back. /oxy
off hides him and /oxy brings him back; a pane you close by hand stays closed
until /oxy. /oxy small|medium|large sets his height. The desktop has no
/reload-plugins, so start a new Code session after ox pet enable.

In Codex, Oxy is a custom terminal pet: he blinks while idle, works at his
laptop while Codex runs, waves when Codex needs you, throws confetti when a
turn finishes, and falls flat on an error. Pick or hide him with /pets, or
say $oxy on|off. Codex pets need a terminal with Kitty graphics or Sixel
(Ghostty, kitty, WezTerm, iTerm2 3.6+) and don't show inside tmux or Zellij.
Codex decides where Oxy sits and how big he is drawn.

Without --host, each command acts on every host found on this machine:
Claude Code when claude is on PATH or Oxy's mod folder exists, Codex when
codex is on PATH or its home ($CODEX_HOME, default ~/.codex) exists.`,
}

var petEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Install Oxy into Claude Code and Codex, or refresh him after an ox upgrade",
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, _ []string) error { return runPetCmd(cmd, petEnable) },
}

var petDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Remove Oxy from Claude Code and Codex",
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, _ []string) error { return runPetCmd(cmd, petDisable) },
}

var petStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether Oxy is installed in Claude Code and Codex",
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, _ []string) error { return runPetCmd(cmd, petStatus) },
}

// petAction is one subcommand's behavior per host.
type petAction struct {
	verb        string // for error messages: "enable", "disable", "check"
	claude      func(cmd *cobra.Command, in pet.Installer) (*pet.Result, error)
	codex       func(cmd *cobra.Command, in pet.CodexInstaller) (*pet.CodexResult, error)
	printClaude func(cmd *cobra.Command, res *pet.Result)
	printCodex  func(cmd *cobra.Command, res *pet.CodexResult)
	guideClaude func(res *pet.Result) string
}

func runPetCmd(cmd *cobra.Command, act petAction) error {
	hosts, err := petHostsFor(cmd)
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	var report petReport
	var errs []error
	if slices.Contains(hosts, petHostClaude) {
		res, err := act.claude(cmd, newPetInstaller())
		if err != nil {
			errs = append(errs, fmt.Errorf("%s Oxy in Claude Code: %w", act.verb, err))
		}
		report.ClaudeCode = res
	}
	if slices.Contains(hosts, petHostCodex) {
		res, err := act.codex(cmd, newCodexPetInstaller())
		if err != nil {
			errs = append(errs, fmt.Errorf("%s Oxy in Codex: %w", act.verb, err))
		}
		report.Codex = res
	}

	var guidance []string
	if report.ClaudeCode != nil {
		guidance = append(guidance, act.guideClaude(report.ClaudeCode))
	}
	if report.Codex != nil {
		guidance = append(guidance, report.Codex.Guidance)
	}
	if len(hosts) == 0 {
		guidance = append(guidance, petNoHostGuidance)
	}
	report.Guidance = strings.Join(guidance, " ")

	if asJSON(cmd) {
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
		return cli.PrintJSONTo(w, report)
	}
	if len(hosts) == 0 {
		cli.PrintInfoTo(w, petNoHostGuidance)
		return nil
	}
	if report.ClaudeCode != nil {
		act.printClaude(cmd, report.ClaudeCode)
	}
	if report.Codex != nil {
		act.printCodex(cmd, report.Codex)
	}
	return errors.Join(errs...)
}

var petEnable = petAction{
	verb: "enable",
	claude: func(cmd *cobra.Command, in pet.Installer) (*pet.Result, error) {
		return in.Enable(cmd.Context())
	},
	codex: func(cmd *cobra.Command, in pet.CodexInstaller) (*pet.CodexResult, error) {
		return in.Enable(cmd.Context())
	},
	printClaude: func(cmd *cobra.Command, res *pet.Result) {
		w := cmd.OutOrStdout()
		if len(res.ManualSteps) > 0 {
			cli.PrintWarningTo(cmd.ErrOrStderr(), "Claude Code's CLI (claude) isn't on PATH, so Oxy is written but not installed.")
			printSteps(w, "In Claude Code, run:", res.ManualSteps)
			return
		}
		cli.PrintSuccessTo(w, "Oxy will show up in Claude Code's thinking line and under Claude's replies in the Claude desktop app's Code tab.")
		cli.PrintHintTo(w, "Run /reload-plugins in an open terminal session, or start a new one; on the desktop, start a new Code session. /oxy off hides him.")
	},
	printCodex: func(cmd *cobra.Command, _ *pet.CodexResult) {
		w := cmd.OutOrStdout()
		cli.PrintSuccessTo(w, "Oxy is Codex's terminal pet.")
		cli.PrintHintTo(w, "Restart Codex or open /pets to see him; $oxy off hides him. "+
			"Needs a terminal with Kitty graphics or Sixel (Ghostty, kitty, WezTerm, iTerm2 3.6+), not tmux.")
	},
	guideClaude: func(res *pet.Result) string {
		if len(res.ManualSteps) > 0 {
			return "Oxy's Claude Code files are written but the claude CLI isn't on PATH; in Claude Code, run " +
				strings.Join(res.ManualSteps, " then ") + "."
		}
		return "Oxy is in Claude Code's thinking line and under Claude's replies in the desktop app's Code tab; run /reload-plugins or start a new session (on the desktop, a new Code session) to see him."
	},
}

var petDisable = petAction{
	verb: "disable",
	claude: func(cmd *cobra.Command, in pet.Installer) (*pet.Result, error) {
		return in.Disable(cmd.Context())
	},
	codex: func(cmd *cobra.Command, in pet.CodexInstaller) (*pet.CodexResult, error) {
		return in.Disable(cmd.Context())
	},
	printClaude: func(cmd *cobra.Command, res *pet.Result) {
		w := cmd.OutOrStdout()
		if len(res.ManualSteps) > 0 {
			printSteps(w, "Oxy's files are removed. In Claude Code, also run:", res.ManualSteps)
			return
		}
		cli.PrintSuccessTo(w, "Oxy is removed from Claude Code. Open sessions keep it until /reload-plugins.")
	},
	printCodex: func(cmd *cobra.Command, _ *pet.CodexResult) {
		cli.PrintSuccessTo(cmd.OutOrStdout(), "Oxy is removed from Codex. Restart Codex for the change to show.")
	},
	guideClaude: func(res *pet.Result) string {
		if len(res.ManualSteps) > 0 {
			return "Oxy's Claude Code files are removed; in Claude Code, also run " +
				strings.Join(res.ManualSteps, " then ") + "."
		}
		return "Oxy is removed from Claude Code; open sessions keep him until /reload-plugins."
	},
}

var petStatus = petAction{
	verb: "check",
	claude: func(cmd *cobra.Command, in pet.Installer) (*pet.Result, error) {
		return in.Status(cmd.Context())
	},
	codex: func(cmd *cobra.Command, in pet.CodexInstaller) (*pet.CodexResult, error) {
		return in.Status(cmd.Context())
	},
	printClaude: func(cmd *cobra.Command, res *pet.Result) {
		w := cmd.OutOrStdout()
		switch {
		case res.Installed && res.InstalledVersion != "" && res.InstalledVersion != res.Version:
			cli.PrintWarningTo(w, fmt.Sprintf("Oxy %s is installed; this ox is %s. Run `ox pet enable` to refresh it.", res.InstalledVersion, res.Version))
		case res.Installed:
			cli.PrintSuccessTo(w, "Oxy is installed in Claude Code.")
		case res.FilesPresent:
			cli.PrintWarningTo(w, "Oxy's files are written but Claude Code hasn't installed them. Run `ox pet enable`.")
		default:
			cli.PrintInfoTo(w, "Oxy isn't enabled. Run `ox pet enable`.")
		}
		fmt.Fprintf(w, "  %s\n", cli.StyleDim.Render(res.Dir))
	},
	printCodex: func(cmd *cobra.Command, res *pet.CodexResult) {
		w := cmd.OutOrStdout()
		switch {
		case !res.FilesPresent:
			cli.PrintInfoTo(w, "Oxy isn't enabled for Codex. Run `ox pet enable --host codex`.")
		case res.InstalledVersion != res.Version:
			cli.PrintWarningTo(w, fmt.Sprintf("Oxy %s is installed for Codex; this ox is %s. Run `ox pet enable` to refresh it.", res.InstalledVersion, res.Version))
		case !res.Selected:
			cli.PrintWarningTo(w, "Oxy is installed for Codex but isn't the selected pet. Pick him in /pets or run `ox pet enable`.")
		default:
			cli.PrintSuccessTo(w, "Oxy is Codex's terminal pet.")
			cli.PrintHintTo(w, pet.CodexSizeNote)
		}
		fmt.Fprintf(w, "  %s\n", cli.StyleDim.Render(res.Dir))
	},
	guideClaude: func(res *pet.Result) string {
		switch {
		case res.Installed && res.InstalledVersion != "" && res.InstalledVersion != res.Version:
			return "Oxy in Claude Code is from an older ox; run `ox pet enable` to refresh him."
		case res.Installed:
			return "Oxy is installed in Claude Code."
		case res.FilesPresent:
			return "Oxy's Claude Code files are written but not installed; run `ox pet enable`."
		default:
			return "Oxy isn't enabled in Claude Code; run `ox pet enable` to add him."
		}
	},
}

// petHostsFor resolves --host: the named hosts (validated, deduplicated, in
// canonical order), or every detected host when the flag is absent.
func petHostsFor(cmd *cobra.Command) ([]string, error) {
	named, _ := cmd.Flags().GetStringSlice("host")
	if len(named) == 0 {
		return detectPetHosts(), nil
	}
	for _, h := range named {
		if !slices.Contains(petHosts, h) {
			return nil, fmt.Errorf("unknown --host %q: use %s", h, strings.Join(petHosts, " or "))
		}
	}
	var hosts []string
	for _, h := range petHosts {
		if slices.Contains(named, h) {
			hosts = append(hosts, h)
		}
	}
	return hosts, nil
}

// detectPetHosts is every host present on this machine: its CLI is on PATH,
// or its folder (Oxy's Claude Code mod, the Codex home) exists.
func detectPetHosts() []string {
	var hosts []string
	if in := newPetInstaller(); in.Claude != nil || pathExists(in.Dir) {
		hosts = append(hosts, petHostClaude)
	}
	if _, err := exec.LookPath("codex"); err == nil || pathExists(newCodexPetInstaller().Home) {
		hosts = append(hosts, petHostCodex)
	}
	return hosts
}

func pathExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return !errors.Is(err, fs.ErrNotExist)
}

func asJSON(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

func printSteps(w io.Writer, intro string, steps []string) {
	fmt.Fprintln(w, intro)
	for _, s := range steps {
		fmt.Fprintf(w, "  %s\n", s)
	}
}

func init() {
	for _, c := range []*cobra.Command{petEnableCmd, petDisableCmd, petStatusCmd} {
		c.Flags().StringSlice("host", nil, "host to act on: claude-code or codex (repeatable; default: every host found)")
	}
	petCmd.AddCommand(petEnableCmd, petDisableCmd, petStatusCmd)
	rootCmd.AddCommand(petCmd)
}
