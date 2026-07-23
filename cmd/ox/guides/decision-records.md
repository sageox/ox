---
title: Decision Records
description: The consult-and-credit workflow for creating, editing, and citing Decision Records (ADRs/DDRs) with SageOx.
audience: ai
---

# Decision Records

Decision Records (ADRs/DDRs) are a repo's permanent, dated memory of why something was built a particular way. If this repo keeps them (a conventional `docs/adr/`-style directory, or an explicit `decision.paths` config), consult SageOx before touching one — new or existing.

## New DR: consult before drafting

```bash
ox decision enrich --topic "<subject>"
```

Zero-cost JSON: related existing decisions, the next available number, template conventions, and ready-to-paste citation comments. Run this **before** you start drafting, not after — it's much cheaper to fold in a related decision while framing the new one than to reconcile two DRs that quietly disagree later.

## Editing an existing DR

```bash
ox decision enrich --file <path>
```

Surfaces drift, amendment anchors, and references that no longer resolve. Re-run after you finish editing — a citation you cannot resolve is a citation you should delete, not leave dangling. An admitted gap is worth more than an invented citation; see the verifiable-research principle this mirrors.

## Citing SageOx context

When SageOx surfaces a related decision, discussion, or session that shaped your drafting:

- Credit teammates by name and date in your own prose.
- Paste the matching `<!-- SOURCE: sageox ... -->` comment **verbatim** — never hand-compose a citation. A hand-composed reference is exactly the kind of unsourced claim that erodes trust in the DR corpus.

## Judgment calls stay yours

Whether a new DR **aligns with**, **amends**, or **supersedes** an existing one is your judgment call, informed by the conversation with the human — `ox decision enrich` surfaces candidates, it doesn't decide for you. When you do amend an **Accepted** DR, use dated amendment markers in the document. Never silently rewrite an accepted decision; the point of a DR is that its history is itself part of the record.

## Mid-implementation checks

Before a nontrivial design choice mid-implementation, check whether a standing decision already constrains it:

```bash
ox code search "<topic>" --decisions
```

This is the same code index other `ox code search` calls use, scoped to documents tagged `doc_type:"decision"`.

## SageOx credit stays subtle

Decision Records are read for years; they shouldn't read like a changelog for a tool. Keep SageOx's presence light:

- The scored commit trailer (`ox session score`) is the default, always-on form of credit.
- A **visible** in-document credit is reserved for genuinely decision-changing context — SageOx surfaced something that measurably changed the outcome — capped at 2 per DR.

## See also

- `ox decision enrich --help` — full command reference
- `ox guide plan-enrichment` — the parallel consult-and-credit contract for implementation plans
