//go:build agents

package agents_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/teamrules"
)

// corpusGlobs are the constructs a rule's glob can use, each next to the near misses
// that decide how an author has to write it. Rows go through real Reconcile, so they say
// what a rule written this way does.
var corpusGlobs = []struct{ glob, construct string }{
	{"site/app/(shop)/**/*.{tsx,css}", "route-group"},
	{"site/app/(account)/**/*.{tsx,css}", "route-group"},
	{"site/app/(shop)/settings/page.tsx", "route-group"},
	{"site/app/shop/**/*.tsx", "route-group"},
	{`site/app/\(shop\)/settings/page.tsx`, "route-group"},
	{"site/app/*/settings/page.tsx", "route-group"},
	{"site/app/(shop|account)/**", "route-group"},
	{"site/app/@(shop)/**", "route-group"},

	{"site/app/orders/[order_id]/page.tsx", "bracket"},
	{"site/app/orders/[order_id]/**", "bracket"},
	{"site/app/(account)/orders/[order_id]/**", "bracket"},
	{`site/app/orders/\[order_id\]/page.tsx`, "bracket"},
	{"site/app/orders/[[]order_id[]]/page.tsx", "bracket"},
	{"site/app/orders/*/page.tsx", "bracket"},
	{"site/app/orders/**/page.tsx", "bracket"},
	{"site/app/orders/[o]/page.tsx", "bracket"},
	{"site/app/orders/?/page.tsx", "bracket"},
	{"**/[order_id]/**", "bracket"},
	{`**/\[order_id\]/**`, "bracket"},

	{"site/blog/{app,src}/**/*.{tsx,css}", "brace"},
	{"**/*.{tsx,css}", "brace"},
	{"site/{blog}/app/page.tsx", "brace"},
	{"site/{api,blog/{app,src}}/**/*.{go,tsx,css}", "brace"},
	{"site/blog/{app/**/*.tsx,src/**/*.css}", "brace"},
	{"site/src/components/**/*.{tsx,css}", "brace"},
	{"lib/ui/**/*.{tsx,css}", "brace"},

	{"**/page.tsx", "double-star"},
	{"site/**/page.tsx", "double-star"},
	{"site/**", "double-star"},
	{"site/app/kiosk/**", "double-star"},
	{"*.tsx", "single-star"},
	{"*.{tsx,css}", "single-star"},
	{"site/*.tsx", "single-star"},
	{"page.tsx", "literal"},
	{"**", "catch-all"},
	{"**/*", "catch-all"},
	{"*", "catch-all"},

	// Whether a pattern is tied to the repository root, or floats to any directory.
	{"ui/**", "anchoring"},
	{"components/**", "anchoring"},
	{"{ui,components}/**", "anchoring"},
	{"src/components/**", "anchoring"},
	{"app/page.tsx", "anchoring"},
	{"blog/app/page.tsx", "anchoring"},
	{"site/app/kiosk/layout.tsx", "anchoring"},
	{"/site/api/main.go", "anchoring"},
	{"./site/api/main.go", "anchoring"},
	{"!site/api/**", "anchoring"},

	{"**/*.yml", "dotfile"},
	{".github/**", "dotfile"},
	{".hidden/**", "dotfile"},
	{"**/.hidden/**", "dotfile"},
	{"**/*.tsx", "dotfile"},
	{"**/.config.tsx", "dotfile"},

	{"site/api/**/*.go", "negative"},
}

// asWrittenGlobs skip Reconcile and reach Claude Code with their braces intact. ox expands
// braces before Claude sees them, so these rows are the record of why: compare one with
// the same glob's row in corpusGlobs.
var asWrittenGlobs = []string{
	"site/{api,blog/{app,src}}/**/*.{go,tsx,css}",
	"site/blog/{app,src}/**/*.{tsx,css}",
	"site/blog/{app/**/*.tsx,src/**/*.css}",
	"{ui,components}/**",
	"*.{tsx,css}",
	"**/*.{tsx,css}",
}

const asWrittenConstruct = "brace-as-written"

