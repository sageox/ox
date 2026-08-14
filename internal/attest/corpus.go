// Package attest reads a project's Attest corpus and answers what it can
// actually demonstrate.
//
// The unit of proof is the CAPABILITY, not the test — a falsifiable claim about
// customer-visible behavior. In a Gherkin corpus that unit already exists and is
// already authored: the `Rule:` block. Nothing here invents a taxonomy, adds a
// tag, or asks anyone to annotate their features.
//
// Everything in this package reads committed files with the Go standard library.
// It never shells out to Node, never needs `@sageox/attest` installed, and never
// touches the network — so "what is proven here?" stays answerable offline, in
// CI, and in a repo that has never been connected to SageOx. Anything that
// RE-PROVES a capability is the attest runner's job, not ours.
package attest

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultCorpusDir is where an Attest project keeps its corpus unless told
// otherwise. Convention over configuration: deliberately NOT parsed out of
// `attest.server.ts`, because carrying a TypeScript reader just to locate a
// directory is a poor trade. `--corpus` is the escape hatch.
const DefaultCorpusDir = "tests/acceptance"

// featuresSubdir is the corpus-relative directory holding the Gherkin sources.
const featuresSubdir = "features"

// Capability is one `Rule:` block — a claim a customer would recognize.
type Capability struct {
	// ID is `<domain>/<feature>#<rule-slug>`, stable across scenario churn and
	// across renaming a scenario. Derived, never authored.
	ID string `json:"id"`
	// Domain is the directory under features/ ("sharing", "team-management").
	// Empty for a feature file sitting directly in features/.
	Domain string `json:"domain"`
	// Feature is the .feature basename without its extension.
	Feature string `json:"feature"`
	// Rule is the `Rule:` text verbatim. Empty for the synthetic capability that
	// holds scenarios written above any Rule — see scanFile.
	Rule string `json:"rule"`
	// Path is repo-relative, so it is clickable in an editor and stable in JSON.
	Path string `json:"path"`
	// Line is the 1-indexed line of the `Rule:` keyword (0 when synthetic).
	Line      int        `json:"line"`
	Scenarios []Scenario `json:"scenarios"`
}

// Title is what a human should read for this capability: the Rule text, or the
// feature name when the capability is the synthetic ungrouped one.
func (c Capability) Title() string {
	if c.Rule != "" {
		return c.Rule
	}
	return c.Feature + " (no Rule: block)"
}

// Scenario is one `Scenario:` / `Scenario Outline:` / `Example:` under a Rule.
type Scenario struct {
	Name string `json:"name"`
	Line int    `json:"line"`
	// Tags is the merged set: feature ∪ rule ∪ scenario, which is the set the
	// Attest compiler applies when deciding whether a scenario dispatches.
	Tags []string `json:"tags"`
	// Validated reports the `@validated` TAG, which is the claim itself.
	//
	// Counted only from real tag lines, never from prose. A naive
	// `grep -rn '@validated'` over the SageOx corpus returns 33 where the truth
	// is 26, because seven of those hits are the string appearing inside
	// explanatory comments ("nothing here is tagged @validated"). Over-counting
	// the claim is the one direction this tool must never err in.
	Validated bool `json:"validated"`
	// Stamp is the `# validated:` provenance comment, when present.
	//
	// Validated && Stamp == nil is a real and materially worse state: someone
	// asserted green with no date and no run id, so the claim cannot even be
	// aged, let alone checked. It is reported separately rather than folded in.
	Stamp *Stamp `json:"stamp,omitempty"`
}

