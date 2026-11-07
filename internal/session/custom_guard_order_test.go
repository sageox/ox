package session

import (
	"regexp"
	"strings"
	"testing"
)

func TestCustomRulesCannotWeakenMandatoryGuard(t *testing.T) {
	const opaque = "fabricated-contextual-credential"
	input := "helm template --set extraEnv[0].name=MAILLAB_API_KEY --set extraEnv[0].value=" + opaque
	for _, mode := range []string{"text", "details", "map"} {
		t.Run(mode, func(t *testing.T) {
			r := NewRedactorWithPatterns([]SecretPattern{{Name: "hide-env-name", Pattern: regexp.MustCompile("MAILLAB_API_KEY"), Redact: "[CUSTOM]"}})
			var out string
			switch mode {
			case "text":
				out, _ = r.RedactString(input)
			case "details":
				out, _ = r.RedactStringWithDetails(input)
			case "map":
				data := map[string]any{"content": input}
				r.RedactMap(data)
				out = data["content"].(string)
			}
			if strings.Contains(out, opaque) {
				t.Fatal("custom transform removed mandatory detection context")
			}
		})
	}
}

func TestCustomReplacementCannotIntroduceCredential(t *testing.T) {
	for _, mode := range []string{"text", "details", "map"} {
		t.Run(mode, func(t *testing.T) {
			r := NewRedactorWithPatterns([]SecretPattern{{Name: "custom-replacement", Pattern: regexp.MustCompile("replace-me"), Redact: "AKIA" + "QWERTYUIOPASDFGH"}})
			var out string
			switch mode {
			case "text":
				out, _ = r.RedactString("replace-me")
			case "details":
				out, _ = r.RedactStringWithDetails("replace-me")
			case "map":
				data := map[string]any{"content": "replace-me"}
				r.RedactMap(data)
				out = data["content"].(string)
			}
			if strings.Contains(out, "AKIA"+"QWERTYUIOPASDFGH") {
				t.Fatal("custom replacement introduced unguarded credential")
			}
		})
	}
}

func TestPartialLegacyMatchCannotEraseSensitiveRemainder(t *testing.T) {
	const secret = "abcdefghijklmnopqrstuvWX12345678!opaque-remainder"
	input := "MY_SECRET=" + secret
	for _, mode := range []string{"text", "details", "map", "allowlist"} {
		t.Run(mode, func(t *testing.T) {
			r := NewRedactor()
			var out string
			switch mode {
			case "text":
				out, _ = r.RedactString(input)
			case "details":
				out, _ = r.RedactStringWithDetails(input)
			case "map":
				data := map[string]any{"content": input}
				r.RedactMap(data)
				out = data["content"].(string)
			case "allowlist":
				out, _ = r.RedactWithAllowlist(input, []string{"MY_SECRET"})
			}
			if strings.Contains(out, "opaque-remainder") {
				t.Fatal("partial builtin match erased the context guarding the remaining value")
			}
		})
	}
}
func TestAllowlistCannotHideSensitiveName(t *testing.T) {
	const secret = "fabricated-allowlist-opaque-credential"
	input := "helm template --set extraEnv[0].name=MAILLAB_API_KEY --set extraEnv[0].value=" + secret
	out, _ := NewRedactor().RedactWithAllowlist(input, []string{"MAILLAB_API_KEY"})
	if strings.Contains(out, secret) {
		t.Fatal("allowlist hid context before mandatory protection")
	}
}
