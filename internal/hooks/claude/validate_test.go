package claude

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateSettingsBytes exhaustively pins the contract Claude Code's
// parser enforces. Each subtest names the failure mode it prevents in the
// "what breaks without this" form so future maintainers can trace any
// loosening of the validator back to a real consumer rejection.
func TestValidateSettingsBytes(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantErr  bool
		wantPart string // substring required to appear in err.Error()
		// Why this case exists. Not asserted, just documentation.
		why string
	}{
		// --- A. Valid shapes that MUST be accepted ---
		{
			name: "empty input",
			body: "",
			why:  "Files with no content are produced by `touch settings.json` or by tools that pre-create empty files; rejecting them would force a fix on a non-bug.",
		},
		{
			name: "no hooks key",
			body: `{"permissions": {"allow": []}}`,
			why:  "Settings files can carry permissions and other Claude Code settings without any hooks configured. Rejecting would block valid configurations.",
		},
		{
			name: "empty hooks object",
			body: `{"hooks": {}}`,
			why:  "Valid intermediate state when hooks have been intentionally cleared.",
		},
		{
			name: "canonical SessionStart entry",
			body: `{"hooks":{"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":"ox agent prime"}]}]}}`,
			why:  "Exact shape ox emits during init. If the validator rejects this, init is broken.",
		},
		{
			name: "matcher absent (treated as empty)",
			body: `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"foo"}]}]}}`,
			why:  "matcher is optional in Claude Code's parser; absence MUST NOT be rejected.",
		},

		// --- B. Failure modes — these are why the validator exists ---
		{
			name:     "string-format hook value (the legacy bug)",
			body:     `{"hooks": {"PostToolUse": "echo legacy"}}`,
			wantErr:  true,
			wantPart: "bare string",
			why:      "This is the exact failure that caused the 2026-04-30 incident — old ox versions emitted bare-string hooks; current Claude Code rejects the entire settings file. The validator's primary job is catching this.",
		},
		{
			name:     "hooks key not an object",
			body:     `{"hooks": ["not an object"]}`,
			wantErr:  true,
			wantPart: "object keyed by event name",
			why:      "Claude Code expects a map of event→entries. Array-shaped hooks would cause a confusing parse error in the consumer.",
		},
		{
			name:     "entry value not an array",
			body:     `{"hooks":{"PostToolUse":{"matcher":"*"}}}`,
			wantErr:  true,
			wantPart: "must be an array",
			why:      "An object where an array is expected is the second-most common upgrade-broken shape after the bare-string case.",
		},
		{
			name:     "entry missing hooks array",
			body:     `{"hooks":{"PostToolUse":[{"matcher":"*"}]}}`,
			wantErr:  true,
			wantPart: "non-empty array",
			why:      "An entry with no hooks[] is a config error that silently disables the event in Claude Code; we want to surface it.",
		},
		{
			name:     "hook type wrong",
			body:     `{"hooks":{"PostToolUse":[{"matcher":"*","hooks":[{"type":"shell","command":"x"}]}]}}`,
			wantErr:  true,
			wantPart: `must be "command"`,
			why:      "Claude Code only supports type=command today. A typo or future-format value would silently disable the hook.",
		},
		{
			name:     "hook command empty",
			body:     `{"hooks":{"PostToolUse":[{"matcher":"*","hooks":[{"type":"command","command":""}]}]}}`,
			wantErr:  true,
			wantPart: "non-empty string",
			why:      "Empty command would cause Claude Code to log an error per invocation; better to reject at write time.",
		},
		{
			name:     "unknown field on entry",
			body:     `{"hooks":{"PostToolUse":[{"matcher":"*","matchers":["*"],"hooks":[{"type":"command","command":"x"}]}]}}`,
			wantErr:  true,
			wantPart: "unknown field",
			why:      "Catches plural/singular typos like matchers→matcher that would silently disable hooks because the real field defaults to empty.",
		},
		{
			name:    "malformed top-level JSON",
			body:    `{"hooks":`,
			wantErr: true,
			why:     "Truncated writes (no atomic write yet — see ox-dfy4) can leave the file in this state. We want a structured error, not a panic.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.why == "" {
				t.Fatal("test case missing 'why' — every case must document the failure it prevents")
			}
			err := ValidateSettingsBytes([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil. why: %s", tt.why)
				}
				if tt.wantPart != "" && !strings.Contains(err.Error(), tt.wantPart) {
					t.Errorf("err = %q; want substring %q", err.Error(), tt.wantPart)
				}
				// All schema violations must wrap the sentinel so callers
				// can distinguish them from generic JSON parse errors.
				if strings.Contains(tt.name, "malformed") {
					return // top-level parse error, not a schema violation
				}
				if !errors.Is(err, ErrSchemaViolation) {
					t.Errorf("err = %v; want errors.Is(err, ErrSchemaViolation) for schema cases", err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v. why: %s", err, tt.why)
			}
		})
	}
}

// TestValidateSettingsBytes_AcceptsOxInitOutput is the contract test that
// would have caught the 2026-04-30 incident: take whatever ox init / ox
// prime injects today and assert the validator accepts it. If a future
// change to the writer regresses to a shape Claude Code rejects, this
// test fails BEFORE the broken file ever ships to a user.
//
// We don't drive the full init flow here — that lives in
// cmd/ox/init_settings_contract_test.go (out of this package's reach due
// to import cycles). Here we exercise the canonical Settings shape that
// MarshalSettings produces, which is the byte-level contract.
//
// Failure prevented: a refactor to Settings, HookEntry, or
// MarshalSettings that produces output Claude Code rejects.
func TestValidateSettingsBytes_AcceptsOxInitOutput(t *testing.T) {
	settings := &Settings{
		Hooks: map[string][]HookEntry{
			"SessionStart": {{
				Matcher: "*",
				Hooks:   []Hook{{Type: HookType, Command: "ox agent prime"}},
			}},
			"PostToolUse": {{
				Matcher: "Edit",
				Hooks:   []Hook{{Type: HookType, Command: "ox hook post-edit"}},
			}},
		},
	}
	data, err := MarshalSettings(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSettingsBytes(data); err != nil {
		t.Errorf("MarshalSettings produced output the strict validator rejects:\n%s\nerr: %v", data, err)
	}
}
