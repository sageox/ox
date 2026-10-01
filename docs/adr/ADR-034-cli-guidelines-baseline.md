# ADR-034 — The Command Line Interface Guidelines (clig.dev) are the ox CLI baseline

**Status:** Proposed — 2026-09-30.

**Date:** 2026-09-30 · **Deciders:** SageOx Engineering · **Supersedes:** nothing

**Source:** [Command Line Interface Guidelines](https://clig.dev/) by Aanand Prasad, Ben Firshman, Carl Tashian and Eva Parish. Restated from upstream revision [`2bd6023e`](https://github.com/cli-guidelines/cli-guidelines/blob/2bd6023eae2aa60a374c4e7275f935d0917c6c86/content/_index.md) (2026-01-26).

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

clig.dev is licensed [CC BY-SA 4.0](https://github.com/cli-guidelines/cli-guidelines/blob/main/LICENSE).
`CLAUDE.md`'s licensing policy bans CC-BY-SA material from this repository, so
this ADR restates each guideline in its own words, adds how it applies to ox, and
links to the source. It copies none of the upstream text.

## Decision

### 1. clig.dev is the baseline for every ox command

Every ox command, argument, flag, environment variable, configuration key, and
output stream follows the guidelines catalogued below. The Basics (`BASE-*`) are
required. Every other guideline is the default: code that does not follow one is
either fixed or carries a recorded deviation (§5).

### 2. A more specific ox rule wins

Where an ox spec, rule, or ADR is more specific — `cli-design-system.md`,
`json-output.md`, `design.md`, `agent-ux-principles.md`, `CLAUDE.md` — it
governs, and the guideline below points to it instead of restating it. If one of
them contradicts this catalog, one of the two is wrong: fix it, and do not let a
reviewer pick silently.

### 3. Two audiences: a person at a terminal, an AI coworker on a pipe

clig.dev's human-first principle holds for every command a person types. AI
coworkers run the same commands, and ox serves them through the mechanisms
clig.dev already defines for programs: TTY detection, `--json`, `--no-input`,
exit codes, and diagnostics on stderr. Nobody declares which audience they are —
a pipe selects the machine path (`.claude/rules/json-output.md`, "Why it colors,
and why that is safe"). Some commands also switch to JSON on their own when they
detect an AI coworker and `--json` was not passed (`ox session list`).

The scoped exception is `ox agent …`, whose caller is an AI coworker by design.
It is machine-first: structured output by default, `--text` for a person,
`--review` for both (`cmd/ox/agent.go`, `docs/specs/agent-ux-principles.md` §3).
`ox agent prime` defaults to tagged XML (`--format json` for JSON)
(`cmd/ox/agent_prime.go`). The human-output guidelines reach `ox agent` through
`--text` and `--help`.

### 4. Reviews cite guideline IDs

Each guideline has a stable ID (`OUT-12`, `ARG-10`). A change that adds or alters
a command, argument, flag, environment variable, configuration key, or output
format is reviewed against this catalog, and review comments cite the ID. This ADR
adds no tooling.

### 5. Deviations are deliberate and written down

clig.dev expects rules to be broken on purpose (P9). An ox deviation is recorded
here or in the spec that owns the behavior, with its reason. A guideline that
turns out wrong for CLIs in general, not only for ox, is also worth proposing
upstream. The deviations ox holds today:

| Guideline | ox behavior | Why |
|---|---|---|
| P1, OUT-1 | `ox agent …` is machine-first (§3). | Its caller is an AI coworker by design. |
| BASE-1, ARG-5 | `ox agent <id> <verb>` is dispatched by hand: the agent ID precedes the verb, `agent` accepts arbitrary arguments, and the verbs' flags are hidden persistent flags on `agent` that the dispatcher re-injects (`runAgentDispatcher` in `cmd/ox/agent.go`). The verbs have no help of their own; `--help` shows `ox agent`'s. | Every verb acts on one AI coworker instance, named by its ID (`docs/specs/agent-ux-ox-implementation.md`). |
| HELP-10, FUT-5 | A curated catalog (`cmd/ox/default_catalog.json`: 40 `auto_execute` entries, confidence ≥ 0.85) re-runs a mistyped command once as its correction, after printing `-> Correcting to: …`. `ox setup` runs `ox init`; `ox auth` runs `ox login`. | Each entry records a guess AI coworkers or people commonly make, so it resolves without a round trip. Accepted cost: some targets change state, and every entry is syntax ox keeps supporting. |
| INT-1 | Yes/no, numbered, and text prompts accept answers piped on a non-TTY stdin; `--no-input` is the strict mode. | Existing scripts pipe answers (`cli-design-system.md` § Prompts and Automation; `internal/cli/confirm.go`). |
| ARG-6 | `-v` is the global `--verbose`; `--version` has no short form. | The familiar-flags list in `agent-ux-principles.md` §5 (`-v, --verbose`). |
| OUT-9 | After `ox init` and `ox login`, the daemon syncs the Ledger and Team Context in the background, and hooks run ox inside AI coworker sessions. | That is the product. `ox init` and `ox login` are the explicit consent, and `ox status` shows the state. |
| CFG-3 | `ox agent prime` and `ox doctor`'s automatic fixes restore missing prime markers and Claude Code hooks without asking (`EnsureOxPrimeMarker`, `ensureClaudeHooks`, `FixLevelAuto`). | `ox init` is the consent; repairs keep that installed state intact (`CLAUDE.md` § Doctor as Last Line of Defense). `ox uninstall` removes it. |
| CFG-4 | User configuration outranks repository and team configuration: flags > environment > user > repo > team > default. | `README.md`: a repo or team can suggest a default, but cannot override a person's own preference, such as recording mode or telemetry. |
| ENV-5 (`TMPDIR`) | Daemon logs go to `/tmp/<user>/sageox` on macOS and Linux, ignoring `TMPDIR` (`paths.TempDir`). | macOS's per-user temporary directory is purged unpredictably (comment in `internal/paths/paths.go`). |
| ENV-8 | `SAGEOX_TOKEN` supplies a SageOx access token from the environment and outranks a stored login (`internal/auth/env_token.go`). ox also reads other tools' standard token variables (`GITHUB_TOKEN`, `GH_TOKEN`, `GITLAB_TOKEN`, …) in `internal/identity/`. | CI/CD, headless AI coworkers, and ephemeral containers have no browser for the device flow, and no flag or file option exists yet. A malformed `SAGEOX_TOKEN` fails closed, and adapter processes get an allowlisted environment (`internal/envutil`). |
| NAME-3 | The binary is `ox`, two letters. | It is the product name, and its two keys fall on alternate hands (NAME-4). |
| DIST-1 | Releases ship `ox` plus ten `ox-adapter-*` binaries. | Adapters are external binaries by design ([ADR-008 — External Adapter Binaries](ADR-008-external-adapter-binaries.md)); each is a CGO-free static build ([ADR-001](ADR-001-pure-go-no-cgo.md)). |

### 6. Known gaps are follow-up work

[Known gaps](#known-gaps-audited-2026-09-30) lists where ox falls short of the
baseline today. This ADR fixes none of them. A gap is closed when its command is
next changed or through its own issue; new code does not add to the list.

---

## The guidelines

IDs are stable: never renumber, and retire an ID rather than reuse it. Each
**ox:** note records how ox meets the guideline today (audited 2026-09-30), or
points to §5 or [Known gaps](#known-gaps-audited-2026-09-30).

clig.dev does not cover full-screen terminal programs such as editors. ox's
full-screen Bubble Tea interfaces follow `.claude/rules/design.md`; entering one
still follows INT-1 and OUT-13.

### Philosophy (P)

- **P1 · Design for the person first.** Classic UNIX tools behaved like library
  functions for other programs. Most CLIs are now driven by people, so their
  interaction design should serve a person first and drop the baggage.
  - **ox:** two audiences — §3.
- **P2 · Compose with other programs.** Whatever ox does, someone will embed it in
  a script, a CI job, or an AI coworker's tool loop. Standard streams, exit
  codes, signals, line-oriented text, and JSON are the contract that makes a
  program a well-behaved part of a larger system. Serving people and serving
  composition do not conflict; most guidelines below serve both.
- **P3 · Be consistent with the terminal.** Users have already paid to learn the
  terminal's conventions — flags, environment variables, streams. Following the
  existing pattern makes a command guessable. Break a convention only when
  following it demonstrably hurts usability, and then deliberately.
  - **ox:** `agent-ux-principles.md` §5 ("Familiar Patterns Win") makes the same
    argument for AI coworkers, whose models were trained on git, docker, and
    kubectl.
- **P4 · Say enough, and no more.** A command that sits silent for minutes looks
  broken; one that floods the screen buries what matters. Either way the user is
  left confused.
  - **ox:** for an AI coworker, surplus output also spends context-window tokens
    (`agent-ux-principles.md` §2).
- **P5 · Make features discoverable.** A CLI can be fast to use and still teach:
  thorough help, plenty of examples, the next command to run, and a concrete fix
  when something fails.
- **P6 · Treat use as a conversation.** People reach a working command by
  iterating — run, read the error, adjust. They also set up in steps before a
  final action, explore state, and dry-run before the real run. Design for that
  loop: suggest corrections, show where a multi-step flow stands, and confirm
  before a risky step.
- **P7 · Be robust, and feel robust.** Handle unexpected input gracefully and make
  operations idempotent where possible. Felt robustness comes from
  responsiveness, keeping the user informed, explaining common errors, and never
  showing a raw stack trace. Simple code is more robust than code full of special
  cases.
- **P8 · Be on the user's side.** People should enjoy using the tool — not through
  gimmicks, but because it is evident the authors thought about their problem and
  want them to succeed.
- **P9 · Break rules on purpose.** The terminal is inconsistent, and its lack of
  constraints is part of its power. When a guideline here is wrong for ox, break
  it deliberately and record why (§5).

### The basics (BASE) — required

- **BASE-1 · Parse with a library.** Use an established argument parser, not
  hand-rolled parsing; it handles flags, help, and misspelling suggestions
  consistently.
  - **ox:** Cobra. `ox agent <id> <verb>` is dispatched by hand — a deviation
    (§5).
- **BASE-2 · Exit status tells success from failure.** Exit 0 on success and
  non-zero on failure; scripts and AI coworkers branch on it. Give distinct
  non-zero codes to the failure modes a caller needs to tell apart.
  - **ox:** 0 on success; 1 on failure; 2 for some usage errors (`conversation`,
    hosted Ledger reads); 130 for an interrupted spinner. A command that has
    already printed its own error returns a typed `commandExitError` with its
    code (`cmd/ox/main.go`). `ox doctor` exits 1 when a check fails or setup is
    missing, even with `--json`; warnings alone exit 0. Gaps in Known gaps.
- **BASE-3 · Results go to stdout.** The command's primary output goes to stdout,
  and so does anything machine-readable, because that is what a pipe carries.
- **BASE-4 · Messages go to stderr.** Progress, warnings, errors, and log lines go
  to stderr, so they reach the person and never the next program in the pipe.
  - **ox:** `cli.PrintError` and `cli.PrintWarning` write to stderr in text and
    JSON modes (`cli-design-system.md` § Output Streams), as do tips and
    suggestion boxes (`internal/cli/output.go`).

### Help (HELP)

- **HELP-1 · Full help on request.** `-h` and `--help` show complete help, on the
  root command and on every subcommand.
- **HELP-2 · Short help when required input is missing.** A command that cannot do
  anything without arguments, run with none, prints brief usage: what it does,
  one or two examples, its flags (unless there are many), and a pointer to
  `--help`. Commands that are interactive by default are exempt.
  - **ox:** `ox` alone, and a command group without a handler, prints help and
    exits 0.
- **HELP-3 · `-h` always means help.** Appending `-h` or `--help` to any invocation
  shows help and ignores everything else on the line; `-h` is never reused. In a
  git-style tool, `ox help`, `ox help <cmd>`, `ox <cmd> --help`, and
  `ox <cmd> -h` all work.
  - **ox:** Cobra supplies all four forms, and `brandedHelp` renders them
    (`cmd/ox/root.go`). Dispatched `ox agent <id> <verb>` commands share
    `ox agent`'s help (§5).
- **HELP-4 · Say where to get support.** Top-level help names where to report a
  bug or give feedback — a website or issue-tracker link.
  - **ox:** not met — Known gaps.
- **HELP-5 · Link to the web docs.** Help text links to the web documentation,
  pointing at the subcommand's own page or anchor when one exists.
  - **ox:** not met — Known gaps.
- **HELP-6 · Lead with examples.** People copy examples before they read flag
  tables, so show examples first — especially the common complex ones — with
  their output when it is short. A sequence of examples can build from simple to
  advanced.
  - **ox:** not met for most commands — Known gaps.
- **HELP-7 · Keep exhaustive examples out of help.** A long catalog of examples
  belongs in a dedicated examples command or on a web page, and a complex
  integration may deserve a full tutorial. Help stays short.
- **HELP-8 · Most-used first.** Order help so the most common commands and flags
  come first, and group commands by task.
  - **ox:** six task groups (Software Development, Knowledge, Teams,
    Authentication, Agent Integration, Diagnostics), alphabetical within each; a
    ★ marks the suggested next step (`cli-design-system.md` § Contextual
    Highlighting).
- **HELP-9 · Format help for scanning, portably.** Use formatting such as bold
  section headings, but emit no raw escape sequences when help is piped or
  paged.
  - **ox:** `brandedHelp` styles its headings; when stdout is not a TTY,
    `cmd/ox/main.go` strips ANSI from everything written to it.
- **HELP-10 · Suggest what the user probably meant.** When input is wrong and the
  intent is guessable, say what to run instead. Offering to run it is fine;
  running it silently is not the default. The mistake may be a logic error rather
  than a typo, guessing is dangerous for a command that changes state, and every
  accepted wrong form becomes syntax you must support forever. Accept an
  alternate form only deliberately, and document both forms.
  - **ox:** frictionax suggests a correction: `Did you mean?` on stderr for a
    person, or a `_suggestion` object on stdout under `--json` or in an AI
    coworker's context (`cmd/ox/main.go`). Auto-running a correction is a
    deviation (§5); misfires are Known gaps.
- **HELP-11 · Don't hang on input that isn't coming.** A command that expects data
  on stdin, started with an interactive terminal on stdin, prints help (or a note
  on stderr) and exits instead of waiting silently.
  - **ox:** most stdin readers check for a TTY and refuse or fall back
    (`ox agent <id> session import`, `session plan`, `session context-trace`).
    Gap: `ox plan enrich` and `ox plan render` (Known gaps).

### Documentation (DOCS)

- **DOCS-1 · Web documentation.** Publish the documentation on the web: it is
  searchable, a section can be linked, and it is the most widely accessible
  format.
  - **ox:** `ox docs` (hidden; run by `make docs`, checked by `docs-check`)
    generates `docs/reference/*.mdx` from the cobra tree, and
    `.github/workflows/docs.yml` publishes it. `internal/docs/postprocess.go`
    leaves out command trees it calls internal or advanced, among them `agent`,
    `config`, `daemon`, and `version`.
- **DOCS-2 · Terminal documentation.** Also ship documentation readable in the
  terminal: it is instant, works offline, and matches the installed version.
  Reach it through the tool itself (a `help` subcommand), because not everyone
  knows `man` and not every platform has it.
  - **ox:** `--help`, and `ox guide <topic>` for bundled topical guides.
- **DOCS-3 · Consider man pages.** Many users try `man <tool>` first. Generating
  man pages and web docs from one source keeps them in sync.
  - **ox:** no man pages. The cobra `doc` package that generates the reference
    docs can generate them too.

### Output (OUT)

- **OUT-1 · People first, decided per stream.** Human-readable output is the
  priority, and whether a person reads a given stream is decided by whether that
  stream is a TTY.
  - **ox:** `theme.Profile` decides color from stdout. When stdout or stderr is
    not a TTY, `cmd/ox/main.go` strips ANSI from that stream, and in an AI
    coworker's context it sets `NO_COLOR=1`.
- **OUT-2 · Machine-readable where it costs people nothing.** Line-oriented text
  lets people and programs compose ox with `grep` and friends. Assume today's
  output becomes the input of a program nobody has written yet.
- **OUT-3 · `--plain` when the human layout breaks one-record-per-line.** If the
  human format spreads a record over several lines (for example, wrapped table
  cells), provide `--plain`: one record per line, no decoration, for `grep` and
  `awk`.
  - **ox:** not met — Known gaps.
- **OUT-4 · `--json` for structure.** `--json` prints the result as formatted
  JSON: richer structure than text, and it works with `jq` and web tooling.
  - **ox:** `.claude/rules/json-output.md` routes every `--json` payload through
    `cli.PrintJSON` / `cli.PrintJSONTo`; `OX_JSON=1` makes JSON the default and an
    explicit `--json`/`--json=false` wins (`cli-design-system.md` § Output
    Streams).
- **OUT-5 · Confirm success, briefly.** Print something on success — silence looks
  like a hang to a person — and keep it short. Provide `-q`/`--quiet` so scripts
  can drop non-essential output without redirecting stderr to `/dev/null`.
  - **ox:** global `-q`/`--quiet` (or `OX_QUIET=1`). Gap: few commands read it
    (Known gaps).
- **OUT-6 · Report state changes.** When a command changes state, say what changed
  — especially when the result is not a direct echo of the request — so the
  user's model of the system stays accurate.
- **OUT-7 · Make current state easy to see.** State that is not visible in the
  filesystem gets a command that shows it (like `git status`), with hints on how
  to change it.
  - **ox:** `ox status`; `ox doctor` for health.
- **OUT-8 · Suggest the next command.** When commands form a workflow, suggest what
  to run next; it teaches the workflow and surfaces features.
  - **ox:** for people, contextual highlighting in help (`cli-design-system.md` §
    Contextual Highlighting); for AI coworkers, the `guidance` field in command
    output, where behavioral guidance lives (ADR-023 Layer 1, `CLAUDE.md`
    thin-relay rule). <!-- SOURCE: sageox adr:docs/adr/ADR-023-skill-injection-two-layer-model.md -->
- **OUT-9 · Crossing the program's boundary is explicit.** Reading or writing files
  the user did not name (other than the program's own state, such as a cache),
  and talking to a remote server, should usually happen only because the user
  asked for it.
  - **ox:** a deviation — §5.
- **OUT-10 · Dense, learnable notation.** Compact notation (like `ls -l` permission
  strings) packs in information that newcomers can skip and experienced users
  read at a glance.
  - **ox:** `.claude/rules/design.md` rule 11 (Tufte minimum).
- **OUT-11 · Color with intent.** Use color to direct attention — red for an
  error, a highlight for the one thing that matters — and sparingly: when
  everything is colored, nothing stands out.
  - **ox:** semantic styles only, never raw colors (`.claude/rules/design.md`
    rule 3).
- **OUT-12 · Turn color off when it can't be seen or isn't wanted.** Disable color
  when the stream is not a TTY (check stdout and stderr separately, so piping
  stdout keeps color on stderr), when `NO_COLOR` is set to a non-empty value, when
  `TERM=dumb`, or when `--no-color` is passed. A program-specific switch is
  optional.
  - **ox:** a true-valued `NO_COLOR` forces no color on every stream
    (`internal/theme/profile.go`; `.claude/rules/design.md` rule 7). `TERM=dumb`,
    `CLICOLOR`, and `CLICOLOR_FORCE` follow the colorprofile library;
    `OX_COLOR_PROFILE` forces a color depth for debugging. Gaps in Known gaps.
- **OUT-13 · No animation off a TTY.** When stdout is not a TTY, show no spinners
  or progress bars; in a CI log they turn into pages of noise.
  - **ox:** `cli.WithSpinner` waits 300 ms before drawing, and draws nothing under
    `--no-interactive` (on by default when `CI=true`) or `--no-input`. Gap: it
    checks stdin, not stdout (Known gaps).
- **OUT-14 · Symbols and emoji where they clarify.** A glyph can separate items,
  draw the eye, or add character — until it clutters the output or makes the tool
  feel like a toy.
- **OUT-15 · Default output is for users, not the authors.** Output only ox's
  developers can interpret belongs behind verbose mode. Ask people new to the
  project to use it; they see what the authors cannot.
  - **ox:** `-v`/`--verbose` turns on debug logs and the per-phase timing tree.
    Gap: slow commands print the tree by default (Known gaps).
- **OUT-16 · stderr is not a log file.** By default, no log-level labels (`ERR`,
  `WARN`) and no extra context on stderr; those belong in verbose mode.
  - **ox:** not met — Known gaps. `.claude/rules/design.md` rule 8 governs log
    format (single-line `key=value`), not whether logs appear by default.
- **OUT-17 · Page long output.** Send long output through a pager (as `git diff`
  does), and only when stdin or stdout is a TTY. `less -FIRX` is a good default: it
  does not page output that fits on one screen, searches case-insensitively,
  passes color through, and leaves the text on screen after quitting. A pager
  library can be more robust than piping to `less`; a broken pager is worse than
  none.
  - **ox:** not met — Known gaps.

### Errors (ERR)

- **ERR-1 · Rewrite expected errors for people.** Catch the errors you can
  anticipate and turn each into guidance: what went wrong and the command that
  fixes it. It is a conversation, not a status code.
  - **ox:** case by case: `cli.PrintSuggestionBox` (title, message, fix),
    `session.SessionError` (`Code`, `Message`, `Retryable`, `Fix`), and
    remediation written into error text — `ox agent <id> session abort` without a
    terminal says to pass `--force`. `agent-ux-principles.md` § Error Responses
    asks JSON errors for an actionable `action`.
- **ERR-2 · Keep the signal-to-noise ratio high.** Irrelevant output slows
  diagnosis. Group many errors of one kind under a single explanatory heading
  instead of printing near-identical lines.
- **ERR-3 · Put the key line where the eye lands.** The end of the output is read
  first, so the most important information goes there. Red draws the eye; use it
  deliberately and rarely.
- **ERR-4 · Unexpected errors come with debug detail and a way to report them.**
  For an error ox did not anticipate, provide debugging and traceback detail and
  how to file a bug — without drowning the user. Consider writing the detail to a
  log file and printing its path.
  - **ox:** not met — Known gaps.
- **ERR-5 · Make bug reports effortless.** For example, print a URL that pre-fills
  the report with as much context as possible.
  - **ox:** not met — Known gaps.

### Arguments and flags (ARG)

*Arguments* are positional, and their order matters (`cp a b` is not `cp b a`).
*Flags* are named — `-r` or `--recursive` — may take a value (`--file x`,
`--file=x`), and their order normally does not matter.

- **ARG-1 · Prefer flags to positional arguments.** More typing, but clearer, and
  new inputs can be added later without ambiguity or breaking existing usage.
- **ARG-2 · Every flag has a long form.** `--help` alongside `-h`. Scripts use the
  long form so readers don't have to decode letters.
- **ARG-3 · Short flags only for common options.** One-letter flags go to
  frequently used options — especially at the top level of a multi-command tool —
  so the short namespace is not used up.
- **ARG-4 · Many arguments for one simple action are fine.** `rm a b c` does the
  same thing to each argument, which also makes shell globs work.
- **ARG-5 · Two positional arguments with different roles is a smell.** The
  exception is a frequent, primary action whose short form is worth learning by
  heart (`cp <src> <dst>`).
  - **ox:** `ox agent <id> <verb>` — the BASE-1 deviation (§5).
- **ARG-6 · Use standard flag names.** When a widely used tool already has a flag
  for a concept, use the same name so people can guess it. The common set:
  `-a`/`--all`; `-d`/`--debug`; `-f`/`--force` (also skips the confirmation a
  destructive action otherwise requires, for scripts); `--json`; `-h`/`--help`
  (help only); `-n`/`--dry-run` (describe the changes without making them);
  `--no-input`; `-o`/`--output` (output file); `-p`/`--port`; `-q`/`--quiet`;
  `-u`/`--user`; `--version`. `-v` is ambiguous between verbose and version;
  clig.dev suggests `-d` for verbose and `-v` for version, or leaving `-v` unused.
  - **ox:** `--json`, `-h`/`--help`, `--no-input`, `-q`/`--quiet`, `--version`,
    `--force` (`-f` on `logout`), and `--dry-run` (`uninstall`, `session prune`,
    `session regenerate`, …) match the standard. `-v` is a deviation (§5); `-d`
    and `-n` are Known gaps.
- **ARG-7 · Defaults serve most users.** Most people never find a flag, or forget
  to keep using it; a better behavior that needs a flag reaches almost nobody.
- **ARG-8 · Prompt for missing input.** In an interactive session, ask for a
  missing argument or flag instead of failing (INT-1).
- **ARG-9 · Never require a prompt.** Anything a prompt asks for can also be passed
  as a flag or argument. When stdin is not a TTY, don't prompt — require the
  flags.
  - **ox:** `--no-input` makes required choices come from arguments or flags
    (`cli-design-system.md` § Prompts and Automation), and a confirmation that
    cannot be asked fails with `confirmation required: re-run in a terminal, or
    pass --yes` (`internal/cli/confirm.go`).
- **ARG-10 · Confirm before dangerous actions, scaled to the danger.**
  Interactively, ask the person to type `y` or `yes`; non-interactively, require
  `-f`/`--force`.
  - *Mild* — a minor local change, like removing a single file. Confirmation is
    optional, and unnecessary when the command's name is the action ("delete").
  - *Moderate* — deleting a directory or a remote resource, or a bulk change that
    is hard to undo. Usually confirm, and consider offering a dry run.
  - *Severe* — deleting something large and complex, such as an entire remote
    application or server. Make accidental confirmation hard — have the person
    type the resource's name — and keep it scriptable with a flag such as
    `--confirm=<name>`.
  - Watch for indirect destruction: an edit that lowers a count from 10 to 1 and
    so deletes nine things is severe.
  - **ox:** moderate — `ox agent <id> session abort` and `session delete` confirm
    at a terminal and otherwise require `--force`; `logout`, `session remove`,
    `session prune`, `session regenerate`, and `coworker remove` confirm. Severe
    — `ox uninstall` has the person type the repository name or `uninstall`,
    offers `--dry-run`, and does not accept `--yes` in place of `--force`.
    `--yes` authorizes yes/no confirmations only (`cli-design-system.md` §
    Prompts and Automation).
- **ARG-11 · `-` means stdin or stdout.** Where an input or output is a file, accept
  `-` to read stdin or write stdout, so commands chain without temporary files.
  - **ox:** `-` reads stdin in `agent redact test`, `bulletin post`, `scout`,
    `session push-summary --file`, `viz render --data`, and `viz lint`. Gap:
    nothing writes stdout on `-` (Known gaps).
- **ARG-12 · An optional value needs a sentinel word.** If a flag's value is
  optional, accept a word such as `none` to mean "no value". An empty value makes
  it ambiguous whether the next token is the flag's value or an argument.
- **ARG-13 · Order-independent where the parser allows.** Flags work before or
  after the subcommand, in any order — people add a flag by pressing up-arrow and
  appending it to the previous command.
  - **ox:** cobra parses a root flag before or after the subcommand
    (`ox --json version` is `ox version --json`).
- **ARG-14 · Never take a secret as a flag value.** A flag value appears in `ps`
  output and shell history, and it invites passing secrets through environment
  variables. Accept secrets from a file (`--password-file`) or stdin;
  `--password "$(< file)"` leaks the same way.
  - **ox:** no flag takes a secret; `ox login` uses the OAuth device flow in a
    browser. Secrets in the environment are ENV-8.

### Interactivity (INT)

- **INT-1 · Interactive only when stdin is a TTY.** A non-TTY stdin means a pipe or
  a script, where nobody can answer a prompt; fail with an error that names the
  flag to pass instead.
  - **ox:** branch on `cli.IsInteractive()` / `cli.IsHeadless()`; every
    interactive widget must have a non-TTY fallback (`.claude/rules/design.md`
    rule 6).
    Piped prompt answers are a deviation (§5).
- **INT-2 · `--no-input` turns off all interaction.** With `--no-input`, nothing
  prompts or waits on the terminal. If required input is missing, fail and say
  which flag supplies it.
  - **ox:** global `--no-input` (`cli-design-system.md` § Prompts and
    Automation). It leaves explicit stdin data and protocol input — session
    imports, Git credential requests — available.
- **INT-3 · Don't echo secrets.** When prompting for a password or token, turn off
  terminal echo.
  - **ox:** ox never prompts for a secret.
- **INT-4 · Let the user escape.** Make the way out obvious. Ctrl-C works even
  while blocked on network I/O. When wrapping a program that receives Ctrl-C
  itself (ssh, tmux), say how to get out, as ssh does with its `~` escape
  sequences.
  - **ox:** menus close on Ctrl-C, `q`, or Esc, and an interrupted spinner prints
    `Interrupted.` and exits 130 (`cli-design-system.md` § Spinner Cancellation).
    Outside a Bubble Tea screen, Go's default handling ends the process at once.

### Subcommands (SUB)

Subcommands cut a complex tool into parts and bring closely related tools under
one name, sharing global flags, help, configuration, and storage.

- **SUB-1 · Consistent across subcommands.** The same flag name for the same
  concept everywhere, and the same output conventions.
  - **ox:** global flags are defined once on the root; command-specific flags stay
    on their command group (`cli-design-system.md` § Flag Scoping). Gaps in Known
    gaps.
- **SUB-2 · Consistent names across levels.** For objects and the operations on
  them, use two levels, noun then verb (`ox session list`), and the same verb for
  the same operation on every noun.
  - **ox:** mostly noun-verb (`ox session list`, `ox plan status`,
    `ox team invite`); the exceptions are Known gaps.
- **SUB-3 · No ambiguous or near-synonym commands.** `update` beside `upgrade`
  confuses; pick distinct words or qualify them.
  - **ox:** `ox upgrade` updates ox itself, and `update` exists only under
    `addons` and `carts`. Other overlaps are Known gaps.

### Robustness (ROB)

- **ROB-1 · Validate input early.** Every input will eventually be bad. Check before
  acting, fail before any side effect, and make the error understandable (ERR-1).
  - **ox:** flag-only commands that change state use `cobra.NoArgs`
    (`cli-design-system.md` § Argument Validation).
- **ROB-2 · Responsive beats fast.** Print something within about 100 ms. Before a
  network request, say so, so the wait does not look like a hang.
  - **ox:** long operations go to the daemon so the command returns
    (`agent-ux-principles.md` §1, "Never Block the Event Loop").
    `cli.WithSpinner` draws only after 300 ms, and `ox login` prints `Initiating
    device authentication flow...` before its first request. Gap in Known gaps.
- **ROB-3 · Show progress on long operations.** Silence looks like a crash. A
  spinner or progress bar reassures; if it could sit still for a while, show an
  estimate or keep something moving. Use a library.
  - **ox:** spinners (`cli.WithSpinner`) cover `sync`, `recap`, `login`, and parts
    of `doctor`, which also rewrites a `Checking …` line as it goes; `ox init`
    hands cloning to the daemon.
- **ROB-4 · Parallelize carefully.** Concurrent work must not interleave output
  into nonsense; use a library that supports several progress bars. Hiding logs
  behind progress bars is fine on success; on failure, print the logs.
- **ROB-5 · Time out.** Network operations have a sensible default timeout, and
  the timeout is configurable, so nothing hangs forever.
  - **ox:** the SageOx clients set timeouts — 2 s for telemetry, 5 to 60 s for API
    and auth calls, up to 5 min for LFS transfers and upgrade downloads — and git
    runs with `http.lowSpeedLimit`/`http.lowSpeedTime`
    (`internal/gitutil/http_timeout.go`). Gap: none is configurable (Known gaps).
- **ROB-6 · Recover by re-running.** After a transient failure (the network
  dropped), running the same command again continues where it stopped.
- **ROB-7 · Crash-only.** Beyond idempotence: avoid needing cleanup, or defer it to
  the next run, so the program can stop at once when it fails or is
  interrupted.
  - **ox:** `CLAUDE.md` makes `ox doctor` the last line of defense: it detects and
    repairs every known failure mode.
- **ROB-8 · Expect misuse.** People will wrap ox in scripts, run it over bad
  connections, run many copies at once, and run it where nobody tested — for
  example on macOS filesystems that are case-insensitive but case-preserving.
  - **ox:** many AI coworkers and worktrees drive one machine's clones at once;
    git operations are serialized per clone ([ADR-030](ADR-030-per-clone-git-serialization.md)).

### Future-proofing (FUT)

Subcommands, arguments, flags, configuration files, and environment variables are
interfaces — and for ox, so are `--json` payloads and the `ox agent` output AI
coworkers parse. Change them only through a long, documented deprecation.
Semantic versioning does not license constant breaking changes.

- **FUT-1 · Prefer additive changes.** Instead of changing a flag incompatibly,
  add a new one, as long as the interface does not bloat.
- **FUT-2 · Warn before a breaking change.** When someone uses what is about to
  change, say so in the program and tell them how to change their usage now, so
  the eventual change does not affect them. Once they have migrated, stop
  warning.
  - **ox:** renamed commands keep hidden aliases (`invite` → `team invite`,
    `daemon log` → `daemon logs`); `session show` carries cobra's `Deprecated`
    notice; deprecated config keys print a notice on `get` and `set`;
    `agent prime --ephemeral` warns on stderr.
- **FUT-3 · Human output may change.** Human-readable output is iterated freely;
  scripts are pointed at `--plain` or `--json`, which stay stable.
- **FUT-4 · No catch-all subcommand.** Don't let an unrecognized first word fall
  through to a default subcommand (`tool foo` meaning `tool run foo`): no
  subcommand with that name could ever be added without breaking someone.
  - **ox:** the root has no catch-all. Gap: `murmur` and `viz` (Known gaps).
- **FUT-5 · No arbitrary abbreviations.** Don't accept any unambiguous prefix of a
  subcommand (`ins` for `install`); every later command starting with those
  letters becomes a breaking change. Explicit aliases are fine, and they stay
  stable.
  - **ox:** cobra's prefix matching is off; aliases are explicit (`teams`, `conv`,
    `bubble`). Each auto-run correction (§5) is an alias in effect.
- **FUT-6 · No time bombs.** Ask whether the command will still work in 20 years,
  or whether it depends on an external service that will be gone — most likely
  your own server. Never make a command block on analytics.
  - **ox:** PostHog events leave through a detached process, so no command waits
    on them (`cmd/ox/main.go`). Gap: the trace flush at exit (Known gaps).

### Signals and control characters (SIG)

- **SIG-1 · Ctrl-C exits fast.** On SIGINT, say something immediately, before any
  cleanup, and put a timeout on the cleanup so it cannot hang.
  - **ox:** no entry-point handler: outside a Bubble Tea screen, Ctrl-C ends the
    process at once. Commands that serve until interrupted (`plan review`,
    `sync --read-only`, hosted Ledger reads, `session trace serve`) use
    `signal.NotifyContext`. Gap: `plan review` (Known gaps).
- **SIG-2 · A second Ctrl-C skips slow cleanup.** If cleanup takes long, a second
  Ctrl-C skips it; when skipping is destructive, say what the second Ctrl-C will
  do. Expect to start after a run whose cleanup never finished (ROB-7).
  - **ox:** no interactive command has slow cleanup to skip. The daemon bounds its
    own shutdown: up to 30 s for the code index, then 5 s for goroutines
    (`internal/daemon/daemon.go`).

### Configuration (CFG)

- **CFG-1 · Choose the mechanism by how the setting varies.**
  1. Changes between invocations (debug output, a dry run) → flags; environment
     variables may help.
  2. Mostly stable for a user or machine, but differs between projects or people
     (a non-default path, color, an HTTP proxy) → flags and environment
     variables; a configuration file if it is complex.
  3. Stable within a project for everyone → a command-specific file under version
     control (like a `Makefile` or `package.json`).
  - **ox:** (1) flags; (2) `SAGEOX_*` and `OX_*` variables and the user config
    `~/.config/sageox/config.yaml`; (3) the committed `.sageox/config.json` and the
    Team Context's `config.toml`. Machine-local project state goes in the
    gitignored `.sageox/config.local.toml`.
- **CFG-2 · Follow the XDG Base Directory specification.** User configuration goes
  under `~/.config` (honoring the `XDG_*` variables), not into new dotfiles in the
  home directory.
  - **ox:** config, data, and cache default to `$XDG_CONFIG_HOME/sageox`,
    `$XDG_DATA_HOME/sageox`, and `$XDG_CACHE_HOME/sageox` on every OS; the legacy
    `~/.sageox` applies only under `OX_XDG_DISABLE` (`internal/paths/`). Changing
    a path is a Required Review (`CLAUDE.md`). Gaps in Known gaps.
- **CFG-3 · Changing another program's configuration needs consent and
  disclosure.** Ask before modifying configuration that belongs to another
  program, and say exactly what will change. Prefer adding a file you own over
  appending to a shared one; when a shared file must change, mark your additions
  (clig.dev suggests a dated comment).
  - **ox:** ox prefers files it owns — skills and rules under reserved names, with
    scoped `.gitignore` blocks marked `# >>> ox-managed … >>>`
    ([ADR-031](ADR-031-cli-skill-rule-inventory.md) §§1, 5) — and no longer
    writes the root `.gitignore`. <!-- SOURCE: sageox adr:docs/adr/ADR-031-cli-skill-rule-inventory.md -->
    Shared files get marked additions: `<!-- ox:prime-check -->` and
    `<!-- ox:prime -->` lines in `AGENTS.md`/`CLAUDE.md`, and
    `# ox <name> hook` … `# end ox hook` in git hooks. `ox doctor` prints the
    shell-profile line to add rather than editing the file. Restoring removed
    markers and hooks is a deviation (§5); the rest is in Known gaps.
- **CFG-4 · Apply precedence, highest first:** flags; the running shell's
  environment variables; project configuration; user configuration; system-wide
  configuration.
  - **ox:** a deviation — §5.

### Environment variables (ENV)

- **ENV-1 · Environment variables carry context.** Use them for behavior that
  varies with where the command runs — the terminal session, the machine, the
  checkout. They can mirror flags or configuration, or stand alone (CFG-1).
- **ENV-2 · Portable names.** Uppercase letters, digits, and underscores only, and
  not starting with a digit.
  - **ox:** every variable ox reads has a portable name. New customer-facing names
    use `SAGEOX_*`, and any new customer-facing name is a Required Review
    (`CLAUDE.md`). Gap: customer-facing `OX_*` names (Known gaps).
- **ENV-3 · Single-line values.** Multi-line values work, but make `env` output
  hard to read.
- **ENV-4 · Don't claim widely used names.** Avoid names already in common use,
  such as the POSIX-standard variables.
- **ENV-5 · Honor the general-purpose variables:** `NO_COLOR` (and `FORCE_COLOR`
  to force color on); `DEBUG` for more output; `EDITOR` when the person must edit
  a file or enter more than one line; `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`,
  and `NO_PROXY` for network operations; `SHELL` to open the person's interactive
  shell (run scripts with a specific interpreter such as `/bin/sh`); `TERM`,
  `TERMINFO`, and `TERMCAP` for terminal-specific escapes; `TMPDIR` for temporary
  files; `HOME` to find configuration; `PAGER` for paging; `LINES` and `COLUMNS`
  for size-dependent output.
  - **ox:** honored — `NO_COLOR` (with a gap), `CLICOLOR_FORCE`, `TERM` (through
    colorprofile), `HOME` (through `os.UserHomeDir`), the proxy variables (through
    Go's default HTTP transport), `COLUMNS` (`recap`), `SHELL` (a doctor check),
    and `XDG_*`. ox opens no editor, so `EDITOR` does not apply. `TMPDIR` is a
    deviation (§5); `FORCE_COLOR`, `DEBUG`, and `PAGER` are Known gaps.
- **ENV-6 · Read `.env` where it fits.** For variables that stay constant while
  working in a directory, also read a local `.env`, so each project can differ
  without retyping.
  - **ox:** ox loads `.env.local`, then `.env`, from the working directory at
    startup, without overriding variables already set; hosted Ledger reads skip
    them (`cmd/ox/main.go`).
- **ENV-7 · `.env` is not a configuration file.** A `.env` is usually not in
  version control (so it has no history), holds only strings, drifts into
  disorder, invites encoding problems, and often holds credentials. When those
  limits matter, use a real configuration file.
- **ENV-8 · Don't read secrets from environment variables.** Exported variables
  reach every child process and leak into logs; `curl -H "Authorization: Bearer
  $TOKEN"` exposes the value in the process list; `docker inspect` and
  `systemctl show` reveal a container's or unit's variables to anyone with access.
  Accept secrets only through credential files, pipes, `AF_UNIX` sockets, secret
  managers, or another IPC channel.
  - **ox:** a deviation — §5.

### Naming (NAME)

- **NAME-1 · Simple and memorable, not generic.** A generic word collides with
  other tools (ImageMagick and Windows both shipped a `convert`).
- **NAME-2 · Lowercase; dashes only when necessary.**
- **NAME-3 · Short, but not the shortest.** The very shortest names belong to the
  everyday utilities (`cd`, `ls`, `ps`).
- **NAME-4 · Easy to type.** Consider the hands: Docker Compose's precursor was
  renamed from `plum` to `fig` because `fig` flows more easily.
  - **ox:** `ox` is lowercase and short; NAME-3 is a deviation (§5). Adapter
    binaries are named `ox-adapter-<name>` ([ADR-009](ADR-009-adapter-naming-convention.md)).

### Distribution (DIST)

- **DIST-1 · One binary.** Ship a single executable where possible; otherwise use
  the platform's package manager, so nothing is scattered that cannot be removed.
  Tread lightly on the machine. (A tool for one language may assume that
  language's interpreter.)
  - **ox:** CGO-free static binaries for macOS, Linux, and FreeBSD
    (`.config/goreleaser.yml`), installed by Homebrew (`brew install
    sageox/tap/ox`) or `scripts/install.sh`; `ox upgrade` updates Homebrew,
    `go install`, and direct installs. The ten adapter binaries are a deviation
    (§5).
- **DIST-2 · Easy to uninstall.** Put uninstall instructions at the end of the
  install instructions; right after installing is when people most often want to
  uninstall.
  - **ox:** `ox uninstall` removes ox from a repository (`--dry-run`,
    type-to-confirm) and prints the command that deletes user configuration. Gap:
    removing the binary (Known gaps).

### Analytics (ANLY)

- **ANLY-1 · No phoning home without consent.** State plainly what usage or crash
  data is collected, why, how it is anonymized, and how long it is kept. Prefer
  opt-in; if collection is on by default, announce it on the website or at first
  run, and make turning it off easy. clig.dev's examples: Angular asks first;
  Homebrew and Next.js collect by default and publish what they collect.
  - **ox:** opt-out. A notice appears once per install, only to a person at a
    terminal — never in an AI coworker's transcript or `--json` output.
    `ox config get telemetry` lists what is sent and what never is;
    `ox config set telemetry off`, `DO_NOT_TRACK=1`, or `SAGEOX_TELEMETRY=false`
    turns it off. Events carry a random per-install ID, with PostHog's GeoIP
    lookup disabled. Gaps in Known gaps.
- **ANLY-2 · Consider alternatives to analytics.** Instrument the web docs (what
  people read and search for), count downloads, and talk to users.

---

## Known gaps (audited 2026-09-30)

Each row is a place ox falls short of the baseline today. None is fixed by this
ADR (§6).

| ID | Today | Where |
|---|---|---|
| BASE-2 | An unknown subcommand under a command group with no handler prints the group's help and exits 0 (`ox plan foo`). A login canceled with Ctrl-C exits 0. | cobra's handling of non-runnable parents; `cmd/ox/login.go` |
| HELP-4, HELP-5 | Help names no support channel and links no web docs. `https://sageox.ai/docs` appears only in the `.sageox/README.md` that `ox init` writes. | `brandedHelp` in `cmd/ox/root.go`; `cmd/ox/init.go` |
| HELP-6 | `brandedHelp` never prints cobra's `Example` field, which four commands set. About 50 commands put examples inside `Long`; `status`, `doctor`, `init`, `login`, `agent prime`, and `plan` have none. | `cmd/ox/root.go` |
| HELP-10 | Suggestions misfire: `ox session lsit` suggests `init`, because candidates are full command paths compared with the single mistyped word; `ox -V` suggests `-c`; the "unknown subcommand" errors from `addons`, `skills`, and `team` get no suggestion. | frictionax's cobra adapter; `cmd/ox/addons.go`, `skills_status.go`, `team.go` |
| HELP-11 | `ox plan enrich` and `ox plan render` without `--topic`, `--file`, or a slug read stdin to EOF, so at a terminal they wait silently. | `plan.Resolve` in `internal/plan/input.go` |
| OUT-3 | No `--plain`. `--text` and `--raw` exist on some commands for other purposes. | — |
| OUT-5 | `--quiet` is global, but only `status`, `code search`, `memory distill`, and the tips printer read it; `init` has its own `-q`. | readers of `cfg.Quiet` |
| OUT-12 | `NO_COLOR` is parsed as a boolean, so `NO_COLOR=yes` leaves color on, and a comment in `cmd/ox/main.go` documents `NO_COLOR=0` as forcing color; no-color.org treats any non-empty value as "no color". The color profile is detected on stdout only. No `--no-color`. | `internal/theme/profile.go`; `cmd/ox/main.go` |
| OUT-13 | Spinners and `doctor`'s progress line are gated on stdin being a TTY (`cli.IsInteractive()`), not on the stream they draw to. | `internal/cli/spinner.go`; `cmd/ox/doctor.go` |
| OUT-15 | A command that takes 3 s or longer prints its per-phase timing tree to stderr by default. | `perf.CLITreeSink` in `internal/perf/sink.go` |
| OUT-16 | The default log handler writes WARN and ERROR records to stderr as `level=WARN msg=…` without `--verbose`. | `internal/logger/logger.go` |
| OUT-17 | No pager. | — |
| ERR-4, ERR-5 | An unexpected error prints `Error: <err>` with no debug-log path or bug-report link. There is no `recover()` at the entry point, so a panic prints a Go stack trace. | `cmd/ox/main.go` |
| ARG-6 | `-d` is `--days` (`index`) and `--description` (`carts`), not `--debug`; `-n` is `--lines` (`daemon logs`) and `--limit` (`carts list`), not `--dry-run`; `--dry-run` has no short form. | `cmd/ox/index.go`, `carts.go`, `daemon.go` |
| ARG-11 | Six commands read stdin on `-`; none writes stdout on `-`. | — |
| SUB-1 | Locally, `-v` also means "show passed and skipped checks" (`doctor`), "show IDs and full paths" (`status`), and "show sync history" (`daemon status`). `-y` exists on `doctor`, `team invite`, and `bulletin` only; the global `--yes` has no short form on purpose (comment in `registerPersistentFlags`). | `cmd/ox/doctor.go`, `status.go`, `daemon.go`, `root.go` |
| SUB-2, SUB-3 | One operation, two paths: `ox index code` and `ox code index`; `ox gc` and `ox doctor --gc`. `query` exists four ways (`query`, `kb query`, hidden `code query`, `agent <id> query`). `export` means "show where your data lives" at the top level and "write markdown" under `session`. One item is displayed by `view` (`session`, `plan`), `show` (`team`, `conversation`, `carts`), or `describe` (`kb`). Nouns mix plural (`addons`, `carts`, `hooks`, `skills`) and singular (`session`, `plan`, `team`). | `cmd/ox/` |
| ROB-2 | `ox init`'s repository registration and `ox logout`'s token revocation print nothing before their network calls. | `cmd/ox/init.go`; `cmd/ox/logout.go` |
| ROB-5 | HTTP timeouts are fixed per client and cannot be configured. | `internal/api`, `internal/auth`, `internal/lfs` |
| FUT-4 | `ox murmur <content>` and `ox viz <id>` treat a word that is not a subcommand as data, so each new subcommand changes what that word does. | `cmd/ox/murmur.go`; `cmd/ox/viz.go` |
| FUT-6 | With telemetry on and a login present, every command waits up to 3 s at exit to flush its timing traces. | `observability.Shutdown` in `internal/observability/otel.go` |
| SIG-1 | `ox plan review` shuts its HTTP server down with no timeout. | `cmd/ox/plan_review.go` |
| CFG-2 | `XDG_STATE_HOME` is never used, and some paths are hard-coded under `~/.local/share` and ignore `XDG_DATA_HOME` (trusted endpoints, redact-history backups, the adapters directory `~/.local/share/ox/adapters`). Any path change is a Required Review. | `internal/paths/`; `cmd/ox/login_trusted_endpoints.go`; `cmd/ox/adapter.go` |
| CFG-3 | `ox init` lists none of the files it changes and has no `--dry-run`; without a TTY it configures every detected AI coworker. The hooks it adds to `.claude/settings.json` carry no marker (ox recognizes them by command text), and its `.gitattributes` block has a start comment but no end marker. | `cmd/ox/init.go`; `internal/hooks/claude/detect.go`; `cmd/ox/doctor_sageox.go` |
| ENV-2 | Documented customer-facing variables use `OX_*`, which `CLAUDE.md` calls an anti-pattern: `OX_USER_CONFIG`, `OX_SESSION_RECORDING`, `OX_PROJECT_ROOT`, `OX_JSON`. Renaming one is a Required Review and follows FUT-2. | `internal/config/` |
| ENV-5 | `FORCE_COLOR`, `DEBUG`, and `PAGER` are not read. | — |
| DIST-2 | `ox uninstall` removes ox from one repository; nothing documents removing the binary itself (Homebrew, or where `scripts/install.sh` put it). | `cmd/ox/uninstall.go`; `README.md` |
| ANLY-1 | The first-run telemetry notice prints after the first event has been sent. Nothing user-facing says how long data is kept; ADR-008 (privacy) states retention but is marked internal and partly aspirational. | `cmd/ox/telemetry_posthog.go`; [`008-privacy.md`](008-privacy.md) |

## Consequences

### Positive

- One catalog, with stable IDs, of what an ox command owes a person and an AI
  coworker. A review cites `ARG-10` instead of re-arguing confirmation levels.
- Contributors and AI coworkers read the baseline in the repository instead of
  fetching a web page mid-review.
- Deviations are listed with reasons, so the next reviewer can tell a choice from
  an accident.
- The gap table is a ready follow-up list, each row pointing at the code.

### Negative / honest limits

- The catalog is a restatement. When clig.dev changes upstream, this copy does
  not; the pinned revision keeps the difference reviewable (diff
  `content/_index.md` from `2bd6023e` to upstream `main`).
- The audit reflects 2026-09-30. Notes and gaps go stale as code changes; fixing a
  gap includes deleting its row.
- The known gaps are real work, and this ADR assigns no owner to them.
- Nothing enforces the catalog but review.

## Alternatives considered

1. **Link clig.dev from `cli-design-system.md` and stop there.** A link binds no one
   and gives reviewers nothing to cite, and it leaves the AI-coworker reading of
   "human-first" undecided.
2. **Vendor the clig.dev text.** CC BY-SA 4.0 is on `CLAUDE.md`'s banned list, and
   share-alike would attach to the copied text. The upstream text also lacks the
   ox bindings, which are the part reviewers need.
3. **Write ox-native guidelines from scratch.** Reinvents a reviewed baseline that
   contributors — and the models behind AI coworkers — already know
   (`agent-ux-principles.md` §5).
4. **Adopt an older or narrower guide** — POSIX utility conventions, the GNU coding
   standards, Heroku's CLI style guide, or 12 Factor CLI Apps. clig.dev builds on
   and links all four; they remain further reading.

## References

- [Command Line Interface Guidelines](https://clig.dev/) ·
  [source](https://github.com/cli-guidelines/cli-guidelines) ·
  [CC BY-SA 4.0](https://github.com/cli-guidelines/cli-guidelines/blob/main/LICENSE)
- Further reading named by clig.dev: *The Unix Programming Environment* (Kernighan
  and Pike); [POSIX Utility Conventions](https://pubs.opengroup.org/onlinepubs/9699919799/basedefs/V1_chap12.html);
  [GNU Coding Standards — Program Behavior for All Programs](https://www.gnu.org/prep/standards/html_node/Program-Behavior.html);
  [12 Factor CLI Apps](https://medium.com/@jdxcode/12-factor-cli-apps-dd3c227a0e46) (Jeff Dickey);
  [Heroku CLI Style Guide](https://devcenter.heroku.com/articles/cli-style-guide).
- Sources behind specific guidelines: [no-color.org](https://no-color.org/) (OUT-12);
  [XDG Base Directory Specification](https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html) (CFG-2);
  [Crash-only software](https://lwn.net/Articles/191059/) (ROB-7, SIG-2);
  [POSIX environment variables](https://pubs.opengroup.org/onlinepubs/009695399/basedefs/xbd_chap08.html) (ENV-4);
  [Google — Writing helpful error messages](https://developers.google.com/tech-writing/error-messages) and
  [NN/g — Error-message guidelines](https://www.nngroup.com/articles/error-message-guidelines) (ERR).
- ox specs and rules: `docs/specs/cli-design-system.md`,
  `docs/specs/agent-ux-principles.md`, `docs/specs/agent-ux-ox-implementation.md`,
  `.claude/rules/json-output.md`, `.claude/rules/design.md`, `CLAUDE.md`.
- Related decisions — this ADR aligns with each and amends none:
  - [ADR-001](ADR-001-pure-go-no-cgo.md) — pure Go, one static binary per platform (DIST-1).
  - [ADR-008 — External Adapter Binaries](ADR-008-external-adapter-binaries.md) — the adapter binaries (DIST-1 deviation).
  - [ADR-008 — Privacy](008-privacy.md) — telemetry (ANLY-1).
  - [ADR-009](ADR-009-adapter-naming-convention.md) — `ox-adapter-<name>` (NAME).
  - [ADR-023](ADR-023-skill-injection-two-layer-model.md) — guidance travels in CLI output (OUT-8). <!-- SOURCE: sageox adr:docs/adr/ADR-023-skill-injection-two-layer-model.md -->
  - [ADR-030](ADR-030-per-clone-git-serialization.md) — concurrent ox processes (ROB-8).
  - [ADR-031](ADR-031-cli-skill-rule-inventory.md) — ox-owned files and marked blocks (CFG-3). <!-- SOURCE: sageox adr:docs/adr/ADR-031-cli-skill-rule-inventory.md -->
  - Considered and not applicable: ADR-015 (WASM adapter runtime), ADR-017 and
    ADR-028 (Knowledge Bubbles), ADR-021 (`ox plan` context vs inference), and
    ADR-022 (adapter security posture) decide no CLI interaction convention.
    ADR-007 (hooks integration) describes hook installation, which CFG-3 covers,
    but is partly superseded, so the CFG-3 notes cite code instead.
