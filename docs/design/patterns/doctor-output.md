# Pattern · `ox doctor` output

How `ox doctor` composes catalog primitives into its canonical output.

## Composition

```
┌────────────────────────────────┐
│ Box (header / title)           │
├────────────────────────────────┤
│ Timeline                       │
│   ├─ Node: Auth                │
│   │   └─ TimelineItem(s)       │
│   ├─ Node: Workspace           │
│   │   └─ TimelineItem(s)       │
│   └─ ...                       │
├────────────────────────────────┤
│ RenderSummaryBox               │
│   pass / warn / fail / skip    │
│   + actionable hint            │
└────────────────────────────────┘
```

## Key decisions

- **Timeline, not table.** Doctor results are *sequential* — auth before workspace before ledger. A table flattens that ordering; a timeline preserves it visually.
- **SummaryBox closes the report.** The user's eye lands on a single line of counts plus a `Run \`ox doctor --fix\`` hint. Doctor's job is to *resolve*, not just *describe* — the hint is the resolution path.
- **Streaming render in `--fix` mode.** Each `TimelineNode` renders as it completes; `RenderTimelineConnector` bridges them. The asciinema recording in the catalog captures this motion.

## Source

- Implementation: [`cmd/ox/doctor.go`](../../../cmd/ox/doctor.go)
- Components used: [Box](../components/box.md), [Timeline](../components/timeline.md)
