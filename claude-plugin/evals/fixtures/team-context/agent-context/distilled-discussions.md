# Distilled discussions — Acme Engineering

## 2026-08-19 reliability review (Devon, Sam, Avery)

Agreed that the artifact store's compaction window is here to stay for the
quarter, so every client retries with capped exponential backoff — ADR-007.
Devon to own the shared helper; Avery to fix the uploader first.

## 2026-08-26 configuration precedence (Sam, Riley)

Riley hit a staging deploy that read a committed config file over the
environment. Settled the precedence order in ADR-012: env > local file > team
defaults, no merging across layers.
