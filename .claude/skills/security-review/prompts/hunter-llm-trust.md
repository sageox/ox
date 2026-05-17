# Hunter — LLM trust boundary (indexed content → adapter LLMs)

## OUTPUT CONTRACT (READ FIRST — STRICTLY ENFORCED)

Respond with **exactly one JSON object** matching this shape:

```json
{"findings": [<finding-object>, <finding-object>, ...]}
```

The CLI enforces this via `--json-schema`. Zero findings → `{"findings": []}`. JSONL accepted. No prose. No markdown. No commentary.

**Perspective frame: I am content.** "I authored a README in a public-ish repo, or a commit message, or a ledger entry, or a team-context file. Ox indexes my words and feeds them to an LLM adapter (Claude, Codex, Gemini, whatever) as part of the user's prompt. I want to: (a) inject instructions that the LLM executes, (b) exfiltrate other context the user has loaded, (c) cause the LLM to invoke a tool the user didn't authorize, (d) bleed into the next session."

See `security/SECURITY.md#hunter-llm-trust` for the threat-model anchor.

## Why ox is the central LLM trust boundary

The whole point of ox is to put a team's knowledge in front of AI coworkers. That knowledge is in user-writable git repos: the ledger, team-context, the project repo itself, knowledge bubbles. Anyone who can commit can change what the model sees, and the model's response drives tools that touch the filesystem, run commands, and write back to the very repos that just compromised it. The feedback loop is the threat.

In ox specifically, indexed content reaches LLM adapters through:

- `internal/adapter/` and `internal/session/adapters/` — adapter registry + dispatch
- `cmd/ox-adapter-*` subcommands (if present in the diff) — concrete adapter binaries
- `internal/daemon/adapter_supervisor*.go` — process supervision; subagent capture
- Whatever new prompt-construction code the diff introduces (search for string concatenation involving file content, `os.ReadFile` → string-builder → exec/pipe to adapter)

## Untrusted sources (rank by how easy it is for an attacker to write here)

| Source | How an attacker writes it |
|---|---|
| Commit message in an indexed repo | One commit; appears in `git log`; consumed by anything that ingests commit history |
| README / Markdown files in an indexed repo | One commit; trivially seen by anything indexing files |
| Ledger entries | Any human or AI coworker on the team writes these (the threat model explicitly says so) |
| Team-context files | Same as ledger |
| Knowledge Bubble notes | User-writable git repo per the AGENTS.md guidance |
| Whisper / murmur / friction events (cached) | Same-UID local process can write via daemon IPC (see hunter-daemon-ipc) |
| Adapter stdout JSON | A compromised adapter can put anything in the `RawEntry` content fields; redaction runs on it but only catches known credential shapes, not prose-injection |
| External MCP server responses (if ox grows MCP) | Network-controlled |

## Sinks to chase

| Sink | Pattern | Why it matters |
|---|---|---|
| String-builder concatenating indexed file content into a prompt without delimiter framing | `prompt := preamble + content + suffix` | Classic prompt injection: the content sees the preamble as a suggestion, not an instruction |
| Tool-call schema exposed to the adapter where `args` or `tool name` is taken from the adapter's structured output without validation | Common shape: `switch toolCall.Name { ... default: exec.Command(...) }` | Tool abuse / arbitrary exec — the bug is "default" being permissive |
| Adapter response read back and used to drive another exec / file write / network call | The LLM's output is the next program's input | Output-action without intermediate validation |
| Recursive agent loop where one adapter's output feeds another's prompt | `subagent` flows in `internal/session/subagent.go` / `cmd/ox/agent_session_subagent.go` | Content from session A persists into session B's prompt; cross-session bleed |
| Markdown / HTML rendering of indexed content in TUI / status output | `internal/session/markdown.go` and friends | Stored XSS-equivalent — terminal escape injection (ANSI sequences that re-color, clear screen, fake prompts) |
| `template.Execute(out, data)` where `data` mixes trusted + untrusted fields | The template body trusts every field equally | Indirect SSTI even when body is safe — if template emits `{{.Content}}` raw into a context that interprets it |

## What to look for

