package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sageox/agentx"
	friction "github.com/sageox/frictionax"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// This file is the "what would ox do with this argv?" half of friction
// recovery. Everything here that plans (planCorrection, planInvocation,
// predictCobraError, argvParsesCleanly) is side-effect free: it never runs a
// command and never prints, so a corpus test can replay real invocations.
// Printing lives in emitCorrection; execution stays in main.go.
//
// Auto-execute rule (see the friction contract): a correction runs unprompted
// only when it keeps the verb the coworker typed and the target is not
// destructive, and only after the corrected argv is proven to parse. Anything
// else is shown as a suggestion and not run.

// correctionSource names which layer produced a correction plan.
type correctionSource string

const (
	correctionNone          correctionSource = "none"
	correctionWordSplit     correctionSource = "word-split"
	correctionFlagAsVerb    correctionSource = "flag-as-verb"
	correctionCatalog       correctionSource = "catalog"
	correctionCatalogPrefix correctionSource = "catalog-prefix"
	correctionFuzzy         correctionSource = "fuzzy"
)

// correctionPlan is what ox would do about one invocation.
type correctionPlan struct {
	// Corrected is the full argv to re-run. Nil when the plan is a bare
	// suggestion that has no argv form (fuzzy guesses and token fixes name one
	// token, not a command line).
	Corrected []string
	// Auto reports whether Corrected may run without asking. Always false when
	// Corrected is nil.
	Auto bool
	// Source is the layer that produced the plan; correctionNone when ox has
	// nothing to offer.
	Source correctionSource
	// Suggestion is the corrected command (or token) as shown to the coworker
	// and recorded in the friction event. Empty when Source is correctionNone.
	Suggestion string
	// Note overrides the generic "Use '<x>' next time" teaching line. Set by
	// the rewrites that know why the invocation was wrong.
	Note string
	// Result is the frictionax result to emit and to send as telemetry, reshaped
	// to match the final plan. Nil when frictionax recognized no failure.
	Result *friction.Result
}

// wordSplitNote teaches the cause: zsh keeps an unquoted $var as one word,
// unlike bash, so a loop over command names passes "plan list" as one argv.
const wordSplitNote = "zsh does not split $var into words; use an array or ${=var}"

// destructiveVerbs are subcommand names that never run from a flag spelling.
// "--kill" meaning "kill" is plausible; running it unprompted because the
// coworker typed a flag is not.
var destructiveVerbs = map[string]bool{
	"abort": true, "cancel": true, "clean": true, "close": true, "delete": true,
	"destroy": true, "drop": true, "gc": true, "kill": true, "logout": true,
	"prune": true, "purge": true, "remove": true, "reset": true, "revoke": true,
	"rm": true, "stop": true, "uninstall": true, "upgrade": true, "wipe": true,
}

// normalizeInvocation applies the safe pre-cobra rewrites (word-split,
// flag-as-verb) and returns the argv to run, announcing any correction. It runs
// before cobra so a correctable invocation costs one attempt, not a failure.
func normalizeInvocation(args []string) []string {
	if len(args) == 0 || headlessLedgerReadRequested(args) {
		return args
	}
	plan := planCorrection(args, nil)
	if !plan.Auto {
		return args
	}
	emitCorrection(plan, argsRequestJSON(args) || (cfg != nil && cfg.JSON) || agentx.IsAgentContext())
	return plan.Corrected
}

// planCorrection decides what ox would do about args.
//
// With a nil err it plans only the rewrites that need no failure to justify
// them (word-split, flag-as-verb), which is the pre-cobra step. With the error
// cobra returned it also consults the friction catalog: exact match, then
// leading-words (prefix) match, then a plausibility-guarded fuzzy guess.
func planCorrection(args []string, err error) correctionPlan {
	rewrite := planRewrite(args)
	if err == nil {
		return rewrite
	}
	if frictionEngine == nil {
		return correctionPlan{Source: correctionNone}
	}
	base := handleFriction(frictionEngine, args, err)
	if base == nil {
		return correctionPlan{Source: correctionNone}
	}

	usageRejection := base.Event != nil &&
		(base.Event.Kind == friction.FailureUnknownCommand || base.Event.Kind == friction.FailureUnknownFlag)
	if usageRejection && rewrite.Source != correctionNone {
		rewrite.Result.Event = base.Event
		return rewrite
	}

	suggestion := base.Suggestion
	if suggestion != nil && suggestion.Type == friction.SuggestionCommandRemap {
		return planExactRemap(base)
	}
	if usageRejection && (suggestion == nil || suggestion.Type == friction.SuggestionLevenshtein) {
		if plan, ok := planPrefixRemap(base, args, err); ok {
			return plan
		}
	}
	if suggestion != nil && suggestion.Type == friction.SuggestionLevenshtein && !plausibleFuzzyMatch(suggestion) {
		base.Suggestion, base.AutoExecute, base.CorrectedArgs = nil, false, nil
		suggestion = nil
	}
	plan := correctionPlan{Source: correctionNone, Result: base}
	if suggestion == nil {
		return plan
	}
	plan.Suggestion = suggestion.Corrected
	plan.Source = correctionFuzzy
	if suggestion.Type == friction.SuggestionTokenFix {
		plan.Source = correctionCatalog
	}
	return plan
}

