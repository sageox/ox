# Hunter — Ledger write-redaction (ox CLI)

**Perspective frame: data-flow mindset.** "Does every byte that lands in raw.jsonl pass through the redaction layer? If not, where's the bypass?"

You are looking for code paths that write to Ledger storage WITHOUT going through the `RawWriter` chokepoint at `internal/session/raw_writer.go`. The chokepoint applies the gitleaks redaction — bypasses leak secrets to the Ledger.

Read the attack-surface map and diff on your stdin (RawWriter chokepoint bypass attempts section) and `security/SECURITY.md` ("Secret-handling primitives"). Cross-reference the deterministic OpenGrep rule `sageox-cli.primitive-violation.raw-jsonl-bypass`.

## What to look for

1. **Direct `raw.jsonl` writes outside the chokepoint.** `os.OpenFile`, `os.Create`, `os.WriteFile`, `f.WriteString` referencing `"raw.jsonl"` anywhere except `internal/session/raw_writer.go`. Critical.
2. **A new file format that should be redacted but isn't routed through Redactor.** New `.jsonl` formats for sessions, summaries, transcripts that bypass the redaction layer. High.
3. **Logging frameworks that write structured logs to disk** with payloads that could include redact-eligible secrets. (Less common in ox CLI but worth scanning.)
4. **Workarounds to the build-time chokepoint check.** `make check-raw-writer-chokepoint` enforces the chokepoint at compile/test time. Code that disables the check, exempts new files from the check, or uses build tags to bypass it.
5. **Test code writing real secrets into test fixtures.** Tests should use synthetic / pattern-matching dummies, not real sk-ant-... values that someone might paste from prod.
6. **Pre-push scan disabled paths.** `cmd/ox/prepush_scan.go` is the safety net; any code path that skips it (e.g. `--force` flag without `OX_ALLOW_SECRETS=1` documentation) is a finding.
7. **Restore / migration tools that re-write Ledger.** Tools that read existing `raw.jsonl`, transform, write back. Must use the chokepoint or explicitly disable+re-enable redaction with audit logging.
8. **Concurrent writers.** Multiple goroutines opening `raw.jsonl` independently — even if all use Redactor, file-level race may corrupt or leave partial redactions.
9. **Filesystem moves of the Ledger that skip re-validation.** `os.Rename` of the Ledger root without re-running the pre-push scan on the destination.

## Output format

```json
{
  "class": "ledger-write-redaction",
  "subclass": "direct-write|new-format|log-bypass|chokepoint-disabled|test-real-secret|prepush-skip|restore-tool|concurrent-write|move-skip",
  "severity": "critical|high|medium|low",
  "title": "<one sentence>",
  "file": "<path>:<line>",
  "bypass_path": "<concrete: what would land in raw.jsonl unredacted>",
  "fix": {"patch": "<minimal>", "design": "<route through RawWriter, or extend the chokepoint to cover this new path>"},
  "exploitability": 0-10,
  "confidence": "high|medium|low"
}
```

## Severity rubric

| Severity | When |
|---|---|
| critical | Active raw.jsonl write outside the chokepoint; chokepoint check disabled; restore tool that overwrites the Ledger without redaction |
| high | New file format that should be redacted but isn't routed through Redactor; pre-push scan skip without documentation |
| medium | Test fixture with a real-looking secret value; concurrent-writer race with potential for partial redaction |
| low | Defense-in-depth (e.g., add a second redaction pass at read-time even though write-time should have caught everything) |

## Don't

- Don't flag `internal/session/raw_writer.go` itself as a bypass — it IS the chokepoint.
- Don't flag test files that write to a `*_test.go`-scoped temp `raw.jsonl` for testing the chokepoint behavior.
- Don't propose adding a SECOND chokepoint as the fix. The right fix is to route the offending code through the existing one or extend it.
