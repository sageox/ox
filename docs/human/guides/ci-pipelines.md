<!-- doc-audience: human -->
# Using ox in CI/CD Pipelines

Run ox in ephemeral environments — CI runners, containers, and automated pipelines — where there's no interactive human or persistent home directory.

---

## Environment Variables

Three env vars solve the most common CI challenges:

| Variable | Purpose | Example |
|----------|---------|---------|
| `OX_PROJECT_ROOT` | Point to the project when cwd isn't inside it | `/workspace/my-repo` |
| `OX_SESSION_RECORDING` | Force session recording mode without config files | `auto`, `manual`, `disabled` |
| `OX_USER_CONFIG` | Load user config from an explicit file path | `/etc/sageox/config.yaml` |

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

The config file uses the same YAML format as `~/.config/sageox/config.yaml`:

```yaml
display_name: ci-pipeline
sessions:
  mode: auto
```

---

## Minimal CI Setup

```bash
# 1. authenticate (use a service account token)
ox login --token "$SAGEOX_TOKEN"

# 2. point to the project
export OX_PROJECT_ROOT="$CI_PROJECT_DIR"

# 3. record sessions automatically
export OX_SESSION_RECORDING=auto

# 4. prime the AI coworker
ox agent prime
```

---

## Other Useful Variables

| Variable | Purpose |
|----------|---------|
| `SAGEOX_ENDPOINT` | Override the SageOx API endpoint |
| `OX_XDG_ENABLE=1` | Use XDG paths (`~/.config/sageox/`) instead of `~/.sageox/` |
| `OX_NO_DAEMON=1` | Skip daemon startup (useful in short-lived containers) |
