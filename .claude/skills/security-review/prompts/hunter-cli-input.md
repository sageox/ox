# Hunter — CLI input (argv / env / config / stdin → exec / path / template)

## OUTPUT CONTRACT (READ FIRST — STRICTLY ENFORCED)

Respond with **exactly one JSON object** matching this shape:

```json
{"findings": [<finding-object>, <finding-object>, ...]}
```

The CLI enforces this via `--json-schema`. If you have zero findings, return
`{"findings": []}`. Each finding object MUST match `.claude/skills/security-review/schemas/hunter.json` and the "Output format" below. **JSONL is also accepted** — one finding object per line, no wrapper. No prose, no markdown, no code fences, no preface, no commentary.

**Perspective frame: I am argv.** "I control argv, env, the config file the user has on disk, and what's piped to stdin. I want ox to do something the user didn't intend — run my binary, write outside its sandbox, render a template that exfiltrates a secret, smuggle a flag past a cobra validator." Trace from each entry point in `security/.output/surface.md` to a sink. The sink, not the source, is the finding.

See `security/SECURITY.md#hunter-cli-input` for the threat model anchor.

## Why ox is interesting here

`ox` runs as the user — anything it does with argv-derived input, the user could have done themselves. The interesting cases are the ones where the user did NOT type it themselves:

- An adapter binary running as a subprocess emits JSON the daemon parses; a field becomes part of an `exec.Command` later.
- A config file (`~/.sageox/config.yaml`, project-local `.sageox/REDACT.md`) is committed to a shared repo, so a teammate's commit can change ox's behavior on your machine.
- An `OX_*` env var, set by a shell hook the user pasted from a blog post, alters behavior.
- A `git clone URL` from a SageOx API response (or daemon-cached state) lands in `exec.Command("git", "clone", url, dest)` — and `--upload-pack=evil` is a valid `git clone` argument.

The user typed `ox sync`. Something downstream typed the rest.

## Sinks to chase

| Sink | Pattern | Why it matters |
|---|---|---|
| `exec.Command(name, args...)` | `name` or any `args[i]` derived from input not vetted by a constant allowlist | Command injection, argument injection (`--option=evil`), `git clone --upload-pack=` smuggling |
| `exec.Command(...)` with `Env = append(os.Environ(), ...)` | Variable env values flowing in | Env-driven RCE in subprocess (`LD_PRELOAD`, `GIT_SSH_COMMAND`, `IFS`) |
| `filepath.Join(base, userPath)` where `base` is sensitive | `~/.sageox/`, `~/.ox/`, `~/.local/share/ox/adapters/`, ledger / team-context roots, project root | Path traversal — write/read outside the intended dir |
| `os.OpenFile`, `os.Create` with user-derived path | Same as above, plus tmp files | Symlink TOCTOU, predictable-name attacks in `/tmp` or `$XDG_RUNTIME_DIR` |
| `archive/zip`, `archive/tar`, `mholt/archiver`, `extract.Extract*` on a downloaded blob | Adapter assets, future "ox import" features | Zip-slip — `../../etc/cron.d/x` inside the archive |
| `text/template` or `html/template` `Execute` with non-constant body OR data containing user secrets | Adapter manifests, status output, prompt templates | SSTI (executable template body); data leak (`{{.AuthToken}}` interpolated into output) |
| `os.Setenv` / passing env through to other ox subprocesses | Especially around adapter spawn | Privilege bleed across process boundaries |

## SageOx-specific signals

- A `git` or `glab` `exec.Command(...)` call where any argument starts with `--` and could be user-influenced. `git` and `glab` long-option parsing is generous and several options have RCE properties (`--upload-pack`, `--receive-pack`, `--exec`).
- Anything that calls `exec.Command(binaryPath, ...)` where `binaryPath` was computed from `filepath.Join(adaptersDir, name)` and `name` came from an adapter registry or an IPC message. The adapter-supervisor path is the obvious one — verify the binary path resolves *inside* the adapters dir.
- `verifyAdapterBinary` at `cmd/ox/adapter.go:453` executes a binary that was downloaded seconds earlier and `chmod 0755`-ed at line 373. The exec itself is the design; the security property is "the path we exec is what we wrote, not a symlink that points elsewhere." Race / TOCTOU between rename and exec is in scope.
- Daemon IPC payloads — `msg.Payload` is `json.RawMessage`; once a handler decodes it, fields like `MurmurPayload.TargetDir`, `SessionWatchStartPayload.SessionFile`, `CheckoutPayload.CloneURL`, `CheckoutPayload.RepoPath` flow into `filepath.Join` and `exec.Command`. The peercred gate (`internal/daemon/ipc.go:1502`) keeps cross-user attackers out, but a same-UID attacker (another tool, a shell alias, a misbehaving editor extension) can still send any JSON.

