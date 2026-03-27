package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetSageOxDarkStyle(t *testing.T) {
	t.Parallel()

	style := GetSageOxDarkStyle()

	// verify the style was parsed (non-zero heading config indicates successful parse)
	assert.NotNil(t, style.Heading, "dark style should have heading config")

	// verify caching: calling again should return same result
	style2 := GetSageOxDarkStyle()
	assert.Equal(t, style, style2, "cached style should match")
}

func TestGetSageOxLightStyle(t *testing.T) {
	t.Parallel()

	style := GetSageOxLightStyle()

	assert.NotNil(t, style.Heading, "light style should have heading config")

	// verify caching
	style2 := GetSageOxLightStyle()
	assert.Equal(t, style, style2, "cached style should match")
}

func TestSageOxDarkStyleJSON_ValidJSON(t *testing.T) {
	t.Parallel()

	// the JSON string should parse without error
	style := GetSageOxDarkStyle()
	// if JSON was invalid, GetSageOxDarkStyle would return empty StyleConfig
	// a valid parse should have document config with margin=0
	assert.NotNil(t, style.Document, "parsed dark style should have document config")
}

func TestSageOxLightStyleJSON_ValidJSON(t *testing.T) {
	t.Parallel()

	style := GetSageOxLightStyle()
	assert.NotNil(t, style.Document, "parsed light style should have document config")
}

func TestGetSageOxDarkStyle_HasExpectedSections(t *testing.T) {
	t.Parallel()

	style := GetSageOxDarkStyle()

	// verify key sections are populated from the JSON
	assert.NotNil(t, style.H1, "should have H1 style")
	assert.NotNil(t, style.H2, "should have H2 style")
	assert.NotNil(t, style.CodeBlock, "should have code block style")
	assert.NotNil(t, style.Link, "should have link style")
	assert.NotNil(t, style.Table, "should have table style")
}

func TestGetSageOxLightStyle_HasExpectedSections(t *testing.T) {
	t.Parallel()

	style := GetSageOxLightStyle()

	assert.NotNil(t, style.H1, "should have H1 style")
	assert.NotNil(t, style.H2, "should have H2 style")
	assert.NotNil(t, style.CodeBlock, "should have code block style")
	assert.NotNil(t, style.Link, "should have link style")
	assert.NotNil(t, style.Table, "should have table style")
}

func TestNewMarkdownRenderer(t *testing.T) {
	t.Parallel()

	renderer, err := NewMarkdownRenderer()
	assert.NoError(t, err, "NewMarkdownRenderer should not error")
	assert.NotNil(t, renderer, "renderer should not be nil")
}

func TestRenderMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		check func(t *testing.T, output string)
	}{
		{
			name:  "plain text passes through",
			input: "Hello world",
			check: func(t *testing.T, output string) {
				assert.Contains(t, output, "Hello world")
			},
		},
		{
			name:  "heading is rendered",
			input: "# Title\n\nBody text",
			check: func(t *testing.T, output string) {
				assert.Contains(t, output, "Title")
				assert.Contains(t, output, "Body text")
			},
		},
		{
			name:  "bullet list renders",
			input: "- item one\n- item two\n- item three",
			check: func(t *testing.T, output string) {
				assert.Contains(t, output, "item one")
				assert.Contains(t, output, "item two")
				assert.Contains(t, output, "item three")
			},
		},
		{
			name:  "empty string",
			input: "",
			check: func(t *testing.T, output string) {
				// should not panic, may return empty or whitespace
				assert.NotNil(t, output)
			},
		},
		{
			name:  "code block",
			input: "```go\nfmt.Println(\"hello\")\n```",
			check: func(t *testing.T, output string) {
				assert.Contains(t, output, "Println")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RenderMarkdown(tt.input)
			tt.check(t, got)
		})
	}
}

func TestRenderMarkdown_GracefulDegradation(t *testing.T) {
	t.Parallel()
	// RenderMarkdown should never panic, even with malformed markdown
	assert.NotPanics(t, func() {
		RenderMarkdown("```\nunclosed code block")
		RenderMarkdown("# " + string([]byte{0xff, 0xfe})) // invalid utf-8
		RenderMarkdown("[broken link](")
	})
}
