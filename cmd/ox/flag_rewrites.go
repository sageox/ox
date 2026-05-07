package main

import (
	"encoding/json"
	"strings"
)

// flagAlias is the wire shape we read out of default_catalog.json's `tokens`
// array. We only use unknown-flag entries here; other kinds (missing-required,
// invalid-arg, parse-error) are friction-suggestion-only.
type flagAlias struct {
	Pattern string `json:"pattern"`
	Target  string `json:"target"`
	Kind    string `json:"kind"`
}

// loadFlagAliases parses the embedded friction catalog and returns the subset
// of `unknown-flag` token mappings whose pattern is a fully-qualified flag
// expression (e.g. "--format=json"). Bare flag-name patterns ("--format") are
// skipped — those drive the friction *suggestion* path after cobra rejects an
// unknown flag, not the runtime rewrite.
//
// We read the embedded JSON rather than the live frictionax engine because
// frictionax v0.1.2 doesn't expose its catalog tokens. When upstream adds an
// accessor (and token-level auto-execute, see ox-54a), this function can be
// replaced with a single call into the engine and the rewrite layer goes
// away entirely.
func loadFlagAliases(catalogJSON []byte) map[string]string {
	if len(catalogJSON) == 0 {
		return nil
	}
	var data struct {
		Tokens []flagAlias `json:"tokens"`
	}
	if err := json.Unmarshal(catalogJSON, &data); err != nil {
		return nil
	}
	out := make(map[string]string, len(data.Tokens))
	for _, t := range data.Tokens {
		if t.Kind != "unknown-flag" || t.Target == "" {
			continue
		}
		// only fully-qualified patterns drive runtime rewrites; bare flag
		// names like "--format" are suggestion-only.
		if !strings.Contains(t.Pattern, "=") {
			continue
		}
		out[t.Pattern] = t.Target
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyCatalogTokenRewrites walks args and rewrites any flag invocation that
// matches a known catalog alias. Handles both joined ("--foo=bar", one arg)
// and split ("--foo", "bar", two args) forms — the catalog stores the joined
// form as canonical and this function synthesizes the split form to match.
//
// Unknown flag values pass through untouched so cobra still produces an
// unknown-flag error and the friction-suggestion path can teach the agent
// the canonical name.
func applyCatalogTokenRewrites(args []string, aliases map[string]string) []string {
	if len(aliases) == 0 {
		return args
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]

		// joined form: arg literally equals a catalog pattern.
		if target, ok := aliases[a]; ok {
			out = append(out, target)
			continue
		}

		// split form: --flag value across two args. Only consume the next
		// arg if it doesn't look like another flag.
		if strings.HasPrefix(a, "--") && !strings.Contains(a, "=") &&
			i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			joined := a + "=" + args[i+1]
			if target, ok := aliases[joined]; ok {
				out = append(out, target)
				i++ // consume the value arg
				continue
			}
		}

		out = append(out, a)
	}
	return out
}
