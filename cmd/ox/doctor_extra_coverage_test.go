package main

import (
	"bytes"
	"testing"

	"github.com/sageox/ox/internal/config"
)

func TestCheckResultConstructors_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("PassedCheck fields", func(t *testing.T) {
		t.Parallel()
		c := PassedCheck("auth", "logged in")
		if !c.passed {
			t.Error("PassedCheck should set passed=true")
		}
		if c.name != "auth" {
			t.Errorf("name = %q, want %q", c.name, "auth")
		}
		if c.message != "logged in" {
			t.Errorf("message = %q, want %q", c.message, "logged in")
		}
		if c.warning || c.skipped {
			t.Error("PassedCheck should not set warning or skipped")
		}
		if c.priority != "" {
			t.Errorf("priority should be empty, got %q", c.priority)
		}
	})

	t.Run("FailedCheck fields", func(t *testing.T) {
		t.Parallel()
		c := FailedCheck("config", "missing file", "run ox init")
		if c.passed {
			t.Error("FailedCheck should set passed=false")
		}
		if c.detail != "run ox init" {
			t.Errorf("detail = %q, want %q", c.detail, "run ox init")
		}
		if c.name != "config" {
			t.Errorf("name = %q, want %q", c.name, "config")
		}
	})

	t.Run("WarningCheck fields", func(t *testing.T) {
		t.Parallel()
		c := WarningCheck("version", "outdated", "upgrade available")
		if !c.passed {
			t.Error("WarningCheck should set passed=true")
		}
		if !c.warning {
			t.Error("WarningCheck should set warning=true")
		}
		if c.detail != "upgrade available" {
			t.Errorf("detail = %q, want %q", c.detail, "upgrade available")
		}
	})

	t.Run("SkippedCheck fields", func(t *testing.T) {
		t.Parallel()
		c := SkippedCheck("network", "offline", "try later")
		if !c.skipped {
			t.Error("SkippedCheck should set skipped=true")
		}
		if c.detail != "try later" {
			t.Errorf("detail = %q, want %q", c.detail, "try later")
		}
	})
}

func TestCountCheck_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("passed increments pass", func(t *testing.T) {
		t.Parallel()
		var p, w, f, s int
		countCheck(PassedCheck("a", "ok"), &p, &w, &f, &s)
		if p != 1 || w != 0 || f != 0 || s != 0 {
			t.Errorf("counts = %d/%d/%d/%d, want 1/0/0/0", p, w, f, s)
		}
	})

	t.Run("warning increments warn", func(t *testing.T) {
		t.Parallel()
		var p, w, f, s int
		countCheck(WarningCheck("a", "old", ""), &p, &w, &f, &s)
		if p != 0 || w != 1 || f != 0 || s != 0 {
			t.Errorf("counts = %d/%d/%d/%d, want 0/1/0/0", p, w, f, s)
		}
	})

	t.Run("failed increments fail", func(t *testing.T) {
		t.Parallel()
		var p, w, f, s int
		countCheck(FailedCheck("a", "bad", ""), &p, &w, &f, &s)
		if p != 0 || w != 0 || f != 1 || s != 0 {
			t.Errorf("counts = %d/%d/%d/%d, want 0/0/1/0", p, w, f, s)
		}
	})

	t.Run("skipped increments skip", func(t *testing.T) {
		t.Parallel()
		var p, w, f, s int
		countCheck(SkippedCheck("a", "off", ""), &p, &w, &f, &s)
		if p != 0 || w != 0 || f != 0 || s != 1 {
			t.Errorf("counts = %d/%d/%d/%d, want 0/0/0/1", p, w, f, s)
		}
	})

	t.Run("multiple checks accumulate", func(t *testing.T) {
		t.Parallel()
		var p, w, f, s int
		countCheck(PassedCheck("a", "ok"), &p, &w, &f, &s)
		countCheck(PassedCheck("b", "ok"), &p, &w, &f, &s)
		countCheck(WarningCheck("c", "old", ""), &p, &w, &f, &s)
		countCheck(FailedCheck("d", "bad", ""), &p, &w, &f, &s)
		countCheck(SkippedCheck("e", "off", ""), &p, &w, &f, &s)
		if p != 2 || w != 1 || f != 1 || s != 1 {
			t.Errorf("counts = %d/%d/%d/%d, want 2/1/1/1", p, w, f, s)
		}
	})
}

