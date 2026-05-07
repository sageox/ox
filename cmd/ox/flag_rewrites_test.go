package main

import (
	"reflect"
	"testing"
)

// formatAliases mirrors the unknown-flag tokens in default_catalog.json that
// drive runtime rewrites. Tests use this fixture (rather than parsing the
// embedded catalog) so a behavior assertion isn't tied to whatever else ships
// in the catalog over time.
var formatAliases = map[string]string{
	"--format=json": "--json",
	"--format=text": "--json=false",
}

// TestApplyCatalogTokenRewrites covers the runtime rewrite layer that turns
// catalog-declared flag aliases into canonical flags before cobra sees them.
// Failure prevented: agent invocations that match a catalog alias falling
// through to "unknown flag" because the rewrite layer mishandled
// joined/split forms or consumed too many args.
func TestApplyCatalogTokenRewrites(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		aliases map[string]string
		want    []string
	}{
		{
			name:    "no aliases configured passes through",
			in:      []string{"session", "list", "--format=json"},
			aliases: nil,
			want:    []string{"session", "list", "--format=json"},
		},
		{
			name:    "no matching args passes through",
			in:      []string{"session", "list"},
			aliases: formatAliases,
			want:    []string{"session", "list"},
		},
		{
			name:    "joined form: --format=json",
			in:      []string{"session", "list", "--format=json"},
			aliases: formatAliases,
			want:    []string{"session", "list", "--json"},
		},
		{
			name:    "split form: --format json",
			in:      []string{"session", "list", "--format", "json"},
			aliases: formatAliases,
			want:    []string{"session", "list", "--json"},
		},
		{
			name:    "joined form: --format=text",
			in:      []string{"session", "list", "--format=text"},
			aliases: formatAliases,
			want:    []string{"session", "list", "--json=false"},
		},
		{
			name:    "split form: --format text",
			in:      []string{"session", "list", "--format", "text"},
			aliases: formatAliases,
			want:    []string{"session", "list", "--json=false"},
		},
		{
			name:    "unknown value passes through (cobra/friction handles)",
			in:      []string{"session", "list", "--format=yaml"},
			aliases: formatAliases,
			want:    []string{"session", "list", "--format=yaml"},
		},
		{
			name:    "split form with unknown value passes through",
			in:      []string{"session", "list", "--format", "yaml"},
			aliases: formatAliases,
			want:    []string{"session", "list", "--format", "yaml"},
		},
		{
			name:    "bare --format with no value passes through",
			in:      []string{"session", "list", "--format"},
			aliases: formatAliases,
			want:    []string{"session", "list", "--format"},
		},
		{
			name:    "split form does not consume next flag",
			in:      []string{"session", "list", "--format", "--limit", "5"},
			aliases: formatAliases,
			want:    []string{"session", "list", "--format", "--limit", "5"},
		},
		{
			name:    "split form does not consume short flag",
			in:      []string{"session", "list", "--format", "-v"},
			aliases: formatAliases,
			want:    []string{"session", "list", "--format", "-v"},
		},
		{
			name:    "rewrite preserves surrounding flags",
			in:      []string{"session", "list", "--limit", "20", "--format=json"},
			aliases: formatAliases,
			want:    []string{"session", "list", "--limit", "20", "--json"},
		},
		{
			name:    "multiple rewrites in one invocation",
			in:      []string{"--format=text", "session", "list", "--format=json"},
			aliases: formatAliases,
			want:    []string{"--json=false", "session", "list", "--json"},
		},
		{
			name:    "empty args",
			in:      []string{},
			aliases: formatAliases,
			want:    []string{},
		},
		{
			name: "alias map with arbitrary patterns works",
			in:   []string{"foo", "--quux=bar", "tail"},
			aliases: map[string]string{
				"--quux=bar": "--baz",
			},
			want: []string{"foo", "--baz", "tail"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyCatalogTokenRewrites(tt.in, tt.aliases)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("applyCatalogTokenRewrites(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestLoadFlagAliases covers the catalog-parsing half of the contract.
// Failure prevented: catalog edits that should add a runtime rewrite get
// silently dropped because the parser rejects the entry.
func TestLoadFlagAliases(t *testing.T) {
	tests := []struct {
		name    string
		catalog string
		want    map[string]string
	}{
		{
			name:    "empty catalog returns nil",
			catalog: ``,
			want:    nil,
		},
		{
			name:    "malformed json returns nil",
			catalog: `{not json`,
			want:    nil,
		},
		{
			name:    "no tokens array returns nil",
			catalog: `{"version":"1","commands":[]}`,
			want:    nil,
		},
		{
			name: "extracts unknown-flag entries with =",
			catalog: `{
				"tokens":[
					{"pattern":"--format=json","target":"--json","kind":"unknown-flag"},
					{"pattern":"--format=text","target":"--json=false","kind":"unknown-flag"}
				]
			}`,
			want: map[string]string{
				"--format=json": "--json",
				"--format=text": "--json=false",
			},
		},
		{
			name: "skips bare flag patterns (suggestion-only)",
			catalog: `{
				"tokens":[
					{"pattern":"--format","target":"--json","kind":"unknown-flag"},
					{"pattern":"--format=json","target":"--json","kind":"unknown-flag"}
				]
			}`,
			want: map[string]string{
				"--format=json": "--json",
			},
		},
		{
			name: "skips non unknown-flag kinds",
			catalog: `{
				"tokens":[
					{"pattern":"--format=json","target":"--json","kind":"unknown-flag"},
					{"pattern":"--badarg=foo","target":"--good","kind":"invalid-arg"}
				]
			}`,
			want: map[string]string{
				"--format=json": "--json",
			},
		},
		{
			name: "skips entries with empty target",
			catalog: `{
				"tokens":[
					{"pattern":"--format=json","target":"","kind":"unknown-flag"}
				]
			}`,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := loadFlagAliases([]byte(tt.catalog))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("loadFlagAliases() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestLoadFlagAliases_EmbeddedCatalog locks in the catalog ↔ rewrite layer
// contract: whatever the catalog ships must be loadable, and the format
// rewrites we expect for this PR must be present.
// Failure prevented: someone edits default_catalog.json and removes a token
// the agent UX depends on, and nothing catches it before release.
func TestLoadFlagAliases_EmbeddedCatalog(t *testing.T) {
	got := loadFlagAliases(defaultCatalogJSON)
	if got == nil {
		t.Fatal("loadFlagAliases returned nil for embedded catalog")
	}
	for pattern, want := range map[string]string{
		"--format=json": "--json",
		"--format=text": "--json=false",
	} {
		if got[pattern] != want {
			t.Errorf("embedded catalog: aliases[%q] = %q, want %q", pattern, got[pattern], want)
		}
	}
}
