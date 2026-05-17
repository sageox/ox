<!-- doc-audience: preserve-voice -->

# Pattern · `ox login` flow

How the OAuth login flow composes catalog primitives.

## Composition

```
┌─────────────────────────────────────────────────────────────┐
│ Box · "Opening sageox.ai/cli/auth in your browser..."       │
└─────────────────────────────────────────────────────────────┘
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Spinner — "Waiting for authorization..."                    │
│   ⠋ press Ctrl+C to cancel                                  │
└─────────────────────────────────────────────────────────────┘
        │ (browser OAuth callback fires)
        ▼
┌─────────────────────────────────────────────────────────────┐
│ ✓ Authenticated as ryan+ox@sageox.ai                        │
│   Endpoint: sageox.ai                                       │
│   Tip: run `ox init` in a repo to get started.              │
└─────────────────────────────────────────────────────────────┘
```

## Key decisions

- **Bubbletea, but minimal.** `loginSpinnerModel` is the smallest possible full-screen program — one spinner, one message line, one `Ctrl+C` handler. Anything more would be ceremony.
- **Browser-first.** ox opens the OAuth URL in the user's default browser via `cli.OpenInBrowser` (handles headless + cross-platform). The spinner is a *passive listener* for the local callback — it has no UI to redirect the user to.
- **Ctrl+C means cancel, not "kill ox".** The model exits cleanly, the local listener closes, the user can retry.
- **Success is a single block.** Three lines max: who, where, what's next. Avoid a wordmark or banner here — `ox login` is a step, not a destination.

## Source

- [`cmd/ox/login.go`](../../../cmd/ox/login.go) — `loginSpinnerModel`
- [`internal/cli/output.go`](../../../internal/cli/output.go) — `OpenInBrowser`

## Components used

[Box](../components/box.md) (opening message + success summary) · [Spinner](../components/spinner.md) (the wait loop)

## Related patterns

The `ox init` [wizard](init-wizard.md) is the natural next step after `ox login`.
