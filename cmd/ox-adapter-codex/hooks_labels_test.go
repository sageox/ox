package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: fresh and existing installs show anonymous approval rows,
// or refreshing SageOx labels damages another integration's hook settings.
func TestHookLabels_InstallAndRefresh(t *testing.T) {
	for _, scope := range []string{"project", "user"} {
		for _, existing := range []bool{false, true} {
			name := scope + "/fresh"
			if existing {
				name = scope + "/existing"
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("USERPROFILE", home)
				params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: scope}
				path := resolveHooksPath(params.RepoRoot, scope)
				// Include an MCP hook and fields this adapter doesn't interpret.
				custom := json.RawMessage(`{"matcher":"Bash","futureGroupField":true,"hooks":[{"type":"mcp_tool","server":"scanner","tool":"scan","input":{"mode":"strict"},"statusMessage":"Scan changes","timeout":17,"async":true}]}`)
				hooks := map[string]json.RawMessage{"Stop": json.RawMessage("[" + string(custom) + "]")}
				if existing {
					for _, event := range codexHookEvents {
						label := ""
						if event == "SessionStart" {
							label = "Loading SageOx context"
						}
						entry, err := json.Marshal([]codexHookEntry{{Hooks: []codexHook{{Type: "command", Command: hookCommand(event), StatusMessage: label, Timeout: 10}}}})
						require.NoError(t, err)
						hooks[event] = entry
					}
					hooks["Stop"] = json.RawMessage(string(hooks["Stop"])[:len(hooks["Stop"])-1] + "," + string(custom) + "]")
				}
				data, err := json.Marshal(map[string]any{"description": "User-owned hooks", "hooks": hooks})
				require.NoError(t, err)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, data, 0o600))

				wantLabels := map[string]string{
					"SessionStart":     "SageOx: loading context; auto-recording if configured",
					"PreToolUse":       "SageOx: pre-tool hook (currently inactive)",
					"PostToolUse":      "SageOx: capturing activity for session recording",
					"UserPromptSubmit": "SageOx: recalling context and delivering team updates",
					"Stop":             "SageOx: saving turns and plans; publishing drafts if configured",
					"SessionEnd":       "SageOx: finalizing recordings; publishing if configured",
				}
				var first []byte
				for pass := range 2 {
					_, err := handleInstallHooks(params)
					require.NoError(t, err)
					data, err = os.ReadFile(path)
					require.NoError(t, err)
					var got struct {
						Description string
						Hooks       map[string][]json.RawMessage
					}
					require.NoError(t, json.Unmarshal(data, &got))
					assert.Equal(t, "User-owned hooks", got.Description)
					customCount := 0
					for event, label := range wantLabels {
						count := 0
						for _, raw := range got.Hooks[event] {
							var entry codexHookEntry
							require.NoError(t, json.Unmarshal(raw, &entry))
							for _, hook := range entry.Hooks {
								if !isOxCommand(hook.Command) {
									customCount++
									assert.JSONEq(t, string(custom), string(raw))
									continue
								}
								count++
								assert.Equal(t, label, hook.StatusMessage, event)
							}
						}
						assert.Equal(t, 1, count, event)
					}
					assert.Equal(t, 1, customCount, "the unrelated hook must survive refresh")
					if pass == 0 {
						first = data
					} else {
						assert.Equal(t, first, data, "reinstall must be idempotent")
					}
				}
				_, err = handleUninstallHooks(params)
				require.NoError(t, err)
				remaining, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.JSONEq(t, `{"description":"User-owned hooks","hooks":{"Stop":[`+string(custom)+`]}}`, string(remaining),
					"uninstall must also preserve the unrelated handler and its options")
			})
		}
	}
}

