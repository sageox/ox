# ox Adapter System: Architecture Diagrams

## 1. Current Architecture (Built-in Adapters)

Everything compiled into one binary. Adding a new agent requires modifying ox source.

```mermaid
graph LR
    subgraph ox_binary["ox binary (monolith)"]
        direction TB
        CLI["CLI (hooks, prime, status)"]
        D["Daemon"]
        CC["ClaudeCodeAdapter"]
        GM["GeminiAdapter"]
        CX["CodexAdapter"]
        GN["GenericJSONLAdapter"]
        TW["TailWatcher"]
    end

    Claude["Claude Code"] -->|hook event| CLI
    Gemini["Gemini CLI"] -->|hook event| CLI
    Codex["Codex CLI"] -->|hook event| CLI

    CLI -->|"IPC (Unix socket)"| D
    D --> CC
    D --> GM
    D --> CX
    D --> GN
    CC --> TW
    GM --> TW

    CC -.->|reads| SF1["~/.claude/**/*.jsonl"]
    GM -.->|reads| SF2["~/.gemini/**/chats/*.json"]
    CX -.->|reads| SF3["~/.codex/sessions/**/*.jsonl"]

    style ox_binary fill:#1a1a2e,stroke:#533483,color:#e0e0e0
```

---

## 2. Proposed Architecture (External Adapters)

Agent-specific logic moves into separate binaries. ox core only knows the protocol.

```mermaid
graph TB
    subgraph agents["AI Coding Agents"]
        Claude["Claude Code"]
        Gemini["Gemini CLI"]
        Amp["Amp"]
        Cursor["Cursor"]
        Custom["Custom Agent"]
    end

    subgraph ox_core["ox core (thin)"]
        CLI["CLI (hooks)"]
        D["Daemon"]
        AS["AdapterSupervisor"]
        Proto["Protocol Types<br/>(adapterprotocol/)"]
    end

    subgraph adapters["External Adapter Binaries"]
        A1["ox-adapter-claude-code"]
        A2["ox-adapter-gemini"]
        A3["ox-adapter-amp"]
        A4["ox-adapter-cursor"]
        A5["ox-adapter-custom"]
    end

    subgraph session_files["Agent Session Files"]
        SF1["~/.claude/**/*.jsonl"]
        SF2["~/.gemini/**/chats/*.json"]
        SF3["~/.amp/sessions/*.jsonl"]
        SF4["~/.cursor/sessions/"]
        SF5["custom location"]
    end

    Claude -->|hook| CLI
    Gemini -->|hook| CLI
    Amp -->|hook| CLI
    Cursor -->|hook| CLI
    Custom -->|hook| CLI

    CLI -->|"IPC (Unix socket)"| D
    D --> AS

    AS -->|"stdin/stdout<br/>NDJSON"| A1
    AS -->|"stdin/stdout<br/>NDJSON"| A2
    AS -->|"stdin/stdout<br/>NDJSON"| A3
    AS -->|"stdin/stdout<br/>NDJSON"| A4
    AS -->|"stdin/stdout<br/>NDJSON"| A5

    A1 -.->|reads| SF1
    A2 -.->|reads| SF2
    A3 -.->|reads| SF3
    A4 -.->|reads| SF4
    A5 -.->|reads| SF5

    style ox_core fill:#0f3460,stroke:#533483,color:#e0e0e0
    style adapters fill:#1a6b3c,stroke:#16213e,color:#e0e0e0
    style agents fill:#1a1a2e,stroke:#16213e,color:#e0e0e0
```

---

## 3. IPC Data Flow: Hook-Driven Recording

Shows the full path from agent tool call to recorded session entry.

