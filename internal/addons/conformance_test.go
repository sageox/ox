package addons

// conformance_test.go — the Provider contract, checked mechanically.
//
// types.go says a Provider is "the catalog contract. The embedded provider
// implements it today; a remote one implements the same three methods. Nothing
// above this interface knows which it is talking to, which is the point." That
// claim is only worth anything if the contract is written down somewhere an
// implementation can be held against, so it is written down here as
// checkProviderContract: one function that takes any Provider and returns the
// list of promises it breaks.
//
// The suite runs the checker three ways, and the third is what makes the first
// two mean anything:
//
//  1. against the real EmbeddedProvider — zero violations;
//  2. against a hand-built conforming provider — zero violations;
//  3. against one deliberately broken provider per invariant — each must be
//     NAMED by the checker.
//
// Without (3), "zero violations" is indistinguishable from a checker that
// cannot return anything at all — the absent-match trap. (3) proves every
// probe can fire.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The invariants a consumer of Provider is entitled to. Named as sentences
// because they end up in failure output, where "invListSorted" tells a reader
// nothing and the sentence tells them everything.
const (
	invSourceNonEmpty             = "Source() identifies the provider with a non-empty string"
	invSourceStable               = "Source() returns the same value on every call"
	invListSucceeds               = "List returns a catalog rather than an error"
	invListSorted                 = "List returns descriptors sorted by name"
	invListUniqueNames            = "List returns each add-on name at most once"
	invListStable                 = "two List calls in a row return the same catalog"
	invDescriptorComplete         = "every descriptor carries a name, version, summary and digest"
	invDescriptorSourceMatches    = "every descriptor's Source matches the provider's Source()"
	invResolveListed              = "every add-on List advertises can be resolved"
	invResolveNameMatches         = "Resolve returns the add-on it was asked for"
	invResolveConcreteVersion     = "Resolve pins a concrete version even when asked for none"
	invResolveHasFiles            = "Resolve returns at least one installable file"
	invListMatchesResolve         = "List and Resolve agree on an add-on's version and digest"
	invFilePathInstallable        = "every File.Path is a clean, slash-separated path under agents/"
	invFilePathUnique             = "no two files in one add-on target the same path"
	invFileHasContent             = "every File carries content"
	invFileDigestPresent          = "every File carries a digest"
	invResolveDeterministic       = "resolving the same (name, version) twice yields the same bytes and digest"
	invResolvePinnedMatchesActual = "resolving the version Resolve just chose yields the same digest"
	invResolveUnknownNameErrors   = "Resolve errors on a name the provider does not carry"
	invResolveUnknownVersionError = "Resolve errors on a version the provider does not offer"
)

// providerViolation is one broken promise, with enough detail to act on.
type providerViolation struct {
	Invariant string
	Detail    string
}

func (v providerViolation) String() string { return v.Invariant + ": " + v.Detail }

// unknownProbeName is a name no real catalog may carry. It is deliberately
// valid per addonNameRE, so a provider that rejects it is rejecting it for
// being absent rather than for being malformed — otherwise the unknown-name
// invariant would pass for the wrong reason.
const unknownProbeName = "conformance-probe-absent-addon"

