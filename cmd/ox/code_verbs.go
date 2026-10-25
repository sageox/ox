// Verb-mode wrappers over `ox code search` DSL. Agents pattern-match on
// verbs (callers, callees, defs, refs, log) more reliably than they
// pattern-match on undocumented DSL filters (calls:, calledby:, type:).
// These commands are thin shells: each one builds the equivalent DSL string
// and delegates to runCodeSearch, so output shape, flags, structured-status
// JSON, and the stderr stats line behave identically across all of them.
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// validateSymbolArg rejects arguments that would smuggle additional DSL filters
// into a verb's constructed query string. Verbs build DSL via Sprintf — without
// validation, `ox code callers "foo calledby:bar"` would render
// `calledby:foo calledby:bar` and the parser would interpret it as a
// multi-filter query rather than a single-symbol lookup. The constraint is
// intentionally strict (one bare identifier-like token): a user who needs the
// full DSL should drop to `ox code search` instead of the verb wrapper.
func validateSymbolArg(name, arg string) error {
	if arg == "" {
		return fmt.Errorf("%s: name argument must not be empty", name)
	}
	if strings.ContainsAny(arg, " \t\n\r") {
		return fmt.Errorf("%s: name must not contain whitespace — use 'ox code search' for multi-term DSL queries", name)
	}
	if strings.ContainsAny(arg, ":\"'") {
		return fmt.Errorf("%s: name must not contain DSL delimiters (':', quotes) — use 'ox code search' for DSL queries", name)
	}
	return nil
}

// validatePathArg is the path-shaped sibling of validateSymbolArg. Paths can
// contain `/` and `.` and `-` but still must not embed DSL filter syntax.
func validatePathArg(name, arg string) error {
	if arg == "" {
		return fmt.Errorf("%s: path argument must not be empty", name)
	}
	if strings.ContainsAny(arg, " \t\n\r") {
		return fmt.Errorf("%s: path must not contain whitespace", name)
	}
	if strings.ContainsAny(arg, ":\"'") {
		return fmt.Errorf("%s: path must not contain DSL delimiters (':', quotes)", name)
	}
	return nil
}

// validateDateArg validates --after/--before values. Unlike validateSymbolArg
// it permits ':' so ISO 8601 timestamps (e.g. 2026-04-01T10:00:00Z) pass; it
// still rejects whitespace and quotes, which are the real DSL-injection vectors
// — a colon inside a single whitespace-free token cannot introduce a second
// filter.
func validateDateArg(name, arg string) error {
	if arg == "" {
		return fmt.Errorf("%s: value must not be empty", name)
	}
	if strings.ContainsAny(arg, " \t\n\r\"'") {
		return fmt.Errorf("%s: value must not contain whitespace or quotes", name)
	}
	return nil
}

// codeCallersCmd — "who calls X?" Wraps `search "" calledby:<name>`.
var codeCallersCmd = &cobra.Command{
	Use:   "callers <name>",
	Short: "Who calls <name>? (resolved call graph)",
	Long: `List the callers of <name> using the ADR-019 resolved call graph.

Equivalent DSL:  ox code search "" calledby:<name>

Examples:
  ox code callers authenticate
  ox code callers ResolveSessionRecording --limit 20`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateSymbolArg("callers", args[0]); err != nil {
			return err
		}
		return runCodeSearch(cmd, fmt.Sprintf(`calledby:%s`, args[0]))
	},
}

// codeCalleesCmd — "what does X call?" Wraps `search "" calls:<name> depth:N`.
var codeCalleesCmd = &cobra.Command{
	Use:   "callees <name>",
	Short: "What does <name> call? (resolved call graph, transitive via --depth)",
	Long: `List the callees of <name> using the ADR-019 resolved call graph.

Equivalent DSL:  ox code search "" calls:<name> depth:<N>

Examples:
  ox code callees Handler
  ox code callees authenticate --depth 3`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateSymbolArg("callees", args[0]); err != nil {
			return err
		}
		depth, _ := cmd.Flags().GetInt("depth")
		q := fmt.Sprintf(`calls:%s`, args[0])
		if depth > 0 {
			q += fmt.Sprintf(` depth:%d`, depth)
		}
		return runCodeSearch(cmd, q)
	},
}

// codeDefsCmd — "where is <name> defined?" Wraps `search "<name>" type:symbol`.
var codeDefsCmd = &cobra.Command{
	Use:   "defs <name>",
	Short: "Where is <name> defined? (symbol search)",
	Long: `Find symbol definitions matching <name>.

Equivalent DSL:  ox code search "<name>" type:symbol

Examples:
  ox code defs ResolveSession
  ox code defs Handler --limit 5`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateSymbolArg("defs", args[0]); err != nil {
			return err
		}
		return runCodeSearch(cmd, fmt.Sprintf(`%s type:symbol`, args[0]))
	},
}