func TestRenderFixLevelBadge_Coverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		level FixLevel
		empty bool
	}{
		{"auto", FixLevelAuto, false},
		{"suggested", FixLevelSuggested, false},
		{"confirm", FixLevelConfirm, false},
		{"check-only", FixLevelCheckOnly, false},
		{"empty level", "", true},
		{"unknown level", FixLevel("custom"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderFixLevelBadge(tt.level)
			if tt.empty && got != "" {
				t.Errorf("expected empty string, got %q", got)
			}
			if !tt.empty && got == "" {
				t.Errorf("expected non-empty badge for %q", tt.level)
			}
		})
	}
}

func TestRenderAgentRequiredBadge_Coverage(t *testing.T) {
	t.Parallel()

	got := renderAgentRequiredBadge()
	if len(got) == 0 {
		t.Error("expected non-empty badge")
	}
}

func TestMessageAnnotation_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("empty message", func(t *testing.T) {
		t.Parallel()
		got := messageAnnotation("")
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("non-empty message returns styled annotation", func(t *testing.T) {
		t.Parallel()
		got := messageAnnotation("some info")
		if len(got) == 0 {
			t.Error("expected non-empty annotation")
		}
	})
}

func TestGetAvailableSlugs_Coverage(t *testing.T) {
	t.Parallel()

	slugs := getAvailableSlugs()
	if len(slugs) == 0 {
		t.Error("expected at least one available slug")
	}
	// verify sorted
	for i := 1; i < len(slugs); i++ {
		if slugs[i] < slugs[i-1] {
			t.Errorf("slugs not sorted: %q before %q", slugs[i-1], slugs[i])
		}
	}
}

func TestDoctorOptions_ShouldFix_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("fix=false no slugs returns false", func(t *testing.T) {
		t.Parallel()
		opts := doctorOptions{fix: false}
		if opts.shouldFix("nonexistent-slug") {
			t.Error("expected false")
		}
	})

	t.Run("fix=true no slugs returns true", func(t *testing.T) {
		t.Parallel()
		opts := doctorOptions{fix: true}
		if !opts.shouldFix("nonexistent-slug") {
			t.Error("expected true when fix=true and no slug filter")
		}
	})

	t.Run("specific slug match returns true", func(t *testing.T) {
		t.Parallel()
		opts := doctorOptions{fix: true, fixSlugs: []string{"config-json"}}
		if !opts.shouldFix("config-json") {
			t.Error("expected true for matching slug")
		}
	})

	t.Run("non-matching slug returns false", func(t *testing.T) {
		t.Parallel()
		opts := doctorOptions{fix: true, fixSlugs: []string{"config-json"}}
		if opts.shouldFix("other-slug") {
			t.Error("expected false for non-matching slug")
		}
	})

	t.Run("auto-fix slug always returns true regardless of opts", func(t *testing.T) {
		t.Parallel()
		var autoSlug string
		for slug, dc := range DoctorCheckRegistry {
			if dc.IsAutoFixable() {
				autoSlug = slug
				break
			}
		}
		if autoSlug == "" {
			t.Skip("no auto-fixable doctor checks found")
		}

		opts := doctorOptions{fix: false}
		if !opts.shouldFix(autoSlug) {
			t.Errorf("auto-fix slug %q should always return true", autoSlug)
		}
	})
}

func TestDoctorCheckIsAutoFixable_Coverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		level    FixLevel
		expected bool
	}{
		{"auto", FixLevelAuto, true},
		{"suggested", FixLevelSuggested, false},
		{"check-only", FixLevelCheckOnly, false},
		{"confirm", FixLevelConfirm, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dc := &DoctorCheck{FixLevel: tt.level}
			if dc.IsAutoFixable() != tt.expected {
				t.Errorf("IsAutoFixable() = %v, want %v", dc.IsAutoFixable(), tt.expected)
			}
		})
	}
}

func TestGetDoctorCheck_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("existing slug returns check", func(t *testing.T) {
		t.Parallel()
		var slug string
		for s := range DoctorCheckRegistry {
			slug = s
			break
		}
		if slug == "" {
			t.Skip("no registered doctor checks")
		}
		dc := GetDoctorCheck(slug)
		if dc == nil {
			t.Errorf("expected non-nil for slug %q", slug)
		}
	})

	t.Run("nonexistent slug returns nil", func(t *testing.T) {
		t.Parallel()
		dc := GetDoctorCheck("definitely-not-a-real-slug-xyz")
		if dc != nil {
			t.Error("expected nil for nonexistent slug")
		}
	})
}

