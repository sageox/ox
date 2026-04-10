package observability

import "strings"

// RedactedPlaceholder is the string that replaces every redacted token in
// the output of RedactArgs. Exposed as a constant so tests and downstream
// code can match against it without hardcoding the literal.
const RedactedPlaceholder = "<REDACTED>"

// RedactArgs returns a copy of args suitable for tracing or telemetry,
// preserving flag *names* but replacing every flag *value* and every
// positional argument with RedactedPlaceholder.
//
// The goal is to make `ox.command.args` queryable ("which flags are users
// passing to ox code search?") without leaking any user content
// ("what is the user searching for?"). The conservative default is to
// redact aggressively: free-form input belongs in command-specific
// attributes (e.g. ox.code.search.query) where redaction can be tuned
// to the field, not at the args level.
//
// Behavior:
//
//   - "--name=value"        -> "--name=<REDACTED>"
//   - "--name", "value"     -> "--name", "<REDACTED>"
//   - "--name", "-5"        -> "--name", "<REDACTED>"  (closes the
//                              "value starts with -" leak)
//   - "-n", "value"         -> "-n", "<REDACTED>"
//   - "positional"          -> "<REDACTED>"
//   - "--bool", "--other"   -> "--bool", "--other"     (chained long flags
//                              are preserved as flag NAMES)
//   - "--", "rest..."       -> "--", "<REDACTED>", "<REDACTED>", ...
//
// We do NOT have a flag schema here, so we cannot distinguish a boolean
// flag from a value-taking flag. The discriminator we use is whether the
// next token starts with "--" — a true long flag begins with "--" by
// convention, so this lets us preserve chained boolean long flags
// (`--verbose --json`) without keeping anything that could plausibly be
// a value. Tokens starting with a single "-" (e.g. "-5", "-k", "-foo")
// are treated as values for the preceding flag and redacted.
//
// Known residual: a value that *literally* begins with "--" (e.g.
// `--message --secret-token` where `--secret-token` is meant as a string
// value, not a flag) will be preserved verbatim. This is statistically
// vanishingly rare for real secrets and was an explicit trade-off to
// keep `--verbose --json`-style chained long flags queryable. A
// follow-up PR can take a flag schema from cobra at the call site and
// disambiguate exactly.
//
// Pure function. Returns a fresh slice — the input is never mutated.
func RedactArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	afterDoubleDash := false

	for i := 0; i < len(args); i++ {
		tok := args[i]

		// Everything after "--" is positional regardless of leading dashes.
		if afterDoubleDash {
			out = append(out, RedactedPlaceholder)
			continue
		}
		if tok == "--" {
			out = append(out, tok)
			afterDoubleDash = true
			continue
		}

		// Long flag with embedded value: --name=value
		if strings.HasPrefix(tok, "--") && strings.Contains(tok, "=") {
			eq := strings.IndexByte(tok, '=')
			out = append(out, tok[:eq+1]+RedactedPlaceholder)
			continue
		}

		// Short flag with embedded value: -n=value (rare but possible)
		if isShortFlag(tok) && strings.Contains(tok, "=") {
			eq := strings.IndexByte(tok, '=')
			out = append(out, tok[:eq+1]+RedactedPlaceholder)
			continue
		}

		// Bare long flag (--name) or bare short flag (-n).
		// The next token is treated as the flag's value and redacted
		// UNLESS it is the "--" terminator OR begins with "--" (a
		// chained long flag we want to preserve in telemetry). This
		// closes the "value starts with -" leak (e.g. --limit -5)
		// while keeping chained boolean long flags queryable (e.g.
		// --verbose --json) AND honoring "--" as a real terminator
		// even when it appears immediately after a flag (e.g.
		// `--verbose -- positional` — the "--" must reach the
		// terminator branch on the next iteration, not get consumed
		// here as a value of --verbose).
		if strings.HasPrefix(tok, "-") && len(tok) > 1 {
			out = append(out, tok)
			if i+1 < len(args) && args[i+1] != "--" && !isLongFlag(args[i+1]) {
				out = append(out, RedactedPlaceholder)
				i++
			}
			continue
		}

		// Plain positional — redact.
		out = append(out, RedactedPlaceholder)
	}
	return out
}

// isLongFlag returns true for tokens that begin with "--" and have at
// least one name character after the dashes. Used as the discriminator
// for "should I redact the next token as a value?": only chained long
// flags survive as flag names, everything else is conservatively
// treated as a value.
func isLongFlag(tok string) bool {
	return len(tok) >= 3 && tok[0] == '-' && tok[1] == '-'
}

// isShortFlag returns true for tokens like "-n" or "-n=value" (single-dash
// followed by non-dash). Used to spot the rare `-x=value` form.
func isShortFlag(tok string) bool {
	return len(tok) >= 2 && tok[0] == '-' && tok[1] != '-'
}
