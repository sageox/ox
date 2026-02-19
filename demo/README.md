# Demo Recording

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
vhs demo/demo.tape
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
├── demo.tape             # VHS tape for recording
├── demo.gif              # Output (generated)
├── .sops.yaml            # SOPS encryption config
├── credentials.sops.yaml # Encrypted credentials (not in git)
├── credentials.example.yaml # Credentials template
├── playwright/           # Browser automation
│   ├── package.json
│   ├── tsconfig.json
│   └── login.ts          # Automated login script
├── claude-demo.tape      # Extended demo with AI agent
└── README.md             # This file
```
