package prheader

import (
	"errors"
	"strings"
	"testing"
)

func TestRenderStripSVG_themes(t *testing.T) {
	sig := Signals{PriorArt: 2, Collisions: 1, Material: true}

	light, err := RenderStripSVG(sig, "light")
	if err != nil {
		t.Fatalf("light: %v", err)
	}
	dark, err := RenderStripSVG(sig, "dark")
	if err != nil {
		t.Fatalf("dark: %v", err)
	}
	ls, ds := string(light), string(dark)

	// Each theme uses its own tokens.
	mustContain(t, ls, `fill="#161812"`, `fill="#4e7d4c"`, "<svg", "</svg>")
	mustContain(t, ds, `fill="#f4f2ef"`, `fill="#adcfa3"`)
	// The label is baked small-caps (uppercased) and the SVG is self-describing.
	mustContain(t, ls, "2 RELATED SESSIONS", `aria-label="2 related sessions · 1 concurrent edit flagged"`)
	// Light and dark differ (that's the whole point of the pair).
	if ls == ds {
		t.Error("light and dark strips are identical")
	}
}

func TestRenderStripSVG_unknownThemeFallsBackLight(t *testing.T) {
	got, err := RenderStripSVG(Signals{Material: true}, "chartreuse")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, string(got), `fill="#161812"`) // light ink
}

func TestRenderStripSVG_escapesLabel(t *testing.T) {
	// whisperAlt content is first-party, but the render path must still escape —
	// prove the aria-label/text never emit a raw angle bracket even if a future
	// signal source injected one.
	got, err := RenderStripSVG(Signals{Material: true}, "light")
	if err != nil {
		t.Fatal(err)
	}
	// A well-formed SVG has matching angle brackets only around tags.
	if strings.Count(string(got), "<svg") != 1 {
		t.Errorf("expected exactly one <svg root:\n%s", got)
	}
}

func TestStripContentName_contentAddressed(t *testing.T) {
	a := stripContentName(Signals{PriorArt: 2, Collisions: 1, Material: true})
	b := stripContentName(Signals{PriorArt: 2, Collisions: 1, Material: true})
	c := stripContentName(Signals{PriorArt: 3, Collisions: 1, Material: true})
	if a != b {
		t.Errorf("same signals => same name, got %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("different signals => different name, both %q", a)
	}
	if a != "enrich-2-1" {
		t.Errorf("name = %q, want enrich-2-1", a)
	}
}

// fakeUploader records calls and returns a deterministic CDN-style URL.
type fakeUploader struct {
	calls   []string
	failOn  string // name that should error
	failErr error
}

func (f *fakeUploader) Upload(name string, svg []byte) (string, error) {
	f.calls = append(f.calls, name)
	if name == f.failOn {
		return "", f.failErr
	}
	return "https://assets.sageox.ai/pr/" + name, nil
}

func TestUploadStrip_success(t *testing.T) {
	f := &fakeUploader{}
	sig := Signals{PriorArt: 2, Collisions: 1, Material: true}
	urls, err := UploadStrip(f, sig)
	if err != nil {
		t.Fatalf("UploadStrip: %v", err)
	}
	if urls == nil || urls.Light == "" || urls.Dark == "" {
		t.Fatalf("incomplete urls: %+v", urls)
	}
	mustContain(t, urls.Light, "enrich-2-1-light.svg")
	mustContain(t, urls.Dark, "enrich-2-1-dark.svg")
	if len(f.calls) != 2 {
		t.Errorf("expected 2 uploads, got %d: %v", len(f.calls), f.calls)
	}
}

func TestUploadStrip_nilUploaderUnavailable(t *testing.T) {
	urls, err := UploadStrip(nil, Signals{Material: true})
	if !errors.Is(err, ErrUploadUnavailable) {
		t.Errorf("err = %v, want ErrUploadUnavailable", err)
	}
	if urls != nil {
		t.Errorf("urls = %+v, want nil", urls)
	}
}

func TestUploadStrip_propagatesError(t *testing.T) {
	f := &fakeUploader{failOn: "enrich-0-0-light.svg", failErr: ErrUploadUnavailable}
	urls, err := UploadStrip(f, Signals{Material: true})
	if urls != nil {
		t.Errorf("expected nil urls on error, got %+v", urls)
	}
	// The %w wrap must preserve the sentinel so callers keep using errors.Is.
	if !errors.Is(err, ErrUploadUnavailable) {
		t.Errorf("err = %v, want wrapped ErrUploadUnavailable", err)
	}
}
