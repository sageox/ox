package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/theme"
	"github.com/sageox/ox/internal/uicatalog"

	// register all catalog entries via their init() functions
	_ "github.com/sageox/ox/internal/uicatalog/entries"

	"github.com/sageox/ox/internal/version"
)

var (
	catalogJSONOut   bool
	catalogComponent string
	catalogExportDir string
	catalogPlayerJS  string
	catalogPlayerCSS string
	catalogAssetsDir string
)

var devCatalogCmd = &cobra.Command{
	Use:    "catalog",
	Short:  "Render the ox UX component catalog (hidden)",
	Hidden: true,
	Long: `Render every reusable ox UX component in the terminal.

This is a maintainer-only command — it doesn't appear in 'ox --help'.
The published browser version lives at:
  https://sageox-design.netlify.app/catalog/cli/

See docs/design/README.md and .claude/rules/design.md.

Modes:
  ox dev catalog                       render every component in this terminal
  ox dev catalog --component=timeline  render just one
  ox dev catalog --json                machine-readable manifest
  ox dev catalog --export=DIR          write self-contained catalog/cli/index.html
                                       (single file; per the sageox-design
                                       catalog/ contract — see catalog/README.md)`,
	RunE: runDevCatalog,
}

func init() {
	devCatalogCmd.Flags().BoolVar(&catalogJSONOut, "json", false, "emit JSON manifest")
	devCatalogCmd.Flags().StringVar(&catalogComponent, "component", "", "render only the named component")
	devCatalogCmd.Flags().StringVar(&catalogExportDir, "export", "", "write self-contained HTML catalog to DIR/cli/index.html")
	devCatalogCmd.Flags().StringVar(&catalogPlayerJS, "player-js", "", "path to vendored asciinema-player JS (inlined)")
	devCatalogCmd.Flags().StringVar(&catalogPlayerCSS, "player-css", "", "path to vendored asciinema-player CSS (inlined)")
	devCatalogCmd.Flags().StringVar(&catalogAssetsDir, "assets-dir", "", "directory containing built .svg + .cast assets to inline")
	devCmd.AddCommand(devCatalogCmd)
}

func runDevCatalog(cmd *cobra.Command, args []string) error {
	switch {
	case catalogExportDir != "":
		return runCatalogExport()
	case catalogJSONOut:
		return runCatalogJSON()
	default:
		return runCatalogTerminal()
	}
}

func runCatalogTerminal() error {
	entries := uicatalog.All()
	if catalogComponent != "" {
		e, ok := uicatalog.Get(catalogComponent)
		if !ok {
			return fmt.Errorf("no component named %q (try `ox dev catalog --json | jq '.[].name'`)", catalogComponent)
		}
		entries = []uicatalog.Entry{e}
	}

	for _, e := range entries {
		printCatalogHeader(e)
		fmt.Println(e.Render())
		fmt.Println()
	}
	return nil
}

func printCatalogHeader(e uicatalog.Entry) {
	bar := strings.Repeat("─", 60)
	fmt.Println(cli.StyleDim.Render(bar))
	title := cli.StyleBrand.Render(e.Name)
	fam := cli.StyleDim.Render("[" + string(e.Family) + "]")
	pkg := cli.StyleAccent.Render(e.Package)
	fmt.Printf("%s  %s  %s\n", title, fam, pkg)
	if e.WhenToUse != "" {
		for _, line := range wrapWords(e.WhenToUse, 72) {
			fmt.Println(cli.StyleDim.Render("  use: " + line))
		}
	}
	fmt.Println(cli.StyleDim.Render(bar))
}

// wrapWords splits s into lines of at most width runes, breaking only on
// whitespace. Lines wider than width without a break stay intact (better a
// long line than a mangled word).
func wrapWords(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		// rune count, not byte count
		if len([]rune(cur))+1+len([]rune(w)) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	lines = append(lines, cur)
	return lines
}

type catalogJSONEntry struct {
	Name      string   `json:"name"`
	Family    string   `json:"family"`
	Package   string   `json:"package"`
	Exports   []string `json:"exports"`
	Since     string   `json:"since"`
	Renderer  string   `json:"renderer"`
	WhenToUse string   `json:"when_to_use"`
	WhenNotTo string   `json:"when_not_to"`
}

func runCatalogJSON() error {
	entries := uicatalog.All()
	out := make([]catalogJSONEntry, 0, len(entries))
	for _, e := range entries {
		r := string(e.Renderer)
		if r == "" {
			r = string(uicatalog.RendererFreeze)
		}
		out = append(out, catalogJSONEntry{
			Name:      e.Name,
			Family:    string(e.Family),
			Package:   e.Package,
			Exports:   e.Exports,
			Since:     e.Since,
			Renderer:  r,
			WhenToUse: e.WhenToUse,
			WhenNotTo: e.WhenNotTo,
		})
	}
	type tokenJSON struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Light    string `json:"light"`
		Dark     string `json:"dark"`
		Use      string `json:"use"`
	}
	tokens := make([]tokenJSON, 0, len(theme.Tokens))
	for _, t := range theme.Tokens {
		tokens = append(tokens, tokenJSON{
			Name:     t.Name,
			Category: string(t.Category),
			Light:    t.LightHex,
			Dark:     t.DarkHex,
			Use:      t.UseCase,
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"surface":    "cli",
		"version":    version.Version,
		"components": out,
		"tokens":     tokens,
	})
}

func runCatalogExport() error {
	// Annotate each entry with its expected asset paths so the HTML
	// builder can inline them. The Makefile target is responsible for
	// running freeze + asciinema upstream and dropping the files at
	// these relative paths.
	for _, e := range uicatalog.All() {
		// SVGPath / CastPath are set on the registered copy; mutate
		// via re-Register would be too invasive, so the HTML builder
		// derives paths from the name when these fields are empty.
		_ = e
	}

	outDir := filepath.Join(catalogExportDir, "cli")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create export dir: %w", err)
	}

	f, err := os.Create(filepath.Join(outDir, "index.html"))
	if err != nil {
		return fmt.Errorf("create index.html: %w", err)
	}
	defer f.Close()

	opts := uicatalog.HTMLExportOpts{
		AssetsDir:     catalogAssetsDir,
		PlayerJSPath:  catalogPlayerJS,
		PlayerCSSPath: catalogPlayerCSS,
		Version:       version.Version,
	}

	if err := uicatalog.ExportHTML(f, opts); err != nil {
		return fmt.Errorf("export html: %w", err)
	}

	fmt.Fprintf(os.Stderr, "wrote %s\n", filepath.Join(outDir, "index.html"))
	return nil
}
