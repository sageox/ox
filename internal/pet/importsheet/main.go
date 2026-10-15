// Command importsheet turns Oxy art into the assets `ox pet` embeds.
//
// From one still picture of Oxy (every animation shows it until an animated
// atlas exists):
//
//	go run ./internal/pet/importsheet -base base.png
//
// From an animation sheet (one animation per row) and its manifest, shrunk
// when it is drawn larger than the thinking line can show:
//
//	go run ./internal/pet/importsheet -png sheet.png -json sheet.json -shrink 2
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/pet"
)

func main() {
	os.Exit(cli(os.Args[1:], os.Stdout, os.Stderr))
}

// cli parses the flags and runs the import, returning the exit code.
func cli(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("importsheet", flag.ContinueOnError)
	fs.SetOutput(stderr)
	basePath := fs.String("base", "", "one picture of Oxy (PNG with transparency) shown for every animation")
	height := fs.Int("height", 512, "with -base: height of the embedded frame in pixels")
	pngPath := fs.String("png", "", "source sprite sheet (PNG)")
	jsonPath := fs.String("json", "", "source sheet manifest (JSON)")
	shrinkBy := fs.Int("shrink", 1, "with -png: shrink the sheet by this whole factor")
	out := fs.String("out", "internal/pet/assets/oxy/pixel", "assets folder, run from the repo root")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := run(*basePath, *height, *pngPath, *jsonPath, *shrinkBy, *out); err != nil {
		fmt.Fprintln(stderr, "importsheet:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s\n", *out)
	return 0
}

func run(basePath string, height int, pngPath, jsonPath string, shrinkBy int, out string) error {
	var outPNG, outJSON []byte
	switch {
	case basePath != "":
		src, err := os.ReadFile(basePath)
		if err != nil {
			return err
		}
		if outPNG, outJSON, err = pet.ImportBase(src, pet.Animations, height); err != nil {
			return err
		}
	case pngPath != "" && jsonPath != "":
		sheetPNG, err := os.ReadFile(pngPath)
		if err != nil {
			return err
		}
		sheetJSON, err := os.ReadFile(jsonPath)
		if err != nil {
			return err
		}
		if outPNG, outJSON, err = pet.ImportSheet(sheetPNG, sheetJSON, shrinkBy); err != nil {
			return err
		}
	default:
		return fmt.Errorf("give -base, or -png with -json")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "sheet.png"), outPNG, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "sheet.json"), outJSON, 0o644); err != nil {
		return err
	}
	return nil
}
