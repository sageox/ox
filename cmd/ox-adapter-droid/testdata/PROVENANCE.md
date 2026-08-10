# testdata/session-real.jsonl provenance

## Original capture

Captured verbatim from a transcript a real Factory Droid 0.126.0 binary
wrote under `~/.factory/sessions/<project-slug>/<uuid>.jsonl`, with only
cwd, session id, and owner anonymized. Nothing else was changed.

That verbatim capture also carried, unredacted:

- the full harness-injected system-reminder text (a deferred-tool catalog,
  a skill catalog, and a real `ls` of the capturing machine's home
  directory — real project and file names unrelated to this repo)
- the model's real local Ollama configuration and today's-date value
- full assistant chain-of-thought reasoning text, including the droid
  system prompt's own behavioral instructions to the model

None of that is needed to prove the parser handles droid's real JSONL
shape: the parser only reads `type`, `message.role`, and
`message.content[].type` / `.text` / `.thinking`.

## What this fixture keeps

- Every record type, field name, nesting level, and content-block
  ordering droid actually writes.
- The `context-<id>` / `parentId` chaining convention droid uses to link
  a harness-injected reminder message to the real user turn that follows
  it (a multi-block `content` array on that reminder message, including
  the `visibility` field).
- The `thinking` + `text` block pair on an assistant turn, and the
  duplicate top-level `chatCompletionReasoningContent` field droid also
  writes alongside it.
- The companion `session-real.settings.json` file, unchanged — it is
  telemetry only (token counts, active-time ms, model id), never carried
  conversation content or identifying information.

## What this fixture replaces

Every piece of real conversation, environment, and reasoning text is
replaced with a short bracketed placeholder (e.g. `[placeholder: ...]`).
The one exception is the final assistant reply text and the "Hello?"
user turn, which were already generic and carried no privacy risk.

## Size change

Reduced from 6 lines / 2 conversational turns / 5 entries to 4 lines / 1
conversational turn / 3 entries (2 user, 1 assistant) — the second Q&A
exchange in the original capture ("What is today's date?") added no new
structural shape over the first and was cut rather than sanitized in
place. `conformance_test.go`'s `Want` and `ResumePoints` were updated to
match the smaller fixture.