// attachSuggestion records the correction ox offered on the friction event, so
// the catalog can learn which fixes agents actually needed. The event's Input
// and ErrorMsg are redacted where the event is sent; Suggestion is redacted
// there too, because a carried-over argument can be a secret.
func attachSuggestion(result *friction.Result) {
	if result == nil || result.Event == nil || result.Suggestion == nil {
		return
	}
	result.Event.Suggestion = result.Suggestion.Corrected
}

// planInvocation answers "what would ox do with this argv?" end to end, without
// running anything: the pre-cobra rewrites first, then (if cobra would reject
// the argv) the recovery plan for the error it would raise.
func planInvocation(args []string) correctionPlan {
	if plan := planCorrection(args, nil); plan.Auto {
		return plan
	}
	err := predictCobraError(args)
	if err == nil {
		return planCorrection(args, nil)
	}
	return planCorrection(args, err)
}

// predictCobraError returns the error cobra would produce for args (unknown
// command, unknown flag, wrong argument count), or nil when it would accept
// them. It resolves against rootCmd as execution does, but parses flags on a
// scratch flag set, so it can be called on any argv without side effects.
//
// Flag values are not validated (`--limit abc` passes): the scratch set knows
// each flag's name and arity, not its type. That is enough to catch what the
// safety net is for, a correction cobra would reject outright.
func predictCobraError(args []string) error {
	cmd, rest, err := rootCmd.Find(args)
	if err != nil {
		return err
	}
	positional := rest
	if !cmd.DisableFlagParsing {
		var help bool
		var parseErr error
		positional, help, parseErr = dryParseFlags(cmd, rest)
		if parseErr != nil {
			return parseErr
		}
		if help {
			return nil
		}
	}
	if !cmd.Runnable() {
		// a group with no action of its own shows help for a bare invocation
		// and rejects stray words, as executeWithFrictionRecovery enforces
		if cmd.HasParent() {
			return cobra.NoArgs(cmd, positional)
		}
		return nil
	}
	return cmd.ValidateArgs(positional)
}

// discardFlagValue lets a scratch flag set accept any value for a flag.
type discardFlagValue struct{}

func (discardFlagValue) String() string   { return "" }
func (discardFlagValue) Set(string) error { return nil }
func (discardFlagValue) Type() string     { return "string" }

// dryParseFlags parses args against a copy of cmd's flag names, shorthands and
// arities, leaving cmd's own flags untouched. Parsing the real flags twice
// would corrupt them: a repeatable flag appends on every Set (--tag a --tag b
// becomes a, b, a, b) and pflag keeps that state private, so it cannot be
// undone afterwards.
func dryParseFlags(cmd *cobra.Command, args []string) (positional []string, help bool, err error) {
	// installs --help and merges inherited persistent flags, as execution does
	cmd.InitDefaultHelpFlag()

	scratch := pflag.NewFlagSet(cmd.CommandPath(), pflag.ContinueOnError)
	scratch.SetOutput(io.Discard)
	scratch.ParseErrorsWhitelist = pflag.ParseErrorsWhitelist(cmd.FParseErrWhitelist)
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		scratch.AddFlag(&pflag.Flag{
			Name:        f.Name,
			Shorthand:   f.Shorthand,
			NoOptDefVal: f.NoOptDefVal,
			Value:       discardFlagValue{},
		})
	})
	if err := scratch.Parse(args); err != nil {
		return nil, false, err
	}
	return scratch.Args(), scratch.Changed("help"), nil
}

// argvParsesCleanly is the safety net before any auto-execute: argv must
// resolve to a real non-root command whose flags parse and whose positional
// arguments the command accepts. The agent dispatcher takes arbitrary
// arguments, so it passes on its flags alone.
func argvParsesCleanly(argv []string) bool {
	cmd, _, err := rootCmd.Find(argv)
	if err != nil || cmd == nil || cmd == rootCmd {
		return false
	}
	return predictCobraError(argv) == nil
}

