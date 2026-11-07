package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// timeOrderedVocabulary is the flag set every time-ordered list shares. AI
// coworkers generalize a flag they saw on one sibling to the next, so a
// sibling that lacks one fails them (observed: `plan list --limit`,
// `session list --since`, `conversation list --until`).
var timeOrderedVocabulary = []string{"limit", "since", "until", "json"}

// timeOrderedLists are the commands that must carry the annotation. Naming
// them here, instead of trusting the walk alone, means deleting the annotation
// from one fails the test rather than quietly exempting the command from it.
var timeOrderedLists = []string{
	"plan list",
	"murmur list",
	"session list",
	"conversation list",
}

// allCommandsIncludingGated returns every command under rootCmd plus the
// feature-gated trees. Gated commands are registered on rootCmd only when
// their flag resolves on, so a plain walk would miss them; walking the command
// variables directly includes them without mutating the shared rootCmd.
func allCommandsIncludingGated() []*cobra.Command {
	var all []*cobra.Command
	seen := map[*cobra.Command]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if seen[c] {
			return
		}
		seen[c] = true
		all = append(all, c)
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)
	for _, gated := range []*cobra.Command{scoutCmd, bulletinCmd, cartsCmd, sessionTraceCmd} {
		walk(gated)
	}
	return all
}

// exposedFlagVisible reports whether name is a local or inherited flag of cmd
// that help shows. A canonical flag hidden from help teaches nobody.
func exposedFlagVisible(cmd *cobra.Command, name string) bool {
	flag := cmd.Flags().Lookup(name)
	if flag == nil {
		flag = cmd.InheritedFlags().Lookup(name)
	}
	if flag == nil {
		// a gated tree that is not attached to rootCmd cannot inherit; its
		// persistent flags are still the root's
		flag = rootCmd.PersistentFlags().Lookup(name)
	}
	return flag != nil && !flag.Hidden
}

// TestTimeOrderedListsShareFlagVocabulary: every list annotated as
// time-ordered answers to --limit, --since, --until, and --json.
// Failure prevented: a new (or edited) list command ships a subset, and the
// next AI coworker that generalizes from a sibling hits "unknown flag".
func TestTimeOrderedListsShareFlagVocabulary(t *testing.T) {
	annotated := 0
	for _, cmd := range allCommandsIncludingGated() {
		if cmd.Annotations[annotationTimeOrdered] != "true" {
			continue
		}
		annotated++
		for _, name := range timeOrderedVocabulary {
			if !exposedFlagVisible(cmd, name) {
				t.Errorf("%q is annotated %s but has no visible --%s; time-ordered lists share --limit, --since, --until, --json",
					cmd.CommandPath(), annotationTimeOrdered, name)
			}
		}
	}
	if annotated < len(timeOrderedLists) {
		t.Errorf("found %d annotated commands, want at least %d; the annotation walk is not seeing the lists", annotated, len(timeOrderedLists))
	}
}

// TestTimeOrderedListsCarryTheAnnotation: the four lists named in the
// friction analysis are marked, so the vocabulary test above covers them.
// Failure prevented: the annotation is dropped from a list and its vocabulary
// silently stops being checked.
func TestTimeOrderedListsCarryTheAnnotation(t *testing.T) {
	for _, path := range timeOrderedLists {
		t.Run(path, func(t *testing.T) {
			cmd, rest, err := rootCmd.Find(strings.Fields(path))
			if err != nil || len(rest) != 0 || cmd.CommandPath() != "ox "+path {
				t.Fatalf("rootCmd.Find(%q) = %q rest=%v err=%v; the list moved or was renamed", path, cmd.CommandPath(), rest, err)
			}
			if cmd.Annotations[annotationTimeOrdered] != "true" {
				t.Errorf("%q lost its %s annotation; its --limit/--since/--until/--json are no longer checked", path, annotationTimeOrdered)
			}
		})
	}
}

// TestLegacyListSpellingsStayAcceptedButHidden: the spellings agents already
// type keep working, and stay out of --help so each list teaches one name.
// Failure prevented: a "cleanup" deletes an alias and re-creates the
// unknown-flag failure it was added to absorb, or unhides it and help starts
// advertising two names for one thing.
func TestLegacyListSpellingsStayAcceptedButHidden(t *testing.T) {
	tests := []struct {
		command string
		flag    string
		why     string
	}{
		{"plan list", "all", "agents carry --all over from session list; plan list has no hidden rows"},
		{"murmur list", "last", "the spelling before --limit became the shared name"},
		{"session list", "window", "an alias of --since"},
	}
	for _, tt := range tests {
		t.Run(tt.command+" --"+tt.flag, func(t *testing.T) {
			cmd, _, err := rootCmd.Find(strings.Fields(tt.command))
			if err != nil {
				t.Fatalf("rootCmd.Find(%q): %v", tt.command, err)
			}
			flag := cmd.Flags().Lookup(tt.flag)
			if flag == nil {
				t.Fatalf("%q dropped --%s (%s)", tt.command, tt.flag, tt.why)
			}
			if !flag.Hidden {
				t.Errorf("--%s on %q is visible in help; it is an accepted spelling, not a documented one (%s)", tt.flag, tt.command, tt.why)
			}
		})
	}
}
