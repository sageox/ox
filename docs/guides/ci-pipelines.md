# Using ox in CI/CD Pipelines

> **Headless auth:** a machine with no browser authenticates with a team token in `SAGEOX_TOKEN`. See [Running ox under a team token](#running-ox-under-a-team-token). The other variables below also help **devroot workflows** (running ox from a parent directory) and **local automation** (scripts on an already-authenticated machine).

---

## Environment Variables

Three env vars solve the most common CI challenges:

| Variable | Purpose | Example |
|----------|---------|---------|
| `OX_PROJECT_ROOT` | Point to the project when cwd isn't inside it | `/workspace/my-repo` |
| `OX_SESSION_RECORDING` | Force session recording mode without config files | `auto`, `manual`, `disabled` |
| `OX_USER_CONFIG` | Load user config from an explicit file path | `/etc/sageox/config.yaml` |
| `SAGEOX_NO_DEVICE_LABEL` | Don't attach a `user@host` label to tokens minted by `ox login` | `1` |

### `OX_PROJECT_ROOT`

Normally ox walks up from cwd to find `.sageox/`. In devroot workflows or CI where the working directory isn't inside the project tree, set this to the project root:

```bash
export OX_PROJECT_ROOT=/workspace/my-repo
ox agent prime
```

Falls back to normal walk-up discovery if the path doesn't contain a valid `.sageox/` directory.

### `OX_SESSION_RECORDING`

Controls whether sessions are recorded. Overrides all config sources (user, project, team).

```bash
# force recording in CI
export OX_SESSION_RECORDING=auto

# disable recording in test environments
export OX_SESSION_RECORDING=disabled
```

Valid values: `auto` (record automatically), `manual` (require explicit start), `disabled` (no recording).

**Priority:** env var > user config > project config > team config > default (`manual`).

### `OX_USER_CONFIG`

Points to a user config file directly. Useful when there's no home directory (containers, ephemeral runners):

```bash
export OX_USER_CONFIG=/etc/sageox/pipeline-config.yaml
```

For a single invocation, `--config <file>` (or `-c <file>`) takes precedence
over `OX_USER_CONFIG` and the default user config location:

```bash
ox --config /etc/sageox/pipeline-config.yaml config get session_recording
ox --config /etc/sageox/pipeline-config.yaml config set session_recording disabled
```

The flag requires an existing, readable YAML file and fails on missing or malformed
files before running the command. User-level reads and writes use that file;
`config set/unset --repo` and `--team` still target the current project's repo and
team files. Setting-specific environment overrides such as `OX_SESSION_RECORDING`
keep their existing priority over stored preferences.
Hosted Ledger reads reject `--config`; they use the explicit endpoint and token
contract described in [Ledger read sync](../specs/ledger-read-sync.md).

The config file uses the same YAML format as `~/.config/sageox/config.yaml`:

```yaml
display_name: ci-pipeline
sessions:
  mode: auto
```

---

## Local Automation Example

On a machine where `ox login` has already been run interactively:

```bash
# point to the project (when cwd isn't inside it)
export OX_PROJECT_ROOT="$PROJECT_DIR"

# record sessions automatically
export OX_SESSION_RECORDING=auto

# prime the AI coworker
ox agent prime
```

> **Future:** Service account and token-based auth (`ox login --token`) is planned but not yet implemented. Once available, this pattern will work in true headless CI/CD environments.

### Check repository health

`ox doctor` exits **1** when checks fail or required setup is missing, and
**0** when there are no failed checks. Warnings and skipped checks alone do not
fail the command. This applies to text output and `--json` (including `OX_JSON=1`).

```bash
if ox doctor --json > doctor.json; then
  echo "Checks passed"
else
  echo "Doctor failed; inspect doctor.json and stderr" >&2
fi
```

The JSON report is still written on check failures, with `summary.has_failed`
and individual check details. Invocation errors or interruption can stop the
command before a complete report is available. Scripts that previously relied
on JSON mode always exiting 0 should handle its exit status explicitly.

---

## Running ox under a team token

A team token (`oxt_…`) belongs to one AI coworker on the team. Set it as `SAGEOX_TOKEN` and ox acts as that coworker, with no `ox login`:

```bash
export SAGEOX_TOKEN="$OX_TEAM_TOKEN"   # from your CI secret store
JOB_STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"   # kept for `ox session wait` below
ox agent prime
```

**Who the work belongs to.** ox asks the server which AI coworker the token belongs to, then records everything as that coworker:

- Sessions have the coworker's `agt_…` ID as `user_id`, and their origin is `agent`.
- Murmurs carry `principal_type: "ai"`.
- Plans are authored by the coworker.
- Ledger commits are authored as `<Name> <agt_…@ai-coworker.invalid>`, not as the machine's git user. Git's own `GIT_AUTHOR_*` and `GIT_COMMITTER_*` variables still override this, so don't set them in the job.

A team token with no AI coworker attached is refused: ox records no session and sends no murmur rather than use the machine's git identity.

**Git credentials.** ox exchanges the token for a short-lived Git PAT, tied to that token. A Git 401 makes ox fetch a fresh PAT. If the token was revoked, nothing is minted. ox's API calls then fail with a message to rotate or re-mint the team token and update `SAGEOX_TOKEN`; Git itself reports only an authentication failure.

**Before the machine goes away.** In each project the job recorded sessions in, wait until they are in the Ledger:

```bash
ox session wait --since "$JOB_STARTED_AT" --timeout 10m --json
```

The command checks the current project only. `--since` is compared with each session's stop time, or its creation time if it never recorded one. It exits 0 once every session it can read is uploaded. It cannot see a session whose recording state is unreadable (for example, a job killed while writing it), and it reports nothing about one, so exit 0 does not prove such a session is safe. The report lists `older_pending` (sessions from earlier jobs) and `failed`. `in_ledger` counts every session the coworker published since `--since`, so parallel jobs under the same coworker are counted too.

**Machines that live for one job.** Set `disposable_machine: true` in the user config (`OX_USER_CONFIG`) when the machine is thrown away after each job but its home directory persists.

---

## Other Useful Variables

| Variable | Purpose |
|----------|---------|
| `SAGEOX_ENDPOINT` | Override the SageOx API endpoint |
| `OX_XDG_ENABLE=1` | Use XDG paths (`~/.config/sageox/`) instead of `~/.sageox/` |
| `OX_NO_DAEMON=1` | Skip daemon startup (useful in short-lived containers) |
