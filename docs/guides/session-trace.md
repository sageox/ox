# Local Claude Code trace capture (experimental)

The trace receiver captures Claude Code execution spans and log events on your
machine. Recordings started while opted in attach scrubbed traces when the
recording is published. Ordinary session recording and publishing keep their
existing settings; enabling the receiver does not itself start a recording.

`FEATURE_TRACE=1` exposes `ox session trace` commands. `enable` saves a local
receiver opt-in and starts it. The Claude Code SessionStart hook restarts the
receiver while opted in, even if the hook does not inherit `FEATURE_TRACE`.
Use `disable` to stop capture; unsetting the feature flag only hides the commands.

Claude Code export is a separate setting. `enable` never edits Claude Code
settings or project files. Launch Claude with environment variables to send
traces to the receiver.

## Try it locally

Use a scratch directory for implementation testing. The subshell limits these
environment variables to this test; the receiver opt-in persists until disabled.

```sh
(
  export FEATURE_TRACE=1
  export CLAUDE_CODE_ENABLE_TELEMETRY=1
  export CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1
  export OTEL_TRACES_EXPORTER=otlp
  export OTEL_LOGS_EXPORTER=otlp
  export OTEL_METRICS_EXPORTER=none
  export OTEL_EXPORTER_OTLP_PROTOCOL=http/json
  export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:14318
  export OTEL_LOG_USER_PROMPTS=0
  export OTEL_LOG_ASSISTANT_RESPONSES=0
  export OTEL_LOG_TOOL_DETAILS=0
  export OTEL_LOG_TOOL_CONTENT=0
  export OTEL_LOG_RAW_API_BODIES=0

  ox session trace enable
  # Confirm receiver listening and local exporter configuration ready.
  claude
  ox session trace status
  ox session trace disable
)
```

Start the receiver before Claude to avoid the receiver-startup race. A hook
restart waits briefly for readiness, but cannot guarantee delivery of telemetry
emitted earlier in Claude's startup. Exporter failures, process crashes, and
delayed batches can still lose data; capture is best-effort.

`enable --port <port>` selects another loopback port. Use that port in Claude's
endpoint too. Disable the current receiver before changing ports. A port
occupied by an unrelated process is reported; ox never terminates that process.

## Inspect and stop

```sh
FEATURE_TRACE=1 ox session trace status --json
FEATURE_TRACE=1 ox session trace disable
# Delete the local capture as well:
FEATURE_TRACE=1 ox session trace disable --purge
```

Status reports the receiver, local storage, last receipt, and the exporter
configuration visible to the current ox process. It cannot inspect a running
Claude process or prove that launcher or server policy did not override settings.
Claude removes `OTEL_*` variables from hooks and tool subprocesses, so run status
from the launch shell. Actual receipt of traces is the end-to-end check.

With the feature flag and saved opt-in, `ox doctor` checks the receiver, local
exporter configuration, and storage use. It automatically restarts a missing
receiver but does not write Claude settings. The hidden `ox session trace serve`
command runs in the foreground for diagnosis and prints receipt summaries to
stderr. Payloads and credentials are never included in those summaries.

## Local storage and lifetime

- Spool: `paths.CacheDir()/trace/spool/<Claude-session-UUID>/` with `traces.jsonl`,
  `logs.jsonl`, `receipts.jsonl`, and `index.json`. Missing or invalid session IDs
  are isolated under `_unattributed`.
- Receiver state and locks: `paths.StateDir()/trace/`.
- Receiver log: `paths.TempDir()/trace.log`.
- The receiver exits after two hours without accepted uploads. The next opted-in
  Claude SessionStart hook restarts it.
- Retention removes folders after 14 days without receipts, except when an
  unfinished recording still references them. Unreadable reference state prevents
  pruning. Retention runs at startup, daily, and when requesting status.

Reference protection uses the recordings present when retention scans. Resuming
an old Claude session after its traces have expired does not recover them; an
older spool with no pending recording may also expire during that resume.

Local files are private to your OS account. Captured payloads are stored as
received and can contain identity attributes. Content capture should remain off;
status warns when locally visible content switches are enabled.

## Recording attachments

An opted-in Claude recording snapshots spool positions at start, pause, resume,
stop, and each new native session ID. Finalization selects only those byte ranges.
Two recordings within one Claude session therefore get separate attachments;
paused bytes and later arrivals are excluded. A retry uses the original stop
boundary. Unknown boundaries omit the affected range instead of widening it.
A raw file without a completed stop boundary omits traces: its header may predate
pauses, so recovery cannot safely infer the missing history. Stale-marker and
orphan recovery preserve an already recorded stop offset; they never use the
current spool size as the end of an earlier recording.

Finalization writes `trace-spans.jsonl.gz` and `trace-events.jsonl.gz` to the
Ledger's local session cache. It removes `user.email`, `user.id`,
`user.account_id`, `user.account_uuid`, and `organization.id` attributes. This
identity scrub is not a general content or secret redactor: keep content export
disabled. The original local spool is unchanged.

The files upload directly from cache to LFS. Only confirmed pointer files enter
the Ledger's git path. A commit guard rejects real content under LFS artifact
names unless the manifest explicitly registers git storage. `meta.json` records
the selected ranges, counts, scrub totals, stop time, and available trace metadata.
Unavailable observations, including readiness before the first prompt, remain
unknown. Unterminated final records are omitted and counted; late bytes visible
at materialization are counted without waiting for more exports.

Explicit stop, SessionEnd, and orphan recovery use the same materialization step.
Trace processing or upload errors are logged and do not prevent the ordinary
recording upload. If an ordinary recording upload fails, finalization keeps local
content for retry and defers the Git commit until LFS confirms the upload.
Recordings started before opt-in receive no trace attachment.

Only OTLP HTTP JSON is supported, optionally gzip-compressed. Requests are limited
to 8 MiB on the wire and after decompression. Multi-session exports are split by
session ID while preserving resource and scope metadata. Single-session exports
retain their original bytes with a trailing newline.
