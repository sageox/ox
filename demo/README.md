# Demo Recording

One tape drives the README GIF, set *inside a coding-agent session* — because the
magic is what the agent can do, not the setup:

- **`demo.tape` → `demo.gif`** — one continuous session that walks through every
  wow moment: automatic session recording, recall of prior work **across agents
  (Codex + Claude Code), machines, and teammates**, a WIP murmur out plus a
  teammate's conflicting work surfaced in plain language, and a plan **enriched**
  with collisions, prior art, and expert routes.

Session capture is **automatic** via `ox agent prime` — there is no manual
`/ox-session-start` ritual. The demo shows the magic, not the internals (no raw
`<system-reminder>` blocks), but the phrasing mirrors real output
(`cmd/ox/agent.go`, `cmd/ox/agent_hook_plan_nudge.go`). Claude and the cloud legs
are mocked in `demo/mock/` for reproducible, offline recording.

## Re-recording the Demo

### Prerequisites

```bash
brew install vhs
brew install sops yq
node --version  # 18+
```

### Authentication Setup

The demo uses a dedicated demo account (`you@yourcompany.com`).

**Option 1: SOPS-encrypted credentials (recommended)**

1. Get SOPS age key from 1Password (search "SageOx SOPS Age Key")
2. Save to `~/.config/sops/age/keys.txt` with `chmod 600`
3. Create `demo/credentials.sops.yaml`:

```bash
cp demo/credentials.example.yaml demo/credentials.yaml
vim demo/credentials.yaml
cd demo && sops -e credentials.yaml > credentials.sops.yaml
rm credentials.yaml
```

**Option 2: Environment variables**

```bash
export DEMO_EMAIL="you@yourcompany.com"
export DEMO_PASSWORD="your-password"
```

### Record

```bash
# Full setup: build ox, authenticate, create demo environment
./demo/setup.sh

# Record the demo
vhs demo/demo.tape   # → demo/demo.gif
```

### Options

```bash
# Just authenticate (skip repo setup)
./demo/setup.sh --auth-only

# Skip authentication (use existing session)
SKIP_AUTH=1 ./demo/setup.sh

# Show browser during login (debugging)
HEADLESS=false ./demo/setup.sh

# Clean up
./demo/setup.sh --clean
```

## Files

```
demo/
├── setup.sh              # Setup script (build, auth, environment)
├── demo.tape             # VHS tape — one session, every wow moment
├── demo.gif              # Output (generated)
├── mock/                 # Mock ox + claude for reproducible recording
│   ├── ox                # login/init/doctor/status/agent prime
│   └── claude            # the one-session agent demo
├── .sops.yaml            # SOPS encryption config
├── credentials.sops.yaml # Encrypted credentials (not in git)
├── credentials.example.yaml # Credentials template
├── playwright/           # Browser automation
│   ├── package.json
│   ├── tsconfig.json
│   └── login.ts          # Automated login script
├── claude-demo.tape      # Extended demo with AI agent + beads
└── README.md             # This file
```