// Failure prevented: an otherwise complete six-hook installation never gets
// offered the new labels, particularly when hooks were installed user-wide.
func TestHookLabels_DoctorOffersScopedRefresh(t *testing.T) {
	for _, scope := range []string{"project", "user"} {
		for _, event := range codexHookEvents {
			t.Run(scope+"/"+event, func(t *testing.T) {
				fakeCodexOnPath(t)
				params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: scope}
				_, err := handleInstallHooks(params)
				require.NoError(t, err)
				path := resolveHooksPath(params.RepoRoot, scope)
				hooks, metadata, err := readHooksFile(path)
				require.NoError(t, err)
				hooks[event][0].Hooks[0].StatusMessage = "Old label"
				require.NoError(t, writeHooksFile(path, hooks, metadata, scope))
				before, err := os.ReadFile(path)
				require.NoError(t, err)
				result, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: params.RepoRoot, Scope: scope})
				require.NoError(t, err)
				require.False(t, result.OK)
				slug := "codex:hook-labels-outdated"
				argv := []string{"ox", "integrate", "install", "--codex"}
				if scope == "user" {
					slug += "-user"
					argv = append(argv, "--user")
				}
				require.Len(t, result.Issues, 1)
				assert.Equal(t, slug, result.Issues[0].Slug)
				assert.Equal(t, argv, result.Issues[0].FixArgv)
				assert.True(t, result.Issues[0].FixSafe)
				assert.Contains(t, result.Issues[0].Detail, "review")
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, before, after, "diagnosis must be read-only")
				_, err = handleInstallHooks(params)
				require.NoError(t, err)
				result, err = handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: params.RepoRoot, Scope: scope})
				require.NoError(t, err)
				assert.True(t, result.OK)
				assert.Empty(t, result.Issues)
			})
		}
	}
}

// Malformed user files must not pass diagnosis, and malformed project files
// must produce one parse issue rather than a misleading missing-hooks repair.
func TestHookLabels_DoctorDistinguishesMalformedFromMissing(t *testing.T) {
	for _, scope := range []string{"project", "user"} {
		for _, tc := range []struct {
			name    string
			data    string
			missing bool
			invalid bool
		}{
			{name: "absent", missing: true},
			{name: "empty"},
			{name: "unconfigured", data: `{}`},
			{name: "malformed-json", data: `{`, invalid: true},
			{name: "null-document", data: `null`, invalid: true},
			{name: "null-hooks", data: `{"hooks":null}`, invalid: true},
			{name: "malformed-hooks", data: `{"hooks":[]}`, invalid: true},
		} {
			t.Run(scope+"/"+tc.name, func(t *testing.T) {
				fakeCodexOnPath(t)
				params := adapterprotocol.DiagnoseParams{RepoRoot: t.TempDir(), Scope: scope}
				path := resolveHooksPath(params.RepoRoot, scope)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				if !tc.missing {
					require.NoError(t, os.WriteFile(path, []byte(tc.data), 0o600))
				}
				result, err := handleDiagnose(params)
				require.NoError(t, err)
				switch {
				case tc.invalid:
					require.Len(t, result.Issues, 1)
					want := "codex:hooks-invalid"
					if scope == "user" {
						want += "-user"
					}
					assert.Equal(t, want, result.Issues[0].Slug)
					assert.Contains(t, result.Issues[0].Detail, path)
					assert.False(t, result.Issues[0].FixSafe)
					assert.Empty(t, result.Issues[0].FixArgv, "reinstall cannot safely repair malformed user-owned JSON")
					assert.False(t, result.OK)
					_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: params.RepoRoot, Scope: scope})
					assert.Error(t, err, "install must refuse malformed files without panicking or replacing them")
				case scope == "project":
					require.Len(t, result.Issues, 1)
					assert.Equal(t, "codex:hooks-missing", result.Issues[0].Slug)
					assert.False(t, result.OK)
				default:
					assert.True(t, result.OK)
					assert.Empty(t, result.Issues)
				}
				if tc.missing {
					assert.NoFileExists(t, path)
				} else {
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, tc.data, string(data), "diagnosis must not rewrite malformed or empty files")
				}
			})
		}
	}
}
