package main

import (
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestWithAttributionGuidance_DefaultAttribution(t *testing.T) {
	attr := config.MergeAttribution(nil, nil) // all defaults
	result := withAttributionGuidance("", true, attr)

	assert.Contains(t, result, "## SageOx Attribution")
	assert.Contains(t, result, "Real-Time Insight Attribution")
	assert.Contains(t, result, "Plan Footer")
	assert.Contains(t, result, "Guided by SageOx")
	assert.Contains(t, result, "Commit Attribution")
	assert.Contains(t, result, "Co-Authored-By: SageOx <ox@sageox.ai>")
	assert.Contains(t, result, "Code Comments")
	assert.Contains(t, result, "PR Attribution (Critical for Squash Merges)")
	assert.NotContains(t, result, "Not Logged In")
}

func TestWithAttributionGuidance_CommitDisabled(t *testing.T) {
	empty := ""
	attr := config.MergeAttribution(&config.Attribution{Commit: &empty}, nil)
	result := withAttributionGuidance("", true, attr)

	// always-on blocks still present
	assert.Contains(t, result, "## SageOx Attribution")
	assert.Contains(t, result, "Real-Time Insight Attribution")
	assert.Contains(t, result, "Plan Footer")
	assert.Contains(t, result, "Guided by SageOx")

	// config-gated blocks omitted
	assert.NotContains(t, result, "Commit Attribution")
	assert.NotContains(t, result, "Co-Authored-By")
	assert.NotContains(t, result, "Code Comments")

	// PR squash section omitted (needs commit trailer format)
	assert.NotContains(t, result, "PR Attribution (Critical for Squash Merges)")
}

func TestWithAttributionGuidance_AllConfigGatedDisabled(t *testing.T) {
	empty := ""
	attr := config.MergeAttribution(&config.Attribution{
		Plan:   &empty,
		Commit: &empty,
		PR:     &empty,
	}, nil)
	result := withAttributionGuidance("", true, attr)

	// section still present (always-on blocks)
	assert.Contains(t, result, "## SageOx Attribution")
	assert.Contains(t, result, "Real-Time Insight Attribution")
	assert.Contains(t, result, "Guided by SageOx")

	// config-gated blocks omitted
	assert.NotContains(t, result, "Commit Attribution")
	assert.NotContains(t, result, "Co-Authored-By")
	assert.NotContains(t, result, "Code Comments")
}

func TestWithAttributionGuidance_NotLoggedIn(t *testing.T) {
	attr := config.MergeAttribution(nil, nil) // defaults
	result := withAttributionGuidance("", false, attr)

	assert.Contains(t, result, "Not Logged In")
	assert.Contains(t, result, "may not be using your latest team context")
	// commit attribution still present (enabled by default)
	assert.Contains(t, result, "Co-Authored-By")
}

func TestWithAttributionGuidance_NotLoggedInAllDisabled(t *testing.T) {
	empty := ""
	attr := config.MergeAttribution(&config.Attribution{
		Plan:   &empty,
		Commit: &empty,
		PR:     &empty,
	}, nil)
	result := withAttributionGuidance("", false, attr)

	// warning always appears
	assert.Contains(t, result, "Not Logged In")
	// plan footer always present
	assert.Contains(t, result, "Guided by SageOx")
	// commit still omitted
	assert.NotContains(t, result, "Commit Attribution")
}

func TestWithAttributionGuidance_PrependsContent(t *testing.T) {
	attr := config.MergeAttribution(nil, nil)
	result := withAttributionGuidance("existing content", true, attr)

	assert.True(t, strings.HasPrefix(result, "existing content"))
	assert.Contains(t, result, "## SageOx Attribution")
}

func TestWithAttributionGuidance_CustomCommitValue(t *testing.T) {
	custom := "Co-Authored-By: Custom <custom@example.com>"
	attr := config.MergeAttribution(&config.Attribution{Commit: &custom}, nil)
	result := withAttributionGuidance("", true, attr)

	assert.Contains(t, result, "Commit Attribution")
	assert.Contains(t, result, "Co-Authored-By: Custom <custom@example.com>")
	assert.NotContains(t, result, "ox@sageox.ai")
}

func TestBuildAttributionTextSection_AllEnabled(t *testing.T) {
	attr := config.MergeAttribution(nil, nil)
	result := buildAttributionTextSection(attr)

	assert.Contains(t, result, "## Attribution")
	assert.Contains(t, result, "**Plans**")
	assert.Contains(t, result, "**Commits**")
	assert.Contains(t, result, "**PRs**")
	assert.Contains(t, result, "survives squash merge")
}

func TestBuildAttributionTextSection_CommitOnly(t *testing.T) {
	empty := ""
	attr := config.MergeAttribution(&config.Attribution{
		Plan: &empty,
		PR:   &empty,
	}, nil)
	result := buildAttributionTextSection(attr)

	assert.Contains(t, result, "## Attribution")
	assert.NotContains(t, result, "**Plans**")
	assert.Contains(t, result, "**Commits**")
	assert.NotContains(t, result, "**PRs**")
}

func TestBuildAttributionTextSection_AllDisabled(t *testing.T) {
	empty := ""
	attr := config.MergeAttribution(&config.Attribution{
		Plan:   &empty,
		Commit: &empty,
		PR:     &empty,
	}, nil)
	result := buildAttributionTextSection(attr)

	// header still present but no items
	assert.Contains(t, result, "## Attribution")
	assert.NotContains(t, result, "**Plans**")
	assert.NotContains(t, result, "**Commits**")
	assert.NotContains(t, result, "**PRs**")
}
