# sageox (PyPI wrapper) — follow-up skeleton

> **Status: skeleton / not yet published.** The primary agent-installable surface
> is the npm wrapper (`../npm`, `@sageox/ox`). This mirrors it for the Python
> ecosystem and is provided so PyPI distribution is a small, well-scoped follow-up
> rather than a from-scratch effort.

```bash
pip install sageox
ox version
```

## What it does

`pip install sageox` installs a console-script `ox`. On first run it downloads the
**official signed `ox` binary** for your platform from
[GitHub Releases](https://github.com/sageox/ox/releases), **verifies its SHA-256
against the release `checksums.txt`** (the same check as `scripts/install.sh`),
caches it under `~/.local/share/ox/bin/ox`, and execs it. It does not build or
vendor a second binary.

Supported platforms: macOS (arm64/x64), Linux (x64/arm64), FreeBSD (x64).

## Before this can ship

- [ ] Own the `sageox` project name on PyPI (or pick `sageox-cli` if taken).
- [ ] Add a PyPI publish job to `.github/workflows/release.yml` — prefer
      [Trusted Publishing (OIDC)](https://docs.pypi.org/trusted-publishers/) over a
      long-lived API token.
- [ ] Decide whether to download at install time (like npm's `postinstall`) or
      only lazily on first run (current behavior — friendlier to `pip` sandboxes).

## Docs

https://sageox.ai/docs/cli
