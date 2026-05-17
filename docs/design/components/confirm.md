<!-- doc-audience: preserve-voice -->
---
component: confirm
package: internal/cli
since: 0.4.0
family: input
renderer: freeze
exports: [ConfirmYesNo, ConfirmDangerousOperation, ConfirmUninstall]
---

# Confirm

> Yes/no gates with a clear default. Stronger variants for destructive ops.

## When to use

Reversible operations: `ConfirmYesNo(prompt, defaultYes)`. Destructive operations: `ConfirmDangerousOperation` which requires typing the exact target name (e.g., `delete-team`), not just pressing y. `ConfirmUninstall` is the bespoke variant for repo uninstall flows.

## When NOT to use

Selecting between equal options (use [Select](select.md)). Any flow where users reflexively press enter — defaulting to dangerous is a footgun. Use `--force` flags for non-interactive bypass, never default-yes for destructive.

## Anatomy

`ox dev catalog --component=confirm` or [sageox-design.netlify.app/catalog/cli/#c-confirm](https://sageox-design.netlify.app/catalog/cli/#c-confirm).

## API

Source: [`internal/cli/confirm.go`](../../../internal/cli/confirm.go)

```go
cli.ConfirmYesNo(prompt string, defaultYes bool) bool
cli.ConfirmDangerousOperation(operationName, exactMatch string, force bool) error
cli.ConfirmUninstall(repoName string, force bool) error
```

## Accessibility & fallbacks

- Default is highlighted with `[Y]` / `[N]` so single-press behavior is obvious.
- `--force` bypasses the prompt entirely. Destructive ops require typing the exact name; force still works but is logged.

## Tests

[`internal/cli/confirm_test.go`](../../../internal/cli/confirm_test.go)
