---
component: confirm
package: internal/cli
since: 0.4.0
family: input
renderer: freeze
exports: [ConfirmYesNo, ConfirmYesNoRequired, ConfirmDangerousOperation, ConfirmUninstall]
---

# Confirm

> Yes/no gates with a clear default. Stronger variants for destructive ops.

## When to use

Reversible operations: `ConfirmYesNo(prompt, defaultYes)`. Operations where taking the default silently would lose work, leave an operation half-done, or act on the user's behalf without consent: `ConfirmYesNoRequired(prompt, defaultYes, force)` — it returns `ErrConfirmationRequired` instead of guessing when nobody answered. Destructive operations: `ConfirmDangerousOperation` which requires typing the exact target name (e.g., `delete-team`), not just pressing y. `ConfirmUninstall` is the bespoke variant for repo uninstall flows.

**Rule of thumb:** if the prompt's default is `true`, it almost certainly wants `ConfirmYesNoRequired` — a silent default-yes performs the action with nobody having agreed to it.

## When NOT to use

Selecting between equal options (use [Select](select.md)). Any flow where users reflexively press enter — defaulting to dangerous is a footgun. Use `--force` flags for non-interactive bypass, never default-yes for destructive.

## Anatomy

`ox dev catalog --component=confirm` or [sageox-design.netlify.app/catalog/cli/#c-confirm](https://sageox-design.netlify.app/catalog/cli/#c-confirm).

## API

Source: [`internal/cli/confirm.go`](../../../internal/cli/confirm.go)

```go
cli.ConfirmYesNo(prompt string, defaultYes bool) bool
cli.ConfirmYesNoRequired(prompt string, defaultYes, force bool) (bool, error)
cli.ConfirmDangerousOperation(operationName, exactMatch string, force bool) error
cli.ConfirmUninstall(repoName string, force bool) error
```

`ConfirmYesNoRequired` returns `cli.ErrConfirmationRequired` when stdin produced no answer at all (closed, empty, unreadable). A piped answer — `echo y | ox …` — is a real answer and is accepted; the distinction is whether an answer arrived, not whether a TTY was attached.

## Accessibility & fallbacks

- Default is highlighted with `[Y]` / `[N]` so single-press behavior is obvious.
- `--force` bypasses the prompt entirely. Destructive ops require typing the exact name; force still works but is logged.
- The global `--yes` (or `OX_YES=1`) satisfies `ConfirmYesNoRequired`. It is never inferred from CI or a missing TTY: "no human is watching" must not become "the absent human agreed."

## Tests

[`internal/cli/confirm_test.go`](../../../internal/cli/confirm_test.go)
