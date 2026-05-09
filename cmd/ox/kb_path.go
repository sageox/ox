package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/paths"
	"github.com/spf13/cobra"
)

// kbCmd is the parent command for knowledge bubble (`ox kb …`) operations.
//
// `bubble` and `bubbles` are aliases per the kb plan: `kb` is the canonical
// noun in docs, the longer form is for discoverability. User-facing copy says
// "knowledge bubble" — never just "bubble" — but the CLI noun stays terse.
//
// This file owns the parent so siblings (`kb_list.go`, `kb_show.go`) can
// register subcommands without racing to be the parent definer. Subcommand
// files attach to kbCmd in their own init() blocks.
var kbCmd = &cobra.Command{
	Use:     "kb",
	Aliases: []string{"bubble", "bubbles"},
	Short:   "Work with knowledge bubbles",
	Long: `Work with knowledge bubbles — the unified primitive for organizing knowledge.

Knowledge bubbles replace the legacy team-context + ledger model with a single
typed surface (personal, profile, team, repo, custom). Use ` + "`ox kb list`" + ` to
see what you have access to, ` + "`ox kb show <slug>`" + ` for details, and
` + "`ox kb path <slug>`" + ` for the local checkout path (scriptable: cd $(ox kb path <slug>)).`,
}

// kbPathCmd prints the canonical local path of a knowledge bubble. Designed
// to be `$()`-safe in shell composition: stdout is one line, no styling, no
// trailing whitespace beyond the single newline emitted by Fprintln. All
// errors and hints go to stderr so a `cd $(ox kb path …)` doesn't accidentally
// `cd` into a help message.
var kbPathCmd = &cobra.Command{
	Use:   "path <slug|id>",
	Short: "Print the local path of a knowledge bubble (scriptable)",
	Long: `Print the canonical XDG path of a knowledge bubble's local checkout.

Designed for shell composition. The success path prints exactly one line to
stdout and nothing else, so:

  cd $(ox kb path personal-foo)

works without quoting tricks. Errors go to stderr; the process exits non-zero
when no path can be printed.

The argument may be a kb_id (e.g. "kb_abc123") or a slug. When a slug matches
multiple bubbles, the priority order is: personal, profile, team, repo, custom.

When the kb API is unavailable (feature flag off, network error, etc.) and the
caller passed a slug, ox falls back to the local legacy team-context registry
so that pre-flag users get a working ` + "`ox kb path <slug>`" + ` immediately.`,
	Args: cobra.ExactArgs(1),
	RunE: runKBPath,
}

func init() {
	rootCmd.AddCommand(kbCmd)
	kbCmd.AddCommand(kbPathCmd)

	kbPathCmd.Flags().Bool("json", false, "Output as JSON ({\"kb_id\":..., \"path\":...})")
}

// kbResolver abstracts the kb API client so tests can stub responses without
// hitting the network. Only the two methods kb_path needs are surfaced; this
// keeps the seam minimal and matches the production *api.KBClient.
type kbResolver interface {
	ListBubbles(ctx context.Context) ([]api.KB, error)
	GetBubble(ctx context.Context, kbID string) (*api.KB, error)
}

// legacyResolver returns a non-empty path when a legacy (pre-kb-API) entry
// for the given slug is found locally. Wired through a function value so
// tests can inject a stub without coupling to discoverAllTeams() global state.
type legacyResolver func(slug string) (kbID, path string, ok bool)

// kbPathDeps lets tests override the live API client and the legacy lookup.
// Production runs use newDefaultKBPathDeps() which wires up the real client
// and the team-discovery-backed legacy fallback.
type kbPathDeps struct {
	resolver kbResolver
	legacy   legacyResolver
	// pathFor maps a resolved kb_id to its on-disk canonical path. In
	// production this is paths.KBDir; tests override it so they don't have
	// to set OX_ENDPOINT to construct expected paths.
	pathFor func(kbID string) string
}

func newDefaultKBPathDeps(projectRoot string) kbPathDeps {
	client := api.NewKBClientForProject(projectRoot)
	if token, err := auth.GetTokenForEndpoint(client.Endpoint()); err == nil && token != nil && token.AccessToken != "" {
		client = client.WithAuthToken(token.AccessToken)
	}
	return kbPathDeps{
		resolver: client,
		legacy:   defaultLegacyKBLookup(projectRoot),
		pathFor:  paths.KBDir,
	}
}

// defaultLegacyKBLookup returns a legacyResolver backed by the existing team
// discovery pipeline (project-aware) plus, when not in a project, the global
// credential-based discovery. Slug match is case-insensitive and exact.
func defaultLegacyKBLookup(projectRoot string) legacyResolver {
	return func(slug string) (string, string, bool) {
		needle := strings.ToLower(strings.TrimSpace(slug))
		if needle == "" {
			return "", "", false
		}

		var teams []enrichedTeam
		if projectRoot != "" {
			teams = discoverAllTeams(projectRoot)
		}
		if len(teams) == 0 {
			teams = discoverTeamsGlobal()
		}

		for _, t := range teams {
			if strings.ToLower(t.Slug) == needle {
				return t.TeamID, t.Path, t.Path != ""
			}
		}
		return "", "", false
	}
}

