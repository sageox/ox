<!-- doc-audience: preserve-voice -->

# Pattern · Session timeline

How a recorded AI coworker session renders in `ox session view` and the dashboard.

## Composition

```
┌─────────────────────────────────────────┐
│ Box (session title + metadata)          │
├─────────────────────────────────────────┤
│ Sparkline (turn cadence, session-wide)  │
├─────────────────────────────────────────┤
│ Timeline                                │
│   ├─ Node: Prompt                       │
│   │   └─ Markdown(user turn body)       │
│   ├─ Node: Response                     │
│   │   └─ Markdown(assistant turn body)  │
│   ├─ Node: Tool use × N                 │
│   │   └─ Log-formatter(tool output)     │
│   └─ ...                                │
└─────────────────────────────────────────┘
```

## Key decisions

- **Markdown inside Timeline.** Long-form turn bodies need structured rendering; `ui.RenderMarkdown` is delegated per item rather than re-implementing prose handling at the timeline level.
- **Log-formatter for tool output.** Tool stdout is structured slog where possible; raw lines otherwise. Single-line per .claude/rules/design.md rule #8.
- **Sparkline at the top, not bottom.** Unlike `ox status` (where the sparkline overlays static facts), the session view leads with cadence to anchor the reader before they read individual turns.

## Source

- Implementation: [`cmd/ox/session_view.go`](../../../cmd/ox/session_view.go) (and dashboard counterparts)
- Components used: [Box](../components/box.md), [Timeline](../components/timeline.md), [Markdown](../components/markdown.md), [Sparkline](../components/sparkline.md), [Log formatter](../components/log-formatter.md)