```mermaid
sequenceDiagram
    participant CC as Claude Code
    participant Hook as ox CLI<br/>(hook process)
    participant Daemon as ox Daemon
    participant Sup as AdapterSupervisor
    participant Adapter as ox-adapter-claude-code<br/>(--serve)
    participant SF as Session File<br/>(~/.claude/...)
    participant Raw as raw.jsonl<br/>(ledger)

    Note over CC: Agent uses a tool (Bash, Edit, Read...)

    CC->>Hook: PostToolUse hook fires<br/>(new process per call)

    Hook->>Daemon: IPC: adapter.read<br/>{agent_id: "OxA1b2", offset: 512}

    Note over Daemon,Sup: First call? Lazy spawn adapter

    alt First hook call for this session
        Sup->>Adapter: spawn: ox-adapter-claude-code --serve
        Sup->>Adapter: stdin: {id:1, method:"find-session",<br/>params:{agent_id:"OxA1b2", since:"..."}}
        Adapter->>SF: scan for matching session file
        SF-->>Adapter: found
        Adapter-->>Sup: stdout: {id:1, result:{session_file:"...", offset:0}}
    end

    Sup->>Adapter: stdin: {id:2, method:"read-from-offset",<br/>params:{agent_id:"OxA1b2", offset:512}}

    Adapter->>SF: read bytes from offset 512
    SF-->>Adapter: new JSONL lines
    Adapter->>Adapter: parse agent-native format<br/>into RawEntry objects

    Adapter-->>Sup: stdout: {id:2, result:<br/>{entries:[...], new_offset:1024}}

    Sup->>Daemon: entries + new offset
    Daemon->>Daemon: redact secrets
    Daemon->>Raw: append entries to raw.jsonl
    Daemon->>Daemon: update offset: 1024

    Daemon-->>Hook: IPC response:<br/>{entries_captured: 3, new_offset: 1024}

    Hook-->>CC: exit 0 (hook complete)

    Note over CC: Agent continues working
```

---

## 4. Adapter Process Lifecycle

How the daemon manages adapter processes across a session.

```mermaid
stateDiagram-v2
    [*] --> Idle: session registered

    Idle --> Spawning: first hook call<br/>(lazy start)

    Spawning --> FindingSession: spawn --serve<br/>send find-session
    FindingSession --> Active: session file found

    Spawning --> Failed: spawn fails<br/>(binary not found)
    FindingSession --> Retrying: session file<br/>not found yet

    Retrying --> FindingSession: retry (2s delay,<br/>max 3 attempts)
    Retrying --> OneShot: all retries<br/>exhausted

    Active --> Active: read-from-offset<br/>(per hook call)
    Active --> Crashed: adapter process exits<br/>unexpectedly

    Crashed --> Respawning: respawn attempt<br/>(max 3 per session)
    Respawning --> Active: respawn succeeds<br/>(resume from last offset)
    Respawning --> OneShot: 3 respawns<br/>exhausted

    Active --> ShuttingDown: session stop

    ShuttingDown --> [*]: shutdown sent,<br/>process exits

    OneShot --> [*]: session stop<br/>(one-shot per call)

    Failed --> [*]: session stop<br/>(nothing recorded)
```

---

## 5. Fallback Behavior: Daemon Available vs Unavailable

```mermaid
graph TB
    Hook["Hook fires<br/>(PostToolUse)"]

    Hook --> Try{"Connect to<br/>daemon IPC?"}

    Try -->|Yes| Fast["FAST PATH<br/>IPC to daemon"]
    Try -->|No| Slow["SLOW PATH<br/>One-shot spawn"]

    Fast --> DRoute["Daemon routes to<br/>adapter --serve process"]
    DRoute --> DRead["adapter.read-from-offset<br/>(cached file handle, no spawn)"]
    DRead --> DWrite["Append to raw.jsonl"]
    DWrite --> Done["Exit 0"]

    Slow --> SSpawn["Spawn:<br/>ox-adapter-claude-code<br/>read-from-offset --offset 512"]
    SSpawn --> SRead["Adapter opens file,<br/>seeks, reads, exits"]
    SRead --> SWrite["Hook writes to raw.jsonl"]
    SWrite --> Done

    style Fast fill:#1a6b3c,color:#fff
    style Slow fill:#8b4513,color:#fff
    style Done fill:#333,color:#fff
```

---

## 6. Adapter Discovery and Registration

How the daemon finds and registers adapters at startup.

```mermaid
flowchart TB
    Start["Daemon starts"]

    Start --> Scan["Scan adapter directories"]

    subgraph discovery["Discovery Order (first match wins)"]
        direction TB
        D1["1. $OX_ADAPTER_PATH<br/>(dev override)"]
        D2["2. ~/.local/share/ox/adapters/<br/>(installed)"]
        D1 --> D2
    end

    Scan --> discovery

    discovery --> Found["Found: ox-adapter-*"]

    Found --> Info["Call: ox-adapter-X info<br/>(one-shot)"]

    Info --> Parse["Parse response:<br/>protocol_version, type,<br/>name, capabilities"]

    Parse --> Check{"protocol_version<br/>>= minimum?"}

    Check -->|No| Skip["Skip adapter<br/>(log: incompatible)"]
    Check -->|Yes| Route{"Route by type"}

    Route -->|session| SR["SessionAdapterRegistry"]
    Route -->|vcs| VR["VCSAdapterRegistry"]
    Route -->|indexer| IR["IndexerRegistry"]
    Route -->|test| TR["TestRegistry<br/>(if test build)"]

    style discovery fill:#0f3460,color:#e0e0e0
```

