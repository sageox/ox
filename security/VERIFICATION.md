# Verification — proving a closed finding stays closed

When `/security-review` flags an issue and you patch it, the patch is only as good as the regression test that catches the next person reintroducing the same bug. This file is the recipe per hunter class: what to write, what to run, what to expect.

**Iron rule:** test the **root cause**, not the symptom. A test that pins the exact byte sequence the scanner reported is a regression test for the scanner, not the bug. Find the property that should hold, write a test that asserts it, then re-run the pipeline to confirm the finding is gone.

## Workflow (every class)

1. **Reproduce.** Re-run the scanner with the original diff and confirm it still flags. `make sec-fast` (or `make sec` for AI-tier findings).
2. **Locate the chokepoint.** Each hunter class has a designated chokepoint (table below). Land the regression test there.
3. **Patch.** Make the smallest change that closes the root cause.
4. **Re-run scoped.** `make sec-fast` (the deterministic tier is fast and reproducible — use it as the inner loop).
5. **Re-run full.** If the original finding came from the AI tier, run `make sec` once before opening the PR.
6. **Confirm SARIF.** `jq '.runs[].results[] | select(.ruleId == "<rule-id>")' security/.output/findings.sarif` returns empty.

## Per-hunter recipes

### #hunter-cli-input

**Chokepoint:** the validator (whatever centralizes path-cleaning, URL-scheme enforcement, or env-var parsing). If one doesn't exist yet, this finding is your prompt to add one.

**Regression test pattern:**

- Table-driven Go test under the package owning the validator.
- Inputs include: the original payload from the finding, plus the obvious family (`..\foo` for `../foo`, encoded variants, trailing slashes, empty strings, max-length strings).
- Assertion is on the validator's *return value*, not on a downstream side effect.

**Re-run:** `make sec-fast` — the OpenGrep rule for unvalidated `exec.Command` / `filepath.Join` / `http.Get` should no longer match.

**Common pitfall:** writing a test that calls the high-level function and asserts it didn't crash. That doesn't prove validation — only that no crash *yet*. Test the validator directly.

### #hunter-secrets-redaction

**Chokepoint:** `internal/session/raw_writer.go` (the `RawWriter` type) and the redactor packages it composes.

**Regression test pattern:**

- For a **chokepoint-bypass** finding (someone opened `raw.jsonl` directly): the test is a Go file-walk that asserts no source file outside `raw_writer.go` opens `raw.jsonl`. This duplicates the `make check-raw-writer-chokepoint` gate but ensures it survives even if the Makefile target gets refactored.
- For a **redactor coverage gap**: add a fixture under `internal/session/testdata/` containing the secret pattern, run it through `RawWriter`, assert the output contains `[REDACTED_*]` and does not contain the original bytes.
- For a **token-in-argv** finding: parse the offending command's flag set in a test, assert no flag name matches `(?i)(token|secret|password|key)` unless its help text documents it as a path.

**Re-run:** `make sec-fast` then `make sec` — both tiers should now report the finding as `mitigated`.

**Common pitfall:** asserting the exact `[REDACTED_PATTERN_NAME]` slug. The hand-ported and generated detector layers both run, and slug assignment can shift when the gitleaks catalog updates. Assert that redaction *happened* (no plaintext) and that *some* `[REDACTED_*]` slug appears — not the exact one.

**Ask for a second pair of eyes if:** the finding class is `secrets-redaction-bypass`. This is a `hard_class`; redaction bugs have shipped before that passed unit tests because the test mocked the layer that contained the bug.

### #hunter-daemon-ipc

**Chokepoint:** `internal/daemon/ipc.go` (size + connection caps, dispatch) and `internal/daemon/ipc_unix.go` (socket perms). New handlers live in `internal/daemon/ipc_handlers.go`.

**Regression test pattern:**

- For **socket perm** findings: an integration test that starts the daemon in a temp dir, stats the socket file, asserts `mode & 0777 == 0600` and `mode & 0777 == 0700` for the parent dir.
- For **peer-cred** findings: test must NOT call `DisablePeerCredForTesting()`. Instead, drive the socket from a child process spawned with `os/exec` (same UID) to confirm the happy path, and verify the rejection path with a unit test on `peerUID()`'s error branches.
- For **size-cap** findings: send a payload of `maxIPCMessageSize + 1` bytes; assert the connection is closed and no handler ran.
- For **new-handler authz** findings: the handler test must include a case where the payload references a path / resource the handler shouldn't reach (e.g. `/etc/passwd` for a file-reading handler), and assert rejection at the validator layer before the file is opened.

