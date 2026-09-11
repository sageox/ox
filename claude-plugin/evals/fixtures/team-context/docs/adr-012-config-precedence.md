---
title: ADR-012 — Runtime configuration precedence
description: Environment variables override the local config file, which overrides team defaults
visibility: indexed
when: touching configuration loading, adding a new setting, or answering how a setting is resolved
---

# ADR-012 — Runtime configuration precedence

**Status:** Accepted · **Date:** 2026-08-26 · **Owner:** Sam (developer experience)

## Decision

Settings resolve in this order, highest priority first:

1. **Environment variables** (`ACME_*`) — the deployment's say.
2. **Local config file** (`.acme/config.toml`) — the developer's say.
3. **Team defaults** shipped in the binary — the team's say.

A value set at a higher layer always wins; lower layers are never merged into
it. `config.Load()` must document the layer it took each value from.

## Why not the reverse

Twelve-Factor: deployment configuration belongs to the environment, not the
checkout. Letting a checked-in file override the environment is how staging
once pointed at production.
