# Provenance

Captured 2026-10-05 from a scratch repo (`SessionImportTesting`) with `scripts/session_import_testbed.py capture`.
These are real Claude Code and Codex Desktop sessions, cleaned to be safe to publish:

- **Kept:** only the records and fields `ox session import` reads, plus structural fields
  (ids, parents, timestamps) that keep each file shaped like the real thing.
- **Dropped:** model reasoning, vendor system prompts and prompt snapshots, safety-classifier
  context, token usage, app UI events and attachments.
- **Blanked:** `<ox-prime>` blocks (SageOx context the session loaded).
- **Capped:** tool inputs and outputs over 4096 bytes.
- **Templated:** the repo path is `__REPO__` and the home directory `__HOME__`; `create` fills them in.
- **Replaced:** the author's name, emails, user name and host name with the persona
  "Devon" (`devon@example.com`).

| Agent | File | Records | Size |
|---|---|---|---|
| claude | `077ecc79-bdf7-4708-b558-7ff5dd82fe5b.jsonl` | 38 | 28 KB |
| claude | `185a01e8-9b38-46b6-a686-b6a4e3509c3d.jsonl` | 2 | 0 KB |
| claude | `21bb267b-4585-4b4c-b7ca-062f545259f0.jsonl` | 96 | 128 KB |
| claude | `8fa7dffa-45e9-4298-91be-05a4ea0df37b.jsonl` | 276 | 314 KB |
| codex | `rollout-2026-10-01T12-32-04-01a0f8f3-d483-7643-b43d-e3fa4d8cee96.jsonl` | 60 | 38 KB |
| codex | `rollout-2026-10-01T12-33-47-01a0f8f5-6803-79e0-a8aa-3e75efe44070.jsonl` | 12 | 26 KB |
| codex | `rollout-2026-10-01T14-20-40-01a0f957-40ec-7192-b67b-71c990b28f23.jsonl` | 77 | 164 KB |
| codex | `rollout-2026-10-01T14-39-22-01a0f968-60ef-7500-aaa4-13cbb34e5557.jsonl` | 80 | 179 KB |
