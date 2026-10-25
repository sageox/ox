---
title: ADR-007 — Retry artifact-store calls with capped exponential backoff
description: Every artifact-store client retries transient failures with exponential backoff, max 3 attempts
visibility: indexed
when: adding or changing any code that calls the artifact store, or any retry logic
---

# ADR-007 — Retry artifact-store calls with capped exponential backoff

**Status:** Accepted · **Date:** 2026-08-19 · **Owner:** Devon (platform)

## Context

Three on-call pages in August traced to the artifact store returning 503 for a
few seconds during its nightly compaction. Callers that gave up on the first
error failed the whole deploy. One caller that retried without a cap held a
deploy open for forty minutes.

## Decision

All artifact-store clients retry **transient** failures (network errors and
5xx responses) with exponential backoff: initial delay 200ms, factor 2, full
jitter, **maximum 3 attempts**. 4xx responses are never retried.

## Alternatives rejected

- Unbounded retry — caused the forty-minute deploy.
- Fixed 1s sleep, 5 attempts — masks the compaction window poorly and adds
  latency on every failure.
- Store-side queueing — right long-term, not available this quarter.

## Consequences

Every upload path gains a small retry helper. A retry that exhausts its cap
returns the last error unchanged so callers can still distinguish 4xx.