// corpusPaths are generic paths in the shape of a Next app. Each one is read in its
// own session, so what loads can only be due to that read.
var corpusPaths = []string{
	"page.tsx",
	"root.css",
	"site/page.tsx",
	"site/x/y/page.tsx",
	"site/api/main.go",
	"site/blog/app/page.tsx",
	"site/blog/src/hero.css",
	"site/blog/other/page.tsx",
	"site/app/(shop)/settings/page.tsx",
	"site/app/(shop)/styles.css",
	"site/app/shop/settings/page.tsx",
	"site/app/(account)/orders/[order_id]/page.tsx",
	"site/app/orders/[order_id]/page.tsx",
	"site/app/orders/o/page.tsx",
	"site/app/orders/order_id/page.tsx",
	"site/app/kiosk/layout.tsx",
	"site/src/components/button.tsx",
	"lib/ui/button.tsx",
	"lib/ui/styles/tokens.css",
	".hidden/page.tsx",
	"site/.hidden/page.tsx",
	".github/workflows/ci.yml",
	".config.tsx",
	"site/.eslintrc.tsx",
	"README.md",
	"site/app/orders/[order_id]/nested/deep.tsx",
	"x/site/app/kiosk/layout.tsx",
	"x/lib/ui/button.tsx",
}

const globCorpusFile = "testdata/glob-conformance.json"

// maxConcurrentCorpusSessions bounds the fan-out. Each row needs a real session, and
// this many at once stays well under an account's rate limits while keeping a full
// derivation to a couple of minutes.
const maxConcurrentCorpusSessions = 6