// checkProviderContract exercises p and reports every invariant it breaks.
// An empty result means conforming.
//
// It calls cleanAddonPath — the installer's own validator — rather than
// re-deriving the path rules, so "this provider conforms" and "the installer
// will accept this provider's answers" cannot drift apart.
func checkProviderContract(ctx context.Context, p Provider) []providerViolation {
	var out []providerViolation
	add := func(invariant, format string, args ...any) {
		out = append(out, providerViolation{Invariant: invariant, Detail: fmt.Sprintf(format, args...)})
	}

	source := p.Source()
	if source == "" {
		add(invSourceNonEmpty, "Source() returned an empty string")
	}
	if again := p.Source(); again != source {
		add(invSourceStable, "Source() returned %q then %q", source, again)
	}

	first, err := p.List(ctx)
	if err != nil {
		add(invListSucceeds, "List returned %v", err)
		return out // nothing below is meaningful without a catalog
	}

	names := make([]string, 0, len(first))
	for _, d := range first {
		names = append(names, d.Name)
	}
	if !slices.IsSorted(names) {
		add(invListSorted, "List returned %v", names)
	}
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if seen[n] {
			add(invListUniqueNames, "%q appears more than once", n)
		}
		seen[n] = true
	}

	second, err := p.List(ctx)
	switch {
	case err != nil:
		add(invListStable, "the second List call returned %v after the first succeeded", err)
	case !slices.EqualFunc(first, second, sameDescriptor):
		add(invListStable, "two List calls disagreed: %v then %v", summarize(first), summarize(second))
	}

	for _, d := range first {
		switch {
		case d.Name == "":
			add(invDescriptorComplete, "a descriptor has an empty name")
		case d.Version == "":
			add(invDescriptorComplete, "%q has no version", d.Name)
		case d.Summary == "":
			add(invDescriptorComplete, "%q has no summary", d.Name)
		case d.Digest == "":
			add(invDescriptorComplete, "%q has no digest", d.Name)
		}
		if d.Source != source {
			add(invDescriptorSourceMatches, "%q reports source %q but the provider reports %q", d.Name, d.Source, source)
		}
		if d.Name == "" {
			continue // nothing resolvable to check
		}
		out = append(out, checkResolve(ctx, p, d)...)
	}

	if _, err := p.Resolve(ctx, unknownProbeName, ""); err == nil {
		add(invResolveUnknownNameErrors, "Resolve(%q) succeeded", unknownProbeName)
	}
	return out
}

// checkResolve is the per-add-on half of the contract: everything that must
// hold for one name List advertised.
func checkResolve(ctx context.Context, p Provider, d Descriptor) []providerViolation {
	var out []providerViolation
	add := func(invariant, format string, args ...any) {
		out = append(out, providerViolation{Invariant: invariant, Detail: fmt.Sprintf(format, args...)})
	}

	resolved, err := p.Resolve(ctx, d.Name, "")
	if err != nil {
		add(invResolveListed, "List advertised %q but Resolve returned %v", d.Name, err)
		return out
	}
	if resolved.Name != d.Name {
		add(invResolveNameMatches, "asked for %q, got %q", d.Name, resolved.Name)
	}
	if resolved.Version == "" {
		add(invResolveConcreteVersion, "%q resolved with an empty version", d.Name)
	}
	if len(resolved.Files) == 0 {
		add(invResolveHasFiles, "%q resolved with no files", d.Name)
	}
	if resolved.Version != d.Version || resolved.Digest != d.Digest {
		add(invListMatchesResolve, "%q lists as %s/%s but resolves as %s/%s",
			d.Name, d.Version, d.Digest, resolved.Version, resolved.Digest)
	}

	paths := make(map[string]bool, len(resolved.Files))
	for _, f := range resolved.Files {
		clean, err := cleanAddonPath(f.Path)
		if err != nil {
			add(invFilePathInstallable, "%q: %v", d.Name, err)
			continue
		}
		if paths[clean] {
			add(invFilePathUnique, "%q ships %s twice", d.Name, clean)
		}
		paths[clean] = true
		if len(f.Content) == 0 {
			add(invFileHasContent, "%q: %s has no content", d.Name, f.Path)
		}
		if f.Digest == "" {
			add(invFileDigestPresent, "%q: %s has no digest", d.Name, f.Path)
		}
	}

	// Determinism is the claim the lock depends on: the digest it records is
	// the team's selection, so two resolutions of the same (name, version)
	// must agree byte for byte.
	repeat, err := p.Resolve(ctx, d.Name, "")
	switch {
	case err != nil:
		add(invResolveDeterministic, "%q resolved once then returned %v", d.Name, err)
	case repeat.Digest != resolved.Digest:
		add(invResolveDeterministic, "%q resolved to digest %s then %s", d.Name, resolved.Digest, repeat.Digest)
	default:
		if !slices.EqualFunc(resolved.Files, repeat.Files, sameFile) {
			add(invResolveDeterministic, "%q resolved to different file content on the second call", d.Name)
		}
	}

	if resolved.Version != "" {
		pinned, err := p.Resolve(ctx, d.Name, resolved.Version)
		switch {
		case err != nil:
			add(invResolvePinnedMatchesActual, "%q@%s (the version Resolve itself chose) returned %v", d.Name, resolved.Version, err)
		case pinned.Digest != resolved.Digest:
			add(invResolvePinnedMatchesActual, "%q@%s resolved to a different digest than the unpinned call", d.Name, resolved.Version)
		}

		absent := resolved.Version + "-conformance-probe-absent"
		if _, err := p.Resolve(ctx, d.Name, absent); err == nil {
			add(invResolveUnknownVersionError, "%q@%s succeeded", d.Name, absent)
		}
	}
	return out
}