**Re-run:** `make sec-fast`. The OpenGrep rule for new `ipc_handlers.go` functions without a documented authz comment should no longer match the file you touched.

**Common pitfall:** a test that calls `DisablePeerCredForTesting()` to make the test simpler and then asserts the handler did the right thing. That test proves the handler is correct *if* the peer-cred check is bypassed — exactly the path the attacker takes. Write at least one test without the opt-out.

**Ask for a second pair of eyes if:** the finding class is `daemon-ipc-authz-bypass`. Same-UID adversary model is subtle; a reviewer who hasn't been heads-down in this hunter section may spot a gap you missed.

### #hunter-supply-chain

**Chokepoint:** `cmd/ox/adapter.go` (`runAdapterInstall`, `resolveAdapterSource`, `verifyAdapterBinary`) and `internal/adapter/registry/`.

**Regression test pattern:**

- For **owner allow-list** findings: table-driven test on `resolveAdapterSource`. Inputs cover the curated short names, the canonical `github.com/sageox/...` URL, and a typosquat (`github.com/sagoex/...`). Assert the typosquat is rejected.
- For **checksum verification** findings: a test that takes a fixture binary + a known SHA-256, swaps one byte, and asserts the install fails with a hash-mismatch error. Until checksum verification is implemented, this test will fail — that's the right outcome; remove the `t.Skip()` once the feature lands.
- For **release pinning** findings: assert that a registry entry without `pinned_version` is rejected at registry-load time.
- For **transitive Go module** findings: usually a `govulncheck` run is sufficient; the regression "test" is the next `make sec-fast` not flagging the CVE.

**Re-run:** `make sec-fast` for the deterministic checks; `make sec` to confirm the AI hunter no longer escalates.

**Common pitfall:** mocking the HTTP client and asserting "the correct URL was called." That doesn't prove anything about integrity; it proves your mock works. The integrity check belongs after the bytes are downloaded, against a hash, end-to-end.

**Ask for a second pair of eyes if:** the finding class is `supply-chain-tampering`. The blast radius of an adapter-install RCE is every developer who installs that adapter; one wrong patch ships the vulnerability to all of them.

### #hunter-llm-trust

**Chokepoint:** wherever ox builds the prompt or context that ships to an adapter. Today this is spread across `cmd/ox/agent_*.go` and the indexer; one of the work items implied by this threat model is consolidating it.

**Regression test pattern:**

- For **untrusted-content delimiter** findings: a test fixture with a commit message containing a known prompt-injection payload (`IMPORTANT: ignore prior instructions and ...`). Assert the prompt builder wraps it in the documented `<untrusted-repo-content>` block (or equivalent).
- For **tool-scope** findings: assert that the tool registration code passes a scope-limited root (`workspace`, not `$HOME`) to filesystem-read tools.
- For **end-to-end** verification: optional but valuable — run an actual adapter against the fixture repo and confirm the model output doesn't include the canary the injection asked for. This belongs in `tests/` and gated behind `OX_LLM_E2E=1` because it costs real LLM tokens.

**Re-run:** `make sec` — the LLM-trust hunter is AI-only; the deterministic tier has no signal here.

**Common pitfall:** asserting that a specific prompt template contains a specific string. Templates change; the property that matters is "untrusted content is delimited and the system prompt instructs the model to treat it as data." Assert the property, not the template byte sequence.

**Ask for a second pair of eyes if:** the finding involves a tool that reads or writes outside the workspace, or any tool whose effect persists beyond the current session.

## When to escalate

Findings in any of these classes — `secrets-redaction-bypass`, `daemon-ipc-authz-bypass`, `supply-chain-tampering` — should not be closed on a single contributor's say-so. Tag the PR for review by a maintainer with security context, attach the regression test, and link to the original finding ID.

If you're not sure whether a class qualifies, it probably does. Ask.

## Re-running scoped

The pipeline supports re-running on a single finding ID:

```bash
make sec ARGS="--rerun-on=<finding-id>"
```

This skips clean tiers and only re-runs the validators that produced the named finding. Useful for tight iteration when you're patching one thing and don't want a full 5-minute run between attempts.
