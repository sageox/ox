# Rule: every `--json` payload goes through `cli.PrintJSON`

**One printer, two audiences.** `internal/cli/json.go` is the only place ox
turns a command result into JSON on a terminal stream.

```go
// RIGHT
return cli.PrintJSONTo(cmd.OutOrStdout(), payload)

// WRONG — opts this command out of color, indent, and newline consistency
enc := json.NewEncoder(os.Stdout)
enc.SetIndent("", "  ")
return enc.Encode(payload)
```

| Call | Use it for |
|---|---|
| `cli.PrintJSON(v)` | stdout, where the caller cannot return an error. Returns nothing — a broken stdout is not actionable. |
| `cli.PrintJSONTo(w, v) error` | the normal case: `return cli.PrintJSONTo(cmd.OutOrStdout(), payload)` |
| `cli.MarshalJSONIndent(v) ([]byte, error)` | you need the bytes — to measure the payload an AI coworker consumes, embed it in a markdown fence, or hash it. Always plain. |
| `cli.WriteJSONBytes(w, b) error` | you encoded it yourself: `SetEscapeHTML(false)`, or a `--pretty` flag picking compact. Keeps your encoding, still themed. |

`MarshalJSONIndent` + `WriteJSONBytes` is the pair for measure-then-print:
encode once, count the **plain** bytes, print the themed form.

## Never colorize these

Color belongs on a terminal stream and nowhere else. Leave the encoder alone
when the bytes go to:

- **a file** — `os.WriteFile`, `AtomicWriteBytes`, any marker, settings, or
  state file;
- **an HTTP response body** — `json.NewEncoder(w)` where `w` is an
  `http.ResponseWriter` (`plan_review.go`);
- **anything hashed, signed, compared, or sent over IPC**;
- **JSON embedded in rendered markdown** — `agent_prime.go --review` puts the
  payload in a fence that `ui.RenderMarkdown` already colorizes; a second pass
  would corrupt it.

When in doubt, leave it plain. A missed migration is cosmetic; a colorized file
write is a corrupted file.

## Why it colors, and why that is safe

A `--json` payload has two audiences with opposite needs:

| Audience | Wants | Gets |
|---|---|---|
| Human at a terminal reading `ox addons list --json` | structure they can scan | syntax color: keys bold, strings, numbers, `true/false/null`, dim punctuation |
| AI coworker, `jq`, a test, a redirect | byte-exact parseable JSON | plain bytes, no escape sequence |

**Nobody has to declare which they are.** An agent's stdout is a pipe, not a
TTY, and `theme.Profile` already resolves a pipe to a no-color profile. So the
same call serves both, and colorized JSON is structurally unable to reach
something that will parse it — piping, redirecting, and capturing all take the
no-TTY path.

`theme.ColorEnabled()` is the switch. Call it whenever "no color" means *a
different rendering path*, not merely a different color — text attributes like
`Bold` still emit `\x1b[1m` under a no-color profile, so checking the profile is
not optional if the output must be byte-exact.

## Non-negotiables

- **The colorizer only ever adds escape sequences.** Stripping them must
  reproduce `json.MarshalIndent` output byte for byte
  (`TestColorizeJSON_PreservesEveryByte`). A colorizer bug may lose color; it
  may never lose content.
- **It lexes, it does not pattern-match.** A string value containing `{` or `:`
  or an escaped quote is ordinary in an ox summary and defeats any regex.
  `TestEndOfJSONString` owns that boundary.
- **Two-space indent, exactly one trailing newline.** Part of the stdout
  contract; `--json` consumers split on it.
- **Diagnostics still go to stderr.** `cli.PrintError` / `cli.PrintWarning`
  keep stdout a single JSON document — see `docs/specs/cli-design-system.md`.

## See also

- `.claude/rules/design.md` rule 3 (semantic styles) and rule 7 (`NO_COLOR` is sacred).
- `docs/specs/cli-design-system.md` — which stream each helper writes to.
