---
paths:
  - "internal/session/**"
  - "internal/daemon/**"
  - "internal/lfs/**"
  - "internal/gitutil/**"
  - "cmd/ox/**"
  - "cmd/ox-adapter-*/**"
  - "pkg/adapter*/**"
---

# Mandatory session redaction boundary

Every session recording mechanism MUST pass through the canonical session
redaction boundary before persisting or publishing session content. This includes
live adapter capture, incremental polling, stop/finalize catch-up, session import,
`capture-prior`, recovered history, reconstruction, retries, and remote publication.
Imported sessions have exactly the same protection as new recordings.

- **Write boundary:** Use the canonical guarded session writers. A custom JSON
  encoder, generic file writer, raw-map method, stream/snapshot writer, or metadata
  field must never provide an alternate route around redaction. Nested content,
  tool arguments, tool output, headers, and footers are untrusted data too.
- **Publication boundary:** Revalidate the exact bytes being committed or uploaded,
  including older/imported artifacts. A successful earlier scan is not proof about
  subsequently changed bytes. Missing or invalid rules, redaction failures, and
  unscannable session artifacts hold publication; they do not fall back to raw data.
  No override, environment variable, or import flag may publish a detected secret.
- **Interfaces:** Keep untrusted input distinct from guarded output. The component
  owning persistence must own redaction; callers cannot supply an optional
  redactor, mark arbitrary bytes trusted, or select a raw encoder. Architectural
  tests must reject a new persistence/publication bypass, not merely document it.
- **Shared adapter contract:** Test redaction once at the normalized session
  boundary shared by adapters. Synthetic protocol entries must exercise user,
  assistant, system, tool input/output, and nested data through production live,
  stored, imported, and reconstructed session writers. Assert that secrets are
  absent while benign turns, tool context, and entry counts survive. Keep native
  format fidelity in existing adapter parser tests; adding an adapter must not
  require copying the redaction suite. Guard persistence centrally so adapter
  callers cannot bypass it. An unavailable external adapter remains blocked;
  shared boundary coverage does not prove support for its native format.
- **Detector updates:** Gitleaks is an explicit exception to ordinary conservative
  dependency updates: automatically discover and incorporate its latest official
  upstream rules at publication/import, rather than waiting for an ox release.
  Retain and verify the complete offline fallback through the catalog refresh
  tool and regression tests. Never freeze a copied subset or silently weaken
  detection semantics. Document deliberate stricter session policies, such as
  ignoring source-repository allowlists while respecting detector entropy.
  Publication, including imported sessions, must establish current upstream
  default-branch rule freshness before sending content and fail closed when that
  cannot be established. Local capture and import previews use the bundled catalog offline. Document this
  policy beside the dependency and refresh code. A refresh tool alone is not a
  scheduled update service; do not claim scheduled jobs that do not exist.
- **Regression discipline:** Include imports, chunk boundaries and restart/recovery,
  command-output correlation, nested tool data, broken/missing rules, and attempted
  override. Use synthetic credentials only. Never print detected secret values in
  failure output, attach raw session/Tilt logs, or read a real user's recordings to
  construct a fixture. Acceptance prose describes intent; executable integration
  tests provide the evidence.

This is a code and interface invariant, not an OS sandbox. Pattern-based detection
cannot promise recognition of every arbitrary unknown string; it must not be
possible for ordinary capture/import code to opt out of the required detector.
