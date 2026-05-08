package main

import (
	"bufio"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

// guidesFS bundles concise, agent-and-human-friendly topical guides into the
// binary so users have offline access without depending on the docs site or
// GitHub raw URLs. Source-of-truth lives next to this file in cmd/ox/guides/.
//
//go:embed guides/*.md
var guidesFS embed.FS

const guidesRoot = "guides"

var guideCmd = &cobra.Command{
	Use:   "guide [topic]",
	Short: "Show a bundled topical guide",
	Long: `Show a bundled topical guide rendered for the terminal.

Without arguments, lists all available topics. With a topic argument,
renders that guide. Topics are bundled into the ox binary, so they
work offline and stay in sync with the version of ox you're running.

Examples:
  ox guide                    # list all topics
  ox guide team-rules         # how to share AI coworker rules across the team
  ox guide getting-started    # five-minute walkthrough
  ox guide team-rules --raw   # plain markdown (for piping to AI agents)`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, _ := cmd.Flags().GetBool("raw")

		if len(args) == 0 {
			return listGuides(raw)
		}

		return showGuide(args[0], raw)
	},
}

func init() {
	guideCmd.Flags().Bool("raw", false, "output raw markdown without terminal rendering (default: false)")
}

// guideEntry is a discovered bundled guide.
type guideEntry struct {
	Topic       string // filename without .md (e.g., "team-rules")
	Title       string // from frontmatter
	Description string // from frontmatter
}

// discoverGuides walks the embedded guides FS and returns a sorted catalog.
func discoverGuides() ([]guideEntry, error) {
	entries, err := fs.ReadDir(guidesFS, guidesRoot)
	if err != nil {
		return nil, fmt.Errorf("read embedded guides: %w", err)
	}

	var guides []guideEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}

		topic := strings.TrimSuffix(name, ".md")
		title, desc := readGuideFrontmatter(name)
		if title == "" {
			title = topic
		}

		guides = append(guides, guideEntry{
			Topic:       topic,
			Title:       title,
			Description: desc,
		})
	}

	sort.Slice(guides, func(i, j int) bool {
		return guides[i].Topic < guides[j].Topic
	})

	return guides, nil
}

// readGuideFrontmatter extracts title + description from a bundled guide's
// YAML frontmatter. Bundled guides are short and well-formed, so a minimal
// line-based parser is sufficient (avoids pulling in a YAML dep).
func readGuideFrontmatter(name string) (title, desc string) {
	f, err := guidesFS.Open(guidesRoot + "/" + name)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	inFrontmatter := false
	lineCount := 0

	for scanner.Scan() {
		lineCount++
		line := scanner.Text()

		if lineCount == 1 {
			if line != "---" {
				return "", ""
			}
			inFrontmatter = true
			continue
		}
		if !inFrontmatter {
			break
		}
		if line == "---" {
			break
		}

		if strings.HasPrefix(line, "title:") {
			title = trimFrontmatterValue(strings.TrimPrefix(line, "title:"))
		} else if strings.HasPrefix(line, "description:") {
			desc = trimFrontmatterValue(strings.TrimPrefix(line, "description:"))
		}

		if lineCount > 20 {
			break
		}
	}

	return title, desc
}

func trimFrontmatterValue(v string) string {
	v = strings.TrimSpace(v)
	v = strings.Trim(v, `"'`)
	return v
}

func listGuides(raw bool) error {
	guides, err := discoverGuides()
	if err != nil {
		return err
	}
	if len(guides) == 0 {
		return fmt.Errorf("no bundled guides found (this is a build error)")
	}

	if raw {
		// raw mode: machine-friendly output for piping
		for _, g := range guides {
			fmt.Printf("%s\t%s\t%s\n", g.Topic, g.Title, g.Description)
		}
		return nil
	}

	var sb strings.Builder
	sb.WriteString("# ox guides\n\n")
	sb.WriteString("Bundled topical guides. Run `ox guide <topic>` to read one.\n\n")
	sb.WriteString("| Topic | Description |\n")
	sb.WriteString("|---|---|\n")
	for _, g := range guides {
		desc := g.Description
		if desc == "" {
			desc = g.Title
		}
		fmt.Fprintf(&sb, "| `%s` | %s |\n", g.Topic, desc)
	}

	fmt.Print(ui.RenderMarkdown(sb.String()))
	return nil
}

func showGuide(topic string, raw bool) error {
	// allow `ox guide team-rules.md` as well as `ox guide team-rules`
	topic = strings.TrimSuffix(topic, ".md")

	path := guidesRoot + "/" + topic + ".md"
	data, err := fs.ReadFile(guidesFS, path)
	if err != nil {
		// surface available topics on miss
		guides, _ := discoverGuides()
		topics := make([]string, 0, len(guides))
		for _, g := range guides {
			topics = append(topics, g.Topic)
		}
		return fmt.Errorf("no bundled guide named %q. Available: %s", topic, strings.Join(topics, ", "))
	}

	body := string(data)
	if raw {
		fmt.Print(body)
		if !strings.HasSuffix(body, "\n") {
			fmt.Println()
		}
		return nil
	}

	// strip YAML frontmatter for human rendering — Glamour treats it as
	// content otherwise and the metadata block shows up at the top of the
	// rendered output. AI agents using --raw still see the frontmatter.
	body = stripGuideFrontmatter(body)
	fmt.Print(ui.RenderMarkdown(body))
	return nil
}

// stripGuideFrontmatter removes a leading YAML frontmatter block (the form
// "---\n...\n---\n") so it doesn't end up in the rendered output. If the
// input has no frontmatter, returns it unchanged.
func stripGuideFrontmatter(s string) string {
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return s
	}
	rest := s
	if strings.HasPrefix(rest, "---\r\n") {
		rest = rest[5:]
	} else {
		rest = rest[4:]
	}
	for {
		i := strings.Index(rest, "\n---")
		if i < 0 {
			return strings.TrimLeft(rest, "\r\n")
		}
		after := i + 4
		if after >= len(rest) || rest[after] == '\n' || rest[after] == '\r' {
			return strings.TrimLeft(rest[after:], "\r\n")
		}
		rest = rest[i+1:]
	}
}
