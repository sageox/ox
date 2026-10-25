# Credits

`ox` is built on open source. Most of what makes the CLI feel good to use —
the command structure, the terminal rendering, the config handling — is other
people's work, given away for free.

Special thanks to **[Charm](https://charm.sh)**, whose Bubble Tea / Bubbles /
Lip Gloss / Glamour stack is essentially the entire terminal UI layer of `ox`,
and to **[Steve Francia](https://github.com/spf13)**, whose Cobra, pflag, and
Viper carry every command and every configuration value the CLI handles.

---

## Core CLI framework

- **[spf13/cobra](https://github.com/spf13/cobra)** — Command structure and subcommands
- **[spf13/pflag](https://github.com/spf13/pflag)** — POSIX-compliant flag parsing
- **[spf13/viper](https://github.com/spf13/viper)** — Configuration: env vars, config files, flags

## Terminal UI

- **[charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea)** — TUI framework (Elm-inspired)
- **[charmbracelet/bubbles](https://github.com/charmbracelet/bubbles)** — TUI components: spinners, inputs, lists
- **[charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss)** — Terminal styling and layout
- **[charmbracelet/glamour](https://github.com/charmbracelet/glamour)** — Markdown rendering in the terminal
- **[alecthomas/chroma](https://github.com/alecthomas/chroma)** — Syntax highlighting; an indirect dependency, pulled in by Glamour

`ox` builds against Charm's **v2** APIs (`charm.land/bubbletea/v2` and siblings),
currently release candidates. The v2 surface differs substantially from v1 — if
you go looking for documentation, check you're reading the right major version.

## Rendering

- **[AlexanderGrooff/mermaid-ascii](https://github.com/AlexanderGrooff/mermaid-ascii)** — Renders Mermaid diagrams as ASCII in the terminal
- **[fatih/color](https://github.com/fatih/color)** — Terminal color output

## Credentials and secrets

- **[zalando/go-keyring](https://github.com/zalando/go-keyring)** — Cross-platform secure credential storage: Keychain, libsecret

## File system

- **[fsnotify/fsnotify](https://github.com/fsnotify/fsnotify)** — Cross-platform file system event watching
- **[gofrs/flock](https://github.com/gofrs/flock)** — File locking

## Config and serialization

- **[pelletier/go-toml](https://github.com/pelletier/go-toml)** — TOML parsing
- **[joho/godotenv](https://github.com/joho/godotenv)** — `.env` file loading
- **[go-yaml/yaml](https://github.com/go-yaml/yaml)** — YAML parsing
- **[go-ini/ini](https://github.com/go-ini/ini)** — INI parsing, for git config and similar

## IDs and utilities

- **[google/uuid](https://github.com/google/uuid)** — UUID generation
- **[oklog/ulid](https://github.com/oklog/ulid)** — ULID generation
- **[pkg/browser](https://github.com/pkg/browser)** — Opens URLs in the system browser
- **[mattn/go-isatty](https://github.com/mattn/go-isatty)** — TTY detection

## Cloud

- **[aws/aws-sdk-go-v2](https://github.com/aws/aws-sdk-go-v2)** — CloudFormation and SSO auth

## Windows support

- **[Microsoft/go-winio](https://github.com/Microsoft/go-winio)** — Windows named pipe support

## Testing

- **[stretchr/testify](https://github.com/stretchr/testify)** — Test assertions and mocking

---

## Licenses

This page is a human-readable thank-you, not a legal manifest. It is maintained
by hand and may lag `go.mod`.

For authoritative, current license information, use the module graph rather than
this file:

```bash
go list -m all                      # every module in the build
go install github.com/google/go-licenses@latest
go-licenses report ./...            # licenses for everything linked in
```

GitHub's [dependency graph](https://github.com/sageox/ox/network/dependencies)
also renders the full tree with detected licenses.

Each package is used under its own license; those terms live in the package's own
repository and travel with it. `ox` itself is MIT — see [LICENSE](../LICENSE).