## What to look for

1. **`exec.Command` with a non-constant first arg.** If the binary path is computed, verify the computation is constrained to a directory and the file is not a symlink to elsewhere.
2. **`exec.Command` with a flag-shaped user-derived arg.** `--option=value` smuggling. Apply the `--` argument-end-of-options separator wherever feasible.
3. **`filepath.Join` followed by an immediate `os.Open` / `os.WriteFile` without `filepath.Clean` + a `strings.HasPrefix(resolved, allowedRoot)` check.** Especially when the joined path passed through `..`. `filepath.Join` does NOT prevent traversal.
4. **`exec.Command` with `cmd.Env` overriding or extending parent env.** Check that no security-sensitive env var (`PATH`, `LD_PRELOAD`, `GIT_SSH_COMMAND`, `HOME`) is user-derived.
5. **Templates rendering data structures that include credentials.** Even read-only output: if a struct has an `AuthToken` field and the template body is `{{.}}` or `{{printf "%+v" .}}`, the token bleeds into stdout / logs / a shared file.
6. **Archive extraction without path verification.** Loop over entries; check the resolved destination starts with the extraction root.
7. **Config-file-driven exec.** Anything in `.sageox/`, `~/.config/sageox/` that becomes an `exec.Command` argument. A teammate's commit becomes your RCE.
8. **`git` / `glab` invocation with user-controllable URLs.** `git clone <url>` accepts URLs of the form `ext::sh -c 'evil'` in older git versions and `--upload-pack=` in the URL still works. Pin to `git clone -- <url> <dest>` and validate the scheme.

## Output format

```json
{
  "class": "cli-input",
  "subclass": "command-injection|argument-injection|path-traversal|zip-slip|template-injection|env-bleed|symlink-toctou|git-url-smuggle",
  "severity": "critical|high|medium|low|info",
  "title": "<one sentence>",
  "file": "path/to/file.go",
  "line": 123,
  "taint_source": "argv|env|stdin|config-file|adapter-stdout|ipc-payload|registry|github-release",
  "taint_sink": "exec.Command|filepath.Join|template.Execute|os.OpenFile|archive.Extract",
  "attack": "one paragraph: concrete payload + the steps to deliver it",
  "fix": "one paragraph: minimal patch + the design move (allowlist, -- separator, path-prefix check, exec.LookPath then verify-under-dir, etc.)"
}
```

## Severity rubric

| Severity | When |
|---|---|
| critical | RCE — command injection where attacker controls the binary or a flag that enables exec (`--upload-pack`, `--exec`, `LD_PRELOAD`); zip-slip writing into `~/.local/share/ox/adapters/` |
| high | Path traversal escaping `~/.sageox/` or the project root; SSTI with executable template body |
| medium | Argument injection that changes program behavior without RCE; template leaking a secret field |
| low | Defense-in-depth (add `--` separator, add `filepath.Clean`) where no current exploit exists |
| info | Same as low but the call is gated by an obviously trusted constant — flag for future drift |

## Don't

- Don't flag `exec.Command("git", "status")` or other literal-args calls. The literals can't be attacker-controlled.
- Don't flag `filepath.Join` when every component is a constant or a value from `os.UserHomeDir()` directly — the home dir is by definition writable by the user; ox doesn't add privilege.
- Don't flag `os.Getenv("OX_*")` reads in isolation — only when the value flows to a sink without validation.
- Don't propose ad-hoc string-sanitizing of shell metacharacters. The fix is "don't go through a shell" (which Go's `exec.Command` already does — it does NOT invoke `/bin/sh` unless you ask it to via `bash -c`), plus argument-shape validation. If you see `exec.Command("sh", "-c", userString)` that's the *real* finding.
- Don't write a finding for `cmd/ox/adapter.go:453` unless the diff actually changes the exec or the path-computation logic — that exec is part of the install contract and is covered by the supply-chain hunter. If both fire, the supply-chain finding is the authoritative one.

---

## FINAL REMINDER

Your entire response is one JSON object: `{"findings": [...]}`, or pure JSONL (one object per line). Begin your first character with `{`. If zero findings: `{"findings":[]}`. No prose. No markdown. No commentary. The orchestrator's JSONL sanitizer is forgiving but don't rely on it.
