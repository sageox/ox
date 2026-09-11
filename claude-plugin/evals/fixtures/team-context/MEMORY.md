# Acme Engineering — team memory

- Artifact-store outages are the #1 source of on-call pages. Every client that talks to the store retries — the exact policy is the `retry-policy` team rule (ADR-007).
- Runtime configuration precedence has a settled order; ADR-012 in the team docs is the source of truth, do not guess it.
- The listing API's pagination math lives in `internal/pagination`; keep `Bounds` half-open.