// HasTag reports whether the scenario carries tag, matched by EXACT equality.
//
// Exact, never prefix: `@pending-migration` is NOT `@pending` and DOES dispatch.
// Prefix-matching here would silently reclassify a whole family of scenarios as
// switched-off, which is the specific mis-report the coverage doc calls worse
// than under-counting.
func (s Scenario) HasTag(tag string) bool {
	for _, t := range s.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Corpus is everything one repo's Attest corpus declares.
type Corpus struct {
	// Root is the absolute corpus directory that was scanned.
	Root         string       `json:"root"`
	Capabilities []Capability `json:"capabilities"`
	// Files is the count of .feature files walked — reported so a caller can
	// tell "no capabilities because the corpus is empty" from "because the path
	// was wrong".
	Files int `json:"files"`
}

// Domains returns the distinct domains present, sorted.
func (c Corpus) Domains() []string {
	seen := map[string]struct{}{}
	for _, cap := range c.Capabilities {
		seen[cap.Domain] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// Gherkin keyword matchers. Anchored and colon-terminated so `Examples:` (an
// Outline's data table) can never be mistaken for `Example:` (a scenario).
var (
	reTagLine  = regexp.MustCompile(`^\s*@\S`)
	reFeature  = regexp.MustCompile(`^\s*Feature:\s*(.*)$`)
	reRule     = regexp.MustCompile(`^\s*Rule:\s*(.*)$`)
	reScenario = regexp.MustCompile(`^\s*(?:Scenario Outline|Scenario Template|Scenario|Example):\s*(.*)$`)
	reTag      = regexp.MustCompile(`@[^\s@]+`)
	reNonSlug  = regexp.MustCompile(`[^a-z0-9]+`)
)

// ScanCorpus walks corpusRoot/features and returns every capability it declares.
// repoRoot is used only to make Capability.Path repo-relative.
func ScanCorpus(repoRoot, corpusRoot string) (*Corpus, error) {
	featuresDir := filepath.Join(corpusRoot, featuresSubdir)
	info, err := os.Stat(featuresDir)
	if err != nil {
		return nil, fmt.Errorf("no Attest corpus at %s: %w", featuresDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("no Attest corpus: %s is not a directory", featuresDir)
	}

	corpus := &Corpus{Root: corpusRoot}
	// Collect paths first, then sort, so the output is deterministic regardless
	// of filesystem iteration order — a caller diffing two runs must not see
	// spurious churn.
	var paths []string
	walkErr := filepath.WalkDir(featuresDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".feature") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk corpus: %w", walkErr)
	}
	sort.Strings(paths)

	for _, path := range paths {
		caps, err := scanFile(repoRoot, featuresDir, path)
		if err != nil {
			return nil, err
		}
		corpus.Files++
		corpus.Capabilities = append(corpus.Capabilities, caps...)
	}
	return corpus, nil
}

// scanFile parses one .feature file into its capabilities.
//
// This is a deliberate Gherkin SUBSET, not a spec-complete parser: keywords,
// tags, and the `@validated` provenance comment. The repo carries no Gherkin
// dependency and one `Rule:` line does not justify adding one — but the subset
// is chosen so that anything it cannot understand is passed through as data
// (an unrecognized line is simply not a keyword) rather than dropped.
func scanFile(repoRoot, featuresDir, path string) ([]Capability, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read feature %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	relPath, err := filepath.Rel(repoRoot, path)
	if err != nil {
		relPath = path
	}
	relPath = filepath.ToSlash(relPath)

	domain := ""
	if d, derr := filepath.Rel(featuresDir, filepath.Dir(path)); derr == nil && d != "." {
		domain = filepath.ToSlash(d)
	}
	feature := strings.TrimSuffix(filepath.Base(path), ".feature")

	var (
		caps        []Capability
		featureTags []string
		ruleTags    []string
		pending     []string // tags read but not yet attached to a keyword
		pendingStmp *Stamp   // `# validated:` comment seen since the last keyword
		current     *Capability
		lineNo      int
		// slugCounts disambiguates two Rules with identical text in one file:
		// the second becomes `<slug>-2`. Without this their ids collide and one
		// capability silently shadows the other.
		slugCounts = map[string]int{}
	)

	// ensureCapability lazily creates the synthetic ungrouped capability so a
	// scenario written above any `Rule:` is never silently discarded. 100% of
	// the SageOx corpus is ruled today, but a customer's need not be, and a
	// vanished scenario is a wrong answer rather than a missing feature.
	ensureCapability := func() *Capability {
		if current != nil {
			return current
		}
		caps = append(caps, Capability{
			ID:      capabilityID(domain, feature, ""),
			Domain:  domain,
			Feature: feature,
			Path:    relPath,
		})
		current = &caps[len(caps)-1]
		return current
	}

	scanner := bufio.NewScanner(f)
	// Feature files are prose-heavy and some carry long comment blocks; the
	// default 64 KiB token limit is ample, but raise the ceiling so a
	// pathological single line fails loudly rather than truncating the file.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()

		if stamp, ok := parseStampComment(line); ok {
			pendingStmp = stamp
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if reTagLine.MatchString(line) {
			pending = append(pending, reTag.FindAllString(line, -1)...)
			continue
		}

		switch {
		case reFeature.MatchString(line):
			featureTags = pending
			pending, ruleTags, pendingStmp = nil, nil, nil
			current = nil

		case reRule.MatchString(line):
			rule := strings.TrimSpace(reRule.FindStringSubmatch(line)[1])
			slug := slugify(rule)
			slugCounts[slug]++
			if n := slugCounts[slug]; n > 1 {
				slug = fmt.Sprintf("%s-%d", slug, n)
			}
			caps = append(caps, Capability{
				ID:      capabilityID(domain, feature, slug),
				Domain:  domain,
				Feature: feature,
				Rule:    rule,
				Path:    relPath,
				Line:    lineNo,
			})
			current = &caps[len(caps)-1]
			ruleTags = pending
			pending, pendingStmp = nil, nil

		case reScenario.MatchString(line):
			name := strings.TrimSpace(reScenario.FindStringSubmatch(line)[1])
			cap := ensureCapability()
			tags := mergeTags(featureTags, ruleTags, pending)
			s := Scenario{
				Name:  name,
				Line:  lineNo,
				Tags:  tags,
				Stamp: pendingStmp,
			}
			s.Validated = s.HasTag(TagValidated)
			cap.Scenarios = append(cap.Scenarios, s)
			pending, pendingStmp = nil, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan feature %s: %w", relPath, err)
	}
	return caps, nil
}

// capabilityID builds the stable `<domain>/<feature>#<rule-slug>` identity.
// A capability with no Rule has no fragment, so the two forms can never collide.
func capabilityID(domain, feature, ruleSlug string) string {
	base := feature
	if domain != "" {
		base = domain + "/" + feature
	}
	if ruleSlug == "" {
		return base
	}
	return base + "#" + ruleSlug
}

// slugify lowercases and hyphenates, so a Rule containing a colon, a slash, or
// punctuation still yields a path- and URL-safe fragment.
func slugify(s string) string {
	out := reNonSlug.ReplaceAllString(strings.ToLower(s), "-")
	out = strings.Trim(out, "-")
	if out == "" {
		return "rule"
	}
	return out
}

// mergeTags unions feature, rule, and scenario tags, de-duplicated and sorted.
// Sorted so JSON output is stable between runs on an unchanged corpus.
func mergeTags(sets ...[]string) []string {
	seen := map[string]struct{}{}
	for _, set := range sets {
		for _, t := range set {
			seen[t] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
