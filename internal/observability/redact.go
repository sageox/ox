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
//   - "--name=value"     -> "--name=<REDACTED>"
//   - "--name", "value"  -> "--name", "<REDACTED>"
//   - "-n", "value"      -> "-n", "<REDACTED>"
//   - "positional"       -> "<REDACTED>"
//   - "--bool-flag"      -> "--bool-flag" (untouched, no value)
//   - "--", "rest..."    -> "--", "<REDACTED>", "<REDACTED>", ...
//
// We do NOT have a flag schema here, so we cannot distinguish a boolean
// flag from a value-taking flag. The heuristic: if the *next* token also
// looks like a flag (starts with "-"), the current flag has no value;
// otherwise the next token is treated as a value and redacted. This
// matches what most shells and shell-history scrubbers do.
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
		// If the next token does not look like another flag, treat it
		// as the value and redact it. Otherwise, leave the flag alone
		// (it's a boolean / standalone flag).
		if strings.HasPrefix(tok, "-") && len(tok) > 1 {
			out = append(out, tok)
			if i+1 < len(args) && !looksLikeFlag(args[i+1]) {
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

// looksLikeFlag returns true if tok would be parsed as a flag by most CLIs:
// it begins with "-" and has at least one more character (so "-" alone — a
// common stdin sentinel — is treated as a positional argument).
func looksLikeFlag(tok string) bool {
	return len(tok) >= 2 && tok[0] == '-'
}

// isShortFlag returns true for tokens like "-n" or "-n=value" (single-dash
// followed by non-dash). Used to spot the rare `-x=value` form.
func isShortFlag(tok string) bool {
	return len(tok) >= 2 && tok[0] == '-' && tok[1] != '-'
}
