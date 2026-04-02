# Docker Test Environment Infrastructure

This directory contains Docker-based test infrastructure for testing the ox CLI against multiple AI coding agents. Each agent gets its own isolated container with the agent CLI pre-installed at a specific version.

## Architecture

- **`base.Dockerfile`**: Shared base image with Go 1.24, Node.js 22, git, jq, and curl
- **Agent-specific Dockerfiles**: One per agent with that agent's CLI installed
- **Version management**: Pinned versions with cache busting for controlled testing

## Supported Agents

| Agent | Package | Installation Method |
|-------|---------|-------------------|
| claude-code | `@anthropic-ai/claude-code` | npm |
| codex | `@openai/codex` | npm |
| gemini | `@google/gemini-cli` | npm |
| amp | `@sourcegraph/amp` | npm |
| pi | `@mariozechner/pi-coding-agent` | npm |
| opencode | `github.com/opencode-ai/opencode` | go install |
| code-puppy | `codepuppy` | npm |

## Building Images

### Build base image first:
```bash
docker build -f test-envs/base.Dockerfile -t ox-test-base .
```

### Build agent-specific images:
```bash
# Use default (latest) versions
docker build -f test-envs/claude-code.Dockerfile -t ox-test-claude-code .

# Use specific versions
docker build -f test-envs/claude-code.Dockerfile \
  --build-arg AGENT_VERSION=1.2.3 \
  --build-arg CACHE_BUST=$(date +%s) \
  -t ox-test-claude-code:1.2.3 .
```

### Build all at once:
```bash
# From project root
make docker-test-images  # (if Makefile target exists)
```

## Cache Management

The `CACHE_BUST` argument forces agent installation to re-run even when the version hasn't changed. This is useful when:
- Testing against the same version but want fresh package installs
- Agent registries have been updated with patches under the same version tag
- Debugging installation issues

```bash
# Force fresh install with cache bust
docker build -f test-envs/claude-code.Dockerfile \
  --build-arg CACHE_BUST=$(date +%s) \
  -t ox-test-claude-code .
```

## Version Management

### Check available agent versions:
```bash
# Get last 5 versions of an agent
./test-envs/agent-versions.sh claude-code 5
./test-envs/agent-versions.sh opencode 3
```

### Pin specific versions:
Edit `test-envs/versions.env` to set defaults, or use build args for one-off builds.

## Running Tests

### Basic test run:
```bash
docker run --rm ox-test-claude-code ./tests/integration/agents/claude/...
```

### With volume mounts for development:
```bash
docker run --rm \
  -v $(pwd):/workspace \
  -w /workspace \
  ox-test-claude-code \
  go test ./tests/integration/agents/claude/...
```

### With secrets (requires decryption setup - see README.md):
```bash
# Use the decrypt-env.sh helper to inject API keys
docker run --rm \
  $(./test-envs/decrypt-env.sh) \
  ox-test-claude-code \
  go test ./tests/integration/agents/claude/...
```

### Test against multiple agent versions:
```bash
for version in 1.0.0 1.1.0 latest; do
  echo "Testing against claude-code@$version"
  docker build -f test-envs/claude-code.Dockerfile \
    --build-arg AGENT_VERSION=$version \
    -t ox-test-claude-code:$version . > /dev/null

  docker run --rm ox-test-claude-code:$version \
    go test ./tests/integration/agents/claude/basic_test.go
done
```

## Future Enhancements

This infrastructure is designed to support:
1. **CI/CD Integration**: Auto-build images for each ox release
2. **Version Matrix Testing**: Test ox against multiple agent versions automatically
3. **GitHub Pages Hosting**: Pre-built images published for faster CI runs
4. **Regression Testing**: Compare ox behavior across agent version updates

## Docker Layer Optimization

The Dockerfiles are structured for optimal caching:
1. **Base layers**: OS packages and tools (changes rarely)
2. **Agent installation**: npm/go install with cache busting (changes when testing new versions)
3. **Go dependencies**: `go mod download` (changes when ox dependencies change)
4. **Source code**: ox source (changes frequently during development)

This ensures that during development, only the ox source layer needs to rebuild.

## Integration with Secrets Management

See [README.md](README.md) for details on managing API keys for integration tests. The Docker infrastructure integrates with the SOPS-encrypted secrets via the `decrypt-env.sh` script.

```bash
# Decrypt secrets and inject as environment variables
docker run --rm \
  $(./test-envs/decrypt-env.sh) \
  ox-test-claude-code \
  go test ./tests/integration/...