package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyntheticHelmSecretsMustNotPersist(t *testing.T) {
	cases := []struct{ name, key, value string }{
		{"aws", "AWS_SECRET_ACCESS_KEY", strings.Repeat("aB3+", 10)},
		{"google", "GEMINI_API_KEY", "AIza" + strings.Repeat("a", 35)},
		{"openai_project", "OPENAI_API_KEY", "sk-proj-" + strings.Repeat("a", 80)},
		{"openai_legacy", "OPENAI_API_KEY", "sk-" + strings.Repeat("a", 24) + "T3BlbkFJ" + strings.Repeat("b", 24)},
		{"e2b", "E2B_API_KEY", "e2b_" + strings.Repeat("a", 40)},
		{"maillab", "MAILLAB_API_KEY", strings.Repeat("a1", 24)},
	}
	for _, tc := range cases {
		for _, shape := range []string{"direct", "helm", "helm_json", "nested_json"} {
			t.Run(tc.name+"/"+shape, func(t *testing.T) {
				input := tc.key + "=" + tc.value
				if shape != "direct" {
					input = fmt.Sprintf("helm template chart --set extraEnv[0].name=%s --set extraEnv[0].value=%s", tc.key, tc.value)
				}
				if shape == "helm_json" {
					data, _ := json.Marshal(map[string]any{"output": input})
					input = string(data)
				}
				if shape == "nested_json" {
					data, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"text": input}}})
					inner, _ := json.Marshal(map[string]any{"result": string(data)})
					input = string(inner)
				}
				base, found := NewRedactor().RedactString(input)
				path := filepath.Join(t.TempDir(), "raw.jsonl")
				w, err := NewRawWriter(path, "")
				if err != nil {
					t.Fatal(err)
				}
				entry := SessionEntry{Type: SessionEntryTypeTool, ToolName: "exec_command", ToolOutput: input}
				if err := w.WriteEntry(&entry); err != nil {
					t.Fatal(err)
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				stored, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(stored), tc.value) {
					t.Error("synthetic secret persisted through production writer")
				}
				t.Logf("base_survives=%t production_writer_survives=%t base_matches=%d", strings.Contains(base, tc.value), strings.Contains(string(stored), tc.value), len(found))
			})
		}
	}
}
