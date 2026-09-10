# Contributing to ox

Pull requests are welcome from anyone. So are issues — and if you'd rather describe the problem than build the fix, that's a real contribution, not a lesser one.

A note on how we review: when AI agents can produce large, plausible-looking changes, quality and security come from scrutinizing the inputs to the development process. Expect PRs to be reviewed on that basis. Small, focused changes with tests get merged faster than large ones, and a PR that explains *why* is easier to trust than one that only shows *what*.

## Automated review

Two bots read every PR here, and **neither one blocks a merge** — they are
readers, not gates. A maintainer still makes the call.

- **CodeRabbit** reviews when the PR opens and again on every push.
- **Greptile** reviews when the PR opens, and then **not again until someone
  asks**. That is deliberate: re-reviewing every intermediate push spends a
  shared review budget on code that is about to change anyway, and the budget
  is org-wide — a repo that burns it degrades review everywhere.

So when you have pushed your fixes and CI is green, **ask Greptile for one
more read of the final state** — comment `@greptileai` on the PR, or press
**"Re-trigger Greptile"** in its comment footer. That gives the PR two good
reviews (as opened, as merging) instead of a dozen partial ones.

Configuration lives in [`greptile.json`](greptile.json) and
[`.coderabbit.yaml`](.coderabbit.yaml). Greptile is told to weight session
capture, LFS upload, and ledger paths above everything else, because a silent
defect there destroys work a user cannot recreate. It is also told to skip
generated files and to leave formatting alone — gofmt, goimports, and
golangci-lint already own that.

If a PR is large and mechanical (a rename, a codegen refresh) and a bot review
would be pure noise, a maintainer can label it `no-greptile` to skip it.

## Codex setup

This repo includes shared SageOx hooks in [`.codex/hooks.json`](.codex/hooks.json)
to load Team Context and record sessions according to the repo's SageOx settings.
Install `ox` on your `PATH` (`make build && make install`), then open Codex in
the repo and use `/hooks` to review and trust the project hooks. New or changed
hooks require review before they run; see [Codex hook trust](https://learn.chatgpt.com/docs/hooks#review-and-trust-hooks).

## Two Ways In

**File an issue** — [bug reports, feature requests, and agent prompts](https://github.com/sageox/ox/issues) are all welcome. Be as detailed as you like. A maintainer reviews it, generates an implementation plan, and SageOx engineers build it with full test coverage and code review. You get the fix or feature you asked for, maintained over time, without having to keep a fork in sync.

**Open a pull request** — fork, branch, and send it. Best results come from:

- **One change per PR.** Unrelated fixes bundled together are hard to review and harder to revert.
- **Tests that fail without your change.** Break it, watch the test fail, restore it, watch it pass.
- **`make lint && make test` green** before you open it.
- **A linked issue** where one exists, so the discussion and the diff live together.
- **A description written for a reviewer who skims** — what broke, what this ships, how you verified it.

If the change is large or architectural, open an issue first and agree on the approach. That's not a gate; it just saves you from building something we'd ask you to rebuild.

## What We Welcome

- **Bug reports** — clear description, steps to reproduce, expected vs actual behavior, environment details
- **Feature requests** — the problem being solved, why existing behavior is insufficient, tradeoffs considered
- **Agent prompts and implementation plans** — well-crafted prompts, detailed plans, and design proposals are valuable contributions. If you've worked out how something should be built, share it.
- **Pull requests** — see above

## Attribution

When your issue leads to a PR we open, we're happy to include you as a co-author. Just let us know your preferred name and email in the issue.

## Source Code in Issues

By including any source code in a bug report or feature request, you grant a full copyright license to SageOx Inc.

## Copyright

The resulting software is the exclusive copyright of SageOx Inc. By submitting a pull request, you grant SageOx Inc. a full copyright license to your contribution.