func sameDescriptor(a, b Descriptor) bool {
	return a.Name == b.Name && a.Version == b.Version && a.Digest == b.Digest && a.Source == b.Source
}

func sameFile(a, b File) bool {
	return a.Path == b.Path && a.Digest == b.Digest && string(a.Content) == string(b.Content)
}

func summarize(ds []Descriptor) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Name+"@"+d.Version)
	}
	return out
}

// --- the provider under the microscope ------------------------------------

// rigged is a Provider whose three answers are each replaceable, so one test
// row can break exactly one promise and leave the rest conforming. That
// one-breach-at-a-time shape is what lets the detector table below assert
// which invariant fired instead of "something was wrong".
type rigged struct {
	source    string
	catalog   []Resolved
	sourceFn  func() string
	listFn    func(context.Context) ([]Descriptor, error)
	resolveFn func(context.Context, string, string) (Resolved, error)
	calls     atomic.Int64
}

func (p *rigged) Source() string { return p.sourceFn() }

func (p *rigged) List(ctx context.Context) ([]Descriptor, error) { return p.listFn(ctx) }

func (p *rigged) Resolve(ctx context.Context, name, version string) (Resolved, error) {
	return p.resolveFn(ctx, name, version)
}

// riggedEntry builds one conforming catalog entry, computing the same digests
// the embedded provider computes (hashBytes per file, addonDigest for the
// add-on) so a rigged provider is byte-compatible with the real one.
func riggedEntry(name, version, summary string, files ...File) Resolved {
	for i := range files {
		if files[i].Mode == 0 {
			files[i].Mode = 0o644
		}
		files[i].Digest = hashBytes(files[i].Content)
	}
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return Resolved{
		Descriptor: Descriptor{
			Name: name, Version: version, Summary: summary,
			Digest: addonDigest(files),
		},
		Files: files,
	}
}

// newRigged returns a fully CONFORMING provider. Every attack in this package
// is expressed by replacing one of its three closures, never by hand-building
// a broken provider from scratch — so a row's diff from conformance is exactly
// the attack and nothing else.
func newRigged(source string, catalog ...Resolved) *rigged {
	p := &rigged{source: source}
	for i := range catalog {
		catalog[i].Source = source
	}
	slices.SortFunc(catalog, func(a, b Resolved) int { return strings.Compare(a.Name, b.Name) })
	p.catalog = catalog

	p.sourceFn = func() string { return p.source }
	p.listFn = func(context.Context) ([]Descriptor, error) {
		out := make([]Descriptor, 0, len(p.catalog))
		for _, r := range p.catalog {
			out = append(out, r.Descriptor)
		}
		return out, nil
	}
	p.resolveFn = func(_ context.Context, name, version string) (Resolved, error) {
		for _, r := range p.catalog {
			if r.Name != name {
				continue
			}
			if version != "" && version != r.Version {
				return Resolved{}, fmt.Errorf("%q has no version %q: %w", name, version, ErrVersionMismatch)
			}
			return r, nil
		}
		return Resolved{}, fmt.Errorf("%q: %w", name, ErrAddonNotFound)
	}
	return p
}