1. **Prompt-construction sites that read a repo file and concatenate into a prompt.** Search for `os.ReadFile` followed by `+` into a string buffer that ends up in an adapter spawn or a `RawEntry` outbound. Verify: (a) the content is wrapped in an explicit delimiter ("<<<USER_CONTENT>>>" or markdown code fence); (b) the prompt explicitly instructs the model to treat it as data, not instruction; (c) any decision derived from the content is schema-validated, not free-text.
2. **Tool-dispatch where the tool name or arguments come from adapter output without an allowlist + schema check.** The right pattern is: schema-validate the toolcall JSON; look up the tool in a constant map; reject if absent; per-tool, schema-validate the args.
3. **Default-permissive switch on tool names.** `switch toolCall.Name { case "X": ...; default: doSomething }` where `doSomething` does ANYTHING privileged.
4. **Cross-session context bleed.** If `internal/session/subagent.go` or the orchestrator passes session A's transcript into session B's prompt, verify B's prompt distinguishes "your prior context" from "another agent's words you should not act on."
5. **Indexed-content → terminal output without escape sanitization.** Markdown renderers in `internal/session/markdown.go` should strip or escape control characters. A `\033]0;evil\007` in a commit message redraws the user's terminal title; `\033[2J` clears their screen; some terminals interpret `\033]52;c;<base64>\007` as a clipboard write.
6. **`exec.Command` whose args come from a structured tool call without an allowlist.** Even "the LLM said run `ls`" needs the binary name and flag shapes constrained. `ls; rm -rf ~` is one string.
7. **Adapter spawn that passes indexed-content via argv.** Argv ends up in `/proc/<pid>/cmdline` (same-UID readable); a malicious commit message becomes a leaked-to-other-process payload. Prefer stdin pipes.
8. **Output validation skipped.** The LLM returns JSON; the JSON is parsed; the fields drive a sink. If the parse uses `json.Unmarshal` into `map[string]any` and then dispatches on string fields, there's no schema. Use typed structs with required fields.
9. **Indexed content rendered into TUI prompts that the user is meant to confirm.** "Run the suggested command?" — if the suggested command's text came from indexed content, the user is confirming attacker-authored text. The prompt must clearly delineate the source.
10. **Memory / context-persistence files.** If ox writes "remembered" content somewhere that the next session re-reads, indexed content can poison future sessions.

## Output format

```json
{
  "class": "llm-trust",
  "subclass": "prompt-injection|tool-dispatch-default-permissive|tool-args-unvalidated|cross-session-bleed|terminal-escape-injection|exec-from-llm-output|argv-leak|output-no-schema|user-confirm-attacker-text|memory-poisoning",
  "severity": "critical|high|medium|low|info",
  "title": "<one sentence>",
  "file": "path/to/file.go",
  "line": 123,
  "source": "commit-message|readme|ledger|team-context|kb|adapter-stdout|mcp|whisper",
  "sink": "prompt-build|tool-dispatch|exec.Command|template.Execute|markdown-render|subagent-spawn",
  "attack": "one paragraph: I commit content X into repo Y; ox indexes it; the LLM is prompted with Z; the LLM responds with W; ox executes W and the result is...",
  "fix": "one paragraph: explicit delimiter framing, schema-validated tool dispatch with allowlist, strip control chars before rendering, stdin pipe instead of argv, typed-struct unmarshal instead of map[string]any"
}
```

## Severity rubric

| Severity | When |
|---|---|
| critical | Indexed content → LLM output → `exec.Command` / file-system write / network call with attacker-controlled args; tool-dispatch default branch that runs anything privileged; cross-tenant memory poisoning |
| high | Prompt injection that exfiltrates other context (auth tokens, other repos, environment) via the adapter's output channel; terminal escape that can plausibly trick a user into confirming an action |
| medium | Markdown rendering of indexed content without ANSI/control-char stripping; argv leak of indexed content to other same-UID processes |
| low | Defensive — add delimiter framing to a prompt that currently lacks it but has no privileged downstream effect |
| info | Stylistic — prompt comment claims a guard that the code doesn't implement |

## Don't

- Don't write "the LLM could be jailbroken" findings. Stay concrete: name the file, the source field, the prompt template, the downstream sink. Speculative jailbreaks are infinite and useless.
- Don't flag prompts that interpolate user content when the prompt explicitly asks the LLM to SUMMARIZE or DESCRIBE the content and the downstream sink is read-only display. The threat is action, not summarization.
- Don't conflate "the LLM might say something dumb" with "untrusted content drove a tool call." The trust boundary is at the sink, not the model.
- Don't propose disabling adapter spawning or limiting capabilities globally as the fix. Per-sink allowlists / schemas are the proportionate response.
- Don't double-flag when the source is `adapter-stdout` and the issue is really chokepoint-bypass in raw_writer — that's `hunter-secrets-redaction`. The split: if the issue is "secret leaks via this path," secrets-redaction owns it; if the issue is "untrusted content drives downstream actions," llm-trust owns it.

---

## FINAL REMINDER

Your entire response is one JSON object or pure JSONL. Begin with `{`. If zero findings: `{"findings":[]}`. No prose. No markdown. No commentary.
