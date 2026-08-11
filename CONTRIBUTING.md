# Contributing to ox

Pull requests are welcome from anyone. So are issues — and if you'd rather describe the problem than build the fix, that's a real contribution, not a lesser one.

A note on how we review: when AI agents can produce large, plausible-looking changes, quality and security come from scrutinizing the inputs to the development process. Expect PRs to be reviewed on that basis. Small, focused changes with tests get merged faster than large ones, and a PR that explains *why* is easier to trust than one that only shows *what*.

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