// planRewrite plans the two rewrites that need no catalog: a command line
// passed as a single word, and a subcommand spelled as a flag.
func planRewrite(args []string) correctionPlan {
	none := correctionPlan{Source: correctionNone}
	if corrected, ok := wordSplitArgs(args); ok {
		if !argvParsesCleanly(corrected) {
			return none
		}
		return rewritePlan(correctionWordSplit, args, corrected, true, wordSplitNote)
	}
	if corrected, verb, ok := flagAsVerbArgs(args); ok {
		if !argvParsesCleanly(corrected) {
			return none
		}
		note := fmt.Sprintf("--%s is a subcommand, not a flag; use '%s'", verb, quoteArgs(corrected))
		return rewritePlan(correctionFlagAsVerb, args, corrected, !destructiveVerbs[verb], note)
	}
	return none
}

func rewritePlan(source correctionSource, original, corrected []string, auto bool, note string) correctionPlan {
	suggestion := &friction.Suggestion{
		Type:        friction.SuggestionCommandRemap,
		Original:    quoteArgs(original),
		Corrected:   quoteArgs(corrected),
		Confidence:  1,
		Description: note,
	}
	result := &friction.Result{Suggestion: suggestion, AutoExecute: auto}
	if auto {
		result.CorrectedArgs = corrected
	}
	return correctionPlan{
		Corrected:  corrected,
		Auto:       auto,
		Source:     source,
		Suggestion: suggestion.Corrected,
		Note:       note,
		Result:     result,
	}
}

// wordSplitArgs recovers a whole command line passed as one word. zsh does not
// split an unquoted $var, so a loop over command names hands ox "plan list" as
// a single argv element. It applies only when the words resolve to a real
// non-root command; a quoted free-text argument never sits in args[0].
func wordSplitArgs(args []string) ([]string, bool) {
	if len(args) == 0 || !strings.ContainsAny(args[0], " \t\n") {
		return nil, false
	}
	fields := strings.Fields(args[0])
	if len(fields) == 0 || strings.HasPrefix(fields[0], "-") {
		return nil, false
	}
	cmd, _, err := rootCmd.Find(fields)
	if err != nil || cmd == nil || cmd == rootCmd {
		return nil, false
	}
	return slices.Concat(fields, args[1:]), true
}

// flagAsVerbArgs recovers a subcommand typed as a flag ("daemon --list" for
// "daemon list"). It applies only when the resolved command does not accept the
// flag and "<command> <word>" is a real subcommand, so "guide --list" stays an
// error: guide reads "list" as a topic name, not a subcommand.
func flagAsVerbArgs(args []string) (corrected []string, verb string, ok bool) {
	cmd, _, err := rootCmd.Find(args)
	if err != nil || cmd == nil || cmd.DisableFlagParsing || cmd.FParseErrWhitelist.UnknownFlags {
		return nil, "", false
	}
	// --help and --version are installed lazily; without this they look unknown
	cmd.InitDefaultHelpFlag()
	cmd.InitDefaultVersionFlag()

	for i, arg := range args {
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "--") || strings.Contains(arg, "=") {
			continue
		}
		word := strings.TrimPrefix(arg, "--")
		if word == "" || cmd.Flags().Lookup(word) != nil {
			continue
		}
		sub, _, findErr := cmd.Find([]string{word})
		if findErr != nil || sub == cmd || sub.Parent() != cmd {
			continue
		}
		candidate := slices.Clone(args)
		candidate[i] = word
		// the swap must land on that subcommand; "--list daemon" must not
		// become "list daemon"
		if got, _, err := rootCmd.Find(candidate); err != nil || got != sub {
			continue
		}
		return candidate, word, true
	}
	return nil, "", false
}

// planExactRemap shapes the catalog's whole-input match, demoting it to a
// suggestion when its argv would not parse.
func planExactRemap(base *friction.Result) correctionPlan {
	corrected := base.CorrectedArgs
	if !base.AutoExecute {
		corrected = strings.Fields(base.Suggestion.Corrected)
	}
	auto := base.AutoExecute && argvParsesCleanly(corrected)
	base.AutoExecute = auto
	base.CorrectedArgs = nil
	if auto {
		base.CorrectedArgs = corrected
	}
	return correctionPlan{
		Corrected:  corrected,
		Auto:       auto,
		Source:     correctionCatalog,
		Suggestion: base.Suggestion.Corrected,
		Result:     base,
	}
}

