# Rule: Authoring customer-intent BDDs for the ox CLI

**Scope:** the `.feature` acceptance corpus under `tests/acceptance/` and the
tests that demonstrate its customer promises. Read this before adding,
editing, or claiming executable coverage for any `.feature` capability.

---

## 1. The altitude — what these BDDs test, and what they do NOT

**A BDD here is an end-to-end, customer-facing flow test. It asserts what a
coworker *experiences* at the terminal and in the product — not how the code is
built.** It is a *different lens*, at a *different level*, than the unit and
integration tests. It is **not a replacement** for either.

| Lens | Question it answers | Lives in | Example |
|---|---|---|---|
| **BDD / capability** (this rule) | "Can the customer do the thing, see it work, and recover when it fails?" | `tests/acceptance/**/*.feature` (+ its executable proof) | *Aborting a recorded session removes it and all its summarized data — from the Ledger, not just my machine.* |
| **Integration** | "Do these real subsystems, wired together against a real git remote / real files, keep the contract?" | `cmd/ox/*_test.go` E2E tests (real bare remote, real `pushLedger`) | `TestAbort_KillsFinalizedSessionAndAllSummarizedData` drives `runAgentSessionAbort` and asserts on a bare remote. |
| **Unit** | "Does this function return the right value for each input, including the failure paths?" | `internal/**/*_test.go`, table-driven | `TestResolveSessionRecording_PrecedenceMatrix`. |

The unit test proves a helper; the integration test proves the subsystems
interoperate; the **BDD proves the customer promise still holds end to end.** A
capability is not covered because a unit test exists — it is covered when the
*promise* has a red-first proof. Derive the mechanics tests separately from the
customer journey.

**Write the customer journey first, the mechanics never.** State the customer's
goal, starting point, natural action, the feedback they see, and how they
continue or recover. Preserve an established journey — do not silently redesign
it to make a scenario easier to automate.

---

## 2. The unit is the CAPABILITY = one Gherkin `Rule:` block

A capability is one `Rule:` block — "a claim a customer would recognize."
Write one coherent customer promise per `Rule:`.

Follow the corpus conventions in `tests/acceptance/README.md`:

- **Layout:** `tests/acceptance/features/<domain>/<name>.feature`. Author new
  capabilities there.
  *(The repo's pre-existing flat `tests/acceptance/<domain>/` files predate this;
  migrating them into `features/` is tracked separately — do not mass-move them
  in an unrelated change.)*
- **`Feature:`** opens with 2–4 sentences of user-facing prose, then `See also:`
  cross-references. No `Background:` blocks.
- **Personas** are named (`Devon`, `Avery`, `Sam`, `Riley`, `Quinn`) — never
  "the user." Each scenario name starts with actor + action.
- **Observable language only.** Say "the plan is rendered as a SageOx
  team-context-optimized HTML page," not the Go function that renders it.
  **Never** put Go function names, file paths, internal structs, routes, status
  codes, database rows, selectors, or validation regexes in Gherkin.
- **Cover the happy path plus the consequential failure, permission, and
  continuity cases** — not every internal error code (those are unit/integration
  scope, per the corpus README's "Scope discipline").
- **Proof claims:** tags and stamp comments describe intent or past runs; they
  do not establish executable coverage. Identify the test and observed result
  when reporting that a capability is covered.

---

## 3. Prove the customer promise

**Prove red-first, every time:**

1. Break the product so the scenario fails **on the step that names the
   customer claim**. Save the *unedited* failure text and the command you ran.
2. Restore; run the same test **green**; save the result.
3. Report the test, the deliberate break, and both observed results in the PR.
   A setup failure does not demonstrate that the customer claim is protected.

**A gate nobody watched fail is a gate nobody has tested.** Report both the red
and the green in the PR.

---

## 4. Executable coverage today

The `.feature` corpus is content-only: no BDD runner is wired into this
repository. See the corpus README's status before claiming scenarios run in CI.

**A Go E2E test demonstrates a capability** by driving the real command surface
and asserting the customer-observable outcome. Use scratch repositories, real
bare remotes, and the real downstream flow. The `.feature` `Rule:` is the
human-readable contract; identify the corresponding test explicitly and verify
that it fails when the customer promise is broken.

A future CLI-oriented runner is separate work. Keep the customer journey
independent of the harness so it remains useful when execution is wired.

---

## 5. No test theater

Each capability must state, in one line, **the real customer failure it
prevents** and **the observable difference** its proof asserts (a Ledger commit
that appears or doesn't, a `/c/<id>` link that resolves or 404s, a commit
trailer present or absent, a secret that reaches the remote or doesn't). If the
proof would pass with the feature removed, it proves nothing. A `.feature` with
no executable proof is a specification — say so, do not present it as covered.

For a settings-driven capability, the proof drives the **real `ox …` command**
and observes the **downstream flow**, never a resolver function in isolation —
that observation is the whole point of the lens.

---

## See also

- `tests/acceptance/README.md` — corpus structure, conventions, personas, scope discipline.
- `.claude/rules/testing.md` — the unit/integration philosophy this rule sits above.
