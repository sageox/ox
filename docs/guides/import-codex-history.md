# Import local Codex history

`ox session import --agent codex` reviews local Codex conversations and publishes selected history to the current repository's Ledger. This pilot feature is off by default. Enabling it does not authorize an upload.

Start from the repository you want to recover:

```sh
ox doctor --check
ox session import --agent codex --dry-run --json
ox session import --agent codex
```

The preview shows the destination team and repository audience, original dates, source size, title, redaction count, and eligibility. SageOx retains a copy; the native Codex files remain untouched. Preview does not upload content, start a daemon, repair the Ledger, or call an LLM.

By default, discovery includes the last 30 days of activity in `$CODEX_HOME/sessions` and `$CODEX_HOME/archived_sessions`, falling back to `~/.codex`. Verified worktrees of this repository are included. Internal worker conversations and unrelated repositories are excluded.

Choose older or specific history with:

```sh
ox session import --agent codex --all-history
ox session import --agent codex --since 2026-08-01
ox session import --agent codex --session <native-session-id>
```

Repeat `--session` to select several conversations. Interactive review asks for each conversation's complete native ID. For unattended use, `--yes` requires an explicit `--session`, `--since`, or `--all-history` selection. It never selects uncertain history.

| Status | Meaning |
| --- | --- |
| `ready` | Source identity and privacy records permit this import. |
| `already_uploaded` | Coverage exists; application verifies fresh remote metadata and the referenced transcript blob. |
| `active` | The source may still be recording. Finish the turn and retry. |
| `uncertain` | Legacy overlap, ancestry, or partial coverage needs individual review. |
| `excluded` | Known pause, abort, deletion, or another exclusion blocks import. |
| `failed` | Parsing, redaction policy, source consistency, or destination verification failed. |

Known privacy exclusions cannot be overridden by confirming an uncertain candidate. Partial coverage or conflicting generations can remain pending after review; the importer does not guess which bytes are safe. Missing content behind an existing upload receipt is a repair case, not permission to recreate a deleted conversation.

An import validates a fixed source snapshot, applies built-in and repository redaction rules, and retains a local journal until publication and registration can be retried safely. Invalid policies or malformed records block completion. Files that grow, shrink, or are replaced need a fresh preview.

Rerun the same selection after interruption. Stable native provenance and the journal retain the conversation's identity. When a previously imported session resumes, the importer can replace that same conversation with its complete continuation only after verifying the old native prefix and its remote transcript. Changed prefixes, uncertain ancestry, or exclusions remain blocked; stale summaries and layer receipts are invalidated for a verified continuation. Upload is independent of summarization: the redacted transcript can be published while the summary remains pending. Indexing and required layers retry until the server proves they are present; a notification response alone is not proof.

If doctor reports Ledger conflicts or staged deletions, preserve its worktree, index, stashes, and unpushed commits before repair. Do not commit a damaged index wholesale. The preview remains observational even when repair is needed.

Codex summary workers require `codex exec` with `--sandbox read-only`, `--ephemeral`, stdin prompts, and the `-c features.hooks=false` configuration override. An unsupported worker invocation reports a retryable summary failure; raw session publication remains independent. Upgrade Codex when the installed CLI rejects these options.