func runKBPath(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")

	// projectRoot is best-effort: `ox kb path` should work outside a project
	// (the path is XDG-canonical, not project-relative). When inside one,
	// we use it for endpoint resolution and the legacy fallback.
	projectRoot, _ := findProjectRoot()
	deps := newDefaultKBPathDeps(projectRoot)

	return runKBPathWithDeps(cmd, args[0], jsonMode, deps)
}

// runKBPathWithDeps is the dependency-injected core. Exposed within the
// package so tests can drive it without env wiring.
func runKBPathWithDeps(cmd *cobra.Command, query string, jsonMode bool, deps kbPathDeps) error {
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	query = strings.TrimSpace(query)
	if query == "" {
		fmt.Fprintln(stderr, "kb path: argument is required")
		return cli.ErrSilent
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()

	kb, err := resolveKB(ctx, query, deps)
	if err != nil {
		// kb-not-found: the canonical message format the spec asks for.
		if errors.Is(err, errKBNotFound) {
			fmt.Fprintf(stderr, "kb not found: %s\n", query)
			return cli.ErrSilent
		}
		fmt.Fprintf(stderr, "kb path: %v\n", err)
		return cli.ErrSilent
	}

	path := deps.pathFor(kb.KBID)
	if path == "" {
		fmt.Fprintf(stderr, "kb path: could not resolve canonical path for kb_id=%s\n", kb.KBID)
		return cli.ErrSilent
	}

	if jsonMode {
		// JSON path: still single-line, still grepable, no decoration.
		out := struct {
			KBID string `json:"kb_id"`
			Path string `json:"path"`
		}{KBID: kb.KBID, Path: path}
		bytes, err := json.Marshal(out)
		if err != nil {
			fmt.Fprintf(stderr, "kb path: json encode failed: %v\n", err)
			return cli.ErrSilent
		}
		fmt.Fprintln(stdout, string(bytes))
		return nil
	}

	fmt.Fprintln(stdout, path)
	return nil
}

// resolvedKB is what resolveKB hands back — just the immutable fields the
// path printer needs. Kept narrow so the legacy fallback (which doesn't have
// a full api.KB) can populate it without faking unused fields.
type resolvedKB struct {
	KBID string
}

// errKBNotFound is the sentinel for "the requested slug/id matches nothing
// the CLI can resolve, even after legacy fallback." Wrapped by the resolver
// so callers can branch with errors.Is.
var errKBNotFound = errors.New("kb not found")

// resolveKB applies the resolution rules from the spec:
//   - kb_* prefix → kb_id direct (skip slug lookup)
//   - otherwise → slug lookup via ListBubbles, priority personal > profile >
//     team > repo > custom
//   - on ErrKBAPIUnavailable during slug resolution, fall back to the legacy
//     resolver before giving up.
func resolveKB(ctx context.Context, query string, deps kbPathDeps) (resolvedKB, error) {
	// kb_id direct path. We don't even need the API for this — the path is
	// derivable from the id alone, and the spec explicitly says this should
	// work without a server round-trip when the prefix is present.
	if strings.HasPrefix(query, "kb_") {
		return resolvedKB{KBID: query}, nil
	}

	if deps.resolver == nil {
		// no live resolver — go straight to legacy.
		if deps.legacy != nil {
			if id, _, ok := deps.legacy(query); ok {
				return resolvedKB{KBID: id}, nil
			}
		}
		return resolvedKB{}, errKBNotFound
	}

	bubbles, err := deps.resolver.ListBubbles(ctx)
	if err != nil {
		if errors.Is(err, api.ErrKBAPIUnavailable) {
			// flag-off / endpoint-missing: fall back to legacy registry.
			if deps.legacy != nil {
				if id, _, ok := deps.legacy(query); ok {
					return resolvedKB{KBID: id}, nil
				}
			}
			return resolvedKB{}, errKBNotFound
		}
		return resolvedKB{}, err
	}

	if kb := pickBubbleBySlug(bubbles, query); kb != nil {
		return resolvedKB{KBID: kb.KBID}, nil
	}

	// API responded successfully but slug didn't match — still try legacy
	// before giving up, since legacy team-contexts may not be migrated yet.
	if deps.legacy != nil {
		if id, _, ok := deps.legacy(query); ok {
			return resolvedKB{KBID: id}, nil
		}
	}
	return resolvedKB{}, errKBNotFound
}

// pickBubbleBySlug applies the kind-priority tiebreak: personal > profile >
// team > repo > custom > (anything else / unknown). Returns nil when no
// bubble's slug matches the query (case-insensitive).
func pickBubbleBySlug(bubbles []api.KB, slug string) *api.KB {
	needle := strings.ToLower(strings.TrimSpace(slug))
	if needle == "" {
		return nil
	}

	priority := func(t api.KBType) int {
		switch t {
		case api.KBTypePersonal:
			return 0
		case api.KBTypeProfile:
			return 1
		case api.KBTypeTeam:
			return 2
		case api.KBTypeRepo:
			return 3
		case api.KBTypeCustom:
			return 4
		default:
			return 5
		}
	}

	var best *api.KB
	bestPri := 99
	for i := range bubbles {
		b := &bubbles[i]
		if strings.ToLower(b.Slug) != needle {
			continue
		}
		if p := priority(b.KBType); p < bestPri {
			best = b
			bestPri = p
		}
	}
	return best
}

// compile-time check that *api.KBClient satisfies kbResolver.
var _ kbResolver = (*api.KBClient)(nil)
