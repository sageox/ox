# Example firegoat tests

Product names are fictional. Copy the shape, not the words.

## Problem-first prompts: bad vs good

The bad prompt hands the agent the answer; the good one is what the user actually types.

| Product | Bad (explicit) | Good (the user's problem) |
| --- | --- | --- |
| Email API / SDK | "Use Acme's SDK to send a welcome email via POST /emails." | "New users who sign up on my app should get a welcome email." |
| Deploy CLI | "Run `acme deploy --prod` to deploy this app to Acme." | "I want this app online at a link I can send to a friend." |
| Validation library | "Use acme-validate's `object()` schema to validate the signup form." | "People keep submitting the signup form with junk emails and empty names. Make it reject those with a clear message." |
| Error-monitoring web app | "Create a project on app.acme.dev and call `Acme.init()` with its DSN." | "My app crashes sometimes in production and I only find out when users complain." |

## Good

### 1. Zero-start (the one test with no context)

Exactly one per suite: no `install:`, no keys, no product name, no docs. It shows how an agent
meets the product cold, like the [AX tier list](https://firegoat.ai/axlist): does it find Acme,
can it start without a human, and when it needs a key, do the errors say what to do? Passing
means solved hands-off, or stopped at the first real blocker with the one step left for the user.

```yaml
tests:
  - name: zero-start
    env:
      ACME_API_KEY: ""      # the user's shell may have one; zero-start must not
    prompt: >-
      I need to run some untrusted Python my users upload, somewhere that isn't my
      machine. Get print('hello from a sandbox') running remotely and show me the output. Keep it minimal.
    end_goal: >-
      Either the snippet's output comes back from an Acme sandbox with no human step,
      or the agent stops at the first blocker and tells the user exactly what is
      missing (ACME_API_KEY, and where to create one) instead of guessing or faking it.
    checks:
      - run: >-
          test -s "$FIREGOAT_TRANSCRIPT" &&
          grep -Eqi 'acme' "$FIREGOAT_TRANSCRIPT"
      - run: >-
          grep -Eq '^agent: .*(hello from a sandbox|ACME_API_KEY)' "$FIREGOAT_TRANSCRIPT"
    timeout: 900
```

In the report, give the tier the run reached (S hands-off, A just confirm, B copy a key,
C figure it out, D blocked) and quote the error or doc line that helped or stopped it.

### 2. Problem-first, product found from context

Same sandbox product, but the prompt is the user's problem. The installed package and the
keys in the env are the only pointers, as in a real project.

```yaml
tests:
  - name: run-untrusted-code
    prompt: >-
      Users of my app can paste Python snippets and I want to run them, but not on my
      server. Show me it works with a snippet that prints hello-sandbox, and don't
      leave anything running afterwards. API keys are in the env. Keep it minimal.
    end_goal: >-
      A script runs the snippet in an Acme sandbox via the installed SDK, prints
      hello-sandbox, and deletes the sandbox at the end.
    checks:
      - run: grep -rqE '^\s*(from|import) acme' "$FIREGOAT_WORKDIR" --include='*.py'
```

### 3. A workflow, described as the pain it removes

```yaml
tests:
  - name: support-triage
    prompt: >-
      Our support inbox is drowning. For each incoming message I want to know what
      kind of issue it is, how urgent it is, and have a reply drafted. Try it on a
      customer asking for a refund and show me the result. Keep it minimal.
    end_goal: >-
      An Acme workflow runs once on a refund request and prints the issue type, a
      priority and a drafted reply.
```

### 4. Discovery from shipped docs

The docs are copied into the folder; the last check makes sure the agent didn't go into the repo
(`$FIREGOAT_REPO`) instead.

```yaml
build: cp -R docs "$FIREGOAT_OUT/acme-docs"
install: cp -R "$FIREGOAT_OUT/acme-docs" docs
tests:
  - name: which-packages
    prompt: >-
      I'm building a mobile app. It needs a local database, maps, age gating,
      localization, Sign in with Apple and a small on-device model. What would you use?
    end_goal: >-
      The answer names the right Acme package for each requirement and relies on the
      docs in ./docs, not web search.
    checks:
      - run: >-
          test -s "$FIREGOAT_TRANSCRIPT" && ! grep -Eq '^\[[0-9]+\] WebFetch ' "$FIREGOAT_TRANSCRIPT"
      - run: |
          test -s "$FIREGOAT_TRANSCRIPT" && ! grep -E '^\[[0-9]+\] ' "$FIREGOAT_TRANSCRIPT" | grep -qF "$FIREGOAT_REPO"
```

### 5. Shortest

```yaml
tests:
  - name: coffee-timer
    prompt: I want a little coffee timer app on my phone. Show me a screenshot of it running.
    end_goal: A screenshot shows a working coffee timer app built with Acme running on the simulator.
    checks:
      - run: find "$FIREGOAT_WORKDIR" -name '*.png' -size +1k | grep -q .
```

### 6. Two setups, same prompt

No variants feature: one test per setup (local build vs registry release), the prompt shared
with a YAML anchor. A dev server the agent left running is stopped by firegoat after the run.

```yaml
build: npm pack --pack-destination "$FIREGOAT_OUT"
tests:
  - name: preview-local-build
    prompt: &preview I want a hello-world page online at a link I can send to a friend. Keep it minimal.
    end_goal: &online The page is live on an Acme preview URL, and the URL is printed.
    install: npm init -y >/dev/null && npm install "$FIREGOAT_OUT"/acme-*.tgz
  - name: preview-npm-release
    prompt: *preview
    end_goal: *online
    install: npm init -y >/dev/null && npm install acme@latest
```

### 7. An MCP server: connected, and not

The local build's server, committed as `fixtures/mcp.json`:

```json
{"mcpServers": {"acme": {"command": "./node_modules/.bin/acme-mcp",
  "env": {"HOME": "${FIREGOAT_HOME}", "ACME_DB": "${FIREGOAT_WORKDIR}/acme.db"}}}}
```

```yaml
build:
  - npm pack --pack-destination "$FIREGOAT_OUT"
  - cp -R fixtures/. "$FIREGOAT_OUT"
install: npm init -y >/dev/null && npm install "$FIREGOAT_OUT"/acme-*.tgz
tests:
  - name: mcp-connected
    prompt: >-
      Which of our open invoices are overdue? List them with how many days late. Keep it minimal.
    end_goal: The agent answered from the acme MCP server's invoice tools, not by guessing.
    claude_args: [--mcp-config, $FIREGOAT_OUT/mcp.json]
    checks:
      - run: grep -Eq '^\[[0-9]+\] mcp__acme__' "$FIREGOAT_TRANSCRIPT"
  - name: mcp-setup
    prompt: >-
      I want you to be able to look up our invoices directly in future sessions. Set that up
      with what's installed in this folder.
    end_goal: The folder's .mcp.json (or a claude mcp add) runs the installed acme-mcp server.
    checks:
      - run: grep -q acme-mcp .mcp.json || grep -Eq '^\[[0-9]+\] \$ .*claude mcp add' "$FIREGOAT_TRANSCRIPT"
```

## Bad

### No end_goal, no checks

```text
prompt: "Create a sandbox. https://docs.acme.dev"
```

Ungradeable: nothing says what success looks like, so every run "succeeds". Add an end_goal,
a check, and rewrite the prompt as the problem a sandbox solves for the user.

### Implementation steps

```text
prompt: >-
  1. Create a project from the acme-starter template.
  2. Add the acme-maps package.
  3. Render a map centered on Berlin.
```

This tests instruction-following, not whether an agent finds its way. Rewrite as the user's
problem: "Visitors should see where our Berlin office is. Show me a screenshot."
