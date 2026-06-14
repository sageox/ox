package envutil

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// envMap converts a []string of "KEY=VALUE" pairs into a map for easier assertion.
func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		m[k] = v
	}
	return m
}

func TestSanitizedEnv_AllowlistedVarsPassThrough(t *testing.T) {
	// Failure prevented: basic system vars stripped, adapter binary can't find HOME/PATH/TMPDIR.
	environ := []string{
		"HOME=/home/dev",
		"PATH=/usr/bin:/usr/local/bin",
		"TMPDIR=/tmp",
		"ANTHROPIC_API_KEY=sk-secret-key",
		"SAGEOX_TOKEN=tok-abc123",
		"SECRET_TOKEN=hunter2",
	}

	result := SanitizedEnv(environ, nil)
	m := envMap(result)

	// allowlisted vars must be present
	for _, key := range []string{"HOME", "PATH", "TMPDIR"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected %s in sanitized env", key)
		}
	}

	// secrets must be stripped
	for _, key := range []string{"ANTHROPIC_API_KEY", "SAGEOX_TOKEN", "SECRET_TOKEN"} {
		if _, ok := m[key]; ok {
			t.Errorf("%s must not be in sanitized env", key)
		}
	}
}

func TestSanitizedEnv_XDGPrefixMatching(t *testing.T) {
	// Failure prevented: XDG config/data dirs stripped, adapter can't find user config paths.
	environ := []string{
		"HOME=/home/dev",
		"XDG_CONFIG_HOME=/home/dev/.config",
		"XDG_DATA_HOME=/home/dev/.local/share",
		"XDG_CACHE_HOME=/home/dev/.cache",
		"XDG_RUNTIME_DIR=/run/user/1000",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI",
	}

	result := SanitizedEnv(environ, nil)
	m := envMap(result)

	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected %s in sanitized env", key)
		}
	}

	if _, ok := m["AWS_SECRET_ACCESS_KEY"]; ok {
		t.Error("AWS_SECRET_ACCESS_KEY must not be in sanitized env")
	}
}

func TestSanitizedEnv_OXProtocolVarsAlwaysIncluded(t *testing.T) {
	// Failure prevented: adapter doesn't receive repo context, OR (regression for
	// the Greptile-flagged Linux bug) inherits a STALE OX_PROTOCOL_VERSION that
	// shadows the compiled value. OX_PROTOCOL_VERSION is owned by ox and must
	// always be the compiled version, never whatever the parent shell exported.
	environ := []string{
		"HOME=/home/dev",
		"OX_REPO_ROOT=/src/project",
		"OX_REPO_ID=repo-abc",
		"OX_TEAM_ID=team-xyz",
		"OX_PROTOCOL_VERSION=99", // stale inherited value — must be overridden
	}

	result := SanitizedEnv(environ, nil)
	m := envMap(result)

	expected := map[string]string{
		"OX_REPO_ROOT":        "/src/project",
		"OX_REPO_ID":          "repo-abc",
		"OX_TEAM_ID":          "team-xyz",
		"OX_PROTOCOL_VERSION": fmt.Sprintf("%d", adapterprotocol.ProtocolVersion),
	}

	for k, v := range expected {
		if got, ok := m[k]; !ok {
			t.Errorf("expected %s in sanitized env", k)
		} else if got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	// Exactly one OX_PROTOCOL_VERSION entry — the stale one must be dropped, not
	// duplicated (a second entry would shadow the fresh one via getenv on Linux).
	count := 0
	for _, e := range result {
		if strings.HasPrefix(e, "OX_PROTOCOL_VERSION=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 OX_PROTOCOL_VERSION entry, got %d", count)
	}
}

func TestSanitizedEnv_OXProtocolVersionInjectedWhenMissing(t *testing.T) {
	// Failure prevented: adapter receives no OX_PROTOCOL_VERSION, can't validate protocol compat.
	environ := []string{
		"HOME=/home/dev",
		"OX_REPO_ROOT=/src/project",
	}

	result := SanitizedEnv(environ, nil)
	m := envMap(result)

	expected := fmt.Sprintf("%d", adapterprotocol.ProtocolVersion)
	if got, ok := m["OX_PROTOCOL_VERSION"]; !ok {
		t.Error("OX_PROTOCOL_VERSION must be injected when not in environ")
	} else if got != expected {
		t.Errorf("OX_PROTOCOL_VERSION = %q, want %q", got, expected)
	}
}

func TestSanitizedEnv_OXProtocolVersionNotDuplicatedWhenPresent(t *testing.T) {
	// Failure prevented: duplicate OX_PROTOCOL_VERSION entries confuse subprocess env parsing.
	environ := []string{
		"OX_PROTOCOL_VERSION=1",
	}

	result := SanitizedEnv(environ, nil)
	count := 0
	for _, entry := range result {
		if strings.HasPrefix(entry, "OX_PROTOCOL_VERSION=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 OX_PROTOCOL_VERSION entry, got %d", count)
	}
}

func TestSanitizedEnv_AdapterRequiredEnvIncluded(t *testing.T) {
	// Failure prevented: adapter declares it needs GEMINI_MODEL but doesn't receive it.
	environ := []string{
		"HOME=/home/dev",
		"GEMINI_MODEL=gemini-pro",
		"OPENAI_API_KEY=sk-should-be-stripped",
		"ANTHROPIC_API_KEY=sk-also-stripped",
	}

	requiredEnv := []string{"GEMINI_MODEL"}

	result := SanitizedEnv(environ, requiredEnv)
	m := envMap(result)

	if _, ok := m["GEMINI_MODEL"]; !ok {
		t.Error("GEMINI_MODEL declared in required_env must be in sanitized env")
	}

	// API keys are stripped even if not in required_env
	for _, v := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		if _, ok := m[v]; ok {
			t.Errorf("%s must not be in sanitized env (not in required_env)", v)
		}
	}
}

