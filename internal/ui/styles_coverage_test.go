package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderFunctions_ReturnNonEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fn   func(string) string
	}{
		{"RenderPass", RenderPass},
		{"RenderWarn", RenderWarn},
		{"RenderFail", RenderFail},
		{"RenderMuted", RenderMuted},
		{"RenderAccent", RenderAccent},
		// RenderCategory uppercases, tested separately
		{"RenderPublic", RenderPublic},
		{"RenderPrivate", RenderPrivate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.fn("test")
			assert.Contains(t, got, "test")
		})
	}
}

func TestRenderIcons_ReturnNonEmpty(t *testing.T) {
	t.Parallel()

	icons := []struct {
		name string
		fn   func() string
	}{
		{"PassIcon", RenderPassIcon},
		{"WarnIcon", RenderWarnIcon},
		{"FailIcon", RenderFailIcon},
		{"SkipIcon", RenderSkipIcon},
		{"InfoIcon", RenderInfoIcon},
		{"AgentIcon", RenderAgentIcon},
		{"Separator", RenderSeparator},
	}

	for _, tt := range icons {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.fn()
			assert.NotEmpty(t, got)
		})
	}
}

func TestRenderVisibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
	}{
		{"public"},
		{"private"},
		{"unknown"},
		// empty input returns empty — expected behavior
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := RenderVisibility(tt.input)
			assert.NotEmpty(t, got)
		})
	}
}

func TestRenderCategory_Uppercases(t *testing.T) {
	t.Parallel()
	got := RenderCategory("sessions")
	assert.Contains(t, got, "SESSIONS")
}

func TestRenderEmpty(t *testing.T) {
	t.Parallel()
	// rendering empty strings should not panic
	assert.NotPanics(t, func() {
		RenderPass("")
		RenderWarn("")
		RenderFail("")
		RenderMuted("")
		RenderAccent("")
	})
}
