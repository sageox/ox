---
name: ox-cli-attest
description: Publish a frozen Attest run to SageOx with `ox attest publish --from <export> --out <dir>`. Read BEFORE publishing an Attest report, regenerating a report from a checkout, or debugging a failed or interrupted publish — ox publishes the frozen export only and never reconstructs a report from current files.
---

# ox attest

**What it does:** packages and publishes a **frozen** Attest run directly to SageOx.

```bash
ox attest publish --from <frozen-export> --out <directory>
```

Both flags are required. `--out` receives the deterministic ZIP and a durable resume
journal.

## The one rule that matters

**ox reads the frozen export only. It never reconstructs a report from the current
checkout.**

The export must have been produced *when the run completed*. This is not a convenience
constraint — it is the whole point of an attestation. A report rebuilt from today's files
would attest to today's code while claiming to describe the run, which is worse than no
report: it looks like evidence and is not.

So if the export is missing, the answer is to re-run the producer, never to point
`--from` at a working directory and hope.

## Flags

| Flag | Purpose |
|---|---|
| `--from` | the frozen producer export directory (**required**) |
| `--out` | directory for the ZIP and resume journal (**required**) |
| `--open` | open the published report when processing finishes |
| `--json` | emit the credential-free publication result as JSON |

`--json` output is **credential-free** by construction, so it is safe to log or hand to
another tool.

## When a publish is interrupted

`--out` holds a **durable resume journal**, so an interrupted publish is resumable rather
than restartable. Before re-running from scratch, look there — and keep the same `--out`
so the journal is found. Pointing at a fresh directory discards the progress record and
makes a resumable failure into a full re-upload.

## Reading the output

The ZIP is **deterministic**: the same frozen export produces the same bytes. That is what
makes the artifact checkable by someone who does not trust the machine that produced it,
and it means a byte difference between two publishes of one export is a real signal, not
noise.

## For AI coworkers

- Never offer to "regenerate" or "rebuild" an Attest report from the repository. That is
  the one thing this command exists to refuse.
- If `--from` or `--out` is missing the command errors immediately and says which — do not
  guess a path; ask.
- Report the publication result from `--json` rather than re-describing it from the human
  output; it is the stable contract.