// TestGlobCorpus_MatchesRealClaude re-derives every row from real Claude Code sessions
// and compares it with the committed corpus. Our matchers are held to the corpus, and
// this is what holds the corpus to the agent.
//
// OX_REDERIVE_GLOB_CORPUS=1 rewrites the corpus instead of comparing. Do that on a Claude
// Code upgrade, then read the diff: a changed row is a change in what authors can write.
// OX_GLOB_CORPUS_COPY names a second place to write it.
func TestGlobCorpus_MatchesRealClaude(t *testing.T) {
	claude := requireLiveAgent(t, "claude")
	version := toolVersion(t, claude)
	t.Logf("agent under test: %s", version)

	repo, rules := newGlobCorpusRepo(t)
	control := rules[len(rules)-1]
	measured := rules[:len(rules)-1]

	// Keyed by rule, not glob: one glob can be measured both through ox and as written.
	var (
		mu       sync.Mutex
		observed = map[string]map[string]bool{}
		atStart  = map[string]bool{}
		slots    = make(chan struct{}, maxConcurrentCorpusSessions)
	)
	t.Run("sessions", func(t *testing.T) {
		for _, path := range corpusPaths {
			t.Run(path, func(t *testing.T) {
				t.Parallel()
				slots <- struct{}{}
				defer func() { <-slots }()

				// One read per turn in this order is what attribution below relies on,
				// and claudeSession guarantees it.
				transcript := claudeSession(t, claude, repo, path, "control/beacon.txt")
				if want := filepath.Join(repo, filepath.FromSlash(path)); transcript.reads[0] != want {
					invalidRun(t, "the session read %v, not %s first", transcript.reads, want)
				}
				if got := transcript.attachmentsHolding(control.marker); len(got) != 1 {
					invalidRun(t, "the control rule loaded %d times reading %s, want once", len(got), path)
				}
				mu.Lock()
				defer mu.Unlock()
				observed[path] = map[string]bool{}
				for _, rule := range measured {
					for _, loaded := range transcript.attachmentsHolding(rule.marker) {
						// Attachments after the second read belong to the control file.
						if loaded.atStart || loaded.reads < 2 {
							observed[path][rule.marker] = true
						}
						if loaded.atStart {
							atStart[rule.marker] = true
						}
					}
				}
			})
		}
	})
	if t.Failed() {
		invalidRun(t, "a session could not measure, so no row is trustworthy")
	}

	derived := globCorpus{
		ClaudeVersion: version,
		Note:          "Each row is one real Claude Code session: a rule scoped to glob, and a Read of path. A rule goes through ox unless as_written says it reached Claude untouched. Regenerate with OX_REDERIVE_GLOB_CORPUS=1.",
	}
	for _, rule := range measured {
		for _, path := range corpusPaths {
			derived.Rows = append(derived.Rows, globCorpusRow{
				Glob: rule.glob, Path: path, Loads: observed[path][rule.marker], Construct: rule.construct,
				AsWritten: rule.asWritten, AtSessionStart: atStart[rule.marker],
			})
		}
	}

	if os.Getenv("OX_REDERIVE_GLOB_CORPUS") == "1" {
		data, err := json.MarshalIndent(derived, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, '\n')
		for _, target := range []string{globCorpusFile, os.Getenv("OX_GLOB_CORPUS_COPY")} {
			if target == "" {
				continue
			}
			if err := os.WriteFile(target, data, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %d rows to %s", len(derived.Rows), target)
		}
		return
	}

	committed := readGlobCorpus(t, globCorpusFile)
	if committed.ClaudeVersion != version {
		t.Logf("the corpus was derived on %s and this is %s; a difference below means re-baseline it", committed.ClaudeVersion, version)
	}
	type rowKey struct {
		glob, path string
		asWritten  bool
	}
	got := make(map[rowKey]bool, len(derived.Rows))
	for _, row := range derived.Rows {
		got[rowKey{row.Glob, row.Path, row.AsWritten}] = row.Loads
	}
	for _, row := range committed.Rows {
		loads, known := got[rowKey{row.Glob, row.Path, row.AsWritten}]
		if !known {
			t.Errorf("corpus row %q on %q was not measured", row.Glob, row.Path)
			continue
		}
		if loads != row.Loads {
			t.Errorf("corpus disagrees with real Claude: glob %q on path %q: corpus says loads=%v, Claude loads=%v", row.Glob, row.Path, row.Loads, loads)
		}
	}
	if len(committed.Rows) != len(derived.Rows) {
		t.Errorf("the corpus has %d rows and this run measured %d", len(committed.Rows), len(derived.Rows))
	}
}

type corpusRule struct {
	glob      string
	construct string
	asWritten bool
	marker    string
}

// newGlobCorpusRepo projects one rule per glob through real Reconcile, so the frontmatter
// Claude reads is byte for byte what ox writes, then adds the as-written rules beside
// them. The control comes last.
func newGlobCorpusRepo(t *testing.T) (string, []corpusRule) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	for _, scoped := range skillmanager.ScopedIgnoreFiles() {
		if scoped.Dir == ".claude" {
			writeFixtureFile(t, repo, ".claude/.gitignore", strings.Join(scoped.Entries, "\n")+"\n")
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, ".claude", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range append(slices.Clone(corpusPaths), "control/beacon.txt") {
		writeFixtureFile(t, repo, path, "// fixture\n")
	}

	team := t.TempDir()
	var rules []corpusRule
	var teamRules []teamdocs.TeamRule
	add := func(index int, glob, construct string) {
		name := fmt.Sprintf("corpus-%02d", index)
		marker := uniqueMarker(t, fmt.Sprintf("GLOB%02d", index))
		source := filepath.Join(team, name+".md")
		if err := os.WriteFile(source, []byte("Rule for one glob. "+marker+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		rules = append(rules, corpusRule{glob: glob, construct: construct, marker: marker})
		teamRules = append(teamRules, teamdocs.TeamRule{
			Name: name, RelPath: name + ".md", AbsPath: source, Globs: []string{glob}, Visibility: teamdocs.VisibilityIndexed,
		})
	}
	for i, entry := range corpusGlobs {
		add(i, entry.glob, entry.construct)
	}
	for i, glob := range asWrittenGlobs {
		marker := uniqueMarker(t, fmt.Sprintf("RAW%02d", i))
		writeFixtureFile(t, repo, fmt.Sprintf(".claude/rules/as-written-%02d.md", i),
			"---\npaths:\n  - "+strconv.Quote(glob)+"\n---\n\nRule for one glob. "+marker+"\n")
		rules = append(rules, corpusRule{glob: glob, construct: asWrittenConstruct, asWritten: true, marker: marker})
	}
	add(len(corpusGlobs), "control/**", "control")

	result, err := teamrules.Reconcile(context.Background(), repo, teamRules)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(result.Written) != len(teamRules) {
		written := slices.Clone(result.Written)
		sort.Strings(written)
		t.Fatalf("Reconcile wrote %d projections, want %d: %v", len(written), len(teamRules), written)
	}
	return repo, rules
}