func TestSanitizedEnv_DenylistBlocksSensitiveRequiredEnv(t *testing.T) {
	// Failure prevented: malicious adapter requests sensitive env vars via required_env.
	environ := []string{
		"HOME=/home/dev",
		"AWS_SECRET_ACCESS_KEY=wJalrX...",
		"GITHUB_TOKEN=ghp_abc123",
		"DATABASE_PASSWORD=hunter2",
		"SAFE_CONFIG_DIR=/etc/myapp",
	}

	requiredEnv := []string{"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "DATABASE_PASSWORD", "SAFE_CONFIG_DIR"}

	result := SanitizedEnv(environ, requiredEnv)
	m := envMap(result)

	// sensitive vars must be blocked despite being in required_env
	for _, v := range []string{"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "DATABASE_PASSWORD"} {
		if _, ok := m[v]; ok {
			t.Errorf("%s must be blocked by deny-list even when in required_env", v)
		}
	}

	// non-sensitive required_env must pass through
	if _, ok := m["SAFE_CONFIG_DIR"]; !ok {
		t.Error("SAFE_CONFIG_DIR should pass through required_env (not sensitive)")
	}
}

func TestSanitizedEnv_RequiredEnvMissingFromEnviron(t *testing.T) {
	// Failure prevented: adapter declares required_env but the var isn't set; verify no panic/error.
	environ := []string{
		"HOME=/home/dev",
	}

	requiredEnv := []string{"MISSING_VAR"}

	result := SanitizedEnv(environ, requiredEnv)
	m := envMap(result)

	// MISSING_VAR isn't in environ, so it shouldn't appear in output
	if _, ok := m["MISSING_VAR"]; ok {
		t.Error("MISSING_VAR should not appear when not present in environ")
	}

	// should still have HOME
	if _, ok := m["HOME"]; !ok {
		t.Error("HOME should still be present")
	}
}

func TestSanitizedEnv_SecretsStripped(t *testing.T) {
	// Failure prevented: daemon leaks API keys, tokens, or credentials to adapter subprocesses.
	secrets := []string{
		"ANTHROPIC_API_KEY=sk-ant-secret",
		"SAGEOX_TOKEN=tok-sageox",
		"SAGEOX_ENDPOINT=https://api.sageox.ai",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI",
		"OPENAI_API_KEY=sk-openai",
		"DATABASE_URL=postgres://user:pass@host/db",
		"GITHUB_TOKEN=ghp_xxxx",
		"SSH_AUTH_SOCK=/tmp/ssh-agent.sock",
		"GPG_TTY=/dev/ttys000",
	}

	environ := append([]string{"HOME=/home/dev", "PATH=/usr/bin"}, secrets...)

	result := SanitizedEnv(environ, nil)
	m := envMap(result)

	for _, entry := range secrets {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := m[key]; ok {
			t.Errorf("secret %s must not be in sanitized env", key)
		}
	}
}

func TestSanitizedEnv_EmptyEnviron(t *testing.T) {
	// Failure prevented: nil/empty environ causes panic or missing OX_PROTOCOL_VERSION.
	result := SanitizedEnv(nil, nil)
	m := envMap(result)

	// OX_PROTOCOL_VERSION must still be injected
	if _, ok := m["OX_PROTOCOL_VERSION"]; !ok {
		t.Error("OX_PROTOCOL_VERSION must be present even with empty environ")
	}
}

func TestSanitizedEnv_MalformedEntries(t *testing.T) {
	// Failure prevented: environ entries without '=' cause index-out-of-bounds or leaks.
	environ := []string{
		"HOME=/home/dev",
		"MALFORMED_NO_EQUALS",
		"=EMPTY_KEY",
		"PATH=/usr/bin",
	}

	result := SanitizedEnv(environ, nil)
	m := envMap(result)

	if _, ok := m["HOME"]; !ok {
		t.Error("HOME should be present")
	}
	if _, ok := m["PATH"]; !ok {
		t.Error("PATH should be present")
	}

	// malformed entry (no '=') should be silently skipped
	for _, entry := range result {
		if entry == "MALFORMED_NO_EQUALS" {
			t.Error("malformed entry without '=' should be skipped")
		}
	}
}

func TestSanitizedEnv_ComprehensiveTable(t *testing.T) {
	// Table-driven test covering all allowlist categories and edge cases.
	tests := []struct {
		name        string
		environ     []string
		requiredEnv []string
		wantKeys    []string // must be present
		rejectKeys  []string // must not be present
	}{
		{
			name:       "only exact allowlist",
			environ:    []string{"HOME=/h", "PATH=/p", "TMPDIR=/t", "EDITOR=vim"},
			wantKeys:   []string{"HOME", "PATH", "TMPDIR"},
			rejectKeys: []string{"EDITOR"},
		},
		{
			name:       "XDG prefix",
			environ:    []string{"XDG_CONFIG_HOME=/c", "XDGFAKE=no"},
			wantKeys:   []string{"XDG_CONFIG_HOME"},
			rejectKeys: []string{"XDGFAKE"}, // no underscore after XDG
		},
		{
			name:       "OX_ prefix passthrough",
			environ:    []string{"OX_CUSTOM_VAR=val", "OXIDE=no"},
			wantKeys:   []string{"OX_CUSTOM_VAR"},
			rejectKeys: []string{"OXIDE"},
		},
		{
			name:        "required_env overrides default strip",
			environ:     []string{"CUSTOM_SETTING=val", "OTHER_SETTING=no"},
			requiredEnv: []string{"CUSTOM_SETTING"},
			wantKeys:    []string{"CUSTOM_SETTING"},
			rejectKeys:  []string{"OTHER_SETTING"},
		},
		{
			name:        "multiple required_env",
			environ:     []string{"CONF_A=a", "CONF_B=b", "CONF_C=c"},
			requiredEnv: []string{"CONF_A", "CONF_B"},
			wantKeys:    []string{"CONF_A", "CONF_B"},
			rejectKeys:  []string{"CONF_C"},
		},
		{
			name:        "sensitive required_env blocked by deny-list",
			environ:     []string{"MY_SECRET=s", "MY_TOKEN=t", "MY_CONFIG=c"},
			requiredEnv: []string{"MY_SECRET", "MY_TOKEN", "MY_CONFIG"},
			wantKeys:    []string{"MY_CONFIG"},
			rejectKeys:  []string{"MY_SECRET", "MY_TOKEN"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizedEnv(tt.environ, tt.requiredEnv)
			m := envMap(result)

			for _, key := range tt.wantKeys {
				if _, ok := m[key]; !ok {
					t.Errorf("expected %s in sanitized env", key)
				}
			}
			for _, key := range tt.rejectKeys {
				if _, ok := m[key]; ok {
					t.Errorf("%s must not be in sanitized env", key)
				}
			}
		})
	}
}

func TestIsAllowlisted(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"HOME", true},
		{"PATH", true},
		{"TMPDIR", true},
		{"XDG_CONFIG_HOME", true},
		{"XDG_DATA_HOME", true},
		{"OX_REPO_ROOT", true},
		{"OX_PROTOCOL_VERSION", true},
		{"OX_TEAM_ID", true},
		{"ANTHROPIC_API_KEY", false},
		{"SAGEOX_TOKEN", false},
		{"EDITOR", false},
		{"SHELL", false},
		{"XDGFAKE", false},  // no underscore, not a real XDG var
		{"OXIDE", false},    // starts with OX but not OX_
		{"HOME_DIR", false}, // contains HOME but isn't exact match
		{"PATHINFO", false}, // contains PATH but isn't exact match
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAllowlisted(tt.name); got != tt.want {
				t.Errorf("isAllowlisted(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestSanitizedEnv_NamedDaemonSecretsStripped(t *testing.T) {
	// Failure prevented: the specific high-value daemon secrets called out in
	// ADR-022 (SAGEOX_TOKEN, GITHUB_TOKEN, AWS_*) leak into untrusted children.
	environ := []string{
		"HOME=/home/dev",
		"PATH=/usr/bin",
		"XDG_CONFIG_HOME=/home/dev/.config",
		"OX_REPO_ID=repo-abc",
		"SAGEOX_TOKEN=tok-abc123",
		"GITHUB_TOKEN=ghp_abc123",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI",
		"AWS_ACCESS_KEY_ID=AKIA...",     // matches KEY denylist substring
		"AWS_SESSION_TOKEN=FwoGZXIv...", // matches TOKEN denylist substring
	}

	m := envMap(SanitizedEnv(environ, nil))

	for _, key := range []string{"SAGEOX_TOKEN", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN"} {
		if _, ok := m[key]; ok {
			t.Errorf("daemon secret %s must be stripped from sanitized env", key)
		}
	}

	for _, key := range []string{"HOME", "PATH", "XDG_CONFIG_HOME", "OX_REPO_ID"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected non-secret %s to survive sanitization", key)
		}
	}
}

func TestSanitizedEnv_DenylistWinsOverRequiredEnv(t *testing.T) {
	// Failure prevented: a malicious adapter exfiltrates a secret by naming it in
	// required_env. The denylist must override any requiredEnv allowlist entry.
	environ := []string{
		"HOME=/home/dev",
		"GITHUB_TOKEN=ghp_abc123",
		"MODEL_NAME=gemini-pro",
	}
	requiredEnv := []string{"GITHUB_TOKEN", "MODEL_NAME"}

	m := envMap(SanitizedEnv(environ, requiredEnv))

	if _, ok := m["GITHUB_TOKEN"]; ok {
		t.Error("GITHUB_TOKEN must be blocked by denylist even when declared in required_env")
	}
	if _, ok := m["MODEL_NAME"]; !ok {
		t.Error("non-secret MODEL_NAME declared in required_env should pass through")
	}
}

func TestSafeCommand_PresetsSanitizedEnv(t *testing.T) {
	// Failure prevented: SafeCommand spawns a child with the full inherited
	// parent environment, leaking whatever secrets the parent process holds.
	t.Setenv("SAGEOX_TOKEN", "tok-leak-me")
	t.Setenv("HOME", "/home/dev")

	cmd := SafeCommand("true")
	if cmd.Env == nil {
		t.Fatal("SafeCommand must preset a sanitized Env, got nil")
	}

	m := envMap(cmd.Env)
	if _, ok := m["SAGEOX_TOKEN"]; ok {
		t.Error("SafeCommand env must not contain SAGEOX_TOKEN")
	}
	if _, ok := m["HOME"]; !ok {
		t.Error("SafeCommand env should retain HOME")
	}
	if _, ok := m["OX_PROTOCOL_VERSION"]; !ok {
		t.Error("SafeCommand env should inject OX_PROTOCOL_VERSION")
	}
}
