# Published schemas

Machine-readable descriptions of the file formats ox writes. They exist so
anything — another agent, another vendor's tooling, a future us — can read a
SageOx session without importing our Go code.

| Schema | `$id` | Describes |
|---|---|---|
| [`v1/raw-jsonl.schema.json`](v1/raw-jsonl.schema.json) | `https://sageox.ai/schemas/session/v1/raw-jsonl.schema.json` | One line of a session `raw.jsonl` |

**License: MIT**, same as the rest of this repo. Copy these into your own
validator, vendor them, generate types from them — no attribution dance needed
for an interface description.

## How to use one

A `raw.jsonl` file is [JSON Lines](https://jsonlines.org/): validate each line
independently against the schema. There is no schema for the file as a whole,
because JSON Lines has no whole-file JSON document to validate.

```bash
# every line of a session must validate
while read -r line; do
  printf '%s' "$line" | check-jsonschema --schemafile schema/v1/raw-jsonl.schema.json -
done < sessions/<name>/raw.jsonl
```

## The rules these schemas deliberately do not express

JSON Schema validates one line. It cannot express file-level or semantic rules,
so those stay normative in
[`docs/specs/session-raw-jsonl.md`](../docs/specs/session-raw-jsonl.md) and are
enforced in Go:

- **Line ordering** — header first, footer last. Not universal in practice
  (imported and in-progress sessions have no footer), which is exactly why it
  isn't a schema constraint.
- **Completeness** — `raw.jsonl` must be the full, unfiltered agent output. A
  truncated file is perfectly valid JSON and completely wrong.
- **Secret redaction** — the only transformation applied before writing.
- **The PII boundary** — `username` is a privacy-safe display name, never an
  email. The schema says so in its `description`; nothing can make a validator
  enforce it.

## Open by construction

Every object in these schemas omits `additionalProperties`, i.e. unknown
properties are allowed. That is deliberate and it is the whole compatibility
promise:

> A consumer holding a v1 copy of the schema must keep validating files written
> by a newer ox.

`additionalProperties: false` would turn every additive field into a validation
break for every deployed validator. Strictness belongs on the **writer** side,
where we control both ends — see `internal/session` and the corpus test — not on
the schema other people run.

Closed vocabularies live in `enum`s instead, and there are deliberately very few.
Entry `type` is **not** enumerated: the shipped vocabulary is wider than the spec
ever documented (`user`, `assistant`, `system`, `tool`, `message`, `tool_call`,
`tool_result`), and a reader that rejects an unfamiliar type would be wrong.
Preserve what you don't recognize.

## Traps the schema documents

Writing this schema against real fixtures surfaced several things the prose spec
had wrong or silent about. They are now in the schema's `description` fields,
where a consumer will actually meet them:

- **`session_id` means two different things.** In a native header it is the
  `ses_` recording identity; in the import dialect it is an agent-supplied
  identifier. Conflating them attributes a session to the wrong recording.
- **`tool_input` / `tool_output` are string *or* object.** ox writes a string;
  the import dialect writes a decoded object. Checking only for a string
  silently drops every imported tool call's arguments.
- **`username` is not an email**, and must never become one — `raw.jsonl` is
  committed to a ledger repo.
- **Aliases**: `ts`→`timestamp`, `tool`→`tool_name`, `ox_username`→`username`,
  `started_at`→`created_at`.
- **`seq` is zero-based from ox, one-based from importers.** It is an ordering
  hint, never an identity.

## Versioning

The directory is the version. Within `v1/`, changes are **append-only**: no
property removed, no `required` entry added or removed, no `enum` value removed,
no type narrowed. Anything else mints `v2/` and freezes `v1/` forever, because
the `$id` is a URL that other people's validators fetch — it can never change
meaning under them.

## Not published here

- **The adapter protocol wire types** (`pkg/adapterprotocol`, incl. `RawEntry`)
  — a different contract with its own version constant and its own reference at
  [`docs/specs/adapter-protocol.md`](../docs/specs/adapter-protocol.md).
  `adapterprotocol.RawEntry` is what an *adapter hands to ox*; the schema here is
  what *ox writes to disk*. They are related but not the same shape, and giving
  one format two homes is how drift starts.
- **Derived session artifacts** (`summary.json`, `session.html`, `meta.json`) —
  no external reader today.