func TestDisplayJSONResults_Coverage(t *testing.T) {
	categories := []checkCategory{
		{
			name: "Auth",
			checks: []checkResult{
				PassedCheck("auth", "logged in"),
				FailedCheck("token", "expired", "re-login").WithFixInfo("token-test", FixLevelSuggested),
			},
		},
		{
			name: "Config",
			checks: []checkResult{
				WarningCheck("version", "old", "upgrade"),
				SkippedCheck("network", "offline", ""),
			},
		},
	}

	var buf bytes.Buffer
	cmd := doctorCmd
	cmd.SetOut(&buf)

	oldCfg := cfg
	cfg = &config.Config{JSON: true}
	defer func() { cfg = oldCfg }()

	hasFailed := displayJSONResults(cmd, categories)
	if !hasFailed {
		t.Error("expected hasFailed=true with a failed check")
	}

	output := buf.String()
	if len(output) == 0 {
		t.Error("expected JSON output")
	}
}

func TestDoctorCheckGetters_Coverage(t *testing.T) {
	t.Parallel()

	dc := &DoctorCheck{
		Slug:     "test-slug",
		Name:     "Test Check",
		Category: "Testing",
		FixLevel: FixLevelSuggested,
		Run: func(fix bool) checkResult {
			return PassedCheck("test", "ok")
		},
	}

	if dc.GetName() != "Test Check" {
		t.Errorf("GetName() = %q, want %q", dc.GetName(), "Test Check")
	}
	if dc.GetCategory() != "Testing" {
		t.Errorf("GetCategory() = %q, want %q", dc.GetCategory(), "Testing")
	}

	result := dc.RunCheck(false)
	if !result.passed {
		t.Error("RunCheck should return passed check")
	}
}

func TestDisplayDoctorResults_Dispatch_Coverage(t *testing.T) {
	categories := []checkCategory{
		{name: "Test", checks: []checkResult{PassedCheck("a", "ok")}},
	}

	var buf bytes.Buffer
	cmd := doctorCmd
	cmd.SetOut(&buf)

	// default mode (priority summary)
	oldCfg := cfg
	cfg = nil
	defer func() { cfg = oldCfg }()

	hasFailed := displayDoctorResults(cmd, categories, doctorOptions{})
	if hasFailed {
		t.Error("expected no failures for all-passed checks")
	}

	output := buf.String()
	if len(output) == 0 {
		t.Error("expected output from displayDoctorResults")
	}
}

func TestFixableSlugsHint_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("empty slugs", func(t *testing.T) {
		t.Parallel()
		got := fixableSlugsHint(nil)
		if got != "" {
			t.Errorf("expected empty hint, got %q", got)
		}
	})

	t.Run("with slugs", func(t *testing.T) {
		t.Parallel()
		slugs := []fixSlugInfo{{slug: "config-json", fixLevel: FixLevelSuggested, name: "config"}}
		got := fixableSlugsHint(slugs)
		if got == "" {
			t.Error("expected non-empty hint")
		}
	})
}

func TestCategorizeCheck_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("critical failed goes to critical bucket", func(t *testing.T) {
		t.Parallel()
		var crit, attn, opt, agent []checkResult
		hasFailed := false
		c := CriticalCheck("auth", "expired", "login")
		categorizeCheck(c, &crit, &attn, &opt, &agent, &hasFailed)
		if len(crit) != 1 {
			t.Errorf("expected 1 critical, got %d", len(crit))
		}
		if !hasFailed {
			t.Error("hasFailed should be true")
		}
	})

	t.Run("regular failed goes to attention", func(t *testing.T) {
		t.Parallel()
		var crit, attn, opt, agent []checkResult
		hasFailed := false
		c := FailedCheck("config", "broken", "fix")
		categorizeCheck(c, &crit, &attn, &opt, &agent, &hasFailed)
		if len(attn) != 1 {
			t.Errorf("expected 1 attention, got %d", len(attn))
		}
	})

	t.Run("info warning goes to optional", func(t *testing.T) {
		t.Parallel()
		var crit, attn, opt, agent []checkResult
		hasFailed := false
		c := InfoCheck("tip", "suggestion", "consider")
		categorizeCheck(c, &crit, &attn, &opt, &agent, &hasFailed)
		if len(opt) != 1 {
			t.Errorf("expected 1 optional, got %d", len(opt))
		}
	})

	t.Run("regular warning goes to attention", func(t *testing.T) {
		t.Parallel()
		var crit, attn, opt, agent []checkResult
		hasFailed := false
		c := WarningCheck("version", "old", "upgrade")
		categorizeCheck(c, &crit, &attn, &opt, &agent, &hasFailed)
		if len(attn) != 1 {
			t.Errorf("expected 1 attention, got %d", len(attn))
		}
	})

	t.Run("agent-required warning goes to agent bucket", func(t *testing.T) {
		t.Parallel()
		var crit, attn, opt, agent []checkResult
		hasFailed := false
		c := AgentRequiredCheck("sessions", "incomplete", "run doctor")
		categorizeCheck(c, &crit, &attn, &opt, &agent, &hasFailed)
		if len(agent) != 1 {
			t.Errorf("expected 1 agent-required, got %d", len(agent))
		}
	})

	t.Run("skipped check not categorized", func(t *testing.T) {
		t.Parallel()
		var crit, attn, opt, agent []checkResult
		hasFailed := false
		c := SkippedCheck("net", "off", "")
		categorizeCheck(c, &crit, &attn, &opt, &agent, &hasFailed)
		total := len(crit) + len(attn) + len(opt) + len(agent)
		if total != 0 {
			t.Errorf("skipped check should not be categorized, got total %d", total)
		}
	})

	t.Run("passed check not categorized", func(t *testing.T) {
		t.Parallel()
		var crit, attn, opt, agent []checkResult
		hasFailed := false
		c := PassedCheck("auth", "ok")
		categorizeCheck(c, &crit, &attn, &opt, &agent, &hasFailed)
		total := len(crit) + len(attn) + len(opt) + len(agent)
		if total != 0 {
			t.Errorf("passed check should not be categorized, got total %d", total)
		}
	})
}

