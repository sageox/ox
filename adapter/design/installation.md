# Adapter Installation Design

## Platform Detection & Binary Selection

Each GitHub release of `sageox/ox-adapters` publishes per-platform binaries. When ox downloads an adapter, it selects the correct binary for the current machine.

Platform matrix for each adapter:

```
ox-adapter-claude-code_darwin_amd64       (Intel Mac)
ox-adapter-claude-code_darwin_arm64       (Apple Silicon)
ox-adapter-claude-code_linux_amd64        (Linux x86_64)
ox-adapter-claude-code_linux_arm64        (Linux ARM64 — Raspberry Pi, AWS Graviton)
ox-adapter-claude-code_windows_amd64.exe  (Windows x86_64)
ox-adapter-claude-code_windows_arm64.exe  (Windows ARM)
```

ox determines the current platform:

```go
// internal/adapterinstall/platform.go
func currentPlatform() string {
    return fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
    // "darwin_arm64", "linux_amd64", etc.
}
```

This matches the binary suffix naming exactly. No lookup table needed.

## Registry File Format

`registry.yaml` in `sageox/ox-adapters` repo (fetched and cached locally):

```yaml
schema_version: 1
updated: 2026-04-02

official:
  - name: claude-code
    display_name: Claude Code
    description: Session reader and hook installer for Claude Code
    detect_commands: [claude]          # binaries that indicate this agent is present
    repo: sageox/ox-adapters
    capabilities: [session_reader, hook_installer, incremental_reader]
    releases:
      latest: "1.2.0"
      platforms:
        darwin_amd64:  {url: "https://...", sha256: "abc123"}
        darwin_arm64:  {url: "https://...", sha256: "def456"}
        linux_amd64:   {url: "https://...", sha256: "ghi789"}
        linux_arm64:   {url: "https://...", sha256: "jkl012"}
        windows_amd64: {url: "https://...", sha256: "mno345"}

  - name: amp
    display_name: Amp
    description: Session reader and hook installer for Amp
    detect_commands: [amp]
    repo: sageox/ox-adapters
    capabilities: [session_reader, hook_installer, incremental_reader]
    releases:
      latest: "0.5.0"
      platforms: {...}

community:
  - name: myagent
    display_name: My Agent
    description: Community adapter maintained by @username
    repo: github.com/username/ox-adapter-myagent
    capabilities: [session_reader]
    # no releases block — ox fetches from GitHub releases API directly
```

## Install Flow

```
ox adapter install claude-code
  1. fetch registry.yaml (cache hit or network fetch)
  2. find "claude-code" entry
  3. determine platform: darwin_arm64
  4. download binary from releases.platforms.darwin_arm64.url
  5. verify sha256 checksum
  6. move to ~/.local/share/ox/adapters/ox-adapter-claude-code
  7. chmod +x
  8. run: ox-adapter-claude-code info
  9. verify protocol_version matches ox minimum
  10. done

ox adapter install github.com/username/ox-adapter-myagent
  1. call GitHub API: GET /repos/username/ox-adapter-myagent/releases/latest
  2. find asset matching current platform suffix
  3. download, verify (if sha256 in release body), install
  4. verify protocol_version
  5. done (no registry needed)
```

## Automatic Adapter Detection at `ox integrate install`

```
ox integrate install
  1. scan PATH for agent binaries: which claude, which gemini, which amp, ...
  2. for each detected agent, check if adapter installed
  3. missing adapter for detected agent:
       → "Claude Code detected. Install ox-adapter-claude-code? [Y/n]"
  4. install missing adapters (parallel downloads)
  5. install hooks for all selected agents
```

Detection heuristics per agent (in registry.yaml `detect_commands`):
- Claude Code: `which claude` or `CLAUDE_CODE_ENTRYPOINT` env var or `~/.claude/` exists
- Gemini: `which gemini` or `~/.gemini/` exists
- Codex: `which codex` or `~/.codex/` exists
- Amp: `which amp` or `~/.amp/` exists

## Local Development Install

### `ox adapter link` (recommended for interactive development)

Creates a symlink in `~/.local/share/ox/adapters/` pointing to the built binary. Rebuilding the
binary in place takes effect after `ox adapter reload` — no copy or daemon restart needed.

```bash
# build
go build -o ./bin/ox-adapter-myagent ./cmd/ox-adapter-myagent

# link (creates symlink, no copy)
ox adapter link ./bin/ox-adapter-myagent
# → linked: ~/.local/share/ox/adapters/ox-adapter-myagent → /path/to/bin/ox-adapter-myagent

# rebuild and reload (hot-reload without daemon restart)
go build -o ./bin/ox-adapter-myagent ./cmd/ox-adapter-myagent && ox adapter reload

# when done
ox adapter unlink myagent
```

This is the preferred workflow. The symlink means the build artifact *is* the installed adapter —
no copy step, no stale binary.

### `$OX_ADAPTER_PATH` (recommended for CI and automation)

Set the env var before starting the daemon. The directory is scanned first, no install step.

```bash
export OX_ADAPTER_PATH=/path/to/my/adapters/bin
go build -o $OX_ADAPTER_PATH/ox-adapter-myagent ./cmd/ox-adapter-myagent
# ox daemon start  ← daemon picks it up at startup
```

### Manual copy (simplest, no symlink)

```bash
go build -o ~/.local/share/ox/adapters/ox-adapter-myagent ./cmd/ox-adapter-myagent
ox adapter reload
```

`$OX_ADAPTER_PATH` is scanned first — it always wins over linked or manually-installed adapters of
the same name. Adapter discovery does **not** scan `$PATH` (see ADR-006 for the security rationale).

## Cache

Registry is cached at `~/.local/share/ox/adapter-registry.yaml` with a TTL of 24 hours. `ox adapter list` reads the cache, falls back to the bundled registry if no network.

A bundled `registry.yaml` ships inside the ox binary (embedded via `go:embed`) as the ultimate fallback — air-gapped environments always have a list, just potentially stale.

## Upgrade

```bash
ox adapter upgrade                   # upgrade all installed adapters
ox adapter upgrade claude-code       # upgrade specific adapter
```

Checks installed version against `releases.latest` in registry. Downloads if newer. Old binary replaced atomically (write to temp, rename).

## `ox adapter list` Output

```
$ ox adapter list

OFFICIAL ADAPTERS
  NAME            INSTALLED    VERSION    LATEST     CAPABILITIES
  claude-code     ✓            1.2.0      1.2.0      session_reader, hook_installer
  gemini          ✓            1.0.3      1.1.0  ⬆   session_reader, hook_installer
  codex           ✓            0.9.1      0.9.1      session_reader
  amp             ✗                       0.5.0      session_reader, hook_installer
  cursor          ✗                       0.3.0      session_reader

COMMUNITY ADAPTERS (installed)
  myagent         ✓            0.1.0      —          session_reader

Run 'ox adapter install amp' to install missing adapters.
Run 'ox adapter upgrade gemini' to upgrade (gemini has update available).
```
