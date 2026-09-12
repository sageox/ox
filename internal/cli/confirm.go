package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// RemovalItem represents an item to be removed during uninstall
type RemovalItem struct {
	Type        string // e.g., "directory", "file", "hook"
	Path        string // relative path from repo root
	Description string // brief description of what this is
}

// DangerousOperationWarning displays a styled warning box for destructive operations
func DangerousOperationWarning(operation, target string) {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(ColorError).
		Padding(1, 2).
		Foreground(ColorError).
		Bold(true)

	warning := fmt.Sprintf("⚠  DESTRUCTIVE OPERATION: %s\n\nTarget: %s", operation, target)
	fmt.Println()
	fmt.Println(boxStyle.Render(warning))
	fmt.Println()
}

// ShowUninstallPreview displays what will be removed, grouped by type
// Shows summary first, then details if items exist
func ShowUninstallPreview(items []RemovalItem, dryRun bool) {
	if len(items) == 0 {
		PrintInfo("No SageOx files found to remove")
		return
	}

	// group items by type for organized display
	grouped := make(map[string][]RemovalItem)
	for _, item := range items {
		grouped[item.Type] = append(grouped[item.Type], item)
	}

	// show summary count
	if dryRun {
		fmt.Println(StyleWarning.Render("DRY RUN MODE - Nothing will be removed"))
		fmt.Println()
	}

	fmt.Println(StyleBold.Render("The following will be removed:"))
	fmt.Println()

	// display grouped items
	typeOrder := []string{"directory", "file", "hook", "config"}
	for _, itemType := range typeOrder {
		if items, exists := grouped[itemType]; exists {
			displayItemGroup(itemType, items)
		}
	}

	// display any types not in our predefined order
	for itemType, items := range grouped {
		if !contains(typeOrder, itemType) {
			displayItemGroup(itemType, items)
		}
	}
}

// displayItemGroup shows a group of removal items with consistent formatting
func displayItemGroup(itemType string, items []RemovalItem) {
	// type header with count
	header := fmt.Sprintf("%s (%d)", cases.Title(language.English).String(itemType), len(items))
	fmt.Println(StyleGroupHeader.Render(header))

	// show items with tree-style formatting
	for i, item := range items {
		prefix := "├─"
		if i == len(items)-1 {
			prefix = "└─"
		}

		// use different colors for visual hierarchy
		pathStyle := StyleFile
		descStyle := StyleDim

		if item.Description != "" {
			fmt.Printf("  %s %s %s\n",
				StyleDim.Render(prefix),
				pathStyle.Render(item.Path),
				descStyle.Render("("+item.Description+")"))
		} else {
			fmt.Printf("  %s %s\n",
				StyleDim.Render(prefix),
				pathStyle.Render(item.Path))
		}
	}
	fmt.Println()
}

// ConfirmUninstall prompts user to confirm a destructive uninstall operation
// Returns error if user cancels or input fails, nil if confirmed
// Bypasses prompt if force is true
func ConfirmUninstall(repoName string, force bool) error {
	if force {
		return nil
	}

	fmt.Println(StyleWarning.Render("⚠  This operation affects ALL users of this repository"))
	fmt.Println()
	fmt.Println("This will:")
	fmt.Println("  • Remove all SageOx configuration and state")
	fmt.Println("  • Uninstall repository-level hooks")
	fmt.Println("  • Affect all team members working on this repo")
	fmt.Println()
	fmt.Println(StyleDim.Render("Note: User-level config (~/.config/sageox/) will NOT be touched"))
	fmt.Println()

	// require typing "uninstall" to confirm
	fmt.Printf("Type %s to confirm: ", StyleCommand.Render("uninstall"))

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading confirmation input: %w", err)
	}

	response = strings.TrimSpace(response)
	if response != "uninstall" {
		return fmt.Errorf("confirmation failed: expected 'uninstall', got '%s'", response)
	}

	return nil
}

