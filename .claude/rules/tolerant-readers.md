---
paths:
  - "cmd/**/*.go"
  - "internal/**/*.go"
  - "pkg/**/*.go"
---

# Readers ignore fields they don't know

**Scope:** any code that reads JSON another program or release wrote: a server
response, a config file another tool owns (Claude Code's `settings.json`), a
ledger or cache file an older or newer ox may have written.

## The rule

- **Ignore unknown fields; validate the ones you know.** Decode with
  `json.Unmarshal` or a `json.Decoder` without `DisallowUnknownFields()`.
- **The writer can add a field at any time.** The server, Claude Code, or the
  next ox release must never be able to break this one by adding data.
- **Breaking changes bump a version.** If a field's meaning changes, the
  writer bumps the schema version and the reader refuses that version with a
  clear reason.

## When strict is right

Say why on the line or the line above: `// strict-reader-ok: <reason>`.

- **A shape with a closed, tiny field set where a typo is the real risk**
  (a hook entry's `matcher`/`hooks`, in `internal/hooks/claude/validate.go`).
- **Tests asserting what ox itself writes.**

Never strict on a nested object a third party extends. Claude Code's hook
objects gained `timeout`, and a strict decode made `ox doctor` report a valid
hook as broken.

## The test

- **Input:** a real document plus an unknown field, as a newer writer sends it.
- **Assert:** it is accepted with the same result.
- **Red-first:** make the reader strict and watch the test fail.
- **Example:** the `hook with a timeout` case in
  `internal/hooks/claude/validate_test.go`.
