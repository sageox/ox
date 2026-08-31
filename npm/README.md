# @sageox/ox

The **[SageOx](https://sageox.ai) `ox` CLI** — the hivemind for agentic engineering — packaged for npm.

```bash
# one-off, no install
npx @sageox/ox --help

# or install globally
npm install -g @sageox/ox
ox version
```

## What this package does

This is a thin wrapper. On install it downloads the **official, prebuilt `ox`
binary** for your platform from the project's [GitHub Releases](https://github.com/sageox/ox/releases),
**verifies its SHA-256 against the release `checksums.txt`** (the same integrity
check the canonical `curl … | bash` installer performs), and installs it.

- It does **not** bundle or vendor a copy of the binary in the npm tarball.
- It does **not** build a second, separately-compiled binary — it fetches the
  exact same signed release artifact you would get from Homebrew or the install
  script.
- If the checksum does not match, or your platform has no prebuilt binary,
  installation **fails loudly** rather than installing something unverified.

### Supported platforms

| OS | Architectures |
|----|---------------|
| macOS (`darwin`) | Apple Silicon (`arm64`), Intel (`x64`) |
| Linux | `x64`, `arm64` |
| FreeBSD | `x64` |

Windows is not currently published as a prebuilt binary — install from source
(`go install github.com/sageox/ox/cmd/ox@latest`) or see the docs.

### Environment variables

- `OX_NPM_SKIP_DOWNLOAD=1` — skip the download during `npm install` (useful for
  CI, offline mirrors, or `--ignore-scripts` flows). The binary is then fetched
  and verified lazily on first `ox` invocation.

## Other ways to install

- **Homebrew:** `brew install sageox/tap/ox`
- **Shell:** `curl -sSL https://raw.githubusercontent.com/sageox/ox/main/scripts/install.sh | bash`
- **Go:** `go install github.com/sageox/ox/cmd/ox@latest`

## Docs

Full CLI documentation: **https://sageox.ai/docs/cli**

## License

MIT © SageOx Inc. See [LICENSE](./LICENSE).