// planPrefixRemap retries the catalog on the leading command words, carrying
// the remaining arguments across. The catalog matches the whole input, so
// "auth --help" misses the "auth" -> "login" entry; the words before the first
// flag still hit it.
//
// It starts at the full run of leading words when flags follow (a regex entry
// such as "agent <id> status" is anchored and the flags break it), and one
// shorter when nothing follows (the whole input was already tried).
func planPrefixRemap(base *friction.Result, args []string, err error) (correctionPlan, bool) {
	words := leadingWords(args)
	longest := len(words)
	if longest == len(args) {
		longest--
	}
	for n := longest; n >= 1; n-- {
		hit := handleFriction(frictionEngine, words[:n], err)
		if hit == nil || hit.Suggestion == nil || hit.Suggestion.Type != friction.SuggestionCommandRemap {
			continue
		}
		mapped := hit.CorrectedArgs
		if !hit.AutoExecute {
			mapped = strings.Fields(hit.Suggestion.Corrected)
		}
		corrected := slices.Concat(mapped, args[n:])
		auto := hit.AutoExecute && argvParsesCleanly(corrected)

		base.Suggestion = &friction.Suggestion{
			Type:        friction.SuggestionCommandRemap,
			Original:    quoteArgs(args),
			Corrected:   quoteArgs(corrected),
			Confidence:  hit.Suggestion.Confidence,
			Description: hit.Suggestion.Description,
		}
		base.AutoExecute = auto
		base.CorrectedArgs = nil
		if auto {
			base.CorrectedArgs = corrected
		}
		return correctionPlan{
			Corrected:  corrected,
			Auto:       auto,
			Source:     correctionCatalogPrefix,
			Suggestion: base.Suggestion.Corrected,
			Result:     base,
		}, true
	}
	return correctionPlan{}, false
}

// leadingWords returns the arguments before the first flag.
func leadingWords(args []string) []string {
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return args[:i]
		}
	}
	return args
}

// plausibleFuzzyMatch reports whether an edit-distance guess is worth showing.
// frictionax allows two edits for any word, which for a short word is a
// different word: "mcp" -> "gc" (a command that re-clones repositories),
// "api" -> "pr". Short words get one edit; longer words may not change more
// than half their letters.
func plausibleFuzzyMatch(s *friction.Suggestion) bool {
	bad := strings.TrimLeft(s.Original, "-")
	good := strings.TrimLeft(s.Corrected, "-")
	distance := editDistance(bad, good)
	length := utf8.RuneCountInString(bad)
	if length <= 4 && distance > 1 {
		return false
	}
	return distance*2 < length
}

// editDistance is the Levenshtein distance between a and b, in runes.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	previous := make([]int, len(rb)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		current := make([]int, len(rb)+1)
		current[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			// a package-level test helper shadows the builtin min with a
			// two-argument version, so nest the calls
			current[j] = min(min(previous[j]+1, current[j-1]+1), previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(rb)]
}

// quoteArgs joins args for display, single-quoting any that hold whitespace so
// "plan list" as one word stays distinguishable from plan list as two.
func quoteArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = arg
		if arg == "" || strings.ContainsAny(arg, " \t\n'\"") {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}

// argsRequestJSON reports whether args ask for JSON output, for the pre-cobra
// step where the --json flag has not been parsed yet.
func argsRequestJSON(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if name != "--json" {
			continue
		}
		if !hasValue {
			return true
		}
		enabled, err := strconv.ParseBool(value)
		return err == nil && enabled
	}
	return false
}

// correctionNote mirrors the frictionax JSON envelope so agents parse one shape
// whether the catalog or a rewrite produced the correction.
type correctionNote struct {
	Corrected  *correctionWas `json:"_corrected,omitempty"`
	Suggestion *correctionTry `json:"_suggestion,omitempty"`
}

type correctionWas struct {
	Was  string `json:"was"`
	Now  string `json:"now"`
	Note string `json:"note"`
}

type correctionTry struct {
	Try  string `json:"try"`
	Note string `json:"note"`
}

// emitCorrection tells the coworker what ox changed or would have changed, for
// learning. Agents (jsonMode) get one JSON line on stdout; people get a short
// line on stderr. Plans without their own note keep frictionax's wording.
func emitCorrection(plan correctionPlan, jsonMode bool) {
	result := plan.Result
	if result == nil || result.Suggestion == nil {
		return
	}
	if plan.Note == "" {
		result.Emit(jsonMode)
		return
	}
	suggestion := result.Suggestion
	if !jsonMode {
		if plan.Auto {
			fmt.Fprintf(os.Stderr, "-> Correcting to: %s (%s)\n", suggestion.Corrected, plan.Note)
		} else {
			fmt.Fprintf(os.Stderr, "Did you mean?\n    %s (%s)\n", suggestion.Corrected, plan.Note)
		}
		return
	}
	var note correctionNote
	if plan.Auto {
		note.Corrected = &correctionWas{Was: suggestion.Original, Now: suggestion.Corrected, Note: plan.Note}
	} else {
		note.Suggestion = &correctionTry{Try: suggestion.Corrected, Note: plan.Note}
	}
	data, err := json.Marshal(note)
	if err != nil {
		return
	}
	fmt.Fprintln(os.Stdout, string(data))
}
