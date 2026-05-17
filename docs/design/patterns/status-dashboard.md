<!-- doc-audience: preserve-voice -->

# Pattern · `ox status` dashboard

## Composition

```
┌──────────────────────────────────────┐
│ Box (workspace + endpoint header)    │
├──────────────────────────────────────┤
│ Columns                              │
│   REPO   TEAM   LEDGER   AGE         │
│   ...                                │
├──────────────────────────────────────┤
│ Sparkline (session activity, 4h)     │
│   ▁▂▃▄▅▆▇█▆▄▃▂▁                      │
│   4h ago        2h        now        │
├──────────────────────────────────────┤
│ Box (next-action hint, optional)     │
└──────────────────────────────────────┘
```

## Key decisions

- **Repo list first, sparkline second.** Static facts (what repos, what state) anchor the page. The sparkline is the "where is energy going right now" overlay below.
- **Columns clamps widths at 80.** `ox status` works in tmux panes, narrow SSH sessions, CI logs. Long repo names get truncated via `TruncateID` rather than blowing up alignment.
- **Hint box is conditional.** If the workspace is healthy with no follow-up, the hint box doesn't render at all. Empty space is information.

## Source

- Implementation: [`cmd/ox/status.go`](../../../cmd/ox/status.go)
- Components used: [Box](../components/box.md), [Columns](../components/columns.md), [Sparkline](../components/sparkline.md)
