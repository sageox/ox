# acme

Internal upload service for Acme Engineering. Chunks artifacts, uploads them
to the artifact store, and exposes a small paginated listing API.

- `internal/upload` — artifact upload client
- `internal/pagination` — page math for the listing API
- `internal/config` — runtime configuration loading
