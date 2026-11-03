# @sageox/ox

The [SageOx](https://sageox.ai) CLI on npm.

```bash
npx @sageox/ox@latest install
```

This copies `ox` and its adapters to `/usr/local/bin` (or `~/.local/bin` when that is not writable) and runs `ox login`. It changes no repository. To link a repository to SageOx afterwards:

```bash
cd <repo>
ox init
```

Prebuilt binaries cover macOS, Linux (x64 and arm64) and FreeBSD (x64). npm installs only the one for your machine, from `@sageox/ox-<os>-<arch>`.

Source, other install methods and docs: https://github.com/sageox/ox