---

## 7. Adapter Types and Capabilities

```mermaid
graph LR
    subgraph session["Session Adapters (type: session)"]
        direction TB
        S1["claude-code<br/>capabilities: session_reader,<br/>hook_installer, incremental_reader,<br/>serve_mode, file_watcher"]
        S2["gemini<br/>capabilities: session_reader,<br/>hook_installer, incremental_reader,<br/>serve_mode"]
        S3["amp<br/>capabilities: session_reader,<br/>hook_installer, incremental_reader,<br/>serve_mode"]
        S4["codex<br/>capabilities: session_reader,<br/>incremental_reader"]
    end

    subgraph indexer["Indexer Adapters (type: indexer)"]
        direction TB
        I1["github<br/>PRs, issues, code search"]
        I2["linear<br/>Linear issues, projects"]
        I3["beads<br/>task tracking"]
    end

    subgraph vcs["VCS Adapters (type: vcs)"]
        direction TB
        V1["git<br/>commits, blame, diff, branches"]
        V2["perforce<br/>(enterprise)"]
    end

    subgraph test_type["Test Adapters (type: test)"]
        direction TB
        T1["test-session<br/>controllable fake"]
        T2["test-crash<br/>failure injection"]
    end

    style session fill:#1a6b3c,color:#e0e0e0
    style indexer fill:#533483,color:#e0e0e0
    style vcs fill:#0f3460,color:#e0e0e0
    style test_type fill:#8b4513,color:#e0e0e0
```

---

## 8. Protocol: One-Shot vs Serve Mode

```mermaid
graph TB
    subgraph oneshot["One-Shot Mode (spawn per call)"]
        direction LR
        O1["ox/daemon"] -->|"exec: ox-adapter-X detect"| O2["Adapter Process"]
        O2 -->|"stdout: JSON + exit"| O1
        O3["Used for: info, detect,<br/>install-hooks, check-hooks,<br/>read, read-metadata"]
    end

    subgraph serve["Serve Mode (long-lived)"]
        direction LR
        S1["ox daemon"] -->|"exec: ox-adapter-X --serve"| S2["Adapter Process<br/>(stays alive)"]
        S1 -->|"stdin: NDJSON requests"| S2
        S2 -->|"stdout: NDJSON responses"| S1
        S2 -->|"stdout: NDJSON events<br/>(push)"| S1
        S3["Used for: find-session,<br/>read-from-offset,<br/>end-session, shutdown"]
    end

    subgraph wire["Wire Format (serve mode)"]
        direction TB
        W1["Request: {id:1, method:'find-session', params:{...}}"]
        W2["Response: {id:1, result:{session_file:'...', offset:0}}"]
        W3["Event: {event:'entries', agent_id:'OxA1', data:{entries:[...], new_offset:2048}}"]
        W4["Error: {id:2, error:{code:'method_not_found', message:'...'}}"]
    end

    style oneshot fill:#1a1a2e,color:#e0e0e0
    style serve fill:#0f3460,color:#e0e0e0
    style wire fill:#333,color:#e0e0e0
```

---

## 9. Installation and Distribution

```mermaid
flowchart TB
    subgraph sources["Installation Sources"]
        Brew["brew install sageox/tap/ox<br/>(bundles ox + common adapters)"]
        Manual["ox adapter install claude-code<br/>(downloads from registry)"]
        Community["ox adapter install<br/>github.com/user/ox-adapter-X"]
        Local["Drop binary in<br/>~/.local/share/ox/adapters/"]
    end

    subgraph registry["Registry (registry.yaml)"]
        direction TB
        R1["Embedded in ox binary<br/>(fallback, may be stale)"]
        R2["Fetched from GitHub<br/>(cached 24h locally)"]
    end

    subgraph install_flow["Install Flow"]
        direction TB
        F1["Fetch registry"]
        F2["Find adapter entry"]
        F3["Detect platform<br/>(darwin_arm64, linux_amd64, ...)"]
        F4["Download binary"]
        F5["Verify SHA-256"]
        F6["Move to ~/.local/share/ox/adapters/"]
        F7["chmod +x"]
        F8["Run: ox-adapter-X info<br/>(verify protocol version)"]
        F1 --> F2 --> F3 --> F4 --> F5 --> F6 --> F7 --> F8
    end

    Manual --> registry
    Community -->|"GitHub Releases API"| F4
    registry --> F1

    subgraph auto["Auto-Detection (ox integrate install)"]
        direction TB
        A1["Scan: which claude,<br/>which gemini, which amp"]
        A2["Check: adapter installed?"]
        A3["Prompt: 'Claude Code detected.<br/>Install ox-adapter-claude-code? [Y/n]'"]
        A4["Install missing adapters<br/>(parallel downloads)"]
        A5["Install hooks for<br/>all selected agents"]
        A1 --> A2 --> A3 --> A4 --> A5
    end
```

