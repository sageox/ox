package observability

import (
	"reflect"
	"testing"
)

// TestRedactArgs covers the contract: every test case names the failure mode
// it prevents, so a future "simplification" of the redactor cannot quietly
// regress to leaking values.
//
// The contract: flag NAMES are queryable in trace backends; flag VALUES and
// positional arguments are NEVER queryable from this attribute. Any test
// here failing means user content is at risk of being exported as telemetry.
func TestRedactArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// what real-world failure does this case prevent
		prevents string
		in       []string
		want     []string
	}{
		{
			name:     "nil_input",
			prevents: "panic on empty argv (e.g. ox with no subcommand)",
			in:       nil,
			want:     nil,
		},
		{
			name:     "empty_slice",
			prevents: "panic on len(args)==0",
			in:       []string{},
			want:     nil,
		},
		{
			name:     "subcommand_only",
			prevents: "leaking subcommand name (it is intentionally kept)",
			in:       []string{"login"},
			// "login" is a positional from argv's perspective; redact it.
			// The subcommand path is captured by the span name, not by args.
			want: []string{"<REDACTED>"},
		},
		{
			name:     "long_flag_separate_value",
			prevents: "search query leaking via `--query 'secret'`",
			in:       []string{"code", "search", "--query", "internal codename"},
			want:     []string{"<REDACTED>", "<REDACTED>", "--query", "<REDACTED>"},
		},
		{
			name:     "long_flag_equals_value",
			prevents: "secret leaking via `--token=ghp_xxx`",
			in:       []string{"--token=ghp_realsecretvalue123"},
			want:     []string{"--token=<REDACTED>"},
		},
		{
			name:     "short_flag_separate_value",
			prevents: "value leaking via `-k 10` style short flags",
			in:       []string{"-k", "10"},
			want:     []string{"-k", "<REDACTED>"},
		},
		{
			name:     "short_flag_equals_value",
			prevents: "value leaking via the `-x=value` short-flag form",
			in:       []string{"-k=10"},
			want:     []string{"-k=<REDACTED>"},
		},
		{
			name:     "chained_long_flags_both_preserved",
			prevents: "losing visibility into chained boolean long flags like `--verbose --json`",
			in:       []string{"--verbose", "--json"},
			want:     []string{"--verbose", "--json"},
		},
		{
			name:     "long_flag_with_negative_number_value",
			prevents: "leaking `--limit -5` style hyphen-prefixed values (CodeRabbit PR488)",
			in:       []string{"--limit", "-5"},
			want:     []string{"--limit", "<REDACTED>"},
		},
		{
			name:     "short_flag_followed_by_short_flag_value",
			prevents: "leaking values starting with `-` after short flags (e.g. `-k -1`)",
			in:       []string{"-k", "-1"},
			want:     []string{"-k", "<REDACTED>"},
		},
		{
			name:     "long_flag_followed_by_short_flag_value",
			prevents: "leaking single-dash values after long flags (e.g. `--name -foo`)",
			in:       []string{"--name", "-foo"},
			want:     []string{"--name", "<REDACTED>"},
		},
		{
			name:     "short_flag_then_long_flag_preserves_long",
			prevents: "false-redacting a chained long flag after a short flag (e.g. `-v --json`)",
			in:       []string{"-v", "--json"},
			want:     []string{"-v", "--json"},
		},
		{
			name:     "boolean_flag_at_end_of_args",
			prevents: "panic on trailing boolean flag",
			in:       []string{"--verbose"},
			want:     []string{"--verbose"},
		},
		{
			name:     "mixed_flags_and_positionals",
			prevents: "any positional leaking — only flag names should survive",
			in:       []string{"query", "auth flow", "--limit", "5", "--source=team"},
			want: []string{
				"<REDACTED>", // "query" (subcommand looks positional from argv)
				"<REDACTED>", // "auth flow"
				"--limit",    // flag name kept
				"<REDACTED>", // "5"
				"--source=<REDACTED>",
			},
		},
		{
			name:     "double_dash_terminator_redacts_everything_after",
			prevents: "post-`--` content (commonly the actual user input) leaking",
			in:       []string{"agent", "OxAAA1", "--", "--session-id", "real-id"},
			want: []string{
				"<REDACTED>",
				"<REDACTED>",
				"--",
				"<REDACTED>",
				"<REDACTED>",
			},
		},
		{
			name:     "double_dash_immediately_after_flag",
			prevents: "the `--` terminator being consumed as a flag value when it follows a flag (CodeRabbit PR488). Without the fix, `--verbose -- secret` would become `--verbose <REDACTED>` and silently drop `secret`.",
			in:       []string{"--verbose", "--", "secret"},
			want: []string{
				"--verbose",
				"--",
				"<REDACTED>",
			},
		},
		{
			name:     "double_dash_immediately_after_short_flag",
			prevents: "same `--`-as-value bug for short flags (e.g. `-v -- secret`)",
			in:       []string{"-v", "--", "secret"},
			want: []string{
				"-v",
				"--",
				"<REDACTED>",
			},
		},
		{
			name:     "stdin_sentinel_dash",
			prevents: "treating bare `-` (stdin) as a flag and skipping next arg",
			in:       []string{"import", "-", "extra"},
			want:     []string{"<REDACTED>", "<REDACTED>", "<REDACTED>"},
		},
		{
			name:     "input_is_not_mutated",
			prevents: "RedactArgs mutating its caller's slice (caused by in-place edit)",
			// covered by the explicit aliasing test below; this case just
			// also exercises a typical capture-prior invocation
			in:   []string{"agent", "Oxslow1", "session", "capture-prior", "--adapter", "claude", "--session-id", "abc"},
			want: []string{"<REDACTED>", "<REDACTED>", "<REDACTED>", "<REDACTED>", "--adapter", "<REDACTED>", "--session-id", "<REDACTED>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RedactArgs(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RedactArgs(%q)\n got: %q\nwant: %q\n(test prevents: %s)",
					tt.in, got, tt.want, tt.prevents)
			}
		})
	}
}

