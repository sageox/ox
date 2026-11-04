//go:build agents

package agents_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// baselineTag is the last release whose Claude projection writes globs:, which Claude Code
// ignores, so a scoped rule loads in every session on any machine still running it.
const baselineTag = "v0.25.0"

const versionMatrixFile = "testdata/version-matrix.json"

// versionMeasurement is what one ox build does with one Team Context.
type versionMeasurement struct {
	// Projections holds the exact bytes written to the Claude root, by file name.
	Projections map[string]string `json:"projections"`
	// AlwaysLoadedScopedBytes is text from scoped rules that a real Claude Code session
	// reading one unrelated file received. It is what those rules cost a teammate on this
	// build, paid per session.
	AlwaysLoadedScopedBytes int `json:"always_loaded_scoped_bytes"`
	// PrimeScopedRuleLines counts scoped rules ox itself listed in prime for that session.
	PrimeScopedRuleLines int `json:"prime_scoped_rule_lines"`
}

type versionMatrix struct {
	ClaudeVersion string                        `json:"claude_version"`
	BaselineTag   string                        `json:"baseline_tag"`
	Builds        map[string]versionMeasurement `json:"builds"`
}

// matrixRules are fixed rather than random so the recorded bytes can be compared
// between runs. Each transcript is its own file, so a marker cannot leak across runs.
func matrixRules() (rules []teamRuleFixture, scoped []teamRuleFixture) {
	for _, entry := range []struct {
		name  string
		globs []string
	}{
		{"scoped-site", []string{"site/**/*.{tsx,css}", "lib/ui/**/*.{tsx,css}"}},
		{"scoped-pages", []string{"site/app/(shop)/**/*.{tsx,css}", "site/app/(account)/**/*.{tsx,css}"}},
		{"scoped-kiosk", []string{"site/app/kiosk/**"}},
	} {
		marker := "MARKER-MATRIX-" + strings.ToUpper(strings.TrimPrefix(entry.name, "scoped-"))
		rule := teamRuleFixture{
			name: entry.name, marker: marker, globs: entry.globs,
			body: scopedRuleBody(entry.name, marker),
		}
		rules = append(rules, rule)
		scoped = append(scoped, rule)
	}
	control := teamRuleFixture{name: "control-beacon", marker: "MARKER-MATRIX-CONTROL", globs: []string{"control/**"}}
	control.body = "Control rule. " + control.marker + "\n"
	return append(rules, control), scoped
}

