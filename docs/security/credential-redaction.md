# Credential Redaction

> For ox's security-review pipeline and threat model (contributor-facing), see [`security/`](../../security/).

ox watches for credentials at every step where data crosses a trust boundary.
This page is the operator's map: what's caught, what isn't, and what to do
when something slips through.

## What's caught at write time

Every session entry ox records — whether it comes from the daemon's session
watcher tailing a live agent, the CLI hook drain, or a one-shot coworker-load
event — passes through two redactors before bytes touch disk in
`.sageox/cache/sessions/<n>/raw.jsonl`:

1. **Command-allowlist redaction** runs first. For known credential-emitting
   commands (`aws sso login`, `aws sts assume-role`, `aws sts get-session-token`,
   `aws configure export-credentials`, `gh auth login`, `gh auth token`,
   `glab auth login`, `glab auth token`, `vault read auth/...`, `op item get`),
   the entire `tool_output` is replaced with `[REDACTED:credential-output:<cmd>]`.
   No regex needs to match every credential shape inside — the whole output
   block goes.

2. **Pattern-based redaction** runs second. Matched substrings are replaced
   with stable `[REDACTED_*]` slugs. The detector set includes:

   | Class | Pattern | Slug |
   |---|---|---|
   | AWS access key | `AKIA…` (20 chars) | `[REDACTED_AWS_KEY]` |
   | AWS STS session key | `ASIA…` (20 chars) | `[REDACTED_AWS_STS_KEY]` |
   | AWS secret key | `aws_secret_access_key=…` (40 chars) | `[REDACTED_AWS_SECRET]` |
   | GitHub PAT | `ghp_/gho_/ghs_/ghr_/ghu_…` | `[REDACTED_GITHUB_TOKEN]` |
   | GitHub fine-grained PAT | `github_pat_…` | `[REDACTED_GITHUB_PAT]` |
   | GitLab PAT | `glpat-…` | `[REDACTED_GITLAB_TOKEN]` |
   | GitLab OAuth / deploy / runner / feed | `gloas-`, `gldt-`, `glrt-`, `glft-` | `[REDACTED_GITLAB_OAUTH]` etc. |
   | SageOx share-session cookie | `sox_share_session=…` | `[REDACTED_SOX_SHARE_SESSION]` |
   | SageOx API key | `mk_…` | `[REDACTED_SAGEOX_API_KEY]` |
   | AgentX key | `axk_…` (exactly 32 chars) | `[REDACTED_AGENTX_KEY]` |
   | Slack token | `xoxb-/xoxp-/xoxa-/xoxs-/xoxr-` | `[REDACTED_SLACK_TOKEN]` |
   | Stripe key | `sk_/rk_live_test_…` | `[REDACTED_STRIPE_KEY]` |
   | Twilio, SendGrid, Mailchimp, NPM, PyPI, Heroku UUID | various | various |
   | Private key headers | `-----BEGIN … PRIVATE KEY-----` | `[REDACTED_PRIVATE_KEY]` |
   | HTTP Bearer header | `Authorization: Bearer …` | `Authorization: Bearer [REDACTED_BEARER_TOKEN]` |
   | HTTP Basic header | `Authorization: Basic …` | `Authorization: Basic [REDACTED_BASIC_AUTH]` |
   | URL with embedded password | `https://user:password@host/…` | `[REDACTED_URL_WITH_CREDENTIALS]` |
   | Database connection string | `postgres://user:pass@…` | `[REDACTED_CONNECTION_STRING]` |
   | Generic `api_key=`, `access_token=`, `secret=`, `password=` assignments | various | `[REDACTED_API_KEY]` etc. |
   | JWT | `eyJ…eyJ….…` | `[REDACTED_JWT]` |

   The `[REDACTED_*]` slugs are a stable contract — downstream consumers can
   `grep` for them without worrying about version drift.

The pattern set is defined in `internal/session/secrets.go`. Per-project
custom rules live in `.sageox/REDACT.md` (literal-string redactions); see the
existing `docs/security/redaction-policy.md` for the rule format.

## What's caught at push time

When ox is about to push the local Ledger to `git.sageox.ai` (the
`ox session stop` / `ox session push` flow), a pre-push gate runs before
`git push` is invoked:

1. Enumerate `git diff --name-only origin/main...HEAD`.
2. Scan each file's current working-tree content against the same
   `DefaultPatterns` as the write-time redactor.
3. If anything matches, **refuse the push**, print the detector name, file,
   and line number, and exit non-zero. The push never goes through.

Bypass: `OX_ALLOW_SECRETS=1` overrides the gate. A loud `WARNING` is emitted
to stderr; the override is for genuine false positives, not routine use.

## What's caught in the GitHub PR / Issue cache

ox writes ledger-tracked JSON for each GitHub PR and Issue at
`<ledger>/data/github/YYYY/MM/DD/{pr,issue}/N-<hash>.json`. User-authored
fields — Title, Body, Comments, commit messages — are scrubbed via the same
pattern redactor before serialization. This closes the historical leak where
PR descriptions containing pasted curl examples carried real Bearer tokens
into the cloud Ledger.

## What's NOT caught

These are explicit non-goals for this layer of defense:

- **Entropy-only detection**. Random-looking strings without a recognizable
  prefix (e.g. arbitrary base64 secrets) are not flagged. False-positive cost
  is too high to enable by default. Run `ox doctor --check=ledger-secrets`
  manually if you want to spot-check; it uses the same pattern set as the
  write-time redactor.
- **Audio**. Voice-mode recordings are not transcribed and scanned.
- **Multi-line spans**. Detectors operate per line. A secret that wraps
  across multiple lines (e.g. a private key body) gets caught at the header
  pattern but the middle lines may pass through if they don't carry their
  own structure.
- **Base64-of-credentials**. If a value is base64-encoded before being
  pasted, the encoded form is opaque to text detectors.
- **Arbitrary adapter binaries**. ox cannot sandbox external adapter
  binaries (`ox-adapter-*`). The hook-content validator
  (`ox doctor --check=hook-content-integrity`) detects known-suspicious
  shapes in already-installed hooks; it doesn't prevent an adapter from
  writing what it wants when invoked.

## Recovery when a credential slips through

The sequence matters:

1. **Rotate the credential first.** Faster than any tooling: revoke the
   leaked token, mint a new one. This is true even if the leak hasn't been
   pushed yet — assume it was screenshotted or backed up.

   | Provider | Action |
   |---|---|
   | AWS IAM (`AKIA…`) | IAM console → security credentials → deactivate / delete |
   | AWS STS (`ASIA…`) | Tokens are short-lived; rotate the underlying role's policy if needed |
   | GitHub (`ghp_…`, `gho_…`, `github_pat_…`) | Settings → Developer settings → Personal access tokens → revoke |
   | GitLab (`glpat-…`, `gloas-…`, `gldt-…`, `glrt-…`, `glft-…`) | User Settings → Access Tokens → revoke |
   | SageOx (`mk_…`) | `ox login` re-issues credentials; revoke at sageox.ai/settings |
   | Database connection strings | rotate the DB user's password and update connection configs |
   | Generic Bearer / Basic | identify the issuer (usually obvious from context) and revoke through their portal |

2. **Find what else is exposed.** Run the local audit:

   ```sh
   ox doctor --check=ledger-secrets
   ```

   This walks every file in your local Ledger working tree and reports
   findings per detector, never printing the matched bytes. Use this to
   answer "did the same token leak anywhere else?" before deciding the
   scope of cleanup.

3. **Clean up local working tree.** For findings in commits that haven't
   been pushed yet:

   ```sh
   ox session redact-history          # interactive, per-finding
   ox session redact-history --dry-run # list only, no changes
   ```

   The tool snapshots the Ledger to an immutable backup at
   `~/.local/share/sageox/backups/redact-history/<name>-<timestamp>.tar.gz`
   (mode 0400, SHA-256 reported) before any modification. For each affected
   file, you approve or skip individually — there's no bulk-approve flag.
   Approved files are redacted in place and the holding commit is amended
   so the original session identity is preserved.

4. **Already pushed?** The `redact-history` tool will list those findings
   but refuse to act on them — rewriting pushed history requires a
   force-push that affects every clone of the Ledger. The current
   recommended response is:

   - The credential rotation in step 1 is the load-bearing fix.
   - The leaked bytes remain in the pushed history. Anyone with read access
     to the Ledger can still see them, but they're now revoked.
   - If the leak is severe enough to warrant a force-push history rewrite,
     follow up with the team — the gated V2 tool (issue ox-54cm) covers
     that flow but requires customer comms + per-finding approval and is
     intentionally not auto-runnable.

## Embedded credentials in `.git/config`

A separate class of leak: when ox cloned a Ledger before the credential-helper
migration shipped, the GitLab PAT was embedded directly in the `origin` URL
of every Ledger's `.git/config`. Time Machine, iCloud, Dropbox, and
`git remote -v` all expose this.

The migration sweep runs automatically on every daemon startup and strips the
embedded PAT, installing the ox credential helper instead so subsequent
`git fetch`/`push` operations resolve credentials via the keychain or
encrypted file store. To check or trigger manually:

```sh
ox doctor --check=ledger-embedded-creds         # report only
ox doctor --check=ledger-embedded-creds --fix   # strip + install helper
```

After migration, PAT rotation becomes a single credential-store write rather
than per-Ledger `.git/config` rewrites. `git remote -v` shows clean URLs.

## Adding a new detector

Add a `SecretPattern` entry to `DefaultPatterns()` in
`internal/session/secrets.go`. Conventions:

- Order matters. More-specific patterns first (prefix-anchored like
  `glpat-` before generic shapes like `connection_string`). Existing test
  cases pin specific slugs for representative inputs; new patterns should
  not preempt those — the test suite enforces this.
- Use the `SkipIf` field for whitelisting already-masked or sentinel
  shapes (e.g. `:x-oauth-basic@` for GitHub URLs that aren't really
  credentials).
- Add positive + negative test cases in `internal/session/secrets_test.go`.
- Update this document with the new slug and one-line description.

Every PR introducing a new detector goes through team review. The pattern
set is shared across write-time, push-time, audit, and history-rewrite
tooling, so a careless regex affects every surface.