// twoAddonCatalog is the smallest catalog that makes sorting, name
// uniqueness, and per-add-on resolution non-trivial. A one-entry catalog
// passes a sortedness check vacuously.
func twoAddonCatalog() []Resolved {
	return []Resolved{
		riggedEntry("alpha", "1.0.0", "the first add-on",
			addonFile("agents/skills/alpha/SKILL.md", "---\nname: alpha\n---\nalpha\n"),
			addonFile("agents/skills/alpha/references/notes.md", "alpha notes\n"),
		),
		riggedEntry("beta", "2.1.0", "the second add-on",
			addonFile("agents/rules/beta-style.md", "# beta style\n"),
		),
	}
}

// --- 1. conforming providers report nothing ------------------------------

// TestProviderContract_EmbeddedProvider holds the shipped provider to the
// contract types.go writes for it. Failure prevented: the embedded catalog
// drifts into something a remote provider could not be swapped for — an
// unsorted List, a digest that changes between calls, a file outside agents/ —
// and nothing notices until the second provider exists.
func TestProviderContract_EmbeddedProvider(t *testing.T) {
	t.Parallel()
	violations := checkProviderContract(context.Background(), NewEmbeddedProvider())
	assert.Empty(t, violations, "the embedded provider must satisfy the Provider contract: %v", violations)
}

// TestProviderContract_ConformingFake proves the checker passes a provider
// built independently of the embedded one, so TestProviderContract_EmbeddedProvider
// is not passing on some accident of embed.FS.
func TestProviderContract_ConformingFake(t *testing.T) {
	t.Parallel()
	p := newRigged("test:conforming", twoAddonCatalog()...)
	violations := checkProviderContract(context.Background(), p)
	assert.Empty(t, violations, "a deliberately conforming provider must report nothing: %v", violations)
}

// --- 2. the checker can actually return something -------------------------

// detectorRow is one deliberately broken provider and the invariant the
// checker must name for it. The table is a function rather than a literal
// inside the test so the completeness check below consumes the SAME rows —
// two copies of this table would be two things to keep in sync, and the one
// that drifts is the one nobody is watching.
type detectorRow struct {
	name string
	want string
	rig  func(*rigged)
}