// TestVersionMatrix_BackendCostAndConvergence runs the last release and this build over
// one Team Context, on real binaries, a real daemon and a real Claude Code session. Its
// numbers say what scoped rules cost a teammate who has not upgraded.
func TestVersionMatrix_BackendCostAndConvergence(t *testing.T) {
	claude := requireLiveAgent(t, "claude")
	version := toolVersion(t, claude)
	t.Logf("agent under test: %s", version)
	head := headOxBinary(t)
	baseline := buildTaggedOx(t, baselineTag)

	rules, scoped := matrixRules()
	control := rules[len(rules)-1]

	measured := versionMatrix{ClaudeVersion: version, BaselineTag: baselineTag, Builds: map[string]versionMeasurement{}}
	builds := []struct{ name, bin string }{{baselineTag, baseline}, {"head", head}}
	for _, build := range builds {
		t.Run("measure "+build.name, func(t *testing.T) {
			fx := newScopingFixture(t, newOxEnvironment(t), rules, []string{fileBackend, fileShopHome})
			fx.syncWith(t, build.bin)

			transcript := claudeSession(t, claude, fx.repo, fileBackend, fx.controlRel)
			if got := transcript.attachmentsHolding(control.marker); len(got) != 1 {
				invalidRun(t, "the control rule loaded %d times, want once; this run measured nothing", len(got))
			}
			prime, err := fx.env.run(t, fx.repo, build.bin, "agent", "prime", "--agent", "claude-code")
			if err != nil {
				t.Fatalf("ox agent prime: %v", err)
			}
			if !strings.Contains(prime, `status="fresh"`) {
				invalidRun(t, "prime did not reach team context on %s", build.name)
			}
			fx.env.stopDaemon(t, fx.repo, build.bin)

			measurement := versionMeasurement{Projections: map[string]string{}}
			for name, content := range fx.projections(t) {
				measurement.Projections[name] = string(content)
			}
			for _, rule := range scoped {
				for _, loaded := range transcript.attachmentsHolding(rule.marker) {
					measurement.AlwaysLoadedScopedBytes += len(loaded.text)
				}
				if teamRuleRow(prime, rule.name) != "" {
					measurement.PrimeScopedRuleLines++
				}
			}
			t.Logf("%s: %d scoped bytes on a backend read, %d scoped rows in prime", build.name, measurement.AlwaysLoadedScopedBytes, measurement.PrimeScopedRuleLines)
			measured.Builds[build.name] = measurement
		})
	}
	if t.Failed() {
		t.FailNow()
	}

	totalScopedBytes := 0
	for _, rule := range scoped {
		totalScopedBytes += len(claudeVisibleBody(rule.body))
	}
	if got := measured.Builds["head"].AlwaysLoadedScopedBytes; got != 0 {
		t.Errorf("this build costs a backend session %d scoped bytes, want 0", got)
	}
	if got := measured.Builds[baselineTag].AlwaysLoadedScopedBytes; got == 0 {
		t.Errorf("%s shows no backend cost, so this run cannot tell a leaking build from a fixed one", baselineTag)
	} else {
		t.Logf("%d scoped rules cost a teammate on %s %d bytes per session (bodies total %d)", len(scoped), baselineTag, got, totalScopedBytes)
	}

	if os.Getenv("OX_REDERIVE_VERSION_MATRIX") == "1" {
		data, err := json.MarshalIndent(measured, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(versionMatrixFile, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", versionMatrixFile)
	} else {
		data, err := os.ReadFile(versionMatrixFile)
		if err != nil {
			t.Fatalf("read %s: %v", versionMatrixFile, err)
		}
		var recorded versionMatrix
		if err := json.Unmarshal(data, &recorded); err != nil {
			t.Fatalf("parse %s: %v", versionMatrixFile, err)
		}
		for name, want := range recorded.Builds {
			got := measured.Builds[name]
			if !maps.Equal(got.Projections, want.Projections) {
				t.Errorf("%s wrote different projection bytes than recorded:\n got %q\nwant %q\nre-record with OX_REDERIVE_VERSION_MATRIX=1 only if the change is meant", name, got.Projections, want.Projections)
			}
			if got.AlwaysLoadedScopedBytes != want.AlwaysLoadedScopedBytes {
				t.Errorf("%s: %d scoped bytes on a backend read, recorded %d", name, got.AlwaysLoadedScopedBytes, want.AlwaysLoadedScopedBytes)
			}
		}
	}

	t.Run("alternating old and new on one checkout converges", func(t *testing.T) {
		fx := newScopingFixture(t, newOxEnvironment(t), rules, []string{fileBackend, fileShopHome})
		expect := map[string]map[string]string{
			baseline: measured.Builds[baselineTag].Projections,
			head:     measured.Builds["head"].Projections,
		}
		for step, bin := range []string{head, baseline, head, baseline, head} {
			fx.syncWith(t, bin)
			got := map[string]string{}
			for name, content := range fx.projections(t) {
				got[name] = string(content)
			}
			if !maps.Equal(got, expect[bin]) {
				t.Fatalf("step %d: this checkout did not converge on what %s writes on a fresh one:\n got %q\nwant %q", step, bin, got, expect[bin])
			}
			status := exec.Command("git", "status", "--porcelain", "--", ".claude")
			status.Dir = fx.repo
			out, err := status.Output()
			if err != nil {
				t.Fatal(err)
			}
			if len(bytes.TrimSpace(out)) != 0 {
				t.Fatalf("step %d: a reconcile left visible changes under .claude: %s", step, out)
			}
		}
		// A second run with nothing to do must not rewrite or duplicate anything.
		before := fx.projections(t)
		fx.syncWith(t, head)
		if after := fx.projections(t); !maps.EqualFunc(before, after, bytes.Equal) {
			t.Fatalf("a run with nothing to do changed the projections: %v", slices.Collect(maps.Keys(after)))
		}
	})
}

// buildTaggedOx builds ox from a tag's exact source. It reads the tag out of git rather
// than checking it out, so it cannot disturb this checkout or any other worktree.
func buildTaggedOx(t *testing.T, tag string) string {
	t.Helper()
	root := repoRoot(t)
	if err := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", tag+"^{commit}").Run(); err != nil {
		t.Fatalf("tag %s is not in this clone, so the version matrix cannot run; fetch tags first: %v", tag, err)
	}
	source := t.TempDir()
	archive := exec.Command("sh", "-c", fmt.Sprintf("git -C %q archive %q | tar -x -C %q", root, tag, source))
	if out, err := archive.CombinedOutput(); err != nil {
		t.Fatalf("extract %s: %v\n%s", tag, err, out)
	}
	bin := filepath.Join(t.TempDir(), "ox-"+tag)
	t.Cleanup(func() { stopProcessesOf(bin) })
	// -trimpath keeps the build cache valid across temp directories.
	build := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/ox")
	build.Dir = source
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", tag, err, out)
	}
	return bin
}
