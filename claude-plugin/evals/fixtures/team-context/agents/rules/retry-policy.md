---
name: retry-policy
description: How every Acme client retries calls to the artifact store
audience: agents
visibility: always
status: active
from-discussion: 2026-08-19 reliability review
---

# Retry policy for artifact-store clients (ADR-007)

Decided 2026-08-19 after the third on-call page in a month. Any client that
calls the artifact store MUST retry transient failures with **exponential
backoff, capped at 3 attempts** (initial delay 200ms, factor 2, jitter). Never
retry on 4xx. Never retry without a cap — the unbounded loop in the old
uploader is what held a deploy open for forty minutes.

Rationale, alternatives considered, and the incident timeline are in
`docs/adr-007-retry-with-backoff.md`.