func detectorRows() []detectorRow {
	return []detectorRow{
		{
			name: "empty source",
			want: invSourceNonEmpty,
			rig:  func(p *rigged) { p.sourceFn = func() string { return "" } },
		},
		{
			name: "source changes between calls",
			want: invSourceStable,
			rig: func(p *rigged) {
				p.sourceFn = func() string { return fmt.Sprintf("test:call-%d", p.calls.Add(1)) }
			},
		},
		{
			name: "List fails",
			want: invListSucceeds,
			rig: func(p *rigged) {
				p.listFn = func(context.Context) ([]Descriptor, error) { return nil, errors.New("catalog unreachable") }
			},
		},
		{
			name: "List is not sorted by name",
			want: invListSorted,
			rig: func(p *rigged) {
				p.listFn = func(context.Context) ([]Descriptor, error) {
					return []Descriptor{p.catalog[1].Descriptor, p.catalog[0].Descriptor}, nil
				}
			},
		},
		{
			name: "List repeats a name",
			want: invListUniqueNames,
			rig: func(p *rigged) {
				p.listFn = func(context.Context) ([]Descriptor, error) {
					return []Descriptor{p.catalog[0].Descriptor, p.catalog[0].Descriptor}, nil
				}
			},
		},
		{
			name: "List changes between calls",
			want: invListStable,
			rig: func(p *rigged) {
				p.listFn = func(context.Context) ([]Descriptor, error) {
					if p.calls.Add(1) > 1 {
						return []Descriptor{p.catalog[0].Descriptor}, nil
					}
					return []Descriptor{p.catalog[0].Descriptor, p.catalog[1].Descriptor}, nil
				}
			},
		},
		{
			name: "descriptor has no summary",
			want: invDescriptorComplete,
			rig:  func(p *rigged) { p.catalog[0].Summary = "" },
		},
		{
			name: "descriptor has no digest",
			want: invDescriptorComplete,
			rig:  func(p *rigged) { p.catalog[0].Digest = "" },
		},
		{
			name: "descriptor claims a different source",
			want: invDescriptorSourceMatches,
			rig:  func(p *rigged) { p.catalog[0].Source = "builtin:someone-else" },
		},
		{
			name: "a listed add-on cannot be resolved",
			want: invResolveListed,
			rig: func(p *rigged) {
				p.resolveFn = func(context.Context, string, string) (Resolved, error) {
					return Resolved{}, errors.New("yanked between list and resolve")
				}
			},
		},
		{
			name: "Resolve returns a different add-on",
			want: invResolveNameMatches,
			rig: func(p *rigged) {
				conforming := p.resolveFn
				p.resolveFn = func(ctx context.Context, name, version string) (Resolved, error) {
					r, err := conforming(ctx, name, version)
					r.Name = "something-else"
					return r, err
				}
			},
		},
		{
			name: "Resolve does not pin a version",
			want: invResolveConcreteVersion,
			rig: func(p *rigged) {
				conforming := p.resolveFn
				p.resolveFn = func(ctx context.Context, name, version string) (Resolved, error) {
					r, err := conforming(ctx, name, version)
					r.Version = ""
					return r, err
				}
			},
		},
		{
			name: "Resolve returns no files",
			want: invResolveHasFiles,
			rig: func(p *rigged) {
				conforming := p.resolveFn
				p.resolveFn = func(ctx context.Context, name, version string) (Resolved, error) {
					r, err := conforming(ctx, name, version)
					r.Files = nil
					return r, err
				}
			},
		},
		{
			name: "List and Resolve disagree on the digest",
			want: invListMatchesResolve,
			rig: func(p *rigged) {
				conforming := p.resolveFn
				p.resolveFn = func(ctx context.Context, name, version string) (Resolved, error) {
					r, err := conforming(ctx, name, version)
					r.Digest = "sha256:not-what-was-listed"
					return r, err
				}
			},
		},
		{
			name: "a file escapes agents/",
			want: invFilePathInstallable,
			rig:  func(p *rigged) { p.catalog[0].Files[0].Path = "../../etc/cron.d/x" },
		},
		{
			name: "two files target one path",
			want: invFilePathUnique,
			rig:  func(p *rigged) { p.catalog[0].Files[1].Path = p.catalog[0].Files[0].Path },
		},
		{
			name: "a file has no content",
			want: invFileHasContent,
			rig:  func(p *rigged) { p.catalog[0].Files[0].Content = nil },
		},
		{
			name: "a file has no digest",
			want: invFileDigestPresent,
			rig:  func(p *rigged) { p.catalog[0].Files[0].Digest = "" },
		},
		{
			name: "Resolve is not deterministic",
			want: invResolveDeterministic,
			rig: func(p *rigged) {
				conforming := p.resolveFn
				p.resolveFn = func(ctx context.Context, name, version string) (Resolved, error) {
					r, err := conforming(ctx, name, version)
					if err != nil || len(r.Files) == 0 {
						return r, err
					}
					files := slices.Clone(r.Files)
					files[0].Content = fmt.Appendf(nil, "call %d\n", p.calls.Add(1))
					files[0].Digest = hashBytes(files[0].Content)
					r.Files = files
					r.Digest = addonDigest(files)
					return r, nil
				}
			},
		},
		{
			name: "the version Resolve chose cannot be resolved",
			want: invResolvePinnedMatchesActual,
			rig: func(p *rigged) {
				conforming := p.resolveFn
				p.resolveFn = func(ctx context.Context, name, version string) (Resolved, error) {
					if version != "" {
						return Resolved{}, fmt.Errorf("pinned versions unsupported: %w", ErrVersionMismatch)
					}
					return conforming(ctx, name, version)
				}
			},
		},
		{
			name: "any name resolves",
			want: invResolveUnknownNameErrors,
			rig: func(p *rigged) {
				conforming := p.resolveFn
				p.resolveFn = func(ctx context.Context, name, version string) (Resolved, error) {
					if r, err := conforming(ctx, name, version); err == nil {
						return r, nil
					}
					// The shape that matters: an unknown name silently answered
					// with some other add-on's bytes under the asked-for name.
					r := p.catalog[0]
					r.Name = name
					return r, nil
				}
			},
		},
		{
			name: "any version resolves",
			want: invResolveUnknownVersionError,
			rig: func(p *rigged) {
				conforming := p.resolveFn
				p.resolveFn = func(ctx context.Context, name, _ string) (Resolved, error) {
					return conforming(ctx, name, "")
				}
			},
		},
	}
}

