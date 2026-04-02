package main

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// writeGFM emits a GitHub Flavored Markdown compatibility table to w.
// Suitable for use as a GitHub Actions step summary.
func writeGFM(w io.Writer, ld *LoadedData) error {
	oxVersion := latestOxVersion(ld)

	fmt.Fprintf(w, "## ox Compatibility Matrix (v%s)\n\n", oxVersion)
	fmt.Fprintf(w, "Generated: %s\n\n", time.Now().UTC().Format(time.RFC3339))

	// Header row: Feature | Agent1 | Agent2 | ...
	headers := []string{"Feature"}
	for _, ak := range ld.AgentOrder {
		ai := ld.Compat.Agents[ak]
		headers = append(headers, ai.DisplayName+" "+tierBadge(ai.Tier))
	}
	fmt.Fprintln(w, "| "+strings.Join(headers, " | ")+" |")

	// Separator row — center-align all agent columns.
	sep := []string{"------"}
	for range ld.AgentOrder {
		sep = append(sep, ":-:")
	}
	fmt.Fprintln(w, "| "+strings.Join(sep, " | ")+" |")

	// Data rows.
	for _, fk := range ld.FeatureOrder {
		fi := ld.Compat.FeatureGroups[fk]
		cells := []string{fi.DisplayName}
		for _, ak := range ld.AgentOrder {
			cs := cellState(ld, ak, fk)
			cells = append(cells, gfmCell(cs))
		}
		fmt.Fprintln(w, "| "+strings.Join(cells, " | ")+" |")
	}

	fmt.Fprintln(w)
	writeLegendGFM(w)
	return nil
}

func gfmCell(cs CellState) string {
	switch cs.Status {
	case "pass":
		if cs.Total > 0 {
			return fmt.Sprintf("✅ %d/%d", cs.Passed, cs.Total)
		}
		return "✅"
	case "warn":
		return fmt.Sprintf("⚠️ %d/%d", cs.Passed, cs.Total)
	case "fail":
		return fmt.Sprintf("❌ %d/%d", cs.Passed, cs.Total)
	case "untested":
		return "⬜"
	case "planned":
		return "📋"
	case "unsupported":
		return "—"
	default: // na
		return "—"
	}
}

func tierBadge(tier string) string {
	switch tier {
	case "gold":
		return "🥇"
	case "silver":
		return "🥈"
	default:
		return "🥉"
	}
}

func writeLegendGFM(w io.Writer) {
	fmt.Fprintln(w, "**Legend:**")
	fmt.Fprintln(w, "✅ All tests pass &nbsp; ⚠️ Some failures &nbsp; ❌ All failed &nbsp; ⬜ Declared, untested &nbsp; 📋 Planned &nbsp; — Not applicable")
}