// TestRedactArgs_DoesNotMutateInput is a separate, dedicated test for the
// non-mutation invariant. Failure prevented: a future refactor that edits
// args in place would alter os.Args (which is what callers will pass) and
// silently change command-line parsing for the rest of the process.
func TestRedactArgs_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	in := []string{"--adapter", "claude", "--session-id", "abc"}
	snapshot := append([]string(nil), in...)

	_ = RedactArgs(in)

	if !reflect.DeepEqual(in, snapshot) {
		t.Errorf("RedactArgs mutated input slice\n  in:  %q\n  was: %q", in, snapshot)
	}
}

// TestRedactArgs_AllValuesAreReplaced is a fuzz-style invariant: for any
// arg that is not a flag NAME or the literal "--" terminator, the output
// must contain RedactedPlaceholder at that position.
//
// Failure prevented: a regex change or new branch that lets a positional
// fall through unredacted. This test catches the *category* of bug rather
// than enumerating every input shape.
func TestRedactArgs_AllValuesAreReplaced(t *testing.T) {
	t.Parallel()

	cases := [][]string{
		{"alpha", "beta", "gamma"},
		{"--key", "secret"},
		{"--key=secret"},
		{"-k", "secret"},
		{"-k=secret"},
		{"cmd", "sub", "--", "anything", "goes"},
	}
	for _, in := range cases {
		got := RedactArgs(in)
		for i, tok := range got {
			// Anything that is not a flag-name surface (e.g. "--key" or
			// "--key=<REDACTED>") and not the bare "--" terminator must
			// equal the placeholder.
			isFlagName := len(tok) > 1 && tok[0] == '-' &&
				(tok == "--" || !containsRune(tok, ' '))
			isFlagWithRedactedValue := len(tok) > 0 && tok[len(tok)-1] == '>' &&
				containsSubstr(tok, "=<REDACTED>")
			isBareTerm := tok == "--"
			isFullyRedacted := tok == RedactedPlaceholder
			if !isFlagName && !isFlagWithRedactedValue && !isBareTerm && !isFullyRedacted {
				t.Errorf("input %q -> output token %d = %q is neither a flag name nor redacted",
					in, i, tok)
			}
		}
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func containsSubstr(s, sub string) bool {
	return len(sub) <= len(s) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
