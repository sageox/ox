package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Mockup expectation: enrich-side, precision-gated detection ---
//
// computeMockupExpectation is the cross-agent signal (surfaced in Result.Guidance)
// that a plan changes a user-facing surface. It must fire on a real UI plan and
// stay SILENT on backend/CLI plans that merely name-drop UI-adjacent words — the
// over-fire the old render-side cue bag caused.

// TestComputeMockupExpectation_HeadingWeightPrecision pins the precision lever: a
// surface noun in the HEADING fires (the agent titled the section that), a single
// incidental mention in body prose does not. Failure prevented: every plan that
// says "screen" once gets nagged for a mockup.
func TestComputeMockupExpectation_HeadingWeightPrecision(t *testing.T) {
	ui := Parse("# X\n\n## Onboarding screen\n\nThe setup flow.\n")
	if computeMockupExpectation(ui) == "" {
		t.Error("a section titled with a surface noun should expect a mockup")
	}
	incidental := Parse("# X\n\n## Backend\n\nLog a line when the screen refreshes.\n")
	if got := computeMockupExpectation(incidental); got != "" {
		t.Errorf("an incidental body mention must not fire, got %q", got)
	}
}

// TestComputeMockupExpectation_Fixtures runs the detector over real-shaped plans
// (golden fixtures distilled from the actual failure modes): a UI plan expects a
// mockup; a backend plan that name-drops "notification"/"component"/"banner" and a
// CLI/TUI plan do NOT. Failure prevented: the mockup nudge mis-fires on the
// backend/CLI plans that dominate an `ox` repo.
func TestComputeMockupExpectation_Fixtures(t *testing.T) {
	cases := map[string]bool{ // file -> expects a mockup section
		"ui-onboarding.md":        true,
		"backend-notification.md": false,
		"cli-status.md":           false,
	}
	for file, want := range cases {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "craft", file))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			got := computeMockupExpectation(Parse(string(raw)))
			switch {
			case want && got == "":
				t.Errorf("%s: expected a mockup expectation, got none", file)
			case !want && got != "":
				t.Errorf("%s: expected NO mockup expectation, got %q (over-fire)", file, got)
			}
		})
	}
}

// TestBuildGuidance_FoldsMockup verifies the mockup expectation reaches the
// cross-agent enrich payload (Guidance), since that — not the render lint — is the
// signal Codex/Gemini actually consume. Failure prevented: the surface is detected
// but never told to the agent before it authors.
func TestBuildGuidance_FoldsMockup(t *testing.T) {
	in := Parse("# X\n\n## Onboarding screen\n\nThe setup flow with a share sheet.\n")
	g := buildGuidance(in, SignalSummary{}, nil, nil, "Onboarding screen")
	if g == "" {
		t.Fatal("guidance empty for a non-empty plan")
	}
	if !strings.Contains(g, "device-mockup") || !strings.Contains(g, "Onboarding screen") {
		t.Errorf("guidance must name the surface and the mockup command, got: %q", g)
	}
}
