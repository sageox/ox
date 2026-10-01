# ADR-034 — ox CLI guidelines, built on the Command Line Interface Guidelines (clig.dev)

**Status:** Proposed — 2026-09-30.

**Date:** 2026-09-30 · **Deciders:** SageOx Engineering · **Supersedes:** nothing

**Sources:** the [Command Line Interface Guidelines](https://clig.dev/) (clig.dev) by Aanand Prasad, Ben Firshman, Carl Tashian and Eva Parish, consulted at revision [`2bd6023e`](https://github.com/cli-guidelines/cli-guidelines/blob/2bd6023eae2aa60a374c4e7275f935d0917c6c86/content/_index.md) (2026-01-26); the POSIX utility conventions; 12 Factor CLI Apps; the GNU coding standards; the Heroku CLI style guide; no-color.org; and ox's own specs.

---

## Context

ox's CLI conventions live in five places, each written when a specific problem
surfaced:

| Where | What it settles |
|---|---|
| `docs/specs/cli-design-system.md` | output streams, spinner cancellation, `--no-input` / `--yes` / `--force`, argument validation, palette, global flags, help layout |
| `.claude/rules/design.md` | semantic styles, `NO_COLOR`, non-TTY fallbacks, 80 columns, the component catalog |
| `.claude/rules/json-output.md` | one JSON printer; color only on a TTY |
| `docs/specs/agent-ux-principles.md` | output an AI coworker consumes: tokens, JSON, non-blocking, next actions |
| `CLAUDE.md` | `SAGEOX_*` env-var namespace, XDG, `cli.OpenInBrowser`, Required Reviews |

None of them says what a new command owes its users in general: exit codes, help
and examples, flag names, confirmation before destructive actions, configuration
precedence, deprecation, Ctrl-C, secrets, telemetry consent. Each review
re-derives those answers, and they drift. `-v` means `--verbose` globally but
something else on `doctor`, `status`, and `daemon status`. Four commands set
cobra's `Example` field, which ox's help renderer never prints. `-d` means
`--days` on one command and `--description` on another.

The [Command Line Interface Guidelines](https://clig.dev/) are the community
baseline for exactly this: traditional UNIX conventions, updated for CLIs that
people use directly. Cobra, ox's parser, is one of the Go libraries it
recommends.

ox differs from the programs clig.dev was written for in one way that matters. A
large share of its invocations come from AI coworkers — through hooks, skills, and
direct tool calls — with stdout on a pipe, nobody to answer a prompt, and a
context window that pays for every byte of output. clig.dev's human-first
principle needs an explicit ox reading (§3).

clig.dev is licensed [CC BY-SA 4.0](https://github.com/cli-guidelines/cli-guidelines/blob/main/LICENSE),
and `CLAUDE.md`'s licensing policy bans CC-BY-SA material from this repository.
Copyright protects a text's wording and its selection and arrangement, not the
ideas it describes. So the guidelines below are ox's own: grouped the way an ox
command runs, worded for ox, merged where ox treats things together, and joined by
ox-specific rules. They cover every idea in clig.dev, and each item names the
clig.dev section it draws on; none of clig.dev's text or arrangement is
reproduced.

## Decision

### 1. These guidelines are the baseline for every ox command

Every ox command, argument, flag, environment variable, configuration key, and
output stream follows the guidelines below. Three are required — INV-1, OUT-1,
and ERR-1: new code may not deviate from them, and the one existing exception
(`ox agent` dispatch, §5) sets no precedent. Every other guideline is the
default: code that does not follow one is either fixed or carries a recorded
deviation (§5).

### 2. A more specific ox rule wins

Where an ox spec, rule, or ADR is more specific — `cli-design-system.md`,
`json-output.md`, `design.md`, `agent-ux-principles.md`, `CLAUDE.md` — it
governs, and the guideline below points to it instead of restating it. If one of
them contradicts these guidelines, one of the two is wrong: fix it, and do not let
a reviewer pick silently.

### 3. Two audiences: a person at a terminal, an AI coworker on a pipe

The guidelines are written for the person who types a command, and every command
a person types is designed for that person. AI coworkers run the same commands,
and ox serves them through the machinery built for programs: TTY detection,
`--json`, `--no-input`, exit codes, and diagnostics on stderr. Nobody declares
which audience they are — a pipe selects the machine path
(`.claude/rules/json-output.md`, "Why it colors, and why that is safe"). Some
commands also switch to JSON on their own when they detect an AI coworker and
`--json` was not passed (`ox session list`).

The scoped exception is `ox agent …`, whose caller is an AI coworker by design.
It is machine-first: structured output by default, `--text` for a person,
`--review` for both (`cmd/ox/agent.go`, `docs/specs/agent-ux-principles.md` §3).
`ox agent prime` defaults to tagged XML (`--format json` for JSON)
(`cmd/ox/agent_prime.go`). The human-output guidelines reach `ox agent` through
`--text` and `--help`.

### 4. Reviews cite guideline IDs

Each guideline has a stable ID (`OUT-10`, `INPUT-4`). A change that adds or alters
a command, argument, flag, environment variable, configuration key, or output
format is reviewed against these guidelines, and review comments cite the ID. This
ADR adds no tooling.

### 5. Deviations are deliberate and written down

A rule worth breaking is broken on purpose (P3). An ox deviation is recorded here
or in the spec that owns the behavior, with its reason. A guideline that turns out
wrong for CLIs in general, not only for ox, is also worth proposing to clig.dev.
The deviations ox holds today:

| Guideline | ox behavior | Why |
|---|---|---|
| P1, OUT-2 | `ox agent …` is machine-first (§3). | Its caller is an AI coworker by design. |
| INV-1, INV-2 | `ox agent <id> <verb>` is dispatched by hand: the agent ID precedes the verb, `agent` accepts arbitrary arguments, and the verbs' flags are hidden persistent flags on `agent` that the dispatcher re-injects (`runAgentDispatcher` in `cmd/ox/agent.go`). The verbs have no help of their own; `--help` shows `ox agent`'s. | Every verb acts on one AI coworker instance, named by its ID (`docs/specs/agent-ux-ox-implementation.md`). |
| HELP-3, INV-12 | A curated catalog (`cmd/ox/default_catalog.json`: 40 `auto_execute` entries, confidence ≥ 0.85) re-runs a mistyped command once as its correction, after printing `-> Correcting to: …`. `ox setup` runs `ox init`; `ox auth` runs `ox login`. | Each entry records a guess AI coworkers or people commonly make, so it resolves without a round trip. Accepted cost: some targets change state, and every entry is syntax ox keeps supporting. |
| INPUT-2 | Yes/no, numbered, and text prompts accept answers piped on a non-TTY stdin; `--no-input` is the strict mode. | Existing scripts pipe answers (`cli-design-system.md` § Prompts and Automation; `internal/cli/confirm.go`). |
| INPUT-6 | `SAGEOX_TOKEN` supplies a SageOx access token from the environment and outranks a stored login (`internal/auth/env_token.go`). ox also reads other tools' standard token variables (`GITHUB_TOKEN`, `GH_TOKEN`, `GITLAB_TOKEN`, …) in `internal/identity/`. | CI/CD, headless AI coworkers, and ephemeral containers have no browser for the device flow, and no flag or file option exists yet. A malformed `SAGEOX_TOKEN` fails closed, and adapter processes get an allowlisted environment (`internal/envutil`). |
| INV-4 | `-v` is the global `--verbose`; `--version` has no short form. | The familiar-flags list in `agent-ux-principles.md` §5 (`-v, --verbose`). |
| OUT-9 | After `ox init` and `ox login`, the daemon syncs the Ledger and Team Context in the background, and hooks run ox inside AI coworker sessions. | That is the product. `ox init` and `ox login` are the explicit consent, and `ox status` shows the state. |
| CONF-2 | User configuration outranks repository and team configuration: flags > environment > user > repo > team > default. | `README.md`: a repo or team can suggest a default, but cannot override a person's own preference, such as recording mode or telemetry. |
| CONF-4 | `ox agent prime` and `ox doctor`'s automatic fixes restore missing prime markers and Claude Code hooks without asking (`EnsureOxPrimeMarker`, `ensureClaudeHooks`, `FixLevelAuto`). | `ox init` is the consent; repairs keep that installed state intact (`CLAUDE.md` § Doctor as Last Line of Defense). `ox uninstall` removes it. |
| CONF-6 (`TMPDIR`) | Daemon logs go to `/tmp/<user>/sageox` on macOS and Linux, ignoring `TMPDIR` (`paths.TempDir`). | macOS's per-user temporary directory is purged unpredictably (comment in `internal/paths/paths.go`). |
| SHIP-1 | The binary is `ox`, two letters. | It is the product name, and its two keys fall on alternate hands. |
| SHIP-2 | Releases ship `ox` plus ten `ox-adapter-*` binaries. | Adapters are external binaries by design ([ADR-008 — External Adapter Binaries](ADR-008-external-adapter-binaries.md)); each is a CGO-free static build ([ADR-001](ADR-001-pure-go-no-cgo.md)). |

### 6. Known gaps are follow-up work

[Known gaps](#known-gaps-audited-2026-09-30) lists where ox falls short of these
guidelines today. This ADR fixes none of them. A gap is closed when its command is
next changed or through its own issue; new code does not add to the list.

---

## The guidelines

IDs are stable: never renumber, and retire an ID rather than reuse it. Each item
names its source. Each **ox:** note records how ox meets the guideline today
(audited 2026-09-30), or points to §5 or
[Known gaps](#known-gaps-audited-2026-09-30).

These guidelines cover command-line programs, not full-screen terminal
applications such as editors. ox's full-screen Bubble Tea screens follow
`.claude/rules/design.md`; entering one still follows INPUT-2 and OUT-11.

### Principles (P)

- **P1 · Two audiences, both first-class.** A person at a terminal and an AI
  coworker on a pipe run the same commands. Design each command for the person
  who types it, and serve programs through the standard machinery: streams, exit
  codes, TTY detection, structured output. *Source: [clig.dev § Human-first
  design](https://clig.dev/#human-first-design); §3.*
- **P2 · Every command is a part in someone else's system.** Scripts, CI jobs,
  and AI coworker tool loops will embed ox in ways nobody planned. Standard
  streams, exit codes, signals, line-oriented text, and JSON are what make it a
  well-behaved part, and none of them costs a person anything. *Source:
  [clig.dev § Simple parts that work together](https://clig.dev/#simple-parts-that-work-together).*
- **P3 · Familiar first; break conventions on purpose.** A command that behaves
  like the tools people already know is guessable — by people, and by the models
  behind AI coworkers. Depart from a convention only when it demonstrably hurts
  usability, and record the departure (§5). *Source: [clig.dev § Consistency
  across programs](https://clig.dev/#consistency-across-programs) and
  [§ Chaos](https://clig.dev/#chaos); `agent-ux-principles.md` §5.*
- **P4 · Spend the reader's attention carefully.** Minutes of silence read as a
  hang; pages of detail bury the one line that matters. For an AI coworker, every
  surplus line also spends context-window tokens. *Source: [clig.dev § Saying
  (just) enough](https://clig.dev/#saying-just-enough); `agent-ux-principles.md`
  §2.*
- **P5 · Teach while working.** People learn a CLI by trying, failing, and
  adjusting. Examples in help, a suggested next command, a concrete fix on
  failure, a clear view of where a multi-step flow stands, and a confirmation
  before a risky step each shorten that loop. *Source: [clig.dev § Ease of
  discovery](https://clig.dev/#ease-of-discovery) and [§ Conversation as the
  norm](https://clig.dev/#conversation-as-the-norm).*
- **P6 · Be robust, and feel it.** Handle bad input gracefully and make operations
  idempotent where possible. Feeling robust comes from responsiveness, keeping
  people informed, explaining common errors, and never showing a raw stack trace.
  Simple code breaks less than code full of special cases. *Source:
  [clig.dev § Robustness](https://clig.dev/#robustness-principle).*
- **P7 · Be on the user's side.** People should enjoy the tool because it is
  evident its authors thought about their problem — not because of gimmicks.
  *Source: [clig.dev § Empathy](https://clig.dev/#empathy).*

### Invocation (INV) — parsing, arguments, flags, subcommands

- **INV-1 · Parse with an established library** — required. Hand-rolled parsing
  gets flags, help, and typo suggestions subtly wrong. *Source: [clig.dev § The
  Basics](https://clig.dev/#the-basics).*
  - **ox:** Cobra. The `ox agent <id> <verb>` dispatcher is the recorded exception
    (§5).
- **INV-2 · Name inputs with flags; keep positional arguments for lists.** Flags
  read clearly and leave room to add inputs later without ambiguity. Positional
  arguments suit a list of like items handled the same way, which also lets shell
  globs work, or a pair so common its order is worth memorizing
  (`cp <src> <dst>`). Otherwise, two positional arguments with different roles
  signal a design problem. *Source: [clig.dev § Arguments and
  flags](https://clig.dev/#arguments-and-flags).*
  - **ox:** `ox agent <id> <verb>` — §5.
- **INV-3 · Long names always; single letters sparingly.** Every flag has a long
  form, which scripts use for readability. Single letters go only to flags people
  use often, especially at the top level, so the supply lasts. *Source:
  [clig.dev § Arguments and flags](https://clig.dev/#arguments-and-flags).*
- **INV-4 · Reuse the flag names other tools use.** People guess flags from other
  tools. The common ones, by purpose:
  - Help and identity — `-h`/`--help` (help only, never anything else) and
    `--version`.
  - Output — `--json`, `-q`/`--quiet`, `-o`/`--output` (an output file), and
    `-d`/`--debug` (debugging output).
  - Safety — `-n`/`--dry-run` (report the changes without making them),
    `-f`/`--force` (also skips the confirmation a destructive action otherwise
    asks for), and `--no-input`.
  - Scope — `-a`/`--all`, `-u`/`--user`, and `-p`/`--port`.
  - `-v` is ambiguous between verbose and version; clig.dev suggests `-d` for
    verbose output and `-v` for version, or leaving `-v` unused.

  *Source: [clig.dev § Arguments and flags](https://clig.dev/#arguments-and-flags).*
  - **ox:** `--json`, `-h`/`--help`, `--no-input`, `-q`/`--quiet`, `--version`,
    `--force` (`-f` on `logout`), and `--dry-run` (`uninstall`, `session prune`,
    `session regenerate`, …) match. `-v` is a deviation (§5); `-d` and `-n` are
    Known gaps.
- **INV-5 · Accept flags wherever the parser allows.** People add a flag by
  recalling the last command and appending it. *Source: [clig.dev § Arguments and
  flags](https://clig.dev/#arguments-and-flags).*
  - **ox:** cobra parses a root flag before or after the subcommand
    (`ox --json version` is `ox version --json`).
- **INV-6 · `-` stands for stdin or stdout.** Wherever an input or output is a
  file, `-` reads stdin or writes stdout, so commands chain without temporary
  files. *Source: [clig.dev § Arguments and
  flags](https://clig.dev/#arguments-and-flags).*
  - **ox:** `-` reads stdin in `agent redact test`, `bulletin post`, `scout`,
    `session push-summary --file`, `viz render --data`, and `viz lint`. Gap:
    nothing writes stdout on `-` (Known gaps).
- **INV-7 · An optional flag value has a word for "nothing".** Accept a sentinel
  such as `none` rather than an empty value, which leaves it unclear whether the
  next token belongs to the flag. *Source: [clig.dev § Arguments and
  flags](https://clig.dev/#arguments-and-flags).*
- **INV-8 · Defaults serve most users.** Few people discover a flag or keep using
  it; behavior that needs one reaches almost nobody. *Source: [clig.dev §
  Arguments and flags](https://clig.dev/#arguments-and-flags).*
- **INV-9 · Subcommands share one structure.** Subcommands split a large tool and
  gather related tools under one name, sharing global flags, help, configuration,
  and storage. Name them noun then verb (`ox session list`), use the same verb for
  the same operation on every noun, and give one concept the same flag name and
  output conventions everywhere. *Source: [clig.dev §
  Subcommands](https://clig.dev/#subcommands).*
  - **ox:** mostly noun-verb (`ox session list`, `ox plan status`,
    `ox team invite`). Global flags are defined once on the root, and
    command-specific flags stay on their command group (`cli-design-system.md` §
    Flag Scoping). Gaps in Known gaps.
- **INV-10 · No near-synonym commands.** `update` beside `upgrade` confuses; pick
  distinct words or qualify them. *Source: [clig.dev §
  Subcommands](https://clig.dev/#subcommands).*
  - **ox:** `ox upgrade` updates ox itself, and `update` exists only under
    `addons` and `carts`. Other overlaps are Known gaps.
- **INV-11 · No implicit default subcommand.** If an unknown first word falls
  through to a default action (`tool foo` meaning `tool run foo`), no subcommand
  with that name can ever be added without breaking someone. *Source:
  [clig.dev § Future-proofing](https://clig.dev/#future-proofing).*
  - **ox:** the root has none. Gap: `murmur` and `viz` (Known gaps).
- **INV-12 · No prefix matching; aliases are explicit and permanent.** Accepting
  any unambiguous prefix (`ins` for `install`) turns every later command that
  shares those letters into a breaking change. *Source: [clig.dev §
  Future-proofing](https://clig.dev/#future-proofing).*
  - **ox:** cobra's prefix matching is off; aliases are explicit (`teams`, `conv`,
    `bubble`). Each auto-run correction (§5) is an alias in effect.

### Help and documentation (HELP)

- **HELP-1 · `-h` and `--help` show full help, from anywhere.** On the root and
  every subcommand, appending `-h` or `--help` shows complete help and ignores the
  rest of the line. A multi-command tool also answers `ox help` and
  `ox help <cmd>`. *Source: [clig.dev § Help](https://clig.dev/#help).*
  - **ox:** cobra supplies every form, and `brandedHelp` renders them
    (`cmd/ox/root.go`). Dispatched `ox agent <id> <verb>` commands share
    `ox agent`'s help (§5).
- **HELP-2 · Missing required input gets short usage.** A command that can do
  nothing without arguments, run with none, prints what it does, one or two
  examples, its flags (unless there are many), and a pointer to `--help`.
  Commands that are interactive by default are exempt. *Source: [clig.dev §
  Help](https://clig.dev/#help).*
  - **ox:** `ox` alone, and a command group without a handler, prints help and
    exits 0.
- **HELP-3 · Suggest the likely command; don't run it unasked.** When input is
  wrong and the intent is guessable, say what to run. Offering to run it is fine;
  running it without asking is not the default, because the mistake may be a
  logic error, guessing is dangerous for a command that changes state, and every
  accepted wrong form becomes syntax to support forever. Accept an alternate form
  only deliberately, and document both. *Source: [clig.dev §
  Help](https://clig.dev/#help).*
  - **ox:** frictionax suggests a correction: `Did you mean?` on stderr for a
    person, or a `_suggestion` object on stdout under `--json` or in an AI
    coworker's context (`cmd/ox/main.go`). Auto-running a correction is a
    deviation (§5); misfires are Known gaps.
- **HELP-4 · Examples come first.** People copy examples before they read flag
  tables. Show the common and the tricky invocations first, with output when it
  is short, building from simple to advanced. A long set belongs in a dedicated
  examples command or on a web page, and an integration may deserve a tutorial.
  *Source: [clig.dev § Help](https://clig.dev/#help).*
  - **ox:** not met for most commands — Known gaps.
- **HELP-5 · Most-used first, grouped by task.** *Source: [clig.dev §
  Help](https://clig.dev/#help).*
  - **ox:** six task groups (Software Development, Knowledge, Teams,
    Authentication, Agent Integration, Diagnostics), alphabetical within each; a
    ★ marks the suggested next step (`cli-design-system.md` § Contextual
    Highlighting).
- **HELP-6 · Scannable, and plain when piped.** Use formatting such as bold
  headings, and emit no escape sequences when help goes to a pipe or a pager.
  *Source: [clig.dev § Help](https://clig.dev/#help).*
  - **ox:** `brandedHelp` styles its headings; when stdout is not a TTY,
    `cmd/ox/main.go` strips ANSI from everything written to it.
- **HELP-7 · Help points onward.** Top-level help names where to report a bug or
  give feedback, and help text links the web documentation — the subcommand's own
  page when there is one. *Source: [clig.dev § Help](https://clig.dev/#help).*
  - **ox:** not met — Known gaps.
- **HELP-8 · Document on the web and in the terminal.** Web documentation can be
  searched and linked. Terminal documentation is instant, works offline, and
  matches the installed version; reach it through the tool itself, because `man`
  is not on every platform and not everyone knows it. Man pages are worth
  considering, generated from the same source as the web docs. *Source:
  [clig.dev § Documentation](https://clig.dev/#documentation).*
  - **ox:** `ox docs` (hidden; run by `make docs`, checked by `docs-check`)
    generates `docs/reference/*.mdx` from the cobra tree, and
    `.github/workflows/docs.yml` publishes it. `internal/docs/postprocess.go`
    leaves out command trees it calls internal or advanced, among them `agent`,
    `config`, `daemon`, and `version`. In the terminal: `--help`, and
    `ox guide <topic>` for bundled topical guides. No man pages; the cobra `doc`
    package that generates the reference docs can generate them too.

### Input, prompts, and secrets (INPUT)

- **INPUT-1 · Never wait silently on a terminal's stdin.** A command that expects
  piped data, started with a terminal on stdin, prints help or a note on stderr
  and exits. *Source: [clig.dev § Help](https://clig.dev/#help).*
  - **ox:** most stdin readers check for a TTY and refuse or fall back
    (`ox agent <id> session import`, `session plan`, `session context-trace`).
    Gap: `ox plan enrich` and `ox plan render` (Known gaps).
- **INPUT-2 · Prompt only when stdin is a TTY.** In an interactive session, ask
  for a missing value instead of failing. Without a TTY nobody can answer, so fail
  with an error that names the flag to pass. *Source: [clig.dev §
  Interactivity](https://clig.dev/#interactivity) and [§ Arguments and
  flags](https://clig.dev/#arguments-and-flags).*
  - **ox:** branch on `cli.IsInteractive()` / `cli.IsHeadless()`; every
    interactive widget must have a non-TTY fallback (`.claude/rules/design.md`
    rule 6). Piped prompt answers are a deviation (§5).
- **INPUT-3 · Every prompt has a flag; `--no-input` turns them all off.** Anything
  a prompt asks for can also be given as a flag or argument. With `--no-input`,
  nothing prompts or waits; missing input fails and names the flag that supplies
  it. *Source: [clig.dev § Interactivity](https://clig.dev/#interactivity) and
  [§ Arguments and flags](https://clig.dev/#arguments-and-flags).*
  - **ox:** global `--no-input` (`cli-design-system.md` § Prompts and Automation),
    which leaves explicit stdin data and protocol input — session imports, Git
    credential requests — available. A confirmation that cannot be asked fails
    with `confirmation required: re-run in a terminal, or pass --yes`
    (`internal/cli/confirm.go`).
- **INPUT-4 · Confirm in proportion to the damage.** At a terminal, ask for `y` or
  `yes`; otherwise require `--force`.
  - *Low* — a small local change, or a command whose name is the action itself.
    Confirmation is optional.
  - *Medium* — removing a directory or a remote resource, or a bulk change that is
    hard to undo. Confirm, and offer a dry run.
  - *High* — destroying something large, such as a whole remote project. Make the
    person type its name, and keep it scriptable with a flag like
    `--confirm=<name>`.
  - Count indirect damage: lowering a number in a config file so that the surplus
    is silently deleted is high.

  *Source: [clig.dev § Arguments and flags](https://clig.dev/#arguments-and-flags).*
  - **ox:** medium — `ox agent <id> session abort` and `session delete` confirm at
    a terminal and otherwise require `--force`; `logout`, `session remove`,
    `session prune`, `session regenerate`, and `coworker remove` confirm. High —
    `ox uninstall` has the person type the repository name or `uninstall`, offers
    `--dry-run`, and does not accept `--yes` in place of `--force`. `--yes`
    authorizes yes/no confirmations only (`cli-design-system.md` § Prompts and
    Automation).
- **INPUT-5 · Make the way out obvious.** Ctrl-C works even during a network wait.
  A wrapper around a program that takes Ctrl-C for itself (ssh, tmux) says how to
  get out. *Source: [clig.dev § Interactivity](https://clig.dev/#interactivity).*
  - **ox:** menus close on Ctrl-C, `q`, or Esc, and an interrupted spinner prints
    `Interrupted.` and exits 130 (`cli-design-system.md` § Spinner Cancellation).
- **INPUT-6 · Secrets never travel in flags or environment variables.** A flag
  value shows up in `ps` output and shell history, and `--password "$(< file)"`
  leaks the same way. An exported variable reaches every child process and can
  end up in logs; expanded into a command line
  (`curl -H "Authorization: Bearer $TOKEN"`), it shows in the process list; and
  `docker inspect` or `systemctl show` reveals it to anyone with that access. Take
  secrets from a file (`--password-file`), stdin or another pipe, an `AF_UNIX`
  socket, a secret manager, or another IPC channel. *Source: [clig.dev §
  Arguments and flags](https://clig.dev/#arguments-and-flags) and [§ Environment
  variables](https://clig.dev/#environment-variables).*
  - **ox:** no flag takes a secret; `ox login` uses the OAuth device flow in a
    browser. `SAGEOX_TOKEN` is a deviation (§5).
- **INPUT-7 · Don't echo a secret as it is typed.** *Source: [clig.dev §
  Interactivity](https://clig.dev/#interactivity).*
  - **ox:** ox never prompts for a secret.

### Output (OUT)

- **OUT-1 · Results to stdout, messages to stderr** — required. The primary result,
  and anything machine-readable, goes to stdout, which is what a pipe carries.
  Progress, warnings, errors, and log lines go to stderr, which reaches the
  person. *Source: [clig.dev § The Basics](https://clig.dev/#the-basics).*
  - **ox:** `cli.PrintError` and `cli.PrintWarning` write to stderr in text and
    JSON modes (`cli-design-system.md` § Output Streams), as do tips and
    suggestion boxes (`internal/cli/output.go`).
- **OUT-2 · Decide per stream whether a person is reading.** A TTY means a person;
  anything else means a program. *Source: [clig.dev §
  Output](https://clig.dev/#output).*
  - **ox:** `theme.Profile` decides color from stdout. When stdout or stderr is
    not a TTY, `cmd/ox/main.go` strips ANSI from that stream, and in an AI
    coworker's context it sets `NO_COLOR=1`. `ox agent` is a deviation (§5).
- **OUT-3 · `--json` prints structured output.** JSON carries structure that text
  cannot, and it works with `jq` and web tooling. *Source: [clig.dev §
  Output](https://clig.dev/#output).*
  - **ox:** `.claude/rules/json-output.md` routes every `--json` payload through
    `cli.PrintJSON` / `cli.PrintJSONTo`; `OX_JSON=1` makes JSON the default, and
    an explicit `--json`/`--json=false` wins (`cli-design-system.md` § Output
    Streams).
- **OUT-4 · Text composes; `--plain` when the layout would not.** Line-oriented
  output should work with `grep` and its relatives — assume today's output feeds a
  program nobody has written yet. When the human layout spreads one record over
  several lines (wrapped table cells, say), `--plain` prints one record per line
  without decoration. *Source: [clig.dev § Output](https://clig.dev/#output).*
  - **ox:** not met — Known gaps.
- **OUT-5 · Output an AI coworker reads is budgeted and actionable.** Lead with the
  instruction, leave out what the reader already has, keep payloads small, and
  carry the next action in the output's `guidance` field rather than in prose a
  skill has to repeat. *Source: `agent-ux-principles.md` §2–§4; ADR-023 Layer 1;
  `CLAUDE.md` thin-relay rule.* <!-- SOURCE: sageox adr:docs/adr/ADR-023-skill-injection-two-layer-model.md -->
  - **ox:** `--json` envelopes carry a `guidance` field (`ox decision enrich`,
    `ox pr header`), and `ox session list` switches to JSON on its own in an AI
    coworker's context.
- **OUT-6 · Say success briefly; `-q` silences the rest.** Silence after success
  reads as a hang, so print something short. `-q`/`--quiet` drops non-essential
  output for scripts, so they need not redirect stderr to `/dev/null`. *Source:
  [clig.dev § Output](https://clig.dev/#output).*
  - **ox:** global `-q`/`--quiet` (or `OX_QUIET=1`). Gap: few commands read it
    (Known gaps).
- **OUT-7 · Default output is for users.** Detail only the authors can interpret,
  log-level labels (`WARN`, `ERR`), and log context belong in verbose mode; stderr
  is not a log file. People new to the project spot this best, so ask them.
  *Source: [clig.dev § Output](https://clig.dev/#output).*
  - **ox:** `-v`/`--verbose` turns on debug logs and the per-phase timing tree.
    `.claude/rules/design.md` rule 8 governs log format (single-line
    `key=value`), not whether logs appear by default. Gaps in Known gaps.
- **OUT-8 · Keep the user's picture of state accurate.** Say what a
  state-changing command changed, especially when the result differs from what
  was literally asked. Give state that is not visible in the filesystem a command
  that shows it, with hints on how to change it. Suggest the next command in a
  workflow. *Source: [clig.dev § Output](https://clig.dev/#output).*
  - **ox:** `ox status`, and `ox doctor` for health. Next steps come from
    contextual highlighting in help for people (`cli-design-system.md` §
    Contextual Highlighting) and from `guidance` for AI coworkers (OUT-5).
- **OUT-9 · Leaving the program's own world is explicit.** Reading or writing files
  the user did not name (other than the program's own state, such as a cache), and
  talking to a server, should happen because the user asked. *Source: [clig.dev §
  Output](https://clig.dev/#output).*
  - **ox:** a deviation — §5.
- **OUT-10 · Color with intent, and only where it can be seen.** Use color to
  direct attention, and sparingly. Turn it off when the stream is not a TTY
  (judged per stream, so piping stdout keeps color on stderr), when `NO_COLOR` has
  any non-empty value, when `TERM=dumb`, or with `--no-color`; `FORCE_COLOR`
  turns it on regardless. A program-specific switch is optional. *Source:
  [clig.dev § Output](https://clig.dev/#output) and [§ Environment
  variables](https://clig.dev/#environment-variables);
  [no-color.org](https://no-color.org/).*
  - **ox:** semantic styles only, never raw colors (`.claude/rules/design.md`
    rule 3). A true-valued `NO_COLOR` forces no color on every stream
    (`internal/theme/profile.go`; `.claude/rules/design.md` rule 7). `TERM=dumb`,
    `CLICOLOR`, and `CLICOLOR_FORCE` follow the colorprofile library;
    `OX_COLOR_PROFILE` forces a color depth for debugging. Gaps in Known gaps.
- **OUT-11 · Animate only when stdout is a TTY.** Spinners and progress bars turn
  into pages of noise in a CI log. *Source: [clig.dev §
  Output](https://clig.dev/#output).*
  - **ox:** `cli.WithSpinner` waits 300 ms before drawing, and draws nothing under
    `--no-interactive` (on by default when `CI=true`) or `--no-input`. Gap: it
    checks stdin, not stdout (Known gaps).
- **OUT-12 · Dense notation and glyphs, where they clarify.** Compact, learnable
  notation (like `ls -l` permission strings) lets experienced readers take in a
  lot at a glance, and symbols or emoji can separate items or draw the eye. Both
  stop helping once they clutter. *Source: [clig.dev §
  Output](https://clig.dev/#output).*
  - **ox:** `.claude/rules/design.md` rule 11 (Tufte minimum).
- **OUT-13 · Page long output.** Page only when stdin or stdout is a TTY, and honor
  `PAGER`. `less -FIRX` is a sound default: no paging for a single screen,
  case-insensitive search, color passed through, and the text left on screen
  after quitting. A pager library can be sturdier than piping to `less`; a broken
  pager is worse than none. *Source: [clig.dev § Output](https://clig.dev/#output)
  and [§ Environment variables](https://clig.dev/#environment-variables).*
  - **ox:** not met — Known gaps.

### Errors and exit status (ERR)

- **ERR-1 · Exit status tells success from failure** — required. 0 means success
  and anything else means failure, with distinct codes for the failure modes a
  caller must tell apart. *Source: [clig.dev § The
  Basics](https://clig.dev/#the-basics).*
  - **ox:** 0 on success; 1 on failure; 2 for some usage errors (`conversation`,
    hosted Ledger reads); 130 for an interrupted spinner. A command that has
    already printed its own error returns a typed `commandExitError` with its
    code (`cmd/ox/main.go`). `ox doctor` exits 1 when a check fails or setup is
    missing, even with `--json`; warnings alone exit 0. Gaps in Known gaps.
- **ERR-2 · Expected errors become instructions.** Catch the failures you can
  foresee, and say what happened and which command fixes it. *Source: [clig.dev §
  Errors](https://clig.dev/#errors).*
  - **ox:** case by case: `cli.PrintSuggestionBox` (title, message, fix),
    `session.SessionError` (`Code`, `Message`, `Retryable`, `Fix`), and
    remediation written into error text — `ox agent <id> session abort` without a
    terminal says to pass `--force`. `agent-ux-principles.md` § Error Responses
    asks JSON errors for an actionable `action`.
- **ERR-3 · Make the important line easy to find.** Group repeated errors of one
  kind under a single explanation instead of many near-identical lines; put the
  most important line last, where the eye lands; and use red sparingly. *Source:
  [clig.dev § Errors](https://clig.dev/#errors).*
- **ERR-4 · Unexpected errors come with a way forward.** Give debugging detail
  without flooding the screen — a log file and its path works well — and make a
  report easy to file, ideally through a URL that pre-fills it. *Source:
  [clig.dev § Errors](https://clig.dev/#errors).*
  - **ox:** not met — Known gaps.

### While it runs (RUN)

- **RUN-1 · Validate everything before acting.** Every input will eventually be
  bad. Check it all first, fail before any side effect, and make the error
  understandable (ERR-2). *Source: [clig.dev §
  Robustness](https://clig.dev/#robustness-guidelines).*
  - **ox:** flag-only commands that change state use `cobra.NoArgs`
    (`cli-design-system.md` § Argument Validation).
- **RUN-2 · Respond within about 100 ms.** Print something quickly, and announce a
  network wait before it starts so it does not look like a hang. *Source:
  [clig.dev § Robustness](https://clig.dev/#robustness-guidelines).*
  - **ox:** long operations go to the daemon so the command returns
    (`agent-ux-principles.md` §1, "Never Block the Event Loop").
    `cli.WithSpinner` draws only after 300 ms, and `ox login` prints `Initiating
    device authentication flow...` before its first request. Gap in Known gaps.
- **RUN-3 · Show progress, and keep parallel output legible.** Long work gets a
  spinner or a progress bar, with an estimate or motion so it never looks stuck.
  Concurrent work must not interleave into noise. Logs hidden behind progress on
  success are printed on failure. Libraries do this better than hand-rolled code.
  *Source: [clig.dev § Robustness](https://clig.dev/#robustness-guidelines).*
  - **ox:** spinners (`cli.WithSpinner`) cover `sync`, `recap`, `login`, and parts
    of `doctor`, which also rewrites a `Checking …` line as it goes; `ox init`
    hands cloning to the daemon.
- **RUN-4 · Time out.** Network operations have a sensible default timeout that
  can be configured, so nothing hangs forever. *Source: [clig.dev §
  Robustness](https://clig.dev/#robustness-guidelines).*
  - **ox:** the SageOx clients set timeouts — 2 s for telemetry, 5 to 60 s for API
    and auth calls, up to 5 min for LFS transfers and upgrade downloads — and git
    runs with `http.lowSpeedLimit`/`http.lowSpeedTime`
    (`internal/gitutil/http_timeout.go`). Gap: none is configurable (Known gaps).
- **RUN-5 · Ctrl-C ends things quickly.** Acknowledge at once, before any cleanup,
  and bound the cleanup with a timeout. A second Ctrl-C skips slow cleanup; when
  skipping it is destructive, say so first. *Source: [clig.dev § Signals and
  control characters](https://clig.dev/#signals).*
  - **ox:** no entry-point handler: outside a Bubble Tea screen, Go's default
    handling ends the process at once. Commands that serve until interrupted
    (`plan review`, `sync --read-only`, hosted Ledger reads,
    `session trace serve`) use `signal.NotifyContext`. The daemon bounds its own
    shutdown: up to 30 s for the code index, then 5 s for goroutines
    (`internal/daemon/daemon.go`). Gap: `plan review` (Known gaps).
- **RUN-6 · Re-running recovers; nothing needs cleanup.** After a transient
  failure, running the same command again picks up where it stopped. Avoid work
  that needs cleanup, or defer cleanup to the next run, so the program can exit at
  once on failure or interrupt — and starts safely after a run whose cleanup never
  finished. *Source: [clig.dev § Robustness](https://clig.dev/#robustness-guidelines);
  [Crash-only software](https://lwn.net/Articles/191059/).*
  - **ox:** `CLAUDE.md` makes `ox doctor` the last line of defense: it detects and
    repairs every known failure mode.
- **RUN-7 · Expect misuse.** ox will be wrapped in scripts, run over bad networks,
  run as many copies at once, and run in environments nobody tested, such as
  filesystems that ignore case. *Source: [clig.dev §
  Robustness](https://clig.dev/#robustness-guidelines).*
  - **ox:** many AI coworkers and worktrees drive one machine's clones at once;
    git operations are serialized per clone
    ([ADR-030](ADR-030-per-clone-git-serialization.md)).

### Configuration and environment (CONF)

- **CONF-1 · Choose where a setting lives by how it varies.**
  1. Changes from one run to the next (debug output, a dry run) → flags;
     environment variables may help.
  2. Stable for a person or machine but different across projects or people (a
     non-default path, color, an HTTP proxy) → flags and environment variables,
     set in a shell profile or a project's `.env` (CONF-7); a configuration file
     if it is complex.
  3. The same for everyone on a project → a command-specific file under version
     control.

  *Source: [clig.dev § Configuration](https://clig.dev/#configuration).*
  - **ox:** (1) flags; (2) `SAGEOX_*` and `OX_*` variables and the user config
    `~/.config/sageox/config.yaml`; (3) the committed `.sageox/config.json` and
    the Team Context's `config.toml`. Machine-local project state goes in the
    gitignored `.sageox/config.local.toml`.
- **CONF-2 · Apply precedence, highest first:** flags; the running shell's
  environment; project configuration; user configuration; system configuration.
  *Source: [clig.dev § Configuration](https://clig.dev/#configuration).*
  - **ox:** a deviation — §5.
- **CONF-3 · Follow the XDG Base Directory specification.** User configuration
  goes under `~/.config`, honoring the `XDG_*` variables, not into new dotfiles in
  the home directory. *Source: [clig.dev § Configuration](https://clig.dev/#configuration);
  [XDG Base Directory Specification](https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html).*
  - **ox:** config, data, and cache default to `$XDG_CONFIG_HOME/sageox`,
    `$XDG_DATA_HOME/sageox`, and `$XDG_CACHE_HOME/sageox` on every OS; the legacy
    `~/.sageox` applies only under `OX_XDG_DISABLE` (`internal/paths/`). Changing
    a path is a Required Review (`CLAUDE.md`). Gaps in Known gaps.
- **CONF-4 · Ask before changing another program's configuration, and say exactly
  what changes.** Prefer adding a file you own over appending to a shared one;
  when a shared file must change, mark your lines (a dated comment works).
  *Source: [clig.dev § Configuration](https://clig.dev/#configuration).*
  - **ox:** ox prefers files it owns — skills and rules under reserved names, with
    scoped `.gitignore` blocks marked `# >>> ox-managed … >>>`
    ([ADR-031](ADR-031-cli-skill-rule-inventory.md) §§1, 5) — and no longer
    writes the root `.gitignore`. <!-- SOURCE: sageox adr:docs/adr/ADR-031-cli-skill-rule-inventory.md -->
    Shared files get marked additions: `<!-- ox:prime-check -->` and
    `<!-- ox:prime -->` lines in `AGENTS.md`/`CLAUDE.md`, and
    `# ox <name> hook` … `# end ox hook` in git hooks. `ox doctor` prints the
    shell-profile line to add rather than editing the file. Restoring removed
    markers and hooks is a deviation (§5); the rest is in Known gaps.
- **CONF-5 · Environment variables describe the context.** Use them for behavior
  that varies with where the command runs — the terminal session, the machine, the
  checkout — whether they mirror a flag or setting or stand alone. Names use
  uppercase letters, digits, and underscores and do not start with a digit;
  values stay on one line so `env` output stays readable; and names already in
  wide use, such as POSIX's, stay off-limits. *Source: [clig.dev § Environment
  variables](https://clig.dev/#environment-variables);
  [POSIX environment variables](https://pubs.opengroup.org/onlinepubs/009695399/basedefs/xbd_chap08.html).*
  - **ox:** every variable ox reads has a portable name. New customer-facing names
    use `SAGEOX_*`, and any new customer-facing name is a Required Review
    (`CLAUDE.md`). Gap: customer-facing `OX_*` names (Known gaps).
- **CONF-6 · Honor the shared variables.**
  - Terminal — `TERM`, `TERMINFO`, `TERMCAP`, `LINES`, `COLUMNS`, `NO_COLOR` and
    `FORCE_COLOR` (OUT-10), and `PAGER` (OUT-13).
  - Locations — `HOME` for configuration and `TMPDIR` for temporary files.
  - Network — `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, and `NO_PROXY`.
  - Tools — `EDITOR` when the person must edit a file or enter more than one line;
    `SHELL` to open the person's interactive shell (run scripts with a fixed
    interpreter such as `/bin/sh`).
  - Detail — `DEBUG` for more output.

  *Source: [clig.dev § Environment variables](https://clig.dev/#environment-variables).*
  - **ox:** honored — `NO_COLOR` (with a gap), `CLICOLOR_FORCE`, `TERM` (through
    colorprofile), `HOME` (through `os.UserHomeDir`), the proxy variables (through
    Go's default HTTP transport), `COLUMNS` (`recap`), `SHELL` (a doctor check),
    and `XDG_*`. ox opens no editor, so `EDITOR` does not apply. `TMPDIR` is a
    deviation (§5); `DEBUG` is a Known gap, and `FORCE_COLOR` and `PAGER` are
    tracked under OUT-10 and OUT-13.
- **CONF-7 · Read `.env`, but don't treat it as configuration.** For variables
  that hold while working in one directory, also read a local `.env`. It is no
  substitute for a configuration file: it is usually not in version control (so it
  has no history), holds only strings, drifts into disorder, invites encoding
  trouble, and often holds credentials. *Source: [clig.dev § Environment
  variables](https://clig.dev/#environment-variables).*
  - **ox:** ox loads `.env.local`, then `.env`, from the working directory at
    startup, without overriding variables already set; hosted Ledger reads skip
    them (`cmd/ox/main.go`).

### Changing the interface (CHANGE)

- **CHANGE-1 · Interfaces change only through a documented deprecation.**
  Subcommands, arguments, flags, configuration files, and environment variables
  are interfaces — and for ox, so are `--json` payloads and the `ox agent` output
  AI coworkers parse. Semantic versioning does not excuse constant breakage.
  *Source: [clig.dev § Future-proofing](https://clig.dev/#future-proofing).*
- **CHANGE-2 · Prefer additive changes.** Add a new flag rather than changing an
  old one incompatibly, as long as the interface does not bloat. *Source:
  [clig.dev § Future-proofing](https://clig.dev/#future-proofing).*
- **CHANGE-3 · Warn before a breaking change.** When someone uses what is about to
  change, say so in the program and show how to switch now; once they have
  switched, stop warning. *Source: [clig.dev §
  Future-proofing](https://clig.dev/#future-proofing).*
  - **ox:** renamed commands keep hidden aliases (`invite` → `team invite`,
    `daemon log` → `daemon logs`); `session show` carries cobra's `Deprecated`
    notice; deprecated config keys print a notice on `get` and `set`;
    `agent prime --ephemeral` warns on stderr.
- **CHANGE-4 · Human-readable output can change freely.** Scripts use `--plain` or
  `--json`, which stay stable. *Source: [clig.dev §
  Future-proofing](https://clig.dev/#future-proofing).*
- **CHANGE-5 · No time bombs.** A command should keep working after the external
  services it calls are gone — the vendor's own servers included — and nothing
  should block on analytics. *Source: [clig.dev §
  Future-proofing](https://clig.dev/#future-proofing).*
  - **ox:** PostHog events leave through a detached process, so no command waits
    on them (`cmd/ox/main.go`). Gap: the trace flush at exit (Known gaps).

### Naming, distribution, and telemetry (SHIP)

- **SHIP-1 · A command name is short, lowercase, and easy to type.** Simple and
  memorable, but not so generic that it collides with other tools; lowercase,
  with dashes only when needed; short, though the very shortest names belong to
  everyday utilities like `cd`, `ls`, and `ps`; and comfortable to type all day.
  *Source: [clig.dev § Naming](https://clig.dev/#naming).*
  - **ox:** `ox` is lowercase and short; its length is a deviation (§5). Adapter
    binaries are named `ox-adapter-<name>`
    ([ADR-009](ADR-009-adapter-naming-convention.md)).
- **SHIP-2 · Ship one binary.** Where that is impossible, use the platform's
  package manager, so nothing is left behind that cannot be removed. A tool for
  one language may assume that language's runtime. *Source: [clig.dev §
  Distribution](https://clig.dev/#distribution).*
  - **ox:** CGO-free static binaries for macOS, Linux, and FreeBSD
    (`.config/goreleaser.yml`), installed by Homebrew
    (`brew install sageox/tap/ox`) or `scripts/install.sh`; `ox upgrade` updates
    Homebrew, `go install`, and direct installs. The ten adapter binaries are a
    deviation (§5).
- **SHIP-3 · Document uninstalling next to installing.** Right after installing is
  when people most often want to uninstall. *Source: [clig.dev §
  Distribution](https://clig.dev/#distribution).*
  - **ox:** `ox uninstall` removes ox from a repository (`--dry-run`,
    type-to-confirm) and prints the command that deletes user configuration. Gap:
    removing the binary (Known gaps).
- **SHIP-4 · No telemetry without consent.** Say plainly what is collected, why,
  how it is anonymized, and how long it is kept. Prefer opt-in; when collection is
  on by default, announce it at first run (and on the website), and offer a simple
  way to switch it off. *Source: [clig.dev § Analytics](https://clig.dev/#analytics).*
  - **ox:** opt-out. A notice appears once per install, only to a person at a
    terminal — never in an AI coworker's transcript or `--json` output.
    `ox config get telemetry` lists what is sent and what never is;
    `ox config set telemetry off`, `DO_NOT_TRACK=1`, or `SAGEOX_TELEMETRY=false`
    turns it off. Events carry a random per-install ID, with PostHog's GeoIP
    lookup disabled. Gaps in Known gaps.
- **SHIP-5 · Look for alternatives to telemetry.** Instrument the web docs, count
  downloads, and talk to users. *Source: [clig.dev §
  Analytics](https://clig.dev/#analytics).*

---

## Known gaps (audited 2026-09-30)

Each row is a place ox falls short of these guidelines today. None is fixed by
this ADR (§6).

| ID | Today | Where |
|---|---|---|
| INV-4 | `-d` is `--days` (`index`) and `--description` (`carts`), not `--debug`; `-n` is `--lines` (`daemon logs`) and `--limit` (`carts list`), not `--dry-run`; `--dry-run` has no short form. | `cmd/ox/index.go`, `carts.go`, `daemon.go` |
| INV-6 | Six commands read stdin on `-`; none writes stdout on `-`. | — |
| INV-9 | Locally, `-v` also means "show passed and skipped checks" (`doctor`), "show IDs and full paths" (`status`), and "show sync history" (`daemon status`). `-y` exists on `doctor`, `team invite`, and `bulletin` only; the global `--yes` has no short form on purpose (comment in `registerPersistentFlags`). | `cmd/ox/doctor.go`, `status.go`, `daemon.go`, `root.go` |
| INV-9, INV-10 | One operation, two paths: `ox index code` and `ox code index`; `ox gc` and `ox doctor --gc`. `query` exists four ways (`query`, `kb query`, hidden `code query`, `agent <id> query`). `export` means "show where your data lives" at the top level and "write markdown" under `session`. One item is displayed by `view` (`session`, `plan`), `show` (`team`, `conversation`, `carts`), or `describe` (`kb`). Nouns mix plural (`addons`, `carts`, `hooks`, `skills`) and singular (`session`, `plan`, `team`). | `cmd/ox/` |
| INV-11 | `ox murmur <content>` and `ox viz <id>` treat a word that is not a subcommand as data, so each new subcommand changes what that word does. | `cmd/ox/murmur.go`; `cmd/ox/viz.go` |
| HELP-3 | Suggestions misfire: `ox session lsit` suggests `init`, because candidates are full command paths compared with the single mistyped word; `ox -V` suggests `-c`; the "unknown subcommand" errors from `addons`, `skills`, and `team` get no suggestion. | frictionax's cobra adapter; `cmd/ox/addons.go`, `skills_status.go`, `team.go` |
| HELP-4 | `brandedHelp` never prints cobra's `Example` field, which four commands set. About 50 commands put examples inside `Long`; `status`, `doctor`, `init`, `login`, `agent prime`, and `plan` have none. | `cmd/ox/root.go` |
| HELP-7 | Help names no support channel and links no web docs. `https://sageox.ai/docs` appears only in the `.sageox/README.md` that `ox init` writes. | `brandedHelp` in `cmd/ox/root.go`; `cmd/ox/init.go` |
| INPUT-1 | `ox plan enrich` and `ox plan render` without `--topic`, `--file`, or a slug read stdin to EOF, so at a terminal they wait silently. | `plan.Resolve` in `internal/plan/input.go` |
| OUT-4 | No `--plain`. `--text` and `--raw` exist on some commands for other purposes. | — |
| OUT-6 | `--quiet` is global, but only `status`, `code search`, `memory distill`, and the tips printer read it; `init` has its own `-q`. | readers of `cfg.Quiet` |
| OUT-7 | A command that takes 3 s or longer prints its per-phase timing tree to stderr by default. The default log handler writes WARN and ERROR records to stderr as `level=WARN msg=…` without `--verbose`. | `perf.CLITreeSink` in `internal/perf/sink.go`; `internal/logger/logger.go` |
| OUT-10 | `NO_COLOR` is parsed as a boolean, so `NO_COLOR=yes` leaves color on, and a comment in `cmd/ox/main.go` documents `NO_COLOR=0` as forcing color; no-color.org treats any non-empty value as "no color". The color profile is detected on stdout only. No `--no-color`, and `FORCE_COLOR` is not read. | `internal/theme/profile.go`; `cmd/ox/main.go` |
| OUT-11 | Spinners and `doctor`'s progress line are gated on stdin being a TTY (`cli.IsInteractive()`), not on the stream they draw to. | `internal/cli/spinner.go`; `cmd/ox/doctor.go` |
| OUT-13 | No pager, and `PAGER` is not read. | — |
| ERR-1 | An unknown subcommand under a command group with no handler prints the group's help and exits 0 (`ox plan foo`). A login canceled with Ctrl-C exits 0. | cobra's handling of non-runnable parents; `cmd/ox/login.go` |
| ERR-4 | An unexpected error prints `Error: <err>` with no debug-log path or bug-report link. There is no `recover()` at the entry point, so a panic prints a Go stack trace. | `cmd/ox/main.go` |
| RUN-2 | `ox init`'s repository registration and `ox logout`'s token revocation print nothing before their network calls. | `cmd/ox/init.go`; `cmd/ox/logout.go` |
| RUN-4 | HTTP timeouts are fixed per client and cannot be configured. | `internal/api`, `internal/auth`, `internal/lfs` |
| RUN-5 | `ox plan review` shuts its HTTP server down with no timeout. | `cmd/ox/plan_review.go` |
| CONF-3 | `XDG_STATE_HOME` is never used, and some paths are hard-coded under `~/.local/share` and ignore `XDG_DATA_HOME` (trusted endpoints, redact-history backups, the adapters directory `~/.local/share/ox/adapters`). Any path change is a Required Review. | `internal/paths/`; `cmd/ox/login_trusted_endpoints.go`; `cmd/ox/adapter.go` |
| CONF-4 | `ox init` lists none of the files it changes and has no `--dry-run`; without a TTY it configures every detected AI coworker. The hooks it adds to `.claude/settings.json` carry no marker (ox recognizes them by command text), and its `.gitattributes` block has a start comment but no end marker. | `cmd/ox/init.go`; `internal/hooks/claude/detect.go`; `cmd/ox/doctor_sageox.go` |
| CONF-5 | Documented customer-facing variables use `OX_*`, which `CLAUDE.md` calls an anti-pattern: `OX_USER_CONFIG`, `OX_SESSION_RECORDING`, `OX_PROJECT_ROOT`, `OX_JSON`. Renaming one is a Required Review and follows CHANGE-3. | `internal/config/` |
| CONF-6 | `DEBUG` is not read; ox's equivalent is `--verbose` / `OX_VERBOSE`. | — |
| CHANGE-5 | With telemetry on and a login present, every command waits up to 3 s at exit to flush its timing traces. | `observability.Shutdown` in `internal/observability/otel.go` |
| SHIP-3 | `ox uninstall` removes ox from one repository; nothing documents removing the binary itself (Homebrew, or where `scripts/install.sh` put it). | `cmd/ox/uninstall.go`; `README.md` |
| SHIP-4 | The first-run telemetry notice prints after the first event has been sent. Nothing user-facing says how long data is kept; ADR-008 (privacy) states retention but is marked internal and partly aspirational. | `cmd/ox/telemetry_posthog.go`; [`008-privacy.md`](008-privacy.md) |

## Consequences

### Positive

- One set of guidelines, with stable IDs, of what an ox command owes a person and
  an AI coworker. A review cites `INPUT-4` instead of re-arguing confirmation
  levels.
- Contributors and AI coworkers read the baseline in the repository instead of
  fetching a web page mid-review.
- Deviations are listed with reasons, so the next reviewer can tell a choice from
  an accident.
- The gap table is a ready follow-up list, each row pointing at the code.

### Negative / honest limits

- When clig.dev changes upstream, these guidelines do not follow on their own; the
  consulted revision (`2bd6023e`) keeps the difference reviewable.
- The audit reflects 2026-09-30. Notes and gaps go stale as code changes; fixing a
  gap includes deleting its row.
- The known gaps are real work, and this ADR assigns no owner to them.
- Nothing enforces the guidelines but review.

## Alternatives considered

1. **Link clig.dev from `cli-design-system.md` and stop there.** A link binds no
   one and gives reviewers nothing to cite, and it leaves the AI-coworker reading
   of "human-first" undecided.
2. **Vendor clig.dev's text, or mark this file CC BY-SA 4.0.** CC BY-SA is on
   `CLAUDE.md`'s banned list.
3. **Restate clig.dev item by item, in its order.** The first draft of this ADR
   did. Mirroring clig.dev's selection and arrangement makes the result arguably
   adapted material under CC BY-SA 4.0, however original the wording; review on
   PR #1126 raised it. The guidelines above share clig.dev's ideas, not its
   expression.
4. **Ask the clig.dev authors for permission.** An outside request with no
   deadline, and unnecessary once the expression is ox's own.
5. **Write ox guidelines with no reference baseline.** Reinvents ideas that
   contributors — and the models behind AI coworkers — already know
   (`agent-ux-principles.md` §5).
6. **Adopt an older or narrower guide** — the POSIX utility conventions, the GNU
   coding standards, Heroku's CLI style guide, or 12 Factor CLI Apps. clig.dev
   builds on and links all four; they remain sources.

## References

- [Command Line Interface Guidelines](https://clig.dev/) ·
  [source](https://github.com/cli-guidelines/cli-guidelines) ·
  [CC BY-SA 4.0](https://github.com/cli-guidelines/cli-guidelines/blob/main/LICENSE)
- Further sources: *The Unix Programming Environment* (Kernighan and Pike);
  [POSIX Utility Conventions](https://pubs.opengroup.org/onlinepubs/9699919799/basedefs/V1_chap12.html);
  [GNU Coding Standards — Program Behavior for All Programs](https://www.gnu.org/prep/standards/html_node/Program-Behavior.html);
  [12 Factor CLI Apps](https://medium.com/@jdxcode/12-factor-cli-apps-dd3c227a0e46) (Jeff Dickey);
  [Heroku CLI Style Guide](https://devcenter.heroku.com/articles/cli-style-guide);
  [no-color.org](https://no-color.org/);
  [XDG Base Directory Specification](https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html);
  [Crash-only software](https://lwn.net/Articles/191059/);
  [POSIX environment variables](https://pubs.opengroup.org/onlinepubs/009695399/basedefs/xbd_chap08.html);
  [Google — Writing helpful error messages](https://developers.google.com/tech-writing/error-messages);
  [NN/g — Error-message guidelines](https://www.nngroup.com/articles/error-message-guidelines).
- ox specs and rules: `docs/specs/cli-design-system.md`,
  `docs/specs/agent-ux-principles.md`, `docs/specs/agent-ux-ox-implementation.md`,
  `.claude/rules/json-output.md`, `.claude/rules/design.md`, `CLAUDE.md`.
- Related decisions — this ADR aligns with each and amends none:
  - [ADR-001](ADR-001-pure-go-no-cgo.md) — pure Go, one static binary per platform (SHIP-2).
  - [ADR-008 — External Adapter Binaries](ADR-008-external-adapter-binaries.md) — the adapter binaries (SHIP-2 deviation).
  - [ADR-008 — Privacy](008-privacy.md) — telemetry (SHIP-4).
  - [ADR-009](ADR-009-adapter-naming-convention.md) — `ox-adapter-<name>` (SHIP-1).
  - [ADR-023](ADR-023-skill-injection-two-layer-model.md) — guidance travels in CLI output (OUT-5). <!-- SOURCE: sageox adr:docs/adr/ADR-023-skill-injection-two-layer-model.md -->
  - [ADR-030](ADR-030-per-clone-git-serialization.md) — concurrent ox processes (RUN-7).
  - [ADR-031](ADR-031-cli-skill-rule-inventory.md) — ox-owned files and marked blocks (CONF-4). <!-- SOURCE: sageox adr:docs/adr/ADR-031-cli-skill-rule-inventory.md -->
  - Considered and not applicable: ADR-015 (WASM adapter runtime), ADR-017 and
    ADR-028 (Knowledge Bubbles), ADR-021 (`ox plan` context vs inference), and
    ADR-022 (adapter security posture) decide no CLI interaction convention.
    ADR-007 (hooks integration) describes hook installation, which CONF-4 covers,
    but is partly superseded, so the CONF-4 notes cite code instead.