---

## 10. Migration Path (Phases 0-4)

```mermaid
gantt
    title Built-in to External Adapter Migration
    dateFormat YYYY-MM
    axisFormat %b %Y

    section Phase 0
    Protocol types + ExternalAdapter struct    :p0a, 2026-04, 1M
    Compliance test skeleton                   :p0b, after p0a, 1M

    section Phase 1
    Extract claude-code adapter                :p1a, after p0b, 2M
    Ship in release tarball                    :p1b, after p1a, 1M

    section Phase 2
    Extract gemini + codex adapters            :p2a, after p1b, 2M
    ox adapter subcommand (list/install/upgrade) :p2b, after p1b, 2M
    Publish registry.yaml                       :p2c, after p2a, 1M

    section Phase 3
    Mark built-ins deprecated                  :p3a, after p2c, 2M
    Ship ox-full for backwards compat          :p3b, after p3a, 1M

    section Phase 4
    Community adapter docs                     :p4a, after p3b, 2M
    Adapter template repo                      :p4b, after p3b, 1M
    Community registry section                 :p4c, after p4a, 1M
```

---

## 11. Daemon Supervisor: Multi-Agent Concurrent Sessions

Shows how one adapter process per type handles multiple concurrent sessions. Two Claude Code
sessions share a single `ox-adapter-claude-code --serve` process; an Amp session gets its own
`ox-adapter-amp --serve` process. Sessions are multiplexed by `agent_id`.

```mermaid
graph TB
    subgraph agents["Concurrent Agent Sessions (same repo)"]
        CC1["Claude Code<br/>Agent r7f3a2-OxA1b2"]
        CC2["Claude Code<br/>Agent r7f3a2-OxC3d4"]
        AM1["Amp<br/>Agent r7f3a2-OxE5f6"]
    end

    subgraph daemon["ox Daemon"]
        IPC["IPC Server<br/>(Unix socket)"]
        AS["AdapterSupervisor"]

        subgraph procs["Adapter Processes (one per type)"]
            P1["ox-adapter-claude-code --serve<br/>pid 1234<br/>sessions: OxA1b2, OxC3d4"]
            P2["ox-adapter-amp --serve<br/>pid 1236<br/>sessions: OxE5f6"]
        end
    end

    CC1 -->|hook| IPC
    CC2 -->|hook| IPC
    AM1 -->|hook| IPC

    IPC --> AS
    AS -->|"agent_id=OxA1b2<br/>agent_id=OxC3d4"| P1
    AS -->|"agent_id=OxE5f6"| P2

    P1 -.->|reads| F1["~/.claude/.../*.jsonl<br/>(open file handles per agent_id)"]
    P2 -.->|reads| F2["~/.amp/sessions/*.jsonl"]

    style daemon fill:#0f3460,color:#e0e0e0
    style procs fill:#1a6b3c,color:#e0e0e0
```

---

## 12. Failure Recovery: Adapter Crash Mid-Session

```mermaid
sequenceDiagram
    participant Hook as ox CLI (hook)
    participant Daemon as Daemon
    participant Sup as AdapterSupervisor
    participant A as ox-adapter-claude-code<br/>(--serve, pid 1234)

    Note over A: Adapter crashes (OOM, panic)

    A->>Sup: pipe EOF (process exited)
    Sup->>Sup: detect crash<br/>respawn count: 1/3
    Sup->>Sup: read last offset from disk: 4096

    Note over Hook: Next hook call arrives

    Hook->>Daemon: IPC: adapter.read {agent_id: "OxA1b2"}
    Daemon->>Sup: route to OxA1b2

    Sup->>Sup: respawn: ox-adapter-claude-code --serve (pid 1237)
    Sup->>A: find-session {agent_id: "OxA1b2"}
    A-->>Sup: {session_file: "...", offset: 0}
    Sup->>Sup: override offset to last known: 4096

    Sup->>A: read-from-offset {offset: 4096}
    A-->>Sup: {entries: [...], new_offset: 5120}

    Sup-->>Daemon: entries captured
    Daemon-->>Hook: success, new_offset: 5120

    Note over Hook: No data lost. Session continues.
```