func TestHasRequiresAgentIssues_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("no issues", func(t *testing.T) {
		t.Parallel()
		categories := []checkCategory{
			{name: "Auth", checks: []checkResult{PassedCheck("auth", "ok")}},
		}
		if hasRequiresAgentIssues(categories) {
			t.Error("expected false for no agent issues")
		}
	})

	t.Run("has agent-required warning", func(t *testing.T) {
		t.Parallel()
		categories := []checkCategory{
			{name: "Sessions", checks: []checkResult{
				AgentRequiredCheck("sessions", "incomplete", "run doctor"),
			}},
		}
		if !hasRequiresAgentIssues(categories) {
			t.Error("expected true for agent-required warning")
		}
	})

	t.Run("agent-required in child", func(t *testing.T) {
		t.Parallel()
		parent := PassedCheck("config", "ok")
		child := AgentRequiredCheck("sub", "needs agent", "fix")
		parent.children = []checkResult{child}
		categories := []checkCategory{
			{name: "Config", checks: []checkResult{parent}},
		}
		if !hasRequiresAgentIssues(categories) {
			t.Error("expected true for agent-required child")
		}
	})

	t.Run("skipped agent-required not counted", func(t *testing.T) {
		t.Parallel()
		c := SkippedCheck("sessions", "offline", "")
		c.requiresAgent = true
		categories := []checkCategory{
			{name: "Sessions", checks: []checkResult{c}},
		}
		if hasRequiresAgentIssues(categories) {
			t.Error("expected false for skipped agent-required check")
		}
	})
}

func TestCollectFixableSlugs_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("passed check not collected", func(t *testing.T) {
		t.Parallel()
		var slugs []fixSlugInfo
		c := PassedCheck("auth", "ok")
		c.slug = "auth-status"
		c.fixLevel = FixLevelAuto
		collectFixableSlugs(c, &slugs)
		if len(slugs) != 0 {
			t.Error("passed check without warning should not be collected")
		}
	})

	t.Run("failed check with fix info collected", func(t *testing.T) {
		t.Parallel()
		var slugs []fixSlugInfo
		c := FailedCheck("config", "broken", "fix").WithFixInfo("config-json", FixLevelSuggested)
		collectFixableSlugs(c, &slugs)
		if len(slugs) != 1 {
			t.Fatalf("expected 1 slug, got %d", len(slugs))
		}
		if slugs[0].slug != "config-json" {
			t.Errorf("slug = %q, want %q", slugs[0].slug, "config-json")
		}
	})

	t.Run("skipped check not collected", func(t *testing.T) {
		t.Parallel()
		var slugs []fixSlugInfo
		c := SkippedCheck("network", "offline", "")
		c.slug = "network"
		c.fixLevel = FixLevelSuggested
		collectFixableSlugs(c, &slugs)
		if len(slugs) != 0 {
			t.Error("skipped check should not be collected")
		}
	})

	t.Run("check-only fix level not collected", func(t *testing.T) {
		t.Parallel()
		var slugs []fixSlugInfo
		c := FailedCheck("auth", "expired", "re-login").WithFixInfo("auth-status", FixLevelCheckOnly)
		collectFixableSlugs(c, &slugs)
		if len(slugs) != 0 {
			t.Error("check-only fix level should not be collected")
		}
	})

	t.Run("no slug not collected", func(t *testing.T) {
		t.Parallel()
		var slugs []fixSlugInfo
		c := FailedCheck("thing", "broken", "fix")
		c.fixLevel = FixLevelSuggested
		collectFixableSlugs(c, &slugs)
		if len(slugs) != 0 {
			t.Error("check without slug should not be collected")
		}
	})
}
