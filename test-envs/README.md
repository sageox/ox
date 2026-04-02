# Test Environment Secrets

Integration tests for ox require API keys for 7+ AI coding agents. Since this is a public repo, secrets are encrypted with [SOPS](https://github.com/getsops/sops) + [age](https://github.com/FiloSottile/age) and committed safely.

**External contributors:** You cannot decrypt these secrets. Integration tests that need API keys will skip gracefully -- lifecycle and unit tests work without any keys.

## Prerequisites

```bash
brew install sops age yq
```

## One-Time Setup (Team Members)

1. **Get the team age private key** from a team member or 1Password (vault: "SageOx Engineering")

2. **Save it locally:**
   ```bash
   mkdir -p ~/.config/sageox
   echo "AGE-SECRET-KEY-..." > ~/.config/sageox/test-age-key.txt
   chmod 600 ~/.config/sageox/test-age-key.txt
   ```

3. **Set the environment variable** (add to your shell profile):
   ```bash
   export SOPS_AGE_KEY_FILE=~/.config/sageox/test-age-key.txt
   ```

4. **Verify decryption works:**
   ```bash
   sops -d test-envs/secrets.enc.yaml
   ```

## How It Works

| File | Purpose | Committed? |
|------|---------|------------|
| `.sops.yaml` | SOPS config with age public key | Yes |
| `secrets.template.yaml` | Structure reference (no real keys) | Yes |
| `secrets.enc.yaml` | Encrypted secrets (safe in public repo) | Yes |
| `secrets.yaml` | Decrypted plaintext (NEVER commit) | No (.gitignored) |

SOPS encrypts only the **values** in the YAML file. Keys (field names) remain visible so you can see the structure without decrypting. The age public key in `.sops.yaml` allows anyone to **encrypt** new values, but only holders of the private key can **decrypt**.

## Common Operations

### View current secrets
```bash
sops -d test-envs/secrets.enc.yaml
```

### Edit secrets (decrypt-in-place via $EDITOR)
```bash
sops test-envs/secrets.enc.yaml
```

### Add a new key
```bash
sops test-envs/secrets.enc.yaml
# Your editor opens with decrypted YAML -- add the new key, save, and close.
# SOPS re-encrypts automatically on save.
git add test-envs/secrets.enc.yaml && git commit -m "add NEW_KEY to test secrets"
```

### Initialize from template (first time only)
```bash
cp test-envs/secrets.template.yaml test-envs/secrets.yaml
# Fill in real API keys in secrets.yaml
sops -e test-envs/secrets.yaml > test-envs/secrets.enc.yaml
rm test-envs/secrets.yaml
git add test-envs/secrets.enc.yaml
```

### Rotate the age key
1. Generate new keypair: `age-keygen`
2. Update `.sops.yaml` with the new public key
3. Re-encrypt: `sops updatekeys test-envs/secrets.enc.yaml`
4. Distribute new private key to team + update CI secret
5. Commit `.sops.yaml` and `secrets.enc.yaml`

## CI (GitHub Actions)

CI uses a single repository secret: `SOPS_AGE_KEY`

The workflow writes it to a temp file and sets `SOPS_AGE_KEY_FILE`:

```yaml
- name: Decrypt test secrets
  env:
    SOPS_AGE_KEY: ${{ secrets.SOPS_AGE_KEY }}
  run: |
    echo "$SOPS_AGE_KEY" > /tmp/age-key.txt
    export SOPS_AGE_KEY_FILE=/tmp/age-key.txt
    eval $(./test-envs/decrypt-env.sh)
```

To set up the CI secret:
1. Go to repo Settings > Secrets and variables > Actions
2. Add `SOPS_AGE_KEY` with the full private key line (`AGE-SECRET-KEY-...`)

## Docker Integration

The `decrypt-env.sh` helper outputs `-e KEY=VALUE` flags for docker:

```bash
docker run $(./test-envs/decrypt-env.sh) ox-test-image make test-integration
```

If no age key is available, the script outputs nothing and tests fall back to lifecycle-only mode.

## Graceful Fallback

Tests check for API keys at runtime:

- **Keys present:** Full integration tests run (real API calls to AI agents)
- **Keys absent:** Integration tests skip with `t.Skip("no ANTHROPIC_API_KEY")`, lifecycle and unit tests run normally

This means external contributors and CI forks can still run the full test suite minus the E2E agent tests.
