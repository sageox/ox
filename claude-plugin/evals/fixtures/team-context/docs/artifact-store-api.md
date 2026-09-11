---
title: Artifact store HTTP API
description: Endpoints, status codes, and payload limits for the artifact store
visibility: indexed
when: calling the artifact store directly or debugging a non-2xx response from it
---

# Artifact store HTTP API

- `POST /artifacts` — octet-stream body, max 64 MiB. 201 on success.
- `GET /artifacts/{id}` — 200 with the blob, 404 if unknown.
- 503 during the nightly compaction window (see ADR-007 for the retry policy).
