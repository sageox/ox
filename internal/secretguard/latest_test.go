package secretguard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLatestRulesRefreshAndFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"new rule", "[[rules]]\nid='new-format'\nregex='future_credential_[a-z]+'\n", 200, false},
		{"outage", "", 503, true},
		{"empty", "", 200, true},
		{"unsupported", "[[rules]]\nid='new'\nregex='x'\nnewProtection=true", 200, true},
		{"malformed", "[[rules]", 200, true},
		{"oversized", strings.Repeat("x", (4<<20)+1), 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Cache-Control") != "no-cache" {
					t.Error("fresh catalog request required")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			rules, err := fetchLatestRules(context.Background(), server.Client(), server.URL)
			if tc.wantError {
				if err == nil || rules != nil {
					t.Fatal("unavailable or unsupported catalog must refuse publication")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if rules.ValidateContent("raw.jsonl", []byte("future_credential_synthetic")) == nil {
				t.Fatal("new upstream format was not enforced")
			}
			if err := rules.ValidateContent("raw.jsonl", []byte("ordinary meeting notes")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLatestRulesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if rules, err := fetchLatestRules(ctx, http.DefaultClient, "http://127.0.0.1:1"); err == nil || rules != nil {
		t.Fatal("canceled refresh must fail closed")
	}
}
