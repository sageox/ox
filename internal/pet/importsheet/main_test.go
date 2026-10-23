package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/pet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRun_WritesTheEmbeddedSheet verifies both import modes land a sheet ox
// can render, in the assets folder, and that a bad call writes nothing.
// Failure prevented: a re-import of new art leaves a sheet `ox pet enable`
// can't cut, which only shows up when a coworker enables Oxy.
func TestRun_WritesTheEmbeddedSheet(t *testing.T) {
	src := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 8))
	for y := 2; y < 8; y++ {
		img.SetNRGBA(1, y, color.NRGBA{R: 0xff, A: 0xff})
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	pngPath := filepath.Join(src, "art.png")
	require.NoError(t, os.WriteFile(pngPath, buf.Bytes(), 0o644))
	jsonPath := filepath.Join(src, "art.json")
	require.NoError(t, os.WriteFile(jsonPath, []byte(`{"cell": 4, "cellHeight": 8, "columns": 1, "animations": [{"name": "blink", "row": 0, "frames": 1, "ms": [100]}]}`), 0o644))

	tests := []struct {
		name            string
		base, png, json string
		height, shrink  int
		wantAnims       int
	}{
		{name: "base picture", base: pngPath, height: 4, shrink: 1, wantAnims: len(pet.Animations)},
		{name: "animation sheet", png: pngPath, json: jsonPath, height: 512, shrink: 2, wantAnims: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "pixel")
			require.NoError(t, run(tt.base, tt.height, tt.png, tt.json, tt.shrink, out))
			sheet, err := os.ReadFile(filepath.Join(out, "sheet.png"))
			require.NoError(t, err)
			decoded, err := png.Decode(bytes.NewReader(sheet))
			require.NoError(t, err)
			var m pet.Manifest
			data, err := os.ReadFile(filepath.Join(out, "sheet.json"))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &m))
			assert.Len(t, m.Animations, tt.wantAnims)
			_, err = pet.BuildFrames(decoded, m)
			assert.NoError(t, err, "the imported sheet must render")
		})
	}

	for name, call := range map[string]func(out string) error{
		"no mode":         func(out string) error { return run("", 512, "", "", 1, out) },
		"missing picture": func(out string) error { return run(filepath.Join(src, "nope.png"), 512, "", "", 1, out) },
		"missing sheet":   func(out string) error { return run("", 512, filepath.Join(src, "nope.png"), jsonPath, 1, out) },
		"missing manifest": func(out string) error {
			return run("", 512, pngPath, filepath.Join(src, "nope.json"), 1, out)
		},
		"bad shrink": func(out string) error { return run("", 512, pngPath, jsonPath, 3, out) },
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "pixel")
			assert.Error(t, call(out))
			assert.NoFileExists(t, filepath.Join(out, "sheet.png"))
		})
	}
}

// TestCLI_ExitCodes verifies the command-line wrapper: success writes the
// sheet, a bad import exits 1 with the reason, a bad flag exits 2.
func TestCLI_ExitCodes(t *testing.T) {
	src := filepath.Join(t.TempDir(), "art.png")
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	require.NoError(t, os.WriteFile(src, buf.Bytes(), 0o644))
	out := filepath.Join(t.TempDir(), "pixel")

	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, cli([]string{"-base", src, "-height", "4", "-out", out}, &stdout, &stderr), stderr.String())
	assert.FileExists(t, filepath.Join(out, "sheet.png"))
	assert.Contains(t, stdout.String(), "wrote")

	stderr.Reset()
	assert.Equal(t, 1, cli([]string{"-out", out}, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "importsheet:")
	assert.Equal(t, 2, cli([]string{"-nope"}, &stdout, &stderr))
}

// TestRun_UnwritableOutputFails verifies an output path ox can't create is an
// error, not a silent no-op.
func TestRun_UnwritableOutputFails(t *testing.T) {
	src := filepath.Join(t.TempDir(), "art.png")
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	require.NoError(t, os.WriteFile(src, buf.Bytes(), 0o644))
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	assert.Error(t, run(src, 4, "", "", 1, filepath.Join(blocker, "pixel")))
	// a directory where sheet.png should go
	out := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(out, "sheet.png"), 0o755))
	assert.Error(t, run(src, 4, "", "", 1, out))
}