// codeRefsCmd — "where is <name> referenced in code text?"
// Wraps `search "<name>" type:code`. Unlike defs, returns text matches across
// the indexed code (not just symbol records).
var codeRefsCmd = &cobra.Command{
	Use:   "refs <name>",
	Short: "Where is <name> referenced in code text? (text search across the index)",
	Long: `Find text occurrences of <name> in indexed code.

Equivalent DSL:  ox code search "<name>" type:code

For the resolved call graph use callers/callees — refs is plain text search.

Examples:
  ox code refs authenticate
  ox code refs migration --lang go`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateSymbolArg("refs", args[0]); err != nil {
			return err
		}
		q := fmt.Sprintf(`%s type:code`, args[0])
		if lang, _ := cmd.Flags().GetString("lang"); lang != "" {
			if err := validateSymbolArg("--lang", lang); err != nil {
				return err
			}
			q += fmt.Sprintf(` lang:%s`, lang)
		}
		return runCodeSearch(cmd, q)
	},
}

// codeLogCmd — "what changed in <path>?" Wraps `search "" file:<path> type:commit`.
// Author/before/after flags compose into the DSL so agents don't have to type
// the grammar.
var codeLogCmd = &cobra.Command{
	Use:   "log <path>",
	Short: "Commits touching <path> (git history via the index)",
	Long: `Show commits that touched <path> using the indexed git history.

Equivalent DSL:  ox code search "" file:<path> type:commit [author:...] [after:...] [before:...]

Examples:
  ox code log internal/codedb/
  ox code log cmd/ox/code.go --author rupak --after 2026-04-01`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validatePathArg("log", args[0]); err != nil {
			return err
		}
		var parts []string
		parts = append(parts, fmt.Sprintf(`file:%s`, args[0]))
		parts = append(parts, "type:commit")
		author, _ := cmd.Flags().GetString("author")
		after, _ := cmd.Flags().GetString("after")
		before, _ := cmd.Flags().GetString("before")
		for _, v := range []struct{ name, val string }{
			{"--author", author}, {"--after", after}, {"--before", before},
		} {
			if v.val == "" {
				continue
			}
			var verr error
			switch v.name {
			case "--after", "--before":
				// Dates carry ':' in ISO 8601 timestamps (2026-04-01T10:00:00Z);
				// validateSymbolArg would wrongly reject them.
				verr = validateDateArg(v.name, v.val)
			default:
				verr = validateSymbolArg(v.name, v.val)
			}
			if verr != nil {
				return verr
			}
		}

		// The CodeDB commit-search executor requires at least one of
		// author:/before:/after:/message: alongside file: (the file filter
		// alone does not satisfy the "give the planner some bound" check in
		// translate.go). When the agent passes no filters, default --after
		// to one year ago so `ox code log <path>` always returns something
		// useful instead of erroring.
		if author == "" && after == "" && before == "" {
			after = time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
		}

		if author != "" {
			parts = append(parts, fmt.Sprintf(`author:%s`, author))
		}
		if after != "" {
			parts = append(parts, fmt.Sprintf(`after:%s`, after))
		}
		if before != "" {
			parts = append(parts, fmt.Sprintf(`before:%s`, before))
		}
		return runCodeSearch(cmd, strings.Join(parts, " "))
	},
}

func init() {
	// Shared flags: every verb takes the same output knobs as `ox code search`
	// so output shape is identical.
	for _, c := range []*cobra.Command{codeCallersCmd, codeCalleesCmd, codeDefsCmd, codeRefsCmd, codeLogCmd} {
		c.Flags().Bool("full-json", false, "full uncompacted JSON output (~4x more context tokens)")
		c.Flags().Int("limit", 10, "max results to return")
		c.Flags().Int("snippet", defaultSnippetLen, "max snippet length per result, in characters")
	}

	// Verb-specific flags
	codeCalleesCmd.Flags().Int("depth", 1, "transitive call depth (1-10)")
	codeRefsCmd.Flags().String("lang", "", "restrict to a language (e.g. go, ts)")
	codeLogCmd.Flags().String("author", "", "restrict to commits by this author")
	codeLogCmd.Flags().String("after", "", "include commits after this date (e.g. 2026-04-01)")
	codeLogCmd.Flags().String("before", "", "include commits before this date")

	codeCmd.AddCommand(codeCallersCmd)
	codeCmd.AddCommand(codeCalleesCmd)
	codeCmd.AddCommand(codeDefsCmd)
	codeCmd.AddCommand(codeRefsCmd)
	codeCmd.AddCommand(codeLogCmd)
}
