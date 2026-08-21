package prheader

import (
	"errors"
	"fmt"
)

// ErrUploadUnavailable is returned by an Uploader that cannot host an asset in
// the current environment (no credentials, no configured endpoint). The command
// treats it as the signal to fall back to the Tier-A text whisper — the feature
// must never fail just because the premium image path is unreachable.
var ErrUploadUnavailable = errors.New("prheader: image upload unavailable")

// Uploader hosts a strip SVG at a public, camo-fetchable URL and returns it.
// It is the single I/O seam of the Tier-B path; keeping it an interface lets the
// pure renderer + the command be tested with a fake, and lets the real
// CDN/cloud-image implementation land later without touching either.
//
// name is a content-addressed base (see stripContentName) plus a theme + ".svg"
// suffix; svg is the bytes from RenderStripSVG.
type Uploader interface {
	Upload(name string, svg []byte) (url string, err error)
}

// UploadStrip renders both theme variants and uploads them, returning the
// StripURLs for Render's Tier B. A nil uploader, or any upload error (including
// ErrUploadUnavailable), yields (nil, err) so the caller falls back to Tier A.
// Pure except for the injected Uploader's I/O.
func UploadStrip(u Uploader, s Signals) (*StripURLs, error) {
	if u == nil {
		return nil, ErrUploadUnavailable
	}
	base := stripContentName(s)
	light, err := RenderStripSVG(s, "light")
	if err != nil {
		return nil, fmt.Errorf("prheader: render light strip: %w", err)
	}
	dark, err := RenderStripSVG(s, "dark")
	if err != nil {
		return nil, fmt.Errorf("prheader: render dark strip: %w", err)
	}
	lightURL, err := u.Upload(base+"-light.svg", light)
	if err != nil {
		return nil, fmt.Errorf("prheader: upload light strip: %w", err)
	}
	darkURL, err := u.Upload(base+"-dark.svg", dark)
	if err != nil {
		return nil, fmt.Errorf("prheader: upload dark strip: %w", err)
	}
	return &StripURLs{Light: lightURL, Dark: darkURL}, nil
}
