package main

import (
	"regexp"
	"testing"

	"github.com/spf13/cobra"
)

func TestPrepareDocsCommandTreeExcludesExperimentalCommands(t *testing.T) {
	root := &cobra.Command{Use: "ox"}
	attest := &cobra.Command{Use: "attest"}
	scout := &cobra.Command{Use: "scout"}
	memory := &cobra.Command{Use: "memory"}
	completion := &cobra.Command{Use: "completion"}
	carts := &cobra.Command{Use: "carts"}
	cartAnalyze := &cobra.Command{Use: "cart-analyze"}
	root.AddCommand(attest, scout, memory, carts, cartAnalyze, completion)

	prepareDocsCommandTree(root)

	for _, command := range []*cobra.Command{attest, scout, memory, completion} {
		if commandRegistered(root, command) {
			t.Errorf("%s command remains registered", command.Name())
		}
	}
	if !carts.Hidden {
		t.Error("carts command is visible")
	}
	if !cartAnalyze.Hidden {
		t.Error("cart-analyze command is visible")
	}
	if !root.CompletionOptions.DisableDefaultCmd {
		t.Error("default completion command remains enabled")
	}
}

// TestPRHeaderLongHasNoBareHTMLTags guards the generated MDX reference.
//
// `ox docs` copies a command's Long description verbatim into
// docs/reference/**/*.mdx as UNFENCED prose, and docs/ is published as the
// @sageox/cli-docs npm package — so a downstream MDX compiler, not this repo,
// is what parses it. MDX treats a lowercase <tag> as a JSX intrinsic element,
// so a bare <picture> or <a> in prose is an unclosed-element parse error there.
// Naming an HTML element in help text is legitimate; it just has to be spelled
// as inline code so both a terminal reader and an MDX parser get something sane.
//
// Failure prevented: `ox pr header --help` grows a bare <tag> again and silently
// breaks the published docs package for whoever compiles it. Red-first: drop the
// backticks around `<picture>` in prHeaderCmd.Long and this fails.
func TestPRHeaderLongHasNoBareHTMLTags(t *testing.T) {
	// strip inline-code spans first — a tag INSIDE backticks is the correct form
	withoutCode := regexp.MustCompile("`[^`]*`").ReplaceAllString(prHeaderCmd.Long, "")

	if m := regexp.MustCompile(`<(/?[a-zA-Z][a-zA-Z0-9-]*)[ >/]`).FindStringSubmatch(withoutCode); m != nil {
		t.Errorf("prHeaderCmd.Long contains bare HTML tag %q outside backticks; "+
			"MDX parses it as an unclosed JSX element in the generated reference. "+
			"Spell it as `%s` instead.", "<"+m[1]+">", "<"+m[1]+">")
	}
}
