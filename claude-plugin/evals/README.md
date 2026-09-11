# ox plugin evals

With-vs-without behavioral evals for the ox Claude Code plugin, run by
`claude plugin eval`. Every other test tier proves ox *delivered* context; these
prove the model *took it up*. Full design, case catalog, and how to read a
result: [`docs/specs/agent-evals.md`](../../docs/specs/agent-evals.md).

```bash
make eval-scaffold-check   # free: sandbox seeds, prime runs offline, seeded bug is red
make eval-smoke            # ~$3: one run of the four cheapest cases
make eval                  # full suite, EVAL_RUNS=3, capped by EVAL_MAX_COST=15
```

Layout: one directory per case (`case.yaml` + a `scaffold.sh` stub), a shared
`scaffold/seed.sh` that builds the anonymized Acme sandbox from `fixtures/`,
and `results/` (gitignored). Nothing here ever touches a real ledger, team
context, or SageOx host — `scaffold/check.sh` asserts that.
