-- Seed data for the goose adapter fixture database.
--
-- content_json shapes below are copied from real rows in
-- ~/.local/share/goose/sessions/sessions.db (captured 2026-08-09), with the
-- user's actual conversation text replaced by synthesized, innocuous
-- placeholders. The one exception is the literal string
-- "(no output)\n\nCommand exited with code 1" in the fx_fail1 tool response:
-- that text is Goose's own generated boilerplate for any failed command with
-- no stdout, not user conversation content, and is kept verbatim because it
-- is the exact evidence for the bug this fixture exists to catch (a failed
-- tool response whose outer toolResult.status stays "success" while the real
-- failure is nested at toolResult.value.isError).
INSERT INTO sessions (id, working_dir, updated_at, provider_name, model_config_json)
VALUES ('fx_session_1', '/repo/fixture', '2026-06-02 01:00:00', 'anthropic', '{"model":"claude-3-5-sonnet-20241022"}');

-- user turn
INSERT INTO messages (session_id, role, content_json, created_timestamp) VALUES (
    'fx_session_1', 'user',
    '[{"type":"text","text":"list the files in this directory"}]',
    1780378900
);

-- assistant replies with text, then issues a tool call. toolRequest carries
-- an unrecognized "_meta" field at the block level, mirroring real rows —
-- the adapter must ignore unknown fields rather than fail to parse.
INSERT INTO messages (session_id, role, content_json, created_timestamp) VALUES (
    'fx_session_1', 'assistant',
    '[{"type":"text","text":"Ill list the files."},{"type":"toolRequest","id":"fx_ok1","toolCall":{"status":"success","value":{"name":"developer__shell","arguments":{"command":"ls"}}},"_meta":{"goose_extension":"developer"}}]',
    1780378905
);

-- successful tool response: toolResponse always arrives on role "user" in
-- real Goose data, and the payload is nested at toolResult.value.content.
INSERT INTO messages (session_id, role, content_json, created_timestamp) VALUES (
    'fx_session_1', 'user',
    '[{"type":"toolResponse","id":"fx_ok1","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"README.md\nMakefile"}],"isError":false}}}]',
    1780378906
);

-- assistant issues a command that will fail
INSERT INTO messages (session_id, role, content_json, created_timestamp) VALUES (
    'fx_session_1', 'assistant',
    '[{"type":"toolRequest","id":"fx_fail1","toolCall":{"status":"success","value":{"name":"developer__shell","arguments":{"command":"exit 1"}}}}]',
    1780378950
);

-- FAILING tool response, real shape: outer toolResult.status is "success"
-- but toolResult.value.isError is true. This is the row Finding 1 covers.
INSERT INTO messages (session_id, role, content_json, created_timestamp) VALUES (
    'fx_session_1', 'user',
    '[{"type":"toolResponse","id":"fx_fail1","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"(no output)\n\nCommand exited with code 1","annotations":{"priority":0.0}}],"structuredContent":{"stdout":"","stderr":"","exit_code":1},"isError":true}}}]',
    1780378951
);

-- assistant issues a command whose output arrives as multiple text blocks
INSERT INTO messages (session_id, role, content_json, created_timestamp) VALUES (
    'fx_session_1', 'assistant',
    '[{"type":"toolRequest","id":"fx_multi1","toolCall":{"status":"success","value":{"name":"developer__shell","arguments":{"command":"find . -name *.go"}}}}]',
    1780378980
);

-- successful tool response split across two text blocks (real Goose
-- responses do this for a command's stdout plus a separate truncation
-- notice) — the adapter must join them, not keep only the first.
INSERT INTO messages (session_id, role, content_json, created_timestamp) VALUES (
    'fx_session_1', 'user',
    '[{"type":"toolResponse","id":"fx_multi1","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"./main.go\n./util.go"},{"type":"text","text":"[Output truncated for fixture brevity]"}],"isError":false}}}]',
    1780378981
);

-- thinking and image blocks: dropped by design, not part of the record
INSERT INTO messages (session_id, role, content_json, created_timestamp) VALUES (
    'fx_session_1', 'assistant',
    '[{"type":"thinking","thinking":"considering the next step"},{"type":"image","data":"base64placeholder"}]',
    1780379000
);