// TestProviderContract_DetectsEveryViolation is the double entry for the two
// tests above. Each row breaks exactly one promise and asserts the checker
// NAMES that promise.
//
// Failure prevented: a checker that silently cannot fire. Three times on this
// branch an empty result meant "the probe is broken", not "the subject is
// clean" — so every invariant here has a row proving it can return something.
func TestProviderContract_DetectsEveryViolation(t *testing.T) {
	t.Parallel()

	for _, tt := range detectorRows() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newRigged("test:rigged", twoAddonCatalog()...)
			tt.rig(p)

			violations := checkProviderContract(context.Background(), p)
			require.NotEmpty(t, violations, "the checker reported a rigged provider as conforming")

			named := make([]string, 0, len(violations))
			for _, v := range violations {
				named = append(named, v.Invariant)
			}
			assert.Contains(t, named, tt.want, "the checker fired, but on the wrong invariant: %v", violations)
		})
	}
}

// TestProviderContract_EveryInvariantHasARow closes the loop the other way:
// an invariant with no row in the detector table is an invariant nobody has
// proven can fire, so "zero violations" says nothing about it. Adding a
// constant without adding a row fails here rather than quietly widening the
// unproven surface.
func TestProviderContract_EveryInvariantHasARow(t *testing.T) {
	t.Parallel()

	declared := []string{
		invSourceNonEmpty, invSourceStable, invListSucceeds, invListSorted,
		invListUniqueNames, invListStable, invDescriptorComplete,
		invDescriptorSourceMatches, invResolveListed, invResolveNameMatches,
		invResolveConcreteVersion, invResolveHasFiles, invListMatchesResolve,
		invFilePathInstallable, invFilePathUnique, invFileHasContent,
		invFileDigestPresent, invResolveDeterministic,
		invResolvePinnedMatchesActual, invResolveUnknownNameErrors,
		invResolveUnknownVersionError,
	}

	covered := map[string]bool{}
	for _, row := range detectorRows() {
		covered[row.want] = true
	}
	for _, inv := range declared {
		assert.True(t, covered[inv], "no rigged provider proves this invariant can fire: %s", inv)
	}
}

// TestProviderContract_ConformingProviderIsInstallable ties the contract to
// the thing that consumes it: a provider the checker passes must be
// installable, because the checker validates paths with the installer's own
// cleanAddonPath. Failure prevented: a "conformance" suite that blesses a
// provider Install would reject — two definitions of valid, drifting apart.
func TestProviderContract_ConformingProviderIsInstallable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := newRigged("test:conforming", twoAddonCatalog()...)
	require.Empty(t, checkProviderContract(ctx, p))

	dir := newTeamRepo(t)
	for _, name := range []string{"alpha", "beta"} {
		result, err := Install(ctx, dir, p, name, "")
		require.NoError(t, err, "a conforming provider's add-on must install")
		require.NotEmpty(t, result.Written)
	}
	assert.True(t, gitStatusClean(t, dir))
}
