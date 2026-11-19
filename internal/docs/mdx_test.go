package docs

import "testing"

func TestEscapeMDX(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"prose placeholders", "Run ox show <name> {id}\n", "Run ox show \\<name> \\{id}\n"},
		{"existing escapes", "Use \\<name> and \\{id}\n", "Use \\<name> and \\{id}\n"},
		{"escaped backslash", "a \\\\<b\n", "a \\\\\\<b\n"},
		{"inline code", "Run `ox show <name> {id}` now\n", "Run `ox show <name> {id}` now\n"},
		{"multiline code span", "Run `ox show\n<name>` now\n", "Run `ox show\n<name>` now\n"},
		{
			"longer fence keeps inner fence",
			"````\n```\nox <name> {id}\n```\n````\n<after>\n",
			"````\n```\nox <name> {id}\n```\n````\n\\<after>\n",
		},
		{
			"frontmatter unchanged",
			"---\ntitle: \"ox {x} <y>\"\n---\nbody <z>\n",
			"---\ntitle: \"ox {x} <y>\"\n---\nbody \\<z>\n",
		},
		{
			"indented line is paragraph text in MDX",
			"Example:\n\n    `ox show <name>` and <id>\n",
			"Example:\n\n    `ox show <name>` and \\<id>\n",
		},
		{
			"HTML block is paragraph text in MDX",
			"<details>\n`ox show <name>`\n",
			"\\<details>\n`ox show <name>`\n",
		},
		{
			"fence inside list item",
			"- ```text\n  <name>\n  ```\n\n<after>\n",
			"- ```text\n  <name>\n  ```\n\n\\<after>\n",
		},
		{
			"indented fence is a fence in MDX",
			"Example:\n\n    ```\n    ox <name>\n    ```\n<after>\n",
			"Example:\n\n    ```\n    ox <name>\n    ```\n\\<after>\n",
		},
		{
			"indented closer ends an MDX fence",
			"~~~\n<name>\n    ~~~\n<after>\n",
			"~~~\n<name>\n    ~~~\n\\<after>\n",
		},
		{
			"an MDX fence can end at lower indentation",
			"    ~~~\n    <name>\n~~~\n<after>\n",
			"    ~~~\n    <name>\n~~~\n\\<after>\n",
		},
		{"autolink is text in MDX", "See <https://sageox.ai> `<x>`\n", "See \\<https://sageox.ai> `<x>`\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(escapeMDX([]byte(tt.in))); got != tt.want {
				t.Errorf("escapeMDX(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}