// ConfirmDangerousOperation is a generic confirmation prompt for destructive operations
// Requires user to type exactMatch string to proceed
// Returns error if user cancels or input doesn't match
func ConfirmDangerousOperation(operationName, exactMatch string, force bool) error {
	if force {
		return nil
	}

	fmt.Printf("Type %s to confirm %s: ",
		StyleCommand.Render(exactMatch),
		operationName)

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading confirmation input: %w", err)
	}

	response = strings.TrimSpace(response)
	if response != exactMatch {
		fmt.Println()
		PrintWarning("Operation canceled")
		return fmt.Errorf("confirmation required")
	}

	return nil
}

// ErrConfirmationRequired is returned by ConfirmYesNoRequired when nobody was
// there to answer — stdin produced no input at all (closed, empty, or
// unreadable) — as opposed to a user who was actually present and pressed
// Enter to accept the default. Reaching this means the command must stop
// rather than guess: a silent default here has repeatedly meant a login that
// never happened, a logout that never happened, and an uninstall that cleaned
// up locally while leaving cloud records behind.
var ErrConfirmationRequired = errors.New("confirmation required: re-run in a terminal, or pass --yes")

// ConfirmYesNo displays a yes/no prompt and loops until valid input.
// Returns true if user confirms with y/yes, false for n/no.
// Empty input uses the default specified by defaultYes.
//
// When no input can be gathered at all, this silently returns defaultYes —
// unchanged from its long-standing behavior, preserved here for every existing
// caller. Callers for whom that silent guess has a real cost should call
// ConfirmYesNoRequired instead.
func ConfirmYesNo(prompt string, defaultYes bool) bool {
	answer, answered := confirmYesNoCore(prompt, defaultYes)
	if !answered {
		return defaultYes
	}
	return answer
}

// ConfirmYesNoRequired behaves like ConfirmYesNo, but returns
// ErrConfirmationRequired instead of silently taking the default when no one
// was there to answer. force (a command's own --force/--yes) and the global
// --yes both short-circuit to an affirmative answer without prompting.
//
// Use this wherever taking the default silently would lose work or leave an
// operation half-done.
func ConfirmYesNoRequired(prompt string, defaultYes, force bool) (bool, error) {
	if force || AssumeYes() {
		return true, nil
	}

	answer, answered := confirmYesNoCore(prompt, defaultYes)
	if !answered {
		return false, ErrConfirmationRequired
	}
	return answer, nil
}

// confirmYesNoCore is the shared implementation behind ConfirmYesNo and
// ConfirmYesNoRequired. answered reports whether answer reflects input a human
// actually supplied — including a deliberate blank Enter, whether typed at a
// terminal or piped in — as opposed to a fallback returned because stdin was
// closed, empty, or unreadable.
//
// Note it does not consult IsInteractive(): `echo y | ox …` is a legitimate
// answer even with no TTY, and rejecting it would break every scripted caller
// that answers honestly. The distinction that matters is whether an answer
// arrived, not whether a terminal was attached.
func confirmYesNoCore(prompt string, defaultYes bool) (answer bool, answered bool) {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("%s %s: ", prompt, StyleDim.Render(suffix))

		response, err := reader.ReadString('\n')
		// A final line with no trailing newline arrives as data *and* io.EOF.
		// That is a real answer, so only treat a read as unanswered when it
		// yielded nothing at all.
		if err != nil && strings.TrimSpace(response) == "" {
			return defaultYes, false
		}

		response = strings.TrimSpace(strings.ToLower(response))

		switch response {
		case "":
			return defaultYes, true
		case "y", "yes":
			return true, true
		case "n", "no":
			return false, true
		}

		// Invalid input. Loop again only if there is more to read — otherwise
		// the last line was garbage at EOF and re-prompting would spin forever.
		if err != nil {
			return defaultYes, false
		}
	}
}

// contains checks if a string slice contains a value
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
