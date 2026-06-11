# JIT Discovery: "ox found" preambles

When you type a question like "how did we handle pagination on the activity feed?", ox quietly checks your locally-cached ledger for prior sessions, murmurs, or decisions that look relevant — and if it finds something, it prepends a short `[ox-recall]` preamble above your prompt so the agent sees it.

You did not configure this. It is on by default, and it runs entirely on your machine.

## What you will see

Above an agent response, a few lines like:

```text
[ox-recall]
2026-04-12-activity-feed · "we cursor-paginate by created_at desc with a tiebreak on id"
2026-03-30-feed-perf · "switched off offset pagination after the N+1 regression on page 30+"
```

That is the whole feature. Five lines max. The agent reads it as additional context.

## When it fires

Only when all of these are true:

- Prompt is at least roughly 40 tokens long (one-word follow-ups don't trigger).
- Prompt looks like a question or a recall ("how did we", "where is", "explain", "find", "remind me"). Pure action prompts ("write a test", "fix this") don't trigger.
- The local ledger cache has at least one matching result.
- The lookup finishes within 100ms. If it doesn't, the preamble is silently skipped and your turn continues unaffected.

If any of those fail, you see nothing — no error, no slowdown.

## Why local-only by default

Every user prompt can contain secrets pasted with a stack trace, proprietary code, or private context. Sending all of them to a remote service to "maybe find a match" was not a tradeoff worth making for the common case. So the default is: the prompt never leaves your machine.

## Opting into cloud-mode

If you want recall to also reach across team-shared SageOx content beyond your local cache:

```bash
ox config set hooks.userpromptsubmit.cloud_query true
```

Even with cloud-mode on, prompts pass through the same secret redactor used by session uploads before any byte transits the network. If you have not run `ox login`, cloud-mode silently falls back to local-only.

## Turning it off

```bash
ox config set hooks.userpromptsubmit.enabled false
```

To see the current state and tradeoffs:

```bash
ox doctor --check=hooks
```

That's it. If the preambles ever feel noisy, lower `hooks.userpromptsubmit.max_results` or raise `hooks.userpromptsubmit.min_tokens` until the signal-to-noise feels right.
